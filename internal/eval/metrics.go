package eval

import (
	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

// Metrics 汇总一组同类 Ground Truth 的命中情况。
type Metrics struct {
	Bugs   int // 真实 bug 数
	Found  int // 被命中的 bug 数
	Claims int // 产出 claim 数
	True   int // 命中 bug 的 claim 数
	False  int // 未命中的 claim 数
	TP     int // 命中的 bug 数
	FP     int // 未命中的 claim 数
	FN     int // 未命中的 bug 数
}

// TraceMetrics 分开记录原始 Claim、accepted Claim 以及两类定位精度。
type TraceMetrics struct {
	RawLine      Metrics `json:"raw_line"`
	AcceptedLine Metrics `json:"accepted_line"`
	RawFile      Metrics `json:"raw_file"`
	AcceptedFile Metrics `json:"accepted_file"`
}

// Compute 用贪心匹配把 claims 对齐到 bugs，容差 tol 行内算命中。
func Compute(bugs []Bug, fs []review.CandidateClaim, tol int) Metrics {
	used := make([]bool, len(bugs))
	m := Metrics{Bugs: len(bugs), Claims: len(fs)}
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

// ComputeTrace 只用 CandidateClaim 自身位置计算命中，Evidence 仅供阶段归因使用。
func ComputeTrace(c *Case, trace *workflow.Trace, tol int) TraceMetrics {
	var raw []review.CandidateClaim
	var accepted []review.CandidateClaim
	if trace != nil {
		raw = trace.FinalReport.Claims
		accepted = acceptedClaims(trace)
	}
	return computePrimary(c, raw, accepted, tol)
}

// FailureMetrics 把未完成的 Case 计入端到端漏检。
func FailureMetrics(c *Case) TraceMetrics {
	return computePrimary(c, nil, nil, 0)
}

func computePrimary(c *Case, raw, accepted []review.CandidateClaim, tol int) TraceMetrics {
	locations := acceptableLocations(c.GT)
	if c.GT.Primary.Line <= 0 {
		return TraceMetrics{
			RawFile:      computeOneIssueAtLocations(locations, raw, tol),
			AcceptedFile: computeOneIssueAtLocations(locations, accepted, tol),
		}
	}
	return TraceMetrics{
		RawLine:      computeOneIssueAtLocations(locations, raw, tol),
		AcceptedLine: computeOneIssueAtLocations(locations, accepted, tol),
	}
}

func computeOneIssueAtLocations(locations []Location, claims []review.CandidateClaim, tol int) Metrics {
	m := Metrics{Bugs: 1, Claims: len(claims), FN: 1}
	matched := false
	for _, claim := range claims {
		claimMatched := false
		for _, location := range locations {
			if claim.File == location.File && (location.Line <= 0 || abs(location.Line-claim.Line) <= tol) {
				claimMatched = true
				break
			}
		}
		if claimMatched && !matched {
			matched = true
			m.True++
			m.TP++
			m.Found = 1
			m.FN = 0
		} else {
			m.False++
			m.FP++
		}
	}
	return m
}

// acceptedClaims 只返回通过通用验证边界的 CandidateClaim。
func acceptedClaims(trace *workflow.Trace) []review.CandidateClaim {
	if trace == nil {
		return nil
	}
	return trace.FinalReport.ClaimsWithStatus(workflow.VerdictAccepted)
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
	m.Claims += o.Claims
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

// Precision 返回命中 bug 的 CandidateClaim 比例。
func (m Metrics) Precision() float64 {
	if m.Claims == 0 {
		return 0
	}
	return float64(m.True) / float64(m.Claims)
}

// FPRate 返回未命中 CandidateClaim 的比例。
func (m Metrics) FPRate() float64 {
	if m.Claims == 0 {
		return 0
	}
	return float64(m.False) / float64(m.Claims)
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
