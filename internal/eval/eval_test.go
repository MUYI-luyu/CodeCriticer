package eval

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

func TestDetectFailureStage(t *testing.T) {
	tests := []struct {
		name  string
		trace *workflow.Trace
		err   error
		want  string
	}{
		{
			name:  "Agent 请求失败",
			trace: &workflow.Trace{LLMCalls: []review.LLMCall{{Stage: "agent", Error: "timeout"}}},
			err:   errors.New("agent timeout"),
			want:  "agent",
		},
		{
			name:  "Agent 决策失败",
			trace: &workflow.Trace{LLMCalls: []review.LLMCall{{Stage: "agent", Error: "EOF"}}},
			err:   errors.New("EOF"),
			want:  "agent",
		},
		{
			name: "Verifier 未产生记录",
			trace: &workflow.Trace{FinalReport: workflow.FinalReport{
				Claims: []review.CandidateClaim{{ID: "c1", File: "main.go", Line: 10}},
			}},
			err:  errors.New("verifier failed"),
			want: "verifier",
		},
	}
	for _, test := range tests {
		if got := detectFailureStage(test.trace, test.err); got != test.want {
			t.Errorf("%s: got=%s want=%s", test.name, got, test.want)
		}
	}
}

func TestRunConcurrentReturnsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runConcurrent(ctx, nil, nil, "testdata/cases", false, 1, "", runOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestAggregateMetricsExcludesInfrastructureFailures(t *testing.T) {
	total := TraceMetrics{}
	total = addCompletedMetrics(total, caseResult{completed: true, metrics: TraceMetrics{AcceptedLine: Metrics{Bugs: 1, Found: 1, TP: 1}}})
	total = addCompletedMetrics(total, caseResult{completed: false, metrics: TraceMetrics{AcceptedLine: Metrics{Bugs: 1, FN: 1}}})
	if total.AcceptedLine.Bugs != 1 || total.AcceptedLine.TP != 1 || total.AcceptedLine.FN != 0 {
		t.Fatalf("infra failure polluted outcome metrics: %+v", total.AcceptedLine)
	}
}

func TestFailedCaseReportsTraceSaveFailure(t *testing.T) {
	blockingPath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockingPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := failedCase(&Case{Name: "save-failure", GT: GroundTruth{Primary: Location{File: "main.go", Line: 1}}}, nil, "agent", errors.New("agent failed"), blockingPath)
	if !strings.Contains(result.output, "save eval trace") {
		t.Fatalf("save failure was swallowed: %s", result.output)
	}
}

func TestMaterializeRejectsRepositoryPathEscape(t *testing.T) {
	_, err := materialize(&Case{Repo: map[string]string{"../escape.go": "package escape"}})
	if err == nil || !strings.Contains(err.Error(), "escapes case root") {
		t.Fatalf("expected path escape rejection, got %v", err)
	}
}

func TestPrintABComparisonSeparatesOutcomeCostAndInfra(t *testing.T) {
	baseline := []caseResult{
		{name: "ok", completed: true, metrics: TraceMetrics{AcceptedLine: Metrics{Bugs: 1, Found: 1}}, axes: EvaluationAxes{Outcome: OutcomeAxis{AcceptedClaims: 1}, Efficiency: EfficiencyAxis{TotalTokens: 100}, Investigation: InvestigationAxis{SuccessfulTools: 2}}},
		{name: "failed", completed: false},
	}
	projected := []caseResult{
		{name: "ok", completed: true, metrics: TraceMetrics{AcceptedLine: Metrics{Bugs: 1, Found: 1}}, axes: EvaluationAxes{Outcome: OutcomeAxis{AcceptedClaims: 1}, Efficiency: EfficiencyAxis{TotalTokens: 80}, Investigation: InvestigationAxis{SuccessfulTools: 1}}, trace: &workflow.Trace{Projection: workflow.ContextProjectionStats{FullBytes: 200, ProjectedBytes: 150, DominatedReadCodeCompacted: 1}}},
		{name: "failed", completed: true},
	}
	var output bytes.Buffer
	printABComparison(&output, baseline, projected)
	text := output.String()
	if !strings.Contains(text, "1->1") || !strings.Contains(text, "-20") || !strings.Contains(text, "-50") || !strings.Contains(text, "infra/n-a") || !strings.Contains(text, "unseeded") {
		t.Fatalf("comparison=%s", text)
	}
}
