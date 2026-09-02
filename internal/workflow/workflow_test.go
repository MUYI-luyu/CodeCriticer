package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

type scriptedLLM struct{ decisions []string }

func (s *scriptedLLM) Plan(context.Context, string) ([]review.Point, review.LLMUsage, error) {
	return []review.Point{{Desc: "检查修改函数的调用关系", Kw: []string{"Run"}}}, review.LLMUsage{}, nil
}

func TestTraceSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "trace.json")
	trace := &Trace{ID: "trace-test", StopReason: "validated"}
	if err := trace.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("trace mode = %o, want 600", info.Mode().Perm())
	}
	var decoded Trace
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.ID != trace.ID {
		t.Fatalf("invalid trace JSON: %v", err)
	}
}
func (s *scriptedLLM) ChatWithUsage(context.Context, string, string, string) (string, review.LLMUsage, error) {
	out := s.decisions[0]
	s.decisions = s.decisions[1:]
	return out, review.LLMUsage{}, nil
}
func (s *scriptedLLM) Review(context.Context, string) ([]review.Finding, review.LLMUsage, error) {
	return []review.Finding{{File: "main.go", Line: 4, Severity: "warning", Msg: "reviewed change", Evidence: "return value"}}, review.LLMUsage{}, nil
}

func TestWorkflowUsesEvidenceBeforeFinishing(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n\nfunc Run() {\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/test\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	diff := []byte("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -2,3 +2,3 @@\n \n-func Run() {\n+func Run() {\n }\n")
	llm := &scriptedLLM{decisions: []string{`{"done":false,"tool":"read_code","args":{"file":"main.go"}}`, `{"done":true}`}}
	wf, err := New(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diff})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trace.Evidence) != 1 || result.Trace.Evidence[0].Type != "code" {
		t.Fatalf("unexpected evidence: %+v", result.Trace.Evidence)
	}
	if len(result.Trace.ToolCalls) != 1 || result.Trace.ToolCalls[0].Tool != "read_code" {
		t.Fatalf("unexpected calls: %+v", result.Trace.ToolCalls)
	}
	if len(result.Trace.Findings) != 1 || !result.Trace.Validations[0].Accepted {
		t.Fatalf("unexpected result: %+v", result.Trace)
	}
}
