package eval

import "github.com/MUYI-luyu/codecritic/internal/review"

// Metrics 汇总一次评测的命中情况。
type Metrics struct {
	Bugs     int // 真实 bug 数
	Found    int // 被命中的 bug 数
	Findings int // 产出 finding 数
	True     int // 命中 bug 的 finding 数
	False    int // 未命中的 finding 数（误报）
}

// Compute 用贪心匹配把 findings 对齐到 bugs，容差 tol 行内算命中。
func Compute(bugs []Bug, fs []review.Finding, tol int) Metrics {
	used := make([]bool, len(bugs))
	m := Metrics{Bugs: len(bugs), Findings: len(fs)}
	for _, f := range fs {
		best := -1
		for i, b := range bugs {
			if used[i] || b.File != f.File {
				continue
			}
			if d := abs(b.Line - f.Line); d > tol {
				continue
			}
			if best == -1 || abs(bugs[best].Line-f.Line) > abs(b.Line-f.Line) {
				best = i
			}
		}
		if best >= 0 {
			used[best] = true
			m.True++
		} else {
			m.False++
		}
	}
	for _, u := range used {
		if u {
			m.Found++
		}
	}
	return m
}

// Add 累加两次评测的计数。
func (m Metrics) Add(o Metrics) Metrics {
	m.Bugs += o.Bugs
	m.Found += o.Found
	m.Findings += o.Findings
	m.True += o.True
	m.False += o.False
	return m
}

// Recall 命中 bug 占比。
func (m Metrics) Recall() float64 {
	if m.Bugs == 0 {
		return 0
	}
	return float64(m.Found) / float64(m.Bugs)
}

// Precision 命中 bug 的 finding 占比。
func (m Metrics) Precision() float64 {
	if m.Findings == 0 {
		return 0
	}
	return float64(m.True) / float64(m.Findings)
}

// FPRate 误报率 = 未命中 finding 占比。
func (m Metrics) FPRate() float64 {
	if m.Findings == 0 {
		return 0
	}
	return float64(m.False) / float64(m.Findings)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
