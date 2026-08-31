package eval

import (
	"path/filepath"

	"github.com/MUYI-luyu/codecritic/internal/agent"
	"github.com/MUYI-luyu/codecritic/internal/recall"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

// BugStage 是一个 bug 在 Reflexion 流水线里的归因阶段。
type BugStage string

const (
	StageRecallMiss BugStage = "recall_miss" // 召回漏：召回上下文没覆盖 bug 行
	StageLLMMiss    BugStage = "llm_miss"     // LLM漏：召回到了但 execute 阶段没报出
	StageSelfHarm   BugStage = "self_harm"    // 自伤：execute 报了但被 validate 阈值丢弃
	StageSuccess    BugStage = "success"      // 成功：execute 报了且 validate 保留
)

// BugAttribution 是单个 bug 的阶段归因结果。
type BugAttribution struct {
	Bug          Bug      `json:"bug"`
	Stage        BugStage `json:"stage"`
	RecallHit    bool     `json:"recall_hit"`    // 召回是否覆盖 bug 行
	ExecuteHit   bool     `json:"execute_hit"`   // execute 原始 findings 是否命中
	ValidateKept bool     `json:"validate_kept"` // 命中的 finding 是否通过置信度阈值
}

// Attribute 用 ground-truth 对 Reflexion 某一轮 attempt 做阶段归因。
// 对每个 bug 判定「召回是否覆盖 / execute 是否命中 / validate 是否保留」，
// 机械地归类到 召回漏 / LLM漏 / 自伤 / 成功 四类。
func Attribute(bugs []Bug, a *agent.Attempt, tol int) []BugAttribution {
	out := make([]BugAttribution, 0, len(bugs))
	for _, b := range bugs {
		recallHit := recallCovers(a.RecalledDocs, b, tol)
		execIdx := executeHitIndex(b, a.Findings, tol)
		execHit := execIdx >= 0

		validateKept := false
		if execHit {
			for _, v := range a.Validations {
				if v.FindingID == execIdx {
					validateKept = v.Confidence >= agent.ConfidenceThreshold
					break
				}
			}
		}

		out = append(out, BugAttribution{
			Bug:          b,
			Stage:        classify(recallHit, execHit, validateKept),
			RecallHit:    recallHit,
			ExecuteHit:   execHit,
			ValidateKept: validateKept,
		})
	}
	return out
}

// classify 由三个二值信号定位阶段。execute 命中优先（一旦报出，就只可能是成功或自伤）。
func classify(recallHit, execHit, validateKept bool) BugStage {
	switch {
	case execHit && validateKept:
		return StageSuccess
	case execHit && !validateKept:
		return StageSelfHarm
	case recallHit:
		return StageLLMMiss
	default:
		return StageRecallMiss
	}
}

// recallCovers 判断召回片段里有没有覆盖 bug 行的。
// docs 的 File 是绝对路径（rg/调用图产出），bug.File 是相对名，故按 basename 比对。
func recallCovers(docs []recall.Doc, b Bug, tol int) bool {
	for _, d := range docs {
		if filepath.Base(d.File) != filepath.Base(b.File) {
			continue
		}
		if b.Line <= 0 { // 文件级 ground-truth：同文件即算覆盖
			return true
		}
		if abs(d.Line-b.Line) <= tol {
			return true
		}
	}
	return false
}

// executeHitIndex 返回命中 bug b 的 finding 下标（无则 -1）。
// 匹配语义与 Compute 保持一致（精确同文件 + 容差行内，取最近）。
func executeHitIndex(b Bug, fs []review.Finding, tol int) int {
	best := -1
	for i, f := range fs {
		if b.File != f.File {
			continue
		}
		if b.Line <= 0 { // 文件级：同文件首个 finding
			return i
		}
		if abs(b.Line-f.Line) > tol {
			continue
		}
		if best == -1 || abs(b.Line-fs[best].Line) > abs(b.Line-f.Line) {
			best = i
		}
	}
	return best
}
