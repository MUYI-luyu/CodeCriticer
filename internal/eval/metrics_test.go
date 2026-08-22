package eval

import (
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

func TestComputeExact(t *testing.T) {
	bugs := []Bug{{File: "a.go", Line: 10}}
	fs := []review.Finding{{File: "a.go", Line: 10}}
	m := Compute(bugs, fs, 3)
	if m.Bugs != 1 || m.Found != 1 || m.Findings != 1 || m.True != 1 || m.False != 0 {
		t.Fatalf("精确命中计数错误: %+v", m)
	}
	if m.Recall() != 1.0 || m.Precision() != 1.0 || m.FPRate() != 0.0 {
		t.Fatalf("精确命中指标错误: R=%.2f P=%.2f FP=%.2f", m.Recall(), m.Precision(), m.FPRate())
	}
}

func TestComputeTolerance(t *testing.T) {
	bugs := []Bug{{File: "a.go", Line: 10}}
	fs := []review.Finding{{File: "a.go", Line: 12}}
	m := Compute(bugs, fs, 3)
	if m.Found != 1 || m.True != 1 {
		t.Fatalf("容差内应命中: %+v", m)
	}

	m2 := Compute(bugs, []review.Finding{{File: "a.go", Line: 14}}, 3)
	if m2.Found != 0 || m2.False != 1 {
		t.Fatalf("容差外应误报: %+v", m2)
	}
}

func TestComputeFalsePositive(t *testing.T) {
	bugs := []Bug{{File: "a.go", Line: 10}}
	fs := []review.Finding{
		{File: "a.go", Line: 10},
		{File: "b.go", Line: 5},
	}
	m := Compute(bugs, fs, 3)
	if m.True != 1 || m.False != 1 {
		t.Fatalf("应有 1 真 1 假: %+v", m)
	}
	if m.Precision() != 0.5 {
		t.Fatalf("Precision 应为 0.5，得到 %.2f", m.Precision())
	}
}
