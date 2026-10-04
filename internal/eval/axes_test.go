package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

func TestComputeAxesKeepsDimensionsIndependent(t *testing.T) {
	trace := &workflow.Trace{
		Evidence: []*workflow.Evidence{
			{ID: "e1", Source: "diff", File: "main.go", Line: 10},
			{ID: "e2", Source: "read_code", File: "main.go", Line: 1, EndLine: 20},
		},
		FinalReport: workflow.FinalReport{
			Claims: []review.CandidateClaim{
				{ID: "c1", EvidenceIDs: []string{"e2"}},
				{ID: "c2", EvidenceIDs: []string{"missing"}},
			},
			Verdicts: []workflow.Verdict{{ClaimID: "c1", Status: workflow.VerdictAccepted}},
		},
		Stats:    workflow.TraceStats{DecisionCount: 4, SuccessfulToolCalls: 1, FailedToolCalls: 1, DuplicateCalls: 1, NoNewEvidenceCalls: 1},
		Usage:    review.LLMUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		LLMCalls: []review.LLMCall{{}, {Error: "timeout"}}, StopReason: workflow.StopAgentDone,
	}
	axes := ComputeAxes(trace, true)
	if axes.Outcome.RawClaims != 2 || axes.Outcome.AcceptedClaims != 1 {
		t.Fatalf("outcome=%+v", axes.Outcome)
	}
	if axes.Evidence.ClaimsWithResolvedRefs != 1 || axes.Evidence.ClaimsWithUnresolvedRefs != 1 || axes.Evidence.Sources["read_code"] != 1 {
		t.Fatalf("evidence=%+v", axes.Evidence)
	}
	if axes.Investigation.ToolEvidence != 1 || axes.Investigation.NoNewEvidenceCalls != 1 {
		t.Fatalf("investigation=%+v", axes.Investigation)
	}
	if axes.Efficiency.TotalTokens != 120 || axes.Efficiency.DuplicateCalls != 1 {
		t.Fatalf("efficiency=%+v", axes.Efficiency)
	}
	if !axes.Infra.Completed || axes.Infra.LLMFailures != 1 {
		t.Fatalf("infra=%+v", axes.Infra)
	}
}

func TestAcceptableLocationsMatchOneIssueWithoutInflatingBugCount(t *testing.T) {
	c := &Case{GT: GroundTruth{
		Primary:             Location{File: "main.go", Line: 10},
		AcceptableLocations: []Location{{File: "main.go", Line: 30}},
	}}
	trace := &workflow.Trace{FinalReport: workflow.FinalReport{
		Claims:   []review.CandidateClaim{{ID: "c1", File: "main.go", Line: 30}},
		Verdicts: []workflow.Verdict{{ClaimID: "c1", Status: workflow.VerdictAccepted}},
	}}
	metrics := ComputeTrace(c, trace, 0)
	if metrics.AcceptedLine.Bugs != 1 || metrics.AcceptedLine.TP != 1 || metrics.AcceptedLine.FN != 0 {
		t.Fatalf("metrics=%+v", metrics.AcceptedLine)
	}
}

type fakeSemanticJudgeClient struct {
	messages []review.ToolMessage
	tools    []review.ToolDefinition
}

func (f *fakeSemanticJudgeClient) AgentModel() string { return "judge-model" }

func (f *fakeSemanticJudgeClient) CompleteToolsWithUsage(_ context.Context, _ string, messages []review.ToolMessage, tools []review.ToolDefinition, _ string) (review.ToolMessage, review.LLMUsage, error) {
	f.messages = append([]review.ToolMessage(nil), messages...)
	f.tools = tools
	return review.ToolMessage{Role: "assistant", ToolCalls: []review.ToolCall{{
		ID: "judge-call", Type: "function", Function: review.FunctionCall{Name: "submit_semantic_evaluation", Arguments: `{"expected_issue":"found","reason":"same cause","claims":[{"claim_id":"c1","outcome":"same_issue","outcome_reason":"matches","evidence":"supported","evidence_reason":"cited code supports it"}]}`},
	}}}, review.LLMUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, nil
}

func TestSemanticJudgeSeparatesOutcomeFromCitedEvidence(t *testing.T) {
	client := &fakeSemanticJudgeClient{}
	judge := newSemanticJudge(client, "")
	c := &Case{GT: GroundTruth{Primary: Location{File: "main.go", Line: 10}, Description: "nil dereference"}}
	trace := &workflow.Trace{
		Evidence: []*workflow.Evidence{
			{ID: "e1", Source: "read_code", Content: "cited"},
			{ID: "e2", Source: "read_code", Content: "must not be visible to evidence judge"},
		},
		FinalReport: workflow.FinalReport{
			Claims:   []review.CandidateClaim{{ID: "c1", File: "main.go", Line: 10, Msg: "nil dereference", EvidenceIDs: []string{"e1"}}},
			Verdicts: []workflow.Verdict{{ClaimID: "c1", Status: workflow.VerdictAccepted}},
		},
	}
	result := judge.Grade(context.Background(), c, trace)
	if result.Error != "" || result.ExpectedIssue != "found" || len(result.Claims) != 1 || result.Claims[0].Evidence != "supported" || result.Usage.TotalTokens != 15 {
		t.Fatalf("result=%+v", result)
	}
	if len(client.messages) != 1 || strings.Contains(client.messages[0].Content, "must not be visible") || !strings.Contains(client.messages[0].Content, "cited") {
		t.Fatalf("judge input=%+v", client.messages)
	}
	if !strings.Contains(client.messages[0].Content, `"verdict":"accepted"`) {
		t.Fatalf("judge input omitted verdict: %s", client.messages[0].Content)
	}
	if len(client.tools) != 1 || !client.tools[0].Function.Strict {
		t.Fatalf("tools=%+v", client.tools)
	}
}

func TestSemanticJudgeRejectsIncompleteClaimGrades(t *testing.T) {
	client := &fakeSemanticJudgeClient{}
	judge := newSemanticJudge(client, "")
	c := &Case{GT: GroundTruth{Primary: Location{File: "main.go", Line: 10}}}
	trace := &workflow.Trace{FinalReport: workflow.FinalReport{Claims: []review.CandidateClaim{
		{ID: "c1", File: "main.go", Line: 10},
		{ID: "c2", File: "main.go", Line: 20},
	}}}
	result := judge.Grade(context.Background(), c, trace)
	if !strings.Contains(result.Error, `omitted claim "c2"`) {
		t.Fatalf("expected omitted-claim error, got %+v", result)
	}
}

func TestSemanticJudgeRejectsVerdictAndEvidenceContradictions(t *testing.T) {
	trace := &workflow.Trace{FinalReport: workflow.FinalReport{
		Claims:   []review.CandidateClaim{{ID: "c1", EvidenceIDs: []string{"missing"}}},
		Verdicts: []workflow.Verdict{{ClaimID: "c1", Status: workflow.VerdictRejected}},
	}}
	result := SemanticEvaluation{
		ExpectedIssue: "found",
		Claims:        []SemanticClaimGrade{{ClaimID: "c1", Outcome: "same_issue", Evidence: "supported"}},
	}
	if err := validateSemanticEvaluation(result, trace); err == nil || !strings.Contains(err.Error(), "resolved cited evidence") {
		t.Fatalf("expected unsupported evidence contradiction, got %v", err)
	}

	trace.Evidence = []*workflow.Evidence{{ID: "missing"}}
	if err := validateSemanticEvaluation(result, trace); err == nil || !strings.Contains(err.Error(), "accepted same-issue claim") {
		t.Fatalf("expected rejected-claim contradiction, got %v", err)
	}
}

func TestAxesSummaryDoesNotCreateCompositeScore(t *testing.T) {
	var summary EvaluationAxesSummary
	summary = summary.Add(EvaluationAxes{
		Outcome:       OutcomeAxis{RawClaims: 2, AcceptedClaims: 1},
		Investigation: InvestigationAxis{SuccessfulTools: 3},
		Efficiency:    EfficiencyAxis{TotalTokens: 100},
		Infra:         InfraAxis{Completed: true},
	})
	summary = summary.Add(EvaluationAxes{Infra: InfraAxis{Completed: false}})
	if summary.Cases != 2 || summary.Outcome.RawClaims != 2 || summary.Investigation.SuccessfulTools != 3 || summary.Efficiency.TotalTokens != 100 || summary.InfraFailures != 1 {
		t.Fatalf("summary=%+v", summary)
	}
}
