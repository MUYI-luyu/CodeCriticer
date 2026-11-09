package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

type messageScriptLLM struct {
	replies     []review.ToolMessage
	transcripts [][]review.ToolMessage
}

func (s *messageScriptLLM) CompleteToolsWithUsage(_ context.Context, _ string, messages []review.ToolMessage, _ []review.ToolDefinition, _ string) (review.ToolMessage, review.LLMUsage, error) {
	s.transcripts = append(s.transcripts, cloneToolMessages(messages))
	if len(s.replies) == 0 {
		return assistantCall("submit_claims", `{"claims":[]}`), review.LLMUsage{}, nil
	}
	reply := s.replies[0]
	s.replies = s.replies[1:]
	return reply, review.LLMUsage{}, nil
}

func TestPlainTextFinalGetsOneProtocolRepairTurn(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {}\n", 3)
	llm := &messageScriptLLM{replies: []review.ToolMessage{
		{Role: "assistant", Content: "I found the copylocks issue."},
		assistantCall("submit_claims", `{"claims":[]}`),
	}}
	run, err := NewReviewRun(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	result, err := run.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err != nil {
		t.Fatal(err)
	}
	if result.Trace.StopReason != StopAgentDone || len(llm.transcripts) != 2 || len(result.Trace.Errors) != 1 {
		t.Fatalf("trace=%+v calls=%d", result.Trace, len(llm.transcripts))
	}
	if len(llm.transcripts[1]) != 4 || !messagesContain(llm.transcripts[1], "Protocol reminder") {
		t.Fatalf("repair prompt missing: %+v", llm.transcripts[1])
	}
}

func TestReviewAllowsModelToSubmitWithoutPrescribedToolOrder(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {\n}\n", 3)
	llm := &scriptedLLM{calls: []review.ToolCall{toolCall("submit_claims", `{"claims":[]}`)}}
	run, err := NewReviewRun(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	result, err := run.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err != nil {
		t.Fatal(err)
	}
	if len(llm.transcripts) != 1 || result.Trace.StopReason != StopAgentDone || result.Trace.ReviewState.Phase != "done" {
		t.Fatalf("trace=%+v", result.Trace)
	}
}

func TestReviewExposesDerivedInvestigationIndexOnNextDecision(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {}\n", 3)
	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("read_diff", `{"file":"main.go","hunk_id":"h1"}`),
		toolCall("submit_claims", `{"claims":[]}`),
	}}
	run, err := NewReviewRun(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Run(context.Background(), Request{Repo: repo, Diff: diffData}); err != nil {
		t.Fatal(err)
	}
	if len(llm.transcripts) != 2 {
		t.Fatalf("model calls=%d", len(llm.transcripts))
	}
	second := llm.transcripts[1]
	if len(second) < 2 || !strings.Contains(second[1].Content, `"diff_hunks_returned"`) || !strings.Contains(second[1].Content, `"hunk_id":"h1"`) {
		t.Fatalf("next decision did not receive the derived diff index: %+v", second)
	}
}

func TestInvestigationIndexSurvivesContextProjection(t *testing.T) {
	repo := t.TempDir()
	largeLine := "// " + strings.Repeat("x", 70<<10)
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"+largeLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/index\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diffData := []byte("diff --git a/main.go b/main.go\n--- /dev/null\n+++ b/main.go\n@@ -0,0 +1,2 @@\n+package main\n+" + largeLine + "\n")
	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("read_diff", `{"file":"main.go","hunk_id":"h1"}`),
		toolCall("submit_claims", `{"claims":[]}`),
	}}
	run, err := NewReviewRun(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	run.SetModelContextBudget(12 << 10)
	if _, err := run.Run(context.Background(), Request{Repo: repo, Diff: diffData}); err != nil {
		t.Fatal(err)
	}
	if len(llm.transcripts) != 2 || !messagesContain(llm.transcripts[1], `"hunk_id":"h1"`) {
		t.Fatalf("projected request lost delivered hunk index: %+v", llm.transcripts)
	}
	for decision, messages := range llm.transcripts {
		if got := toolMessagesBytes(messages); got > 12<<10 {
			t.Fatalf("request %d is %d bytes, over budget", decision, got)
		}
	}
}

func TestContextProjectionReplacesInlineKernelWithManifest(t *testing.T) {
	repo := t.TempDir()
	inlineBody := "// inline-body-" + strings.Repeat("z", 6<<10)
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"+inlineBody+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/kernel\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diffData := []byte("diff --git a/main.go b/main.go\n--- /dev/null\n+++ b/main.go\n@@ -0,0 +1,2 @@\n+package main\n+" + inlineBody + "\n")
	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"file_start"}}`),
		toolCall("submit_claims", `{"claims":[]}`),
	}}
	run, err := NewReviewRun(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	run.SetModelContextBudget(12 << 10)
	if _, err := run.Run(context.Background(), Request{Repo: repo, Diff: diffData}); err != nil {
		t.Fatal(err)
	}
	if len(llm.transcripts) != 2 || !strings.Contains(llm.transcripts[0][0].Content, "inline-body-") {
		t.Fatalf("initial request did not include the expected hunk body: %+v", llm.transcripts)
	}
	if strings.Contains(llm.transcripts[1][0].Content, "inline-body-") || !strings.Contains(llm.transcripts[1][0].Content, `"omitted_hunks":["main.go:h1"]`) {
		t.Fatalf("projected kernel did not retain only the manifest: %+v", llm.transcripts[1])
	}
}

func TestToolCallLimitProducesPartialOnlyWhenModelDoesNotSubmit(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {\n}\n", 3)
	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"symbol","symbol":"Run"}}`),
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"symbol","symbol":"Run"}}`),
	}}
	run, err := NewReviewRun(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	run.SetMaxToolCalls(1)
	run.SetMaxTurns(2)
	result, err := run.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err != nil {
		t.Fatal(err)
	}
	if result.Trace.StopReason != StopPartial || result.Trace.ReviewState.Phase != "partial" {
		t.Fatalf("trace=%+v", result.Trace)
	}
}

func TestTokenSafetyValveProducesPartialRatherThanCompletion(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {}\n", 3)
	llm := &scriptedLLM{
		calls:  []review.ToolCall{toolCall("read_code", `{"file":"main.go","selector":{"kind":"file_start"}}`)},
		usages: []review.LLMUsage{{PromptTokens: 9, CompletionTokens: 2, TotalTokens: 11}},
	}
	run, err := NewReviewRun(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	run.SetMaxTotalTokens(10)
	result, err := run.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err != nil {
		t.Fatal(err)
	}
	if result.Trace.StopReason != StopPartial || result.Trace.ReviewState.Phase != "partial" || len(llm.transcripts) != 1 {
		t.Fatalf("trace=%+v calls=%d", result.Trace, len(llm.transcripts))
	}
}
