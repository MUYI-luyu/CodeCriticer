package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/agent"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

// EvalTrace 是单个评测用例的离线观测快照。
// 它把「ground-truth、baseline 产出、Reflexion 完整轨迹、阶段归因」聚在一份 JSON 里，
// 让 31 例自伤 / 19 例真漏这类问题可以事后逐阶段回看，而不是黑盒。
type EvalTrace struct {
	Name             string           `json:"name"`
	Bugs             []Bug            `json:"bugs"`              // ground-truth
	BaselineFindings []review.Finding `json:"baseline_findings"` // 无 Reflexion 的单次产出
	Reflex           *agent.Result    `json:"reflex"`            // Reflexion 全轨迹（含每轮 RecalledDocs）
	Attributions     []BugAttribution `json:"attributions"`      // 对末轮 attempt 的阶段归因
	Dimension        *CaseDimension   `json:"dimension"`         // 运行时计算的维度（Scale/Scope）
	CostSummary      CostSummary      `json:"cost_summary"`      // 本 case 的 token 成本汇总
}

// SaveTrace 把一份 EvalTrace 持久化成 <dir>/<name>.json。
// dir 由调用方保证已存在（RunConcurrent 启动时统一创建）。
func SaveTrace(dir string, t EvalTrace) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, safeName(t.Name)+".json")
	return os.WriteFile(path, data, 0o644)
}

// safeName 把用例名里的路径分隔符换成下划线，避免写到子目录/越界。
func safeName(name string) string {
	r := strings.NewReplacer("/", "_", string(filepath.Separator), "_")
	return r.Replace(name)
}

// AttributionCounts 汇总各阶段的 bug 计数，用于末尾分布表。
type AttributionCounts struct {
	RecallMiss int
	LLMMiss    int
	SelfHarm   int
	Success    int
}

// Add 把一个 case 的归因结果累加进分布。
func (c AttributionCounts) Add(attrs []BugAttribution) AttributionCounts {
	for _, a := range attrs {
		switch a.Stage {
		case StageRecallMiss:
			c.RecallMiss++
		case StageLLMMiss:
			c.LLMMiss++
		case StageSelfHarm:
			c.SelfHarm++
		case StageSuccess:
			c.Success++
		}
	}
	return c
}

// Total 是四类之和（应等于参与归因的 bug 总数）。
func (c AttributionCounts) Total() int {
	return c.RecallMiss + c.LLMMiss + c.SelfHarm + c.Success
}

// Print 打印阶段归因分布表到 w。
func (c AttributionCounts) Print(w io.Writer) {
	total := c.Total()
	fmt.Fprintln(w, "\n=== 阶段归因分布（Reflexion 末轮）===")
	fmt.Fprintf(w, "%-12s %8s %8s\n", "阶段", "数量", "占比")
	rows := []struct {
		label string
		n     int
	}{
		{"成功", c.Success},
		{"自伤", c.SelfHarm},
		{"LLM漏", c.LLMMiss},
		{"召回漏", c.RecallMiss},
	}
	for _, r := range rows {
		fmt.Fprintf(w, "%-12s %8d %7.0f%%\n", r.label, r.n, ratio(r.n, total)*100)
	}
	fmt.Fprintf(w, "%-12s %8d\n", "合计", total)
}

func ratio(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total)
}
