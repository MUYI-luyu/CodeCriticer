package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

func TestLoadAndSelectProjectMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	data := `{"version":1,"entries":[{"id":"payment-error","path_prefix":"internal/payment","content":"Errors returned by the gateway must retain operation context.","tags":["errors"],"source":"human_confirmed"},{"id":"global","path_prefix":"","content":"Never log credentials.","source":"human_confirmed"},{"id":"other","path_prefix":"cmd","content":"Command flags are backwards compatible.","source":"human_confirmed"}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := LoadProjectMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	selected := selectProjectMemory(entries, []string{"internal/payment/charge.go"})
	if len(selected) != 2 || selected[0].ID != "payment-error" || selected[1].ID != "global" {
		t.Fatalf("selected=%+v", selected)
	}
}

func TestProjectMemoryRejectsUnconfirmedOrEscapingEntry(t *testing.T) {
	for _, fixture := range []string{
		`{"version":1,"entries":[{"id":"bad","path_prefix":"../secret","content":"x","source":"human_confirmed"}]}`,
		`{"version":1,"entries":[{"id":"bad","path_prefix":"","content":"x","source":"model_generated"}]}`,
		`{"version":1,"entries":[{"id":"bad","path_prefix":"","content":"x","source":"human_confirmed","unexpected":true}]}`,
		`{"version":1,"entries":[{"id":"bad","path_prefix":"","content":"x","source":"human_confirmed"}]} {"version":1,"entries":[]}`,
	} {
		path := filepath.Join(t.TempDir(), "memory.json")
		if err := os.WriteFile(path, []byte(fixture), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadProjectMemory(path); err == nil {
			t.Fatalf("fixture was accepted: %s", fixture)
		}
	}
}

func TestSelectProjectMemoryRespectsDirectoryBoundariesAndLimit(t *testing.T) {
	entries := []MemoryEntry{
		{ID: "prefix-only", PathPrefix: "internal/pay", Content: "must not match payment", Source: "human_confirmed"},
		{ID: "specific", PathPrefix: "internal/payment", Content: "specific", Source: "human_confirmed"},
		{ID: "global", PathPrefix: "", Content: "global", Source: "human_confirmed"},
	}
	for i := 0; i < 8; i++ {
		entries = append(entries, MemoryEntry{ID: "payment-" + string(rune('a'+i)), PathPrefix: "internal/payment", Content: "rule", Source: "human_confirmed"})
	}
	selected := selectProjectMemory(entries, []string{"internal/payment/charge.go"})
	if len(selected) != 6 {
		t.Fatalf("selected=%d: %+v", len(selected), selected)
	}
	for _, entry := range selected {
		if entry.ID == "prefix-only" {
			t.Fatalf("path prefix leaked across directory boundary: %+v", selected)
		}
	}
	if selected[0].ID != "payment-a" || selected[len(selected)-1].ID != "payment-f" {
		t.Fatalf("selection is not deterministic/specific-first: %+v", selected)
	}
	if got := selectProjectMemory(entries, nil); len(got) != 0 {
		t.Fatalf("memory should not be injected without changed paths: %+v", got)
	}
}

func TestProjectMemoryRejectsOversizedContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	fixture := `{"version":1,"entries":[{"id":"too-long","path_prefix":"","content":"` + strings.Repeat("x", 1201) + `","source":"human_confirmed"}]}`
	if err := os.WriteFile(path, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProjectMemory(path); err == nil {
		t.Fatal("oversized memory entry was accepted")
	}
}

func TestProjectMemoryIsPathScopedInTraceAndModelPrompt(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {}\n", 3)
	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"symbol","symbol":"Run"}}`),
		toolCall("submit_claims", `{"claims":[]}`),
	}}
	run, err := NewReviewRun(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	run.SetProjectMemory([]MemoryEntry{
		{ID: "matching", PathPrefix: "", Content: "matching project rule", Source: "human_confirmed"},
		{ID: "unrelated", PathPrefix: "internal/payment", Content: "unrelated project rule", Source: "human_confirmed"},
	})
	result, err := run.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trace.RetrievedMemory) != 1 || result.Trace.RetrievedMemory[0].ID != "matching" {
		t.Fatalf("retrieved memory=%+v", result.Trace.RetrievedMemory)
	}
	if len(llm.transcripts) == 0 {
		t.Fatal("model was never called")
	}
	prompt := llm.transcripts[0][0].Content
	if !strings.Contains(prompt, "matching project rule") || strings.Contains(prompt, "unrelated project rule") {
		t.Fatalf("path-scoped memory prompt=%s", prompt)
	}
}
