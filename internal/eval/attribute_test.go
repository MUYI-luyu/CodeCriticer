package eval

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

// TestAttributeStages 覆盖输入、调查、Claim、Verdict 四段信号。
func TestAttributeStages(t *testing.T) {
	c := &Case{GT: GroundTruth{
		Primary: Location{File: "a.go", Line: 10},
		Related: []Location{{File: "b.go", Line: 20}},
	}}
	trace := &workflow.Trace{
		Evidence: []*workflow.Evidence{
			{Source: "diff", File: "a.go", Line: 10},
			{Source: "read_code", File: "a.go", Line: 8, EndLine: 12},
			{Source: "find_callers", File: "b.go", Line: 20},
		},
		FinalReport: workflow.FinalReport{
			Claims:   []review.CandidateClaim{{ID: "c1", File: "a.go", Line: 10}},
			Verdicts: []workflow.Verdict{{ClaimID: "c1", Status: workflow.VerdictAccepted}},
		},
	}
	got := Attribute(c, trace, tol)
	if len(got) != 1 {
		t.Fatalf("每个 Case 应只归因一个 Primary: %+v", got)
	}
	attr := got[0]
	if attr.Stage != StageSuccess || !attr.InputHit || !attr.InvestigationHit ||
		!attr.RawClaimHit || !attr.AcceptedClaimHit {
		t.Fatalf("四阶段信号不完整: %+v", attr)
	}
	if attr.RelatedEvidenceCovered != 1 || attr.RelatedEvidenceTotal != 1 {
		t.Fatalf("Related 只应计入调查覆盖: %+v", attr)
	}

	counts := AttributionCounts{}.Add(got)
	if counts.Total() != 1 || counts.Success != 1 {
		t.Errorf("归因汇总异常: %+v", counts)
	}
}

func TestClassifyFirstUnclosedStage(t *testing.T) {
	tests := []struct {
		name                                string
		input, investigation, raw, accepted bool
		want                                BugStage
	}{
		{"输入漏", false, false, false, false, StageInputMiss},
		{"调查漏", true, false, false, false, StageInvestigationMiss},
		{"Claim漏", true, true, false, false, StageClaimMiss},
		{"Verdict误杀", true, true, true, false, StageVerdictSelfHarm},
		{"成功", true, true, true, true, StageSuccess},
	}
	for _, test := range tests {
		if got := classify(test.input, test.investigation, test.raw, test.accepted); got != test.want {
			t.Errorf("%s: got=%s want=%s", test.name, got, test.want)
		}
	}
}

// TestSaveTraceRoundtrip 验证 trace 落盘后能读回、且含召回全文。
func TestSaveTraceRoundtrip(t *testing.T) {
	dir := t.TempDir()
	tr := EvalTrace{
		Name:         "pkg/case-1", // 带分隔符，验证 safeName
		Bugs:         []Bug{{File: "a.go", Line: 10, Desc: "data race"}},
		Workflow:     &workflow.Trace{Evidence: []*workflow.Evidence{{File: "a.go", Line: 10, Content: "go func() { x++ }()"}}},
		Attributions: []BugAttribution{{Bug: Bug{File: "a.go", Line: 10}, Stage: StageSuccess}},
	}
	if err := SaveTrace(dir, tr); err != nil {
		t.Fatalf("SaveTrace: %v", err)
	}

	path := filepath.Join(dir, "pkg_case-1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回 trace: %v", err)
	}
	var back EvalTrace
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("反序列化: %v", err)
	}
	if len(back.Workflow.Evidence) != 1 {
		t.Fatalf("召回轨迹丢失: %+v", back.Workflow)
	}
	if txt := back.Workflow.Evidence[0].Content; txt != "go func() { x++ }()" {
		t.Errorf("召回全文未保留，got %q", txt)
	}
}

func TestRescoreTraceDirUsesCurrentManifestLabels(t *testing.T) {
	dataset := t.TempDir()
	caseDir := filepath.Join(dataset, "case-a")
	if err := os.MkdirAll(caseDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataset, "manifest.json"), []byte(`{"cases":[{"directory":"case-a","category":"semantic_positive","ground_truth":{"primary":{"file":"main.go","line":10},"acceptable_locations":[{"file":"main.go","line":30}]}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "case.json"), []byte(`{"name":"case-a","file":"main.go","ground_truth":{"primary":{"file":"main.go","line":10}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"buggy.go", "fixed.go"} {
		if err := os.WriteFile(filepath.Join(caseDir, name), []byte("package main\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	traces := t.TempDir()
	if err := SaveTrace(traces, EvalTrace{Name: "case-a", Workflow: &workflow.Trace{FinalReport: workflow.FinalReport{
		Claims:   []review.CandidateClaim{{ID: "c1", File: "main.go", Line: 30}},
		Verdicts: []workflow.Verdict{{ClaimID: "c1", Status: workflow.VerdictAccepted}},
	}}}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RescoreTraceDir(&out, dataset, traces); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "status=done") || !strings.Contains(out.String(), "accepted_hit=1") || !strings.Contains(out.String(), "TP=1") {
		t.Fatalf("rescore did not use acceptable location: %s", out.String())
	}
}
