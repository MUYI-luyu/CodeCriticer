package eval

import (
	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

// Metrics 汇总一组同类 Ground Truth 的命中情况。
type Metrics struct {
	Bugs     int // 真实 bug 数
	Found    int // 被命中的 bug 数
	Findings int // 产出 finding 数
	True     int // 命中 bug 的 finding 数
	False    int // 未命中的 finding 数
	TP       int // 命中的 bug 数
	FP       int // 未命中的 finding 数
	FN       int // 未命中的 bug 数
}

// TraceMetrics 分开记录 Review 原始输出、Evaluate 接受输出以及两类定位精度。
type TraceMetrics struct {
	RawLine      Metrics `json:"raw_line"`
	AcceptedLine Metrics `json:"accepted_line"`
	RawFile      Metrics `json:"raw_file"`
	AcceptedFile Metrics `json:"accepted_file"`
}

// Compute 用贪心匹配把 findings 对齐到 bugs，容差 tol 行内算命中。
func Compute(bugs []Bug, fs []review.Finding, tol int) Metrics {
	used := make([]bool, len(bugs))
	m := Metrics{Bugs: len(bugs), Findings: len(fs)}
	for _, f := range fs {
		best := matchingBug(bugs, used, f.File, f.Line, tol)
		if best >= 0 {
			used[best] = true
			m.True++
			m.TP++
		} else {
			m.False++
			m.FP++
		}
	}
	for _, ok := range used {
		if ok {
			m.Found++
		} else {
			m.FN++
		}
	}
	return m
}

// ComputeTrace 只用 Finding 自身位置计算命中，Evidence 仅供阶段归因使用。
func ComputeTrace(c *Case, trace *workflow.Trace, tol int) TraceMetrics {
	var raw []review.Finding
	var accepted []review.Finding
	if trace != nil {
		raw = trace.Findings
		accepted = acceptedFindings(trace)
	}
	return computePrimary(c, raw, accepted, tol)
}

// FailureMetrics 把未完成的 Case 计入端到端漏检。
func FailureMetrics(c *Case) TraceMetrics {
	return computePrimary(c, nil, nil, 0)
}

func computePrimary(c *Case, raw, accepted []review.Finding, tol int) TraceMetrics {
	bug := Bug{File: c.GT.Primary.File, Line: c.GT.Primary.Line, Desc: c.GT.Description}
	if bug.Line <= 0 {
		return TraceMetrics{
			RawFile:      Compute([]Bug{bug}, raw, tol),
			AcceptedFile: Compute([]Bug{bug}, accepted, tol),
		}
	}
	return TraceMetrics{
		RawLine:      Compute([]Bug{bug}, raw, tol),
		AcceptedLine: Compute([]Bug{bug}, accepted, tol),
	}
}

// acceptedFindings 只返回有明确接受记录的 Finding。
func acceptedFindings(trace *workflow.Trace) []review.Finding {
	if trace == nil || len(trace.Validations) == 0 {
		return nil
	}
	accepted := make(map[int]bool, len(trace.Validations))
	for _, validation := range trace.Validations {
		if validation.Accepted {
			accepted[validation.FindingIndex] = true
		}
	}
	out := make([]review.Finding, 0, len(accepted))
	for i, finding := range trace.Findings {
		if accepted[i] {
			out = append(out, finding)
		}
	}
	return out
}

func matchingBug(bugs []Bug, used []bool, file string, line, tol int) int {
	best := -1
	for i, b := range bugs {
		if used[i] || b.File != file {
			continue
		}
		if b.Line <= 0 {
			return i
		}
		if abs(b.Line-line) <= tol && (best < 0 || abs(bugs[best].Line-line) > abs(b.Line-line)) {
			best = i
		}
	}
	return best
}

// Add 累加两次评测的计数。
func (m Metrics) Add(o Metrics) Metrics {
	m.Bugs += o.Bugs
	m.Found += o.Found
	m.Findings += o.Findings
	m.True += o.True
	m.False += o.False
	m.TP += o.TP
	m.FP += o.FP
	m.FN += o.FN
	return m
}

// Add 累加两组 Trace 指标。
func (m TraceMetrics) Add(o TraceMetrics) TraceMetrics {
	m.RawLine = m.RawLine.Add(o.RawLine)
	m.AcceptedLine = m.AcceptedLine.Add(o.AcceptedLine)
	m.RawFile = m.RawFile.Add(o.RawFile)
	m.AcceptedFile = m.AcceptedFile.Add(o.AcceptedFile)
	return m
}

// Recall 返回命中 bug 的比例。
func (m Metrics) Recall() float64 {
	if m.Bugs == 0 {
		return 0
	}
	return float64(m.Found) / float64(m.Bugs)
}

// Precision 返回命中 bug 的 Finding 比例。
func (m Metrics) Precision() float64 {
	if m.Findings == 0 {
		return 0
	}
	return float64(m.True) / float64(m.Findings)
}

// FPRate 返回未命中 Finding 的比例。
func (m Metrics) FPRate() float64 {
	if m.Findings == 0 {
		return 0
	}
	return float64(m.False) / float64(m.Findings)
}

// F1 返回 Precision 和 Recall 的调和平均。
func (m Metrics) F1() float64 {
	r := m.Recall()
	p := m.Precision()
	if r+p == 0 {
		return 0
	}
	return 2 * r * p / (r + p)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
