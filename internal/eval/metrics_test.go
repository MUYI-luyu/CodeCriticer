package eval

import (
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

func TestComputeExactMatch(t *testing.T) {
	bugs := []Bug{{File: "main.go", Line: 7}}
	fs := []review.CandidateClaim{{File: "main.go", Line: 7}}
	m := Compute(bugs, fs, 3)
	if m.True != 1 || m.False != 0 || m.Found != 1 {
		t.Fatalf("应精确命中: %+v", m)
	}
	if m.Recall() != 1 || m.Precision() != 1 || m.FPRate() != 0 {
		t.Fatalf("指标异常: %+v", m)
	}
}

func TestComputeToleranceAndFP(t *testing.T) {
	bugs := []Bug{{File: "main.go", Line: 7}}
	fs := []review.CandidateClaim{
		{File: "main.go", Line: 9},  // 容差内命中
		{File: "main.go", Line: 20}, // 误报
	}
	m := Compute(bugs, fs, 3)
	if m.True != 1 || m.False != 1 {
		t.Fatalf("应 1 命中 1 误报: %+v", m)
	}
	if m.Precision() != 0.5 || m.FPRate() != 0.5 {
		t.Fatalf("指标异常: %+v", m)
	}
}

func TestComputeGreedyOnePerBug(t *testing.T) {
	bugs := []Bug{{File: "main.go", Line: 7}}
	fs := []review.CandidateClaim{
		{File: "main.go", Line: 7},
		{File: "main.go", Line: 8},
	}
	m := Compute(bugs, fs, 3)
	if m.True != 1 || m.False != 1 || m.Found != 1 {
		t.Fatalf("同一 bug 只应命中一次: %+v", m)
	}
}

func TestComputeWrongFile(t *testing.T) {
	bugs := []Bug{{File: "main.go", Line: 7}}
	fs := []review.CandidateClaim{{File: "other.go", Line: 7}}
	m := Compute(bugs, fs, 3)
	if m.True != 0 || m.False != 1 {
		t.Fatalf("文件不符应判误报: %+v", m)
	}
}

func TestComputeTraceDoesNotUseEvidenceLocation(t *testing.T) {
	c := &Case{GT: GroundTruth{Primary: Location{File: "main.go", Line: 14}}}
	trace := &workflow.Trace{
		Evidence: []*workflow.Evidence{{ID: "e1", File: "main.go", Line: 14, Content: "write"}},
		FinalReport: workflow.FinalReport{
			Claims:   []review.CandidateClaim{{ID: "c1", File: "main.go", Line: 9, EvidenceIDs: []string{"e1"}}},
			Verdicts: []workflow.Verdict{{ClaimID: "c1", Status: workflow.VerdictAccepted}},
		},
	}
	m := ComputeTrace(c, trace, 3)
	if m.RawLine.Found != 0 || m.RawLine.False != 1 || m.AcceptedLine.Found != 0 {
		t.Fatalf("Evidence 位置不得替代 CandidateClaim 命中: %+v", m)
	}
}

func TestComputeTraceSplitsRawAndAcceptedFindings(t *testing.T) {
	c := &Case{GT: GroundTruth{Primary: Location{File: "main.go", Line: 10}}}
	trace := &workflow.Trace{
		FinalReport: workflow.FinalReport{
			Claims: []review.CandidateClaim{
				{ID: "c1", File: "main.go", Line: 10},
				{ID: "c2", File: "other.go", Line: 20},
			},
			Verdicts: []workflow.Verdict{
				{ClaimID: "c1", Status: workflow.VerdictAccepted},
				{ClaimID: "c2", Status: workflow.VerdictRejected},
			},
		},
	}
	m := ComputeTrace(c, trace, 3)
	if m.RawLine.Claims != 2 || m.RawLine.Found != 1 || m.RawLine.False != 1 {
		t.Fatalf("Raw 指标应包含全部 CandidateClaim: %+v", m.RawLine)
	}
	if m.AcceptedLine.Claims != 1 || m.AcceptedLine.Found != 1 || m.AcceptedLine.False != 0 {
		t.Fatalf("Accepted 指标应只包含通过边界的 CandidateClaim: %+v", m.AcceptedLine)
	}
}

func TestComputeTraceRequiresExplicitVerdict(t *testing.T) {
	c := &Case{GT: GroundTruth{Primary: Location{File: "main.go", Line: 10}}}
	trace := &workflow.Trace{FinalReport: workflow.FinalReport{Claims: []review.CandidateClaim{{ID: "c1", File: "main.go", Line: 10}}}}
	m := ComputeTrace(c, trace, 3)
	if m.RawLine.Found != 1 || m.AcceptedLine.Found != 0 || m.AcceptedLine.FN != 1 {
		t.Fatalf("缺少 Evaluate 记录时不得默认接受: %+v", m)
	}
}

func TestComputeTraceSeparatesFileGroundTruth(t *testing.T) {
	c := &Case{GT: GroundTruth{Primary: Location{File: "main.go", Line: 0}}}
	trace := &workflow.Trace{
		FinalReport: workflow.FinalReport{
			Claims:   []review.CandidateClaim{{ID: "c1", File: "main.go", Line: 30}},
			Verdicts: []workflow.Verdict{{ClaimID: "c1", Status: workflow.VerdictAccepted}},
		},
	}
	m := ComputeTrace(c, trace, 3)
	if m.RawLine.Bugs != 0 || m.AcceptedLine.Bugs != 0 {
		t.Fatalf("文件级 GT 不得进入行级指标: %+v", m)
	}
	if m.RawFile.Found != 1 || m.AcceptedFile.Found != 1 {
		t.Fatalf("文件级 GT 应只进入文件级指标: %+v", m)
	}
}

func TestComputeTraceUsesOnlyPrimaryAsBug(t *testing.T) {
	c := &Case{GT: GroundTruth{
		Primary: Location{File: "main.go", Line: 10},
		Related: []Location{{File: "main.go", Line: 20}},
	}}
	trace := &workflow.Trace{
		FinalReport: workflow.FinalReport{
			Claims:   []review.CandidateClaim{{ID: "c1", File: "main.go", Line: 20}},
			Verdicts: []workflow.Verdict{{ClaimID: "c1", Status: workflow.VerdictAccepted}},
		},
	}
	m := ComputeTrace(c, trace, 3)
	if m.RawLine.Bugs != 1 || m.RawLine.Found != 0 {
		t.Fatalf("Related 不能作为独立 Bug 或替代 Primary: %+v", m)
	}
}

func TestFailureMetricsCountsEndToEndFalseNegative(t *testing.T) {
	lineCase := &Case{GT: GroundTruth{Primary: Location{File: "main.go", Line: 10}}}
	lineMetrics := FailureMetrics(lineCase)
	if lineMetrics.AcceptedLine.Bugs != 1 || lineMetrics.AcceptedLine.FN != 1 {
		t.Fatalf("失败的行级 Case 必须计入端到端 FN: %+v", lineMetrics)
	}

	fileCase := &Case{GT: GroundTruth{Primary: Location{File: "main.go", Line: 0}}}
	fileMetrics := FailureMetrics(fileCase)
	if fileMetrics.AcceptedFile.Bugs != 1 || fileMetrics.AcceptedFile.FN != 1 {
		t.Fatalf("失败的文件级 Case 必须计入端到端 FN: %+v", fileMetrics)
	}
}
