package eval

import "github.com/MUYI-luyu/codecritic/internal/review"

// Metrics 汇总一次评测的命中情况。
type Metrics struct {
	Bugs     int // 真实 bug 数
	Found    int // 被命中的 bug 数
	Findings int // 产出 finding 数
	True     int // 命中 bug 的 finding 数
	False    int // 未命中的 finding 数（误报）

	// === 新增：底层聚合数据（支持 micro-average）===
	TP int // True Positive（命中的 bug 数，等同于 Found）
	FP int // False Positive（误报的 finding 数，等同于 False）
	FN int // False Negative（漏报的 bug 数）
}

// Compute 用贪心匹配把 findings 对齐到 bugs，容差 tol 行内算命中。
// 支持多位置 GT：Primary 100% + Related 50%（部分覆盖也算贡献）。
func Compute(bugs []Bug, fs []review.Finding, tol int) Metrics {
	used := make([]bool, len(bugs))
	m := Metrics{Bugs: len(bugs), Findings: len(fs)}
	for _, f := range fs {
		best := -1
		for i, b := range bugs {
			if used[i] || b.File != f.File {
				continue
			}
			if b.Line <= 0 { // 文件级 ground truth（无精确行号）：同文件即命中
				best = i
				break
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
			m.TP++ // 命中
		} else {
			m.False++
			m.FP++ // 误报
		}
	}
	for _, u := range used {
		if u {
			m.Found++
		} else {
			m.FN++ // 漏报
		}
	}
	return m
}

// ComputeMultiLocation 支持多位置 GT（Primary + Related）。
// Primary 100%，Related 各 50%，总覆盖率达到阈值算命中（默认阈值 0.5）。
func ComputeMultiLocation(c *Case, fs []review.Finding, tol int, threshold float64) Metrics {
	// 构造位置列表：Primary + Related
	locations := []Location{c.GT.Primary}
	locations = append(locations, c.GT.Related...)

	// 每个位置的权重：Primary 1.0，Related 0.5
	weights := make([]float64, len(locations))
	weights[0] = 1.0 // Primary
	for i := 1; i < len(weights); i++ {
		weights[i] = 0.5 // Related
	}

	// 总权重
	totalWeight := 0.0
	for _, w := range weights {
		totalWeight += w
	}

	// 贪心匹配 findings 到位置
	used := make([]bool, len(locations))
	coveredWeight := 0.0
	trueFindings := 0

	m := Metrics{Bugs: 1, Findings: len(fs)} // 一个 case = 一个 bug

	for _, f := range fs {
		best := -1
		for i, loc := range locations {
			if used[i] || loc.File != f.File {
				continue
			}
			if loc.Line <= 0 { // 文件级：同文件即命中
				best = i
				break
			}
			if d := abs(loc.Line - f.Line); d > tol {
				continue
			}
			if best == -1 || abs(locations[best].Line-f.Line) > abs(loc.Line-f.Line) {
				best = i
			}
		}
		if best >= 0 {
			used[best] = true
			coveredWeight += weights[best]
			trueFindings++
		} else {
			m.False++
			m.FP++
		}
	}

	// 覆盖率达到阈值算命中
	coverage := 0.0
	if totalWeight > 0 {
		coverage = coveredWeight / totalWeight
	}

	if coverage >= threshold {
		m.Found = 1
		m.TP = 1
		m.FN = 0
	} else {
		m.Found = 0
		m.TP = 0
		m.FN = 1
	}

	m.True = trueFindings
	return m
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

// F1 是 Precision 和 Recall 的调和平均。
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
