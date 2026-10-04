package eval

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunToolProbeUsesProductionToolsAndCoversAnnotatedLocations(t *testing.T) {
	c := &Case{
		Name: "probe",
		Repo: map[string]string{"main.go": "package main\n\nfunc Run() {\n\tprintln(1)\n}\n"},
		Diff: []byte("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -4 +4 @@\n-\tprintln(0)\n+\tprintln(1)\n"),
		GT:   GroundTruth{Primary: Location{File: "main.go", Line: 4}, Related: []Location{{File: "main.go", Line: 3}}},
	}
	result := runToolProbe(context.Background(), c)
	if !result.PrimaryCovered || result.RelatedCovered != 1 || len(result.Evidence) == 0 {
		t.Fatalf("probe=%+v", result)
	}
	for _, evidence := range result.Evidence {
		if evidence.Source == "diff" {
			t.Fatalf("probe leaked input evidence: %+v", evidence)
		}
	}
}

func TestInferFailureLayerUsesInterventionDecisionTable(t *testing.T) {
	base := caseResult{attrs: []BugAttribution{{InvestigationHit: false}}}
	diagnosis := FailureDiagnosis{Probe: ToolProbeResult{PrimaryCovered: true}, EvidenceInjection: DiagnosticIntervention{LocationMatch: true}}
	layer, _ := inferFailureLayer(base, diagnosis)
	if layer != "tool_selection_or_sampling" {
		t.Fatalf("layer=%s", layer)
	}

	base.attrs[0].InvestigationHit = true
	layer, _ = inferFailureLayer(base, diagnosis)
	if layer != "evidence_use_or_sampling" {
		t.Fatalf("layer=%s", layer)
	}

	diagnosis.EvidenceInjection = DiagnosticIntervention{}
	diagnosis.FullContext = DiagnosticIntervention{LocationMatch: true}
	layer, _ = inferFailureLayer(base, diagnosis)
	if layer != "context_or_sampling" {
		t.Fatalf("layer=%s", layer)
	}

	diagnosis.FullContext = DiagnosticIntervention{}
	layer, _ = inferFailureLayer(base, diagnosis)
	if layer != "unresolved_after_interventions" {
		t.Fatalf("layer=%s", layer)
	}

	diagnosis.Probe = ToolProbeResult{Errors: []string{"failed"}}
	layer, _ = inferFailureLayer(base, diagnosis)
	if layer != "diagnostic_infrastructure" {
		t.Fatalf("layer=%s", layer)
	}
}

func TestInterventionFoundPrefersSemanticOutcome(t *testing.T) {
	result := DiagnosticIntervention{
		LocationMatch: true,
		Semantic:      &SemanticEvaluation{ExpectedIssue: "missed"},
	}
	if interventionFound(result) {
		t.Fatal("semantic mismatch must not be hidden by a positional match")
	}
	result.Semantic = &SemanticEvaluation{ExpectedIssue: "found"}
	if !interventionFound(result) {
		t.Fatal("semantic match should count as recovered")
	}
	result.Semantic = &SemanticEvaluation{ExpectedIssue: "found", Error: "judge failed"}
	if !interventionFound(result) {
		t.Fatal("judge failure should fall back to deterministic location match")
	}
}

func TestDiagnosticProbeCallsAreBounded(t *testing.T) {
	c := &Case{GT: GroundTruth{Primary: Location{File: "main.go", Line: 1}}}
	for i := 2; i <= 20; i++ {
		c.GT.Related = append(c.GT.Related, Location{File: "main.go", Line: i})
	}
	if calls := diagnosticProbeCalls(c); len(calls) != 8 {
		t.Fatalf("calls=%d", len(calls))
	}
}

func TestDiagnosticProbeUsesFileStartForFileLevelGroundTruth(t *testing.T) {
	c := &Case{GT: GroundTruth{Primary: Location{File: "main.go"}}}
	calls := diagnosticProbeCalls(c)
	if len(calls) != 1 || calls[0].Function.Name != "read_code" || !strings.Contains(calls[0].Function.Arguments, `"kind":"file_start"`) {
		t.Fatalf("calls=%+v", calls)
	}
}

func TestDiagnoseFailuresHonorsCancellationBeforeInterventions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := diagnoseFailures(ctx, nil, nil, "testdata/cases", "", []caseResult{{name: "bools", completed: true}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
