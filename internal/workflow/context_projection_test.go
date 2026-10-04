package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

func projectionPair(id, name, content string) []review.ToolMessage {
	return []review.ToolMessage{
		{Role: "assistant", ToolCalls: []review.ToolCall{{ID: id, Type: "function", Function: review.FunctionCall{Name: name, Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: id, Content: content},
	}
}

func projectionEvidence(id, source, file string, start, end int, content string) string {
	data, _ := json.Marshal(map[string]any{"evidence": []*Evidence{{ID: id, Source: source, File: file, Line: start, EndLine: end, Content: content}}, "error": ""})
	return string(data)
}

func TestProjectContextCompactsCoveredReadCodeAndPreservesPairs(t *testing.T) {
	messages := []review.ToolMessage{{Role: "user", Content: "review"}}
	messages = append(messages, projectionPair("narrow", "read_code", projectionEvidence("e1", "read_code", "main.go", 10, 20, "narrow"))...)
	messages = append(messages, projectionPair("wide", "read_code", projectionEvidence("e1", "read_code", "main.go", 1, 30, "wide"))...)
	original := cloneToolMessages(messages)

	projected, stats := projectContext(messages)
	if len(projected) != len(messages) {
		t.Fatalf("message count changed: %+v", projected)
	}
	for i := 1; i+1 < len(projected); i += 2 {
		if projected[i].Role != "assistant" || len(projected[i].ToolCalls) != 1 || projected[i+1].Role != "tool" || projected[i].ToolCalls[0].ID != projected[i+1].ToolCallID {
			t.Fatalf("assistant/tool sequence changed at %d: %+v", i, projected)
		}
	}
	if !strings.Contains(projected[2].Content, "read_code_covered") || projected[4].Content != messages[4].Content {
		t.Fatalf("unexpected projection: %+v", projected)
	}
	if stats.DominatedReadCodeCompacted != 1 || stats.ProjectedBytes >= stats.FullBytes {
		t.Fatalf("stats=%+v", stats)
	}
	if messages[2].Content != original[2].Content {
		t.Fatal("projection mutated full conversation")
	}
}

func TestProjectContextCompactsOnlySuccessfulExactDuplicates(t *testing.T) {
	result := projectionEvidence("e1", "search_code", "main.go", 10, 10, "match")
	messages := []review.ToolMessage{{Role: "user", Content: "review"}}
	messages = append(messages, projectionPair("first", "search_code", result)...)
	messages = append(messages, projectionPair("second", "search_code", result)...)
	messages = append(messages, projectionPair("error1", "search_code", `{"evidence":null,"error":"search failed"}`)...)
	messages = append(messages, projectionPair("error2", "search_code", `{"evidence":null,"error":"search failed"}`)...)
	messages = append(messages, projectionPair("submit", "submit_claims", `{"accepted":false,"verdicts":[{"reason":"missing"}]}`)...)

	projected, stats := projectContext(messages)
	if !strings.Contains(projected[2].Content, `"reason":"duplicate"`) || projected[4].Content != result {
		t.Fatalf("successful duplicate handling=%+v", projected)
	}
	if projected[6].Content != messages[6].Content || projected[8].Content != messages[8].Content || projected[10].Content != messages[10].Content {
		t.Fatal("error or verdict feedback must not be compacted")
	}
	if stats.DuplicateResultsCompacted != 1 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestProjectContextDoesNotTreatPartialOverlapAsDominance(t *testing.T) {
	messages := []review.ToolMessage{{Role: "user", Content: "review"}}
	messages = append(messages, projectionPair("left", "read_code", projectionEvidence("e1", "read_code", "main.go", 1, 20, "left"))...)
	messages = append(messages, projectionPair("right", "read_code", projectionEvidence("e2", "read_code", "main.go", 15, 30, "right"))...)
	projected, stats := projectContext(messages)
	if projected[2].Content != messages[2].Content || projected[4].Content != messages[4].Content || stats.DominatedReadCodeCompacted != 0 {
		t.Fatalf("partial overlap was compacted: %+v %+v", projected, stats)
	}
}

func TestProjectContextDoesNotCompactDisjointReadCodeEvidence(t *testing.T) {
	disjoint, _ := json.Marshal(map[string]any{"evidence": []*Evidence{
		{ID: "e1", Source: "read_code", File: "main.go", Line: 1, EndLine: 5, Content: "first"},
		{ID: "e2", Source: "read_code", File: "main.go", Line: 20, EndLine: 25, Content: "second"},
	}, "error": ""})
	covering := projectionEvidence("e3", "read_code", "main.go", 1, 25, "covering")
	messages := []review.ToolMessage{{Role: "user", Content: "review"}}
	messages = append(messages, projectionPair("disjoint", "read_code", string(disjoint))...)
	messages = append(messages, projectionPair("covering", "read_code", covering)...)

	projected, stats := projectContext(messages)
	if projected[2].Content != messages[2].Content || stats.DominatedReadCodeCompacted != 0 {
		t.Fatalf("disjoint evidence was compacted: %+v %+v", projected, stats)
	}
}
