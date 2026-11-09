package workflow

import (
	"strings"
	"testing"
)

func TestDeriveInvestigationIndexAcceptsReplayedTraceArguments(t *testing.T) {
	trace := &Trace{
		Evidence: []*Evidence{{ID: "e7", StepID: "s4", Source: "read_diff", File: "main.go"}},
		Investigation: []InvestigationStep{{
			ID: "s4", Tool: "read_diff", Args: map[string]any{"file": "main.go", "hunk_id": "h2"},
		}},
	}
	index := deriveInvestigationIndex(trace)
	if len(index.DiffHunksReturned) != 1 || index.DiffHunksReturned[0].HunkID != "h2" || index.DiffHunksReturned[0].EvidenceIDs[0] != "e7" {
		t.Fatalf("replayed trace did not derive a hunk index: %+v", index)
	}
}

func TestInvestigationOutcomeSeparatesNoNewEvidenceFromToolFailure(t *testing.T) {
	trace := &Trace{Investigation: []InvestigationStep{
		{ID: "s1", Tool: "read_evidence", Error: "工具调用未产生新证据"},
		{ID: "s2", Tool: "read_code", Error: "lstat missing.go: no such file or directory"},
	}}
	index := deriveInvestigationIndex(trace)
	if index.ToolOutcomes[0].Status != "returned_no_new_evidence" || index.ToolOutcomes[1].Status != "error" {
		t.Fatalf("outcomes=%+v", index.ToolOutcomes)
	}
}

func TestInvestigationIndexDoesNotPresentEvidenceAsAReReadQueue(t *testing.T) {
	prompt := investigationIndexPrompt(&Trace{})
	for _, want := range []string{"stable Claim references", "not a re-read checklist", "only when an exact source detail is necessary"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("index prompt missing %q: %s", want, prompt)
		}
	}
}
