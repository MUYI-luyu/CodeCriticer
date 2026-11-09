package workflow

import "testing"

func TestAgentStateIsTraceMetadataNotPromptMemory(t *testing.T) {
	state := newAgentState(&Trace{Scope: ReviewScope{TargetFiles: []string{"main.go"}}})
	if got := state.snapshot(); got.Phase != "investigate" || state.prompt() != "" {
		t.Fatalf("unexpected initial state: %+v\nprompt=%s", got, state.prompt())
	}
	for i := 0; i < 14; i++ {
		state.observe("read_code", []*Evidence{{ID: "e" + string(rune('a'+i)), Source: "read_code", File: "main.go", Line: i + 1, Content: "code"}}, "")
	}
	got := state.snapshot()
	if len(got.Facts) != 12 || got.Facts[0].EvidenceID != "ec" || got.Facts[11].EvidenceID != "en" {
		t.Fatalf("facts must retain newest twelve: %+v", got.Facts)
	}
	if len(got.RecentActions) != 4 || state.prompt() != "" {
		t.Fatalf("state=%+v prompt=%s", got, state.prompt())
	}
}
