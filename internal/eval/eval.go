package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MUYI-luyu/codecritic/internal/agent"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

const tol = 3 // 定位容差（行）

// Run 跑全量评测，对比 Baseline（无 Reflexion）vs Reflexion Agent。
func Run(ctx context.Context, llm *review.LLM, datasetDir string, verbose bool) error {
	cases, err := Load(datasetDir)
	if err != nil {
		return err
	}
	if len(cases) == 0 {
		return fmt.Errorf("数据集为空: %s", datasetDir)
	}

	var base, reflexion Metrics
	var failed int
	for _, c := range cases {
		repo, err := materialize(c)
		if err != nil {
			fmt.Printf("%-16s materialize 失败: %v\n", c.Name, err)
			failed++
			continue
		}

		// Baseline: 使用旧的单次 Plan-and-Execute（无 Reflexion）
		baseRes, err := review.Analyze(ctx, llm, repo, c.Diff)
		if err != nil {
			fmt.Printf("%-16s baseline 失败: %v\n", c.Name, err)
			os.RemoveAll(repo)
			failed++
			continue
		}
		baseFindings := append(append([]review.Finding{}, baseRes.Rules...), baseRes.LLM...)
		b := Compute(c.Bugs, baseFindings, tol)
		base = base.Add(b)

		// Reflexion Agent: 完整的 Reflexion Loop
		reflexAgent, err := agent.New(llm, repo, agent.WithMaxAttempts(3))
		if err != nil {
			fmt.Printf("%-16s agent 创建失败: %v\n", c.Name, err)
			os.RemoveAll(repo)
			failed++
			continue
		}

		// 从 Impact 提取 Sym 列表
		syms := make([]review.Sym, len(baseRes.Impact))
		for i, imp := range baseRes.Impact {
			syms[i] = review.Sym{Name: imp.Symbol, File: ""} // File 从 Caller 中提取
			if len(imp.Callers) > 0 {
				syms[i].File = imp.Callers[0].File
			}
		}

		reflexResult, err := reflexAgent.Review(ctx, c.Diff, syms)
		if err != nil {
			fmt.Printf("%-16s reflexion 失败: %v\n", c.Name, err)
			os.RemoveAll(repo)
			failed++
			continue
		}
		r := Compute(c.Bugs, reflexResult.FinalFindings, tol)
		reflexion = reflexion.Add(r)

		if verbose {
			printDetailedTrace(c.Name, reflexResult, baseFindings, c.Bugs, tol)
		} else {
			fmt.Printf("%-16s bug=%d  baseline[F=%d hit=%d fp=%d]  reflexion[F=%d hit=%d fp=%d rounds=%d]\n",
				c.Name, len(c.Bugs),
				b.Findings, b.Found, b.False,
				r.Findings, r.Found, r.False,
				len(reflexResult.Attempts))
		}
		os.RemoveAll(repo)
	}

	if failed > 0 {
		fmt.Printf("\n%d 个用例失败（已跳过，不打断整体评测）\n", failed)
	}

	fmt.Println()
	fmt.Printf("%-12s %10s %10s %8s %10s\n", "", "Recall", "Precision", "FP", "误报率")
	fmt.Printf("%-12s %9.0f%% %10.0f%% %8d %9.0f%%\n", "Baseline", pct(base.Recall()), pct(base.Precision()), base.False, pct(base.FPRate()))
	fmt.Printf("%-12s %9.0f%% %10.0f%% %8d %9.0f%%\n", "Reflexion", pct(reflexion.Recall()), pct(reflexion.Precision()), reflexion.False, pct(reflexion.FPRate()))
	return nil
}

// materialize 把用例写成临时仓库，返回仓库路径。
func materialize(c *Case) (string, error) {
	dir, err := os.MkdirTemp("", "cceval")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/eval\n\ngo 1.22\n"), 0o644); err != nil {
		return "", err
	}
	for name, content := range c.Repo {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func pct(f float64) float64 { return f * 100 }

// printDetailedTrace 打印详细的 Agent 执行轨迹（三层输出）。
func printDetailedTrace(caseName string, result *agent.Result, baselineFindings []review.Finding, bugs []Bug, tol int) {
	fmt.Printf("\n=== Case: %s ===\n\n", caseName)

	// Layer 2: Agent Trace
	fmt.Println("\n=== Agent Trace ===")
	for _, att := range result.Attempts {
		fmt.Printf("[Round %d] Duration: %v\n\n", att.Round, att.Duration)

		// Tool Calls
		if len(att.ToolCalls) > 0 {
			fmt.Println("Tool Calls:")
			for i, tc := range att.ToolCalls {
				fmt.Printf("  #%d %s\n", i+1, tc.Tool)
				if len(tc.Args) > 0 {
					fmt.Printf("      args: %v\n", formatJSON(tc.Args, 100))
				}
				if tc.Result != nil {
					fmt.Printf("      result: %v\n", formatJSON(tc.Result, 100))
				}
				if tc.Error != "" {
					fmt.Printf("      error: %s\n", tc.Error)
				}
			}
			fmt.Println()
		}

		// Findings
		fmt.Printf("Findings: %d\n", len(att.Findings))
		for i, f := range att.Findings {
			fmt.Printf("  F%d [%s] %s:%d\n", i+1, f.Severity, f.File, f.Line)
			fmt.Printf("      %s\n", truncate(f.Msg, 80))
		}
		fmt.Println()

		// Validations
		fmt.Printf("Validations: %d\n", len(att.Validations))
		for _, v := range att.Validations {
			status := "PASS"
			if v.Confidence < agent.ConfidenceThreshold {
				status = "REJECT"
			}
			fmt.Printf("  F%d → %s (conf=%.2f)\n", v.FindingID+1, status, v.Confidence)
			if len(v.Gaps) > 0 {
				fmt.Printf("      gaps: %s\n", truncate(fmt.Sprint(v.Gaps), 80))
			}
		}
		fmt.Println()

		// Critiques
		if len(att.Critiques) > 0 {
			fmt.Printf("Critiques: %d\n", len(att.Critiques))
			for _, c := range att.Critiques {
				fmt.Printf("  F%d: %s\n", c.FindingID+1, truncate(c.Reason, 80))
				fmt.Printf("      suggestion: %s\n", truncate(c.Suggestion, 80))
			}
			fmt.Println()
		}

		if att.Error != "" {
			fmt.Printf("Error: %s\n\n", att.Error)
		}
	}

	// Layer 3: Runtime Metrics
	fmt.Println("\n=== Runtime Metrics ===")

	var totalTools int
	toolCount := make(map[string]int)
	for _, att := range result.Attempts {
		totalTools += len(att.ToolCalls)
		for _, tc := range att.ToolCalls {
			toolCount[tc.Tool]++
		}
	}

	fmt.Printf("Rounds:        %d\n", len(result.Attempts))
	fmt.Printf("Converged:     %v (%s)\n", result.Converged, result.Reason)
	fmt.Printf("Total Time:    %v\n", result.TotalDuration)
	fmt.Printf("Tool Calls:    %d\n", totalTools)
	if len(toolCount) > 0 {
		fmt.Println("Tool Usage:")
		for tool, count := range toolCount {
			fmt.Printf("  %-20s %d\n", tool, count)
		}
	}
	fmt.Println()

	// Layer 1: Summary
	fmt.Println("\n=== Summary ===")

	baseMetrics := Compute(bugs, baselineFindings, tol)
	reflexMetrics := Compute(bugs, result.FinalFindings, tol)

	fmt.Printf("Bug Count:     %d\n\n", len(bugs))

	fmt.Println("Baseline:")
	fmt.Printf("  Findings:    %d\n", baseMetrics.Findings)
	fmt.Printf("  Hit:         %d\n", baseMetrics.Found)
	fmt.Printf("  FP:          %d\n", baseMetrics.False)
	fmt.Printf("  Recall:      %.0f%%\n", pct(baseMetrics.Recall()))
	fmt.Printf("  Precision:   %.0f%%\n\n", pct(baseMetrics.Precision()))

	fmt.Println("Reflexion:")
	fmt.Printf("  Findings:    %d\n", reflexMetrics.Findings)
	fmt.Printf("  Hit:         %d\n", reflexMetrics.Found)
	fmt.Printf("  FP:          %d\n", reflexMetrics.False)
	fmt.Printf("  Recall:      %.0f%%\n", pct(reflexMetrics.Recall()))
	fmt.Printf("  Precision:   %.0f%%\n\n", pct(reflexMetrics.Precision()))

	fmt.Println("Final Findings:")
	for i, f := range result.FinalFindings {
		isHit := false
		for _, bug := range bugs {
			if f.File == bug.File && abs(f.Line-bug.Line) <= tol {
				isHit = true
				break
			}
		}
		marker := "FP"
		if isHit {
			marker = "HIT"
		}
		fmt.Printf("  [%s] F%d %s:%d - %s\n", marker, i+1, f.File, f.Line, truncate(f.Msg, 60))
	}
	fmt.Println()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func formatJSON(v interface{}, max int) string {
	s := fmt.Sprintf("%v", v)
	return truncate(s, max)
}
