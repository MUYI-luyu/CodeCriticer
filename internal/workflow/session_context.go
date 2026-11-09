package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

var errCheckpointNeedsToolExchange = errors.New("context checkpoint needs a complete assistant/tool exchange")

// This intentionally small manager follows the common coding-agent split:
// durable history is retained for audit; the model sees an active window made
// of a checkpoint plus a recent verbatim tail. It does not try to infer facts
// in Go or turn a checkpoint into evidence.
const (
	defaultActiveContextBudget = 96 << 10
	defaultContextTailBudget   = 32 << 10
)

type activeContext struct {
	budget        int
	tailBudget    int
	enabled       bool
	projected     bool
	index         string
	compactKernel review.ToolMessage
	checkpointAt  int // durable-history index represented by checkpoint
	checkpoint    *ContextCheckpoint
	stats         ContextCompactionStats
}

func (m *activeContext) setInvestigationIndex(content string) {
	m.index = content
}

func (m *activeContext) setCompactedKernel(message review.ToolMessage) {
	m.compactKernel = message
}

func newActiveContext(budget int, enabled bool) *activeContext {
	if budget <= 0 {
		budget = defaultActiveContextBudget
	}
	return &activeContext{budget: budget, tailBudget: min(defaultContextTailBudget, budget/3), enabled: enabled, stats: ContextCompactionStats{Enabled: enabled}}
}

func (m *activeContext) messages(history []review.ToolMessage) []review.ToolMessage {
	if !m.projected || len(history) == 0 {
		if len(history) == 0 {
			return nil
		}
		return m.withFixedContext(history, cloneToolMessages(history[1:]))
	}
	return m.projectedMessages(history, m.checkpoint, m.checkpointAt)
}

func (m *activeContext) withFixedContext(history, rest []review.ToolMessage) []review.ToolMessage {
	if len(history) == 0 {
		return nil
	}
	kernel := history[0]
	if m.projected && m.compactKernel.Content != "" {
		kernel = m.compactKernel
	}
	out := []review.ToolMessage{kernel}
	if m.index != "" {
		out = append(out, review.ToolMessage{Role: "user", Content: m.index})
	}
	return append(out, rest...)
}

func (m *activeContext) projectedMessages(history []review.ToolMessage, checkpoint *ContextCheckpoint, tailStart int) []review.ToolMessage {
	if len(history) == 0 {
		return nil
	}
	rest := make([]review.ToolMessage, 0, len(history))
	if checkpoint != nil {
		rest = append(rest, review.ToolMessage{Role: "user", Content: checkpointPrompt(*checkpoint)})
	}
	if tailStart < 1 {
		tailStart = 1
	}
	if tailStart > len(history) {
		tailStart = len(history)
	}
	if tailStart < len(history) {
		rest = append(rest, cloneToolMessages(history[tailStart:])...)
	}
	return m.withFixedContext(history, rest)
}

func checkpointPrompt(checkpoint ContextCheckpoint) string {
	return "Context checkpoint from earlier investigation. It is a navigation handoff, not Claim evidence. Re-read code with tools before relying on it.\n" + mustJSON(checkpoint)
}

func mustJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
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

func (m *activeContext) needsCompaction(history []review.ToolMessage) bool {
	return m.enabled && len(history) > 0 && toolMessagesBytes(m.messages(history)) > m.budget
}

func (m *activeContext) compact(ctx context.Context, s *ReviewSession) error {
	// The fixed task/Diff manifest is deliberately never summarized away.
	// Checkpoint and tail are optional, but the kernel is not.
	if len(s.messages) > 0 && toolMessagesBytes(m.withFixedContext(s.messages, nil)) > m.budget {
		m.stats.Failed = true
		return fmt.Errorf("context_budget_exhausted: fixed task context exceeds budget %d", m.budget)
	}
	active, err := m.messagesForCheckpoint(s.messages)
	if errors.Is(err, errCheckpointNeedsToolExchange) {
		// A checkpoint that has not seen any tool result would only restate the
		// immutable task prompt. Keep the review running with the bounded
		// projection instead of inventing a handoff from no investigation.
		m.projected = true
		m.checkpoint = nil
		active, err = m.fit(s.messages)
		if err == nil {
			m.stats.FullBytes = toolMessagesBytes(s.messages)
			m.stats.ActiveBytes = toolMessagesBytes(active)
			s.trace.Checkpoint = nil
			s.trace.Compaction = m.stats
		}
		return err
	}
	if err != nil {
		m.stats.Failed = true
		return err
	}
	m.stats.FullBytes = toolMessagesBytes(s.messages)
	m.stats.CheckpointInputs = append(m.stats.CheckpointInputs, ContextCheckpointInput{
		DurableStart: m.checkpointAt,
		DurableEnd:   len(s.messages),
		ToolPairs:    completeToolExchangeCount(active),
		InputBytes:   toolMessagesBytes(active),
	})
	observedCalls := len(s.trace.LLMCalls)
	message, usage, err := s.run.llm.CompleteToolsWithUsage(ctx, contextCheckpointSystemPrompt, active, []review.ToolDefinition{contextCheckpointTool()}, s.run.model)
	if len(s.trace.LLMCalls) == observedCalls {
		s.recordUsage(usage)
	}
	if err != nil {
		m.stats.Failed = true
		return fmt.Errorf("context checkpoint request: %w", err)
	}
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Name != "save_context_checkpoint" {
		m.stats.Failed = true
		return fmt.Errorf("context checkpoint must call save_context_checkpoint exactly once")
	}
	checkpoint, err := decodeStrict[ContextCheckpoint](message.ToolCalls[0].Function.Arguments)
	if err != nil {
		m.stats.Failed = true
		return fmt.Errorf("context checkpoint arguments: %w", err)
	}
	if err := validateCheckpoint(checkpoint, max(1024, m.budget/4)); err != nil {
		m.stats.Failed = true
		return err
	}
	m.checkpoint = &checkpoint
	m.projected = true
	m.stats.Checkpoints++
	active, err = m.fit(s.messages)
	if err != nil {
		m.stats.Failed = true
		return err
	}
	m.stats.ActiveBytes = toolMessagesBytes(active)
	s.trace.Checkpoint = m.checkpoint
	s.trace.Compaction = m.stats
	return nil
}

// fit builds the bounded active context after a checkpoint attempt. The
// durable conversation remains untouched; when the checkpoint cannot coexist
// with the fixed kernel it is discarded before the run is made partial.
func (m *activeContext) fit(history []review.ToolMessage) ([]review.ToolMessage, error) {
	if len(history) == 0 {
		return nil, nil
	}
	kernelBytes := toolMessagesBytes(m.withFixedContext(history, nil))
	if kernelBytes > m.budget {
		return nil, fmt.Errorf("context_budget_exhausted: fixed task context exceeds budget %d", m.budget)
	}

	if m.checkpoint != nil {
		checkpointOnly := m.projectedMessages(history, m.checkpoint, len(history))
		if toolMessagesBytes(checkpointOnly) <= m.budget {
			m.checkpointAt = contextTailStart(history, min(m.tailBudget, m.budget-toolMessagesBytes(checkpointOnly)))
			active := m.projectedMessages(history, m.checkpoint, m.checkpointAt)
			if toolMessagesBytes(active) <= m.budget {
				return active, nil
			}
		}
		// A checkpoint is optional navigation context. Drop it rather than
		// abandoning a review whose immutable task still fits.
		m.checkpoint = nil
	}

	// With no checkpoint, the remaining budget belongs to the verbatim tail.
	// This keeps as much recent code as possible after dropping the optional
	// handoff, while the hard active budget still bounds it.
	m.checkpointAt = contextTailStart(history, m.budget-kernelBytes)
	active := m.projectedMessages(history, nil, m.checkpointAt)
	if toolMessagesBytes(active) > m.budget {
		return nil, fmt.Errorf("context_budget_exhausted: projected context exceeds budget %d", m.budget)
	}
	return active, nil
}

// messagesForCheckpoint bounds the compaction request itself. A checkpoint
// may summarize less old material when the durable history has already grown
// beyond budget, but the host never sends an oversized request to the model.
func (m *activeContext) messagesForCheckpoint(history []review.ToolMessage) ([]review.ToolMessage, error) {
	if len(history) == 0 {
		return nil, nil
	}
	if m.projected {
		active, err := m.fit(history)
		if err != nil {
			return nil, err
		}
		if !hasCompleteToolExchange(active) {
			return nil, errCheckpointNeedsToolExchange
		}
		return active, nil
	}
	kernelBytes := toolMessagesBytes(m.withFixedContext(history, nil))
	if kernelBytes > m.budget {
		return nil, fmt.Errorf("context_budget_exhausted: fixed task context exceeds budget %d", m.budget)
	}
	start := contextTailStart(history, m.budget-kernelBytes)
	m.checkpointAt = start
	active := m.projectedMessages(history, nil, start)
	if toolMessagesBytes(active) > m.budget {
		return nil, fmt.Errorf("context_budget_exhausted: checkpoint request exceeds budget %d", m.budget)
	}
	if !hasCompleteToolExchange(active) {
		return nil, errCheckpointNeedsToolExchange
	}
	return active, nil
}

func hasCompleteToolExchange(messages []review.ToolMessage) bool {
	return completeToolExchangeCount(messages) > 0
}

func completeToolExchangeCount(messages []review.ToolMessage) int {
	count := 0
	for i := 1; i+1 < len(messages); i++ {
		if messages[i].Role == "assistant" && messages[i+1].Role == "tool" {
			count++
		}
	}
	return count
}

func contextTailStart(history []review.ToolMessage, budget int) int {
	if len(history) <= 1 {
		return len(history)
	}
	start, used := len(history), 0
	for start > 1 {
		candidate := start - 1
		// Keep assistant/tool exchanges together. The normal loop guarantees a
		// tool result immediately follows its assistant tool call.
		if history[candidate].Role == "tool" && candidate > 1 && history[candidate-1].Role == "assistant" {
			candidate--
		}
		groupBytes := toolMessagesBytes(history[candidate:start])
		if used+groupBytes > budget {
			break
		}
		used += groupBytes
		start = candidate
	}
	return start
}

func validateCheckpoint(checkpoint ContextCheckpoint, maxBytes int) error {
	if len(mustJSON(checkpoint)) > maxBytes {
		return fmt.Errorf("context checkpoint exceeds %d-byte budget", maxBytes)
	}
	if strings.TrimSpace(checkpoint.Task) == "" {
		return fmt.Errorf("context checkpoint task is empty")
	}
	if len(checkpoint.Task) > 240 {
		return fmt.Errorf("context checkpoint task is too long")
	}
	if len(checkpoint.Reviewed) > 8 || len(checkpoint.OpenQuestions) > 6 || len(checkpoint.RejectedHypotheses) > 8 || len(checkpoint.ClaimsInProgress) > 6 {
		return fmt.Errorf("context checkpoint has too many entries")
	}
	for _, fact := range checkpoint.Reviewed {
		if strings.TrimSpace(fact.File) == "" || strings.TrimSpace(fact.Range) == "" || strings.TrimSpace(fact.Fact) == "" {
			return fmt.Errorf("context checkpoint reviewed fact is incomplete")
		}
		if len(fact.File) > 160 || len(fact.Range) > 80 || len(fact.Fact) > 300 {
			return fmt.Errorf("context checkpoint reviewed fact is too long")
		}
	}
	for _, question := range checkpoint.OpenQuestions {
		if strings.TrimSpace(question.Question) == "" || strings.TrimSpace(question.NextTool) == "" {
			return fmt.Errorf("context checkpoint open question is incomplete")
		}
		if len(question.Question) > 240 || len(question.NextTool) > 64 {
			return fmt.Errorf("context checkpoint open question is too long")
		}
		if !checkpointToolNames[question.NextTool] {
			return fmt.Errorf("context checkpoint refers to unavailable tool %q", question.NextTool)
		}
	}
	for _, item := range append(append([]string{}, checkpoint.RejectedHypotheses...), checkpoint.ClaimsInProgress...) {
		if len(item) > 240 {
			return fmt.Errorf("context checkpoint entry is too long")
		}
	}
	return nil
}

var checkpointToolNames = map[string]bool{
	"read_diff":        true,
	"read_code":        true,
	"search_code":      true,
	"read_evidence":    true,
	"run_static_rules": true,
	"submit_claims":    true,
}

const contextCheckpointSystemPrompt = `Summarize only the tool results present in this request into the required function arguments. This is a tiny handoff: at most 8 terse facts and 6 questions; each fact must name a file/range and fit one sentence. Omit routine tool output, copied source, and prose explanations. Preserve only task-relevant, evidence-backed code facts, unresolved questions, and rejected hypotheses. Do not invent findings or claim any hunk was read unless its result is present. next_tool must name a provided function. This checkpoint is navigation context, never final evidence.`

func contextCheckpointTool() review.ToolDefinition {
	stringValue := func(max int) map[string]any {
		return map[string]any{"type": "string", "pattern": `\S`, "maxLength": max}
	}
	return functionTool("save_context_checkpoint", "Save a structured handoff for a long-running review. It is not a final review and it must not make unsupported bug claims.", map[string]any{
		"task":                stringValue(240),
		"reviewed":            map[string]any{"type": "array", "maxItems": 8, "items": strictObject(map[string]any{"file": stringValue(160), "range": stringValue(80), "fact": stringValue(300)}, []string{"file", "range", "fact"})},
		"open_questions":      map[string]any{"type": "array", "maxItems": 6, "items": strictObject(map[string]any{"question": stringValue(240), "next_tool": map[string]any{"type": "string", "enum": []string{"read_diff", "read_code", "search_code", "read_evidence", "run_static_rules", "submit_claims"}}}, []string{"question", "next_tool"})},
		"rejected_hypotheses": map[string]any{"type": "array", "maxItems": 8, "items": stringValue(240)},
		"claims_in_progress":  map[string]any{"type": "array", "maxItems": 6, "items": stringValue(240)},
	}, []string{"task", "reviewed", "open_questions", "rejected_hypotheses", "claims_in_progress"})
}
