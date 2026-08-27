package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MUYI-luyu/codecritic/internal/agent"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

// OrchestrationMetrics 评测 orchestration 模式的指标。
type OrchestrationMetrics struct {
	CaseName       string
	Bugs           int
	BaselineResult CaseResult
	OrchResult     CaseResult
}

// CaseResult 单个用例的审查结果。
type CaseResult struct {
	Findings  int
	Found     int // 命中的 bug 数
	False     int // 误报数
	Rounds    int // 尝试轮数
	ToolCalls int // 工具调用次数
}

// RunOrchestration 评测 orchestration 模式 vs baseline。
func RunOrchestration(ctx context.Context, llm *review.LLM, datasetDir string, verbose bool) error {
	cases, err := Load(datasetDir)
	if err != nil {
		return err
	}
	if len(cases) == 0 {
		return fmt.Errorf("数据集为空: %s", datasetDir)
	}

	var baseTotal, orchTotal Metrics
	var results []OrchestrationMetrics

	for _, c := range cases {
		repo, err := materialize(c)
		if err != nil {
			return err
		}
		defer os.RemoveAll(repo)

		// Baseline: 旧的 Plan-and-Execute（无 orchestration）
		baseRes, err := review.Analyze(ctx, llm, repo, c.Diff)
		if err != nil {
			return fmt.Errorf("%s baseline: %w", c.Name, err)
		}
		baseFindings := append(append([]review.Finding{}, baseRes.Rules...), baseRes.LLM...)
		baseMetrics := Compute(c.Bugs, baseFindings, tol)

		// Orchestration: 动态工具编排
		orchAgent, err := agent.New(llm, repo,
			agent.WithMaxAttempts(1), // 单轮评测
			agent.WithOrchestration(true))
		if err != nil {
			return fmt.Errorf("%s agent: %w", c.Name, err)
		}

		syms := make([]review.Sym, len(baseRes.Impact))
		for i, imp := range baseRes.Impact {
			syms[i] = review.Sym{Name: imp.Symbol, File: ""}
			if len(imp.Callers) > 0 {
				syms[i].File = imp.Callers[0].File
			}
		}

		orchResult, err := orchAgent.Review(ctx, c.Diff, syms)
		if err != nil {
			return fmt.Errorf("%s orchestration: %w", c.Name, err)
		}
		orchMetrics := Compute(c.Bugs, orchResult.FinalFindings, tol)

		baseTotal = baseTotal.Add(baseMetrics)
		orchTotal = orchTotal.Add(orchMetrics)

		toolCallsCount := 0
		if len(orchResult.Attempts) > 0 {
			toolCallsCount = len(orchResult.Attempts[0].ToolCalls)
		}

		result := OrchestrationMetrics{
			CaseName: c.Name,
			Bugs:     len(c.Bugs),
			BaselineResult: CaseResult{
				Findings:  baseMetrics.Findings,
				Found:     baseMetrics.Found,
				False:     baseMetrics.False,
				Rounds:    1,
				ToolCalls: 0,
			},
			OrchResult: CaseResult{
				Findings:  orchMetrics.Findings,
				Found:     orchMetrics.Found,
				False:     orchMetrics.False,
				Rounds:    len(orchResult.Attempts),
				ToolCalls: toolCallsCount,
			},
		}
		results = append(results, result)

		fmt.Printf("%-20s bugs=%d  baseline[F=%d hit=%d fp=%d]  orch[F=%d hit=%d fp=%d tools=%d]\n",
			c.Name, len(c.Bugs),
			baseMetrics.Findings, baseMetrics.Found, baseMetrics.False,
			orchMetrics.Findings, orchMetrics.Found, orchMetrics.False,
			toolCallsCount)
	}

	fmt.Println()
	fmt.Printf("%-15s %10s %10s %8s %10s\n", "", "Recall", "Precision", "FP", "误报率")
	fmt.Printf("%-15s %9.0f%% %10.0f%% %8d %9.0f%%\n",
		"Baseline", pct(baseTotal.Recall()), pct(baseTotal.Precision()),
		baseTotal.False, pct(baseTotal.FPRate()))
	fmt.Printf("%-15s %9.0f%% %10.0f%% %8d %9.0f%%\n",
		"Orchestration", pct(orchTotal.Recall()), pct(orchTotal.Precision()),
		orchTotal.False, pct(orchTotal.FPRate()))

	// 保存详细结果
	reportPath := filepath.Join(datasetDir, "..", "orchestration_report.json")
	if err := saveReport(reportPath, results, baseTotal, orchTotal); err != nil {
		fmt.Printf("警告: 保存报告失败: %v\n", err)
	} else {
		fmt.Printf("\n详细报告已保存: %s\n", reportPath)
	}

	return nil
}

// saveReport 保存评测报告到 JSON。
func saveReport(path string, results []OrchestrationMetrics, baseTotal, orchTotal Metrics) error {
	report := map[string]interface{}{
		"summary": map[string]interface{}{
			"baseline": map[string]interface{}{
				"recall":    baseTotal.Recall(),
				"precision": baseTotal.Precision(),
				"fp_rate":   baseTotal.FPRate(),
				"false":     baseTotal.False,
				"found":     baseTotal.Found,
				"bugs":      baseTotal.Bugs,
			},
			"orchestration": map[string]interface{}{
				"recall":    orchTotal.Recall(),
				"precision": orchTotal.Precision(),
				"fp_rate":   orchTotal.FPRate(),
				"false":     orchTotal.False,
				"found":     orchTotal.Found,
				"bugs":      orchTotal.Bugs,
			},
		},
		"cases": results,
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
