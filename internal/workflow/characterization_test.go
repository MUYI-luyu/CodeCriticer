package workflow

import (
	"context"
	"reflect"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

// TestCharacterizationBaseline freezes the current single-session contract at
// the boundary that Phase 1 is intended to preserve. The fake LLM responses
// are deliberately deterministic so the test records the request count, tool
// sequence, evidence provenance, verdict, stop reason, and token accounting.
func TestCharacterizationBaseline(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {\n}\n", 3)
	llm := &characterizationLLM{
		responses: []review.ToolMessage{
			assistantCall("read_code", `{"file":"main.go","selector":{"kind":"file_start"}}`),
			assistantCall("submit_claims", `{"claims":[{"file":"main.go","line":3,"severity":"warning","msg":"Run has an issue","evidence_ids":["e2"]}]}`),
		},
		usages: []review.LLMUsage{
			{PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14},
			{PromptTokens: 20, CompletionTokens: 5, TotalTokens: 25},
		},
	}
	wf, err := NewReviewRun(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err != nil {
		t.Fatal(err)
	}
	if len(llm.requests) != 2 {
		t.Fatalf("requests=%d", len(llm.requests))
	}
	if !reflect.DeepEqual(llm.systems, []string{agentSystemPrompt, agentSystemPrompt}) {
		t.Fatalf("systems=%q", llm.systems)
	}
	if !reflect.DeepEqual(llm.models, []string{"characterization-model", "characterization-model"}) {
		t.Fatalf("models=%q", llm.models)
	}
	expectedTools := []string{"read_diff", "read_code", "search_code", "inspect_symbol", "inspect_concurrency", "run_static_rules", "run_go_validation", "submit_claims"}
	for requestIndex, definitions := range llm.tools {
		if len(definitions) != len(expectedTools) {
			t.Fatalf("request %d tools=%d", requestIndex, len(definitions))
		}
		for i, definition := range definitions {
			if definition.Function.Name != expectedTools[i] || !definition.Function.Strict {
				t.Fatalf("request %d tool %d=%+v", requestIndex, i, definition.Function)
			}
		}
	}
	if len(llm.requests[0]) != 1 || len(llm.requests[1]) != 3 || llm.requests[1][1].Role != "assistant" || llm.requests[1][2].Role != "tool" || llm.requests[1][2].ToolCallID != llm.requests[1][1].ToolCalls[0].ID {
		t.Fatalf("request history=%+v", llm.requests)
	}
	if len(result.Trace.Investigation) != 1 || result.Trace.Investigation[0].Tool != "read_code" || result.Trace.Investigation[0].Error != "" {
		t.Fatalf("investigation=%+v", result.Trace.Investigation)
	}
	if result.Trace.Stats.DecisionCount != 2 || result.Trace.Stats.SuccessfulToolCalls != 1 || result.Trace.Stats.FailedToolCalls != 0 || result.Trace.Stats.DuplicateCalls != 0 || result.Trace.Stats.NoNewEvidenceCalls != 0 {
		t.Fatalf("stats=%+v", result.Trace.Stats)
	}
	if len(result.Trace.Evidence) != 3 || result.Trace.Evidence[0].ID != "e1" || result.Trace.Evidence[1].ID != "e2" || result.Trace.Evidence[2].ID != "e3" || result.Trace.Evidence[2].StepID != "s1" {
		t.Fatalf("evidence=%+v", result.Trace.Evidence)
	}
	if len(result.Trace.FinalReport.Claims) != 1 || len(result.Trace.FinalReport.Verdicts) != 1 || result.Trace.FinalReport.Claims[0].ID != "c1" || result.Trace.FinalReport.Verdicts[0].Status != VerdictAccepted {
		t.Fatalf("final_report=%+v", result.Trace.FinalReport)
	}
	if result.Trace.StopReason != StopAgentDone {
		t.Fatalf("stop_reason=%q", result.Trace.StopReason)
	}
	if result.Trace.Usage.PromptTokens != 30 || result.Trace.Usage.CompletionTokens != 9 || result.Trace.Usage.TotalTokens != 39 {
		t.Fatalf("usage=%+v", result.Trace.Usage)
	}
	if len(result.Trace.Conversation) != 4 || result.Trace.Conversation[0].Role != "user" || result.Trace.Conversation[1].ToolCalls[0].Function.Name != "read_code" || result.Trace.Conversation[3].ToolCalls[0].Function.Name != "submit_claims" {
		t.Fatalf("conversation=%+v", result.Trace.Conversation)
	}
}

type characterizationLLM struct {
	responses []review.ToolMessage
	usages    []review.LLMUsage
	requests  [][]review.ToolMessage
	systems   []string
	tools     [][]review.ToolDefinition
	models    []string
}

func (l *characterizationLLM) AgentModel() string { return "characterization-model" }

func (l *characterizationLLM) CompleteToolsWithUsage(_ context.Context, system string, messages []review.ToolMessage, tools []review.ToolDefinition, model string) (review.ToolMessage, review.LLMUsage, error) {
	l.requests = append(l.requests, append([]review.ToolMessage(nil), messages...))
	l.systems = append(l.systems, system)
	l.tools = append(l.tools, append([]review.ToolDefinition(nil), tools...))
	l.models = append(l.models, model)
	idx := len(l.requests) - 1
	return l.responses[idx], l.usages[idx], nil
}
