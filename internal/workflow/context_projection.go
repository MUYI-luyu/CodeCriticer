package workflow

import (
	"encoding/json"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

type projectedResult struct {
	assistant int
	tool      int
	name      string
	canonical string
	evidence  []*Evidence
}

// projectContext preserves the native assistant/tool message sequence and only
// replaces mechanically redundant tool-result payloads with a small marker.
func projectContext(messages []review.ToolMessage) ([]review.ToolMessage, ContextProjectionStats) {
	projected := cloneToolMessages(messages)
	stats := ContextProjectionStats{FullBytes: toolMessagesBytes(messages)}
	results := collectProjectableResults(messages)
	compacted := make(map[int]map[string]any)

	// Retain the most recent byte-identical result. Errors and submit feedback are
	// deliberately excluded by collectProjectableResults.
	latest := make(map[string]int)
	for i, result := range results {
		if previous, ok := latest[result.canonical]; ok {
			compacted[results[previous].tool] = map[string]any{
				"reason": "duplicate", "same_as": messages[result.assistant].ToolCalls[0].ID,
			}
		}
		latest[result.canonical] = i
	}

	// A read_code window is redundant only when another successful read_code
	// result for the same file fully contains every line in it.
	for i, candidate := range results {
		if candidate.name != "read_code" || compacted[candidate.tool] != nil {
			continue
		}
		file, start, end, ok := readCodeWindow(candidate.evidence)
		if !ok {
			continue
		}
		best := -1
		for j, covering := range results {
			if i == j || covering.name != "read_code" || compacted[covering.tool] != nil {
				continue
			}
			otherFile, otherStart, otherEnd, otherOK := readCodeWindow(covering.evidence)
			if !otherOK || file != otherFile || otherStart > start || otherEnd < end {
				continue
			}
			if otherStart == start && otherEnd == end {
				continue
			}
			if best < 0 || windowSize(otherStart, otherEnd) < windowSizeFor(results[best]) ||
				(windowSize(otherStart, otherEnd) == windowSizeFor(results[best]) && covering.tool > results[best].tool) {
				best = j
			}
		}
		if best >= 0 {
			callID := messages[results[best].assistant].ToolCalls[0].ID
			compacted[candidate.tool] = map[string]any{
				"reason": "read_code_covered", "by": callID,
			}
		}
	}

	for index, marker := range compacted {
		payload, _ := json.Marshal(map[string]any{"context_projection": marker})
		if len(payload) >= len(projected[index].Content) {
			continue
		}
		projected[index].Content = string(payload)
		if marker["reason"] == "duplicate" {
			stats.DuplicateResultsCompacted++
		} else {
			stats.DominatedReadCodeCompacted++
		}
	}
	stats.ProjectedBytes = toolMessagesBytes(projected)
	return projected, stats
}

func collectProjectableResults(messages []review.ToolMessage) []projectedResult {
	results := make([]projectedResult, 0)
	for i := 0; i+1 < len(messages); i++ {
		assistant, tool := messages[i], messages[i+1]
		if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 || tool.Role != "tool" || tool.ToolCallID != assistant.ToolCalls[0].ID {
			continue
		}
		name := assistant.ToolCalls[0].Function.Name
		if name == "submit_claims" || tool.Content == "" {
			continue
		}
		var payload struct {
			Evidence []*Evidence `json:"evidence"`
			Error    string      `json:"error"`
		}
		if json.Unmarshal([]byte(tool.Content), &payload) != nil || payload.Error != "" {
			continue
		}
		results = append(results, projectedResult{assistant: i, tool: i + 1, name: name, canonical: canonicalToolResult(name, payload.Evidence), evidence: payload.Evidence})
		i++
	}
	return results
}

func canonicalToolResult(name string, evidence []*Evidence) string {
	type fact struct {
		Source  string `json:"source"`
		Type    string `json:"type"`
		File    string `json:"file,omitempty"`
		Line    int    `json:"line,omitempty"`
		EndLine int    `json:"end_line,omitempty"`
		Content string `json:"content"`
		Symbol  string `json:"symbol,omitempty"`
	}
	facts := make([]fact, 0, len(evidence))
	for _, item := range evidence {
		if item == nil {
			facts = append(facts, fact{})
			continue
		}
		facts = append(facts, fact{Source: item.Source, Type: item.Type, File: item.File, Line: item.Line, EndLine: item.EndLine, Content: item.Content, Symbol: item.Symbol})
	}
	encoded, _ := json.Marshal(struct {
		Tool     string `json:"tool"`
		Evidence []fact `json:"evidence"`
	}{Tool: name, Evidence: facts})
	return string(encoded)
}

func readCodeWindow(evidence []*Evidence) (file string, start, end int, ok bool) {
	// read_code currently returns exactly one contiguous window. Refuse to infer
	// dominance from multiple records because their bounding range may contain
	// lines that were never present in the tool result.
	if len(evidence) != 1 {
		return "", 0, 0, false
	}
	for _, item := range evidence {
		if item == nil || item.Source != "read_code" || item.File == "" || item.Line <= 0 {
			return "", 0, 0, false
		}
		itemEnd := maxEvidenceLine(item)
		if file == "" {
			file, start, end = item.File, item.Line, itemEnd
			continue
		}
		if item.File != file {
			return "", 0, 0, false
		}
		if item.Line < start {
			start = item.Line
		}
		if itemEnd > end {
			end = itemEnd
		}
	}
	return file, start, end, true
}

func cloneToolMessages(messages []review.ToolMessage) []review.ToolMessage {
	out := append([]review.ToolMessage(nil), messages...)
	for i := range out {
		out[i].ToolCalls = append([]review.ToolCall(nil), messages[i].ToolCalls...)
	}
	return out
}

func toolMessagesBytes(messages []review.ToolMessage) int {
	data, _ := json.Marshal(messages)
	return len(data)
}

func windowSize(start, end int) int { return end - start + 1 }

func windowSizeFor(result projectedResult) int {
	_, start, end, ok := readCodeWindow(result.evidence)
	if !ok {
		return int(^uint(0) >> 1)
	}
	return windowSize(start, end)
}
