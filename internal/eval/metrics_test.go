package eval

import (
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

func TestComputeExactMatch(t *testing.T) {
	bugs := []Bug{{File: "main.go", Line: 7}}
	fs := []review.Finding{{File: "main.go", Line: 7}}
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
	fs := []review.Finding{
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
	fs := []review.Finding{
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
	fs := []review.Finding{{File: "other.go", Line: 7}}
	m := Compute(bugs, fs, 3)
	if m.True != 0 || m.False != 1 {
		t.Fatalf("文件不符应判误报: %+v", m)
	}
}

func TestComputeTraceUsesEvidenceLocation(t *testing.T) {
	trace := &workflow.Trace{
		Findings: []review.Finding{{File: "main.go", Line: 9, EvidenceIDs: []string{"e1"}}},
		Evidence: []*workflow.Evidence{{ID: "e1", File: "main.go", Line: 14, Content: "write"}},
	}
	m := ComputeTrace([]Bug{{File: "main.go", Line: 14}}, trace, 3)
	if m.Found != 1 || m.False != 0 {
		t.Fatalf("应使用 Evidence 位置命中: %+v", m)
	}
}

func TestComputeTraceIgnoresRejectedFindings(t *testing.T) {
	trace := &workflow.Trace{
		Findings: []review.Finding{
			{File: "main.go", Line: 10},
			{File: "other.go", Line: 20},
		},
		Validations: []workflow.Validation{
			{FindingIndex: 0, Accepted: true},
			{FindingIndex: 1, Accepted: false},
		},
	}
	m := ComputeTrace([]Bug{{File: "main.go", Line: 10}}, trace, 3)
	if m.Findings != 1 || m.Found != 1 || m.False != 0 {
		t.Fatalf("拒绝的 Finding 不应计入指标: %+v", m)
	}
}
