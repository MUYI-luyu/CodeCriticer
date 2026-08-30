package eval

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/MUYI-luyu/codecritic/internal/agent"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

const tol = 3 // 定位容差（行）

// DefaultConcurrency 是默认并发 case 数（受 LLM 限流约束，保守取值）。
const DefaultConcurrency = 4

// caseResult 是单个 case 评测的产出，供并发收集后串行汇总。
type caseResult struct {
	name    string
	base    Metrics
	reflex  Metrics
	output  string // verbose trace 或单行摘要（原子打印，避免交错）
	err     error
}

// Run 跑全量评测，对比 Baseline（无 Reflexion）vs Reflexion Agent。
// concurrency<=0 时取 DefaultConcurrency。
func Run(ctx context.Context, llm *review.LLM, datasetDir string, verbose bool) error {
	return RunConcurrent(ctx, llm, datasetDir, verbose, DefaultConcurrency)
}

// RunConcurrent 以指定并发度跑评测。case 之间相互独立，可安全并发。
func RunConcurrent(ctx context.Context, llm *review.LLM, datasetDir string, verbose bool, concurrency int) error {
	cases, err := Load(datasetDir)
	if err != nil {
		return err
	}
	if len(cases) == 0 {
		return fmt.Errorf("数据集为空: %s", datasetDir)
	}
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	if concurrency > len(cases) {
		concurrency = len(cases)
	}

	jobs := make(chan *Case)
	results := make(chan caseResult)

	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				results <- runCase(ctx, llm, c, verbose)
			}
		}()
	}

	go func() {
		for _, c := range cases {
			jobs <- c
		}
		close(jobs)
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	var base, reflexion Metrics
	var failed, done int
	total := len(cases)
	for res := range results {
		done++
		if res.err != nil {
			fmt.Printf("[%d/%d] %s\n", done, total, res.output)
			failed++
			continue
		}
		base = base.Add(res.base)
		reflexion = reflexion.Add(res.reflex)
		// 原子打印单个 case 的完整输出（worker 内已拼好）
		fmt.Printf("[%d/%d] %s", done, total, res.output)
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

// runCase 跑单个 case 的 baseline + reflexion，输出拼进 buffer 由调用方原子打印。
func runCase(ctx context.Context, llm *review.LLM, c *Case, verbose bool) caseResult {
	repo, err := materialize(c)
	if err != nil {
		return caseResult{name: c.Name, err: err, output: fmt.Sprintf("%-16s materialize 失败: %v", c.Name, err)}
	}
	defer os.RemoveAll(repo)

	// Baseline: 单次 Plan-and-Execute（无 Reflexion）
	baseRes, err := review.Analyze(ctx, llm, repo, c.Diff)
	if err != nil {
		return caseResult{name: c.Name, err: err, output: fmt.Sprintf("%-16s baseline 失败: %v", c.Name, err)}
	}
	baseFindings := append(append([]review.Finding{}, baseRes.Rules...), baseRes.LLM...)
	b := Compute(c.Bugs, baseFindings, tol)

	// Reflexion Agent: 完整的 Reflexion Loop
	reflexAgent, err := agent.New(llm, repo, agent.WithMaxAttempts(3))
	if err != nil {
		return caseResult{name: c.Name, err: err, output: fmt.Sprintf("%-16s agent 创建失败: %v", c.Name, err)}
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
		return caseResult{name: c.Name, err: err, output: fmt.Sprintf("%-16s reflexion 失败: %v", c.Name, err)}
	}
	r := Compute(c.Bugs, reflexResult.FinalFindings, tol)

	var buf bytes.Buffer
	if verbose {
		printDetailedTrace(&buf, c.Name, reflexResult, baseFindings, c.Bugs, tol)
	} else {
		fmt.Fprintf(&buf, "%-16s bug=%d  baseline[F=%d hit=%d fp=%d]  reflexion[F=%d hit=%d fp=%d rounds=%d]\n",
			c.Name, len(c.Bugs),
			b.Findings, b.Found, b.False,
			r.Findings, r.Found, r.False,
			len(reflexResult.Attempts))
	}

	return caseResult{name: c.Name, base: b, reflex: r, output: buf.String()}
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

// printDetailedTrace 打印详细的 Agent 执行轨迹（三层输出）到 w。
func printDetailedTrace(w io.Writer, caseName string, result *agent.Result, baselineFindings []review.Finding, bugs []Bug, tol int) {
	fmt.Fprintf(w, "\n=== Case: %s ===\n\n", caseName)

	// Layer 2: Agent Trace
	fmt.Fprintln(w, "\n=== Agent Trace ===")
	for _, att := range result.Attempts {
		fmt.Fprintf(w, "[Round %d] Duration: %v\n\n", att.Round, att.Duration)

		// Tool Calls
		if len(att.ToolCalls) > 0 {
			fmt.Fprintln(w, "Tool Calls:")
			for i, tc := range att.ToolCalls {
				fmt.Fprintf(w, "  #%d %s\n", i+1, tc.Tool)
				if len(tc.Args) > 0 {
					fmt.Fprintf(w, "      args: %v\n", formatJSON(tc.Args, 100))
				}
				if tc.Result != nil {
					fmt.Fprintf(w, "      result: %v\n", formatJSON(tc.Result, 100))
				}
				if tc.Error != "" {
					fmt.Fprintf(w, "      error: %s\n", tc.Error)
				}
			}
			fmt.Fprintln(w)
		}

		// Findings
		fmt.Fprintf(w, "Findings: %d\n", len(att.Findings))
		for i, f := range att.Findings {
			fmt.Fprintf(w, "  F%d [%s] %s:%d\n", i+1, f.Severity, f.File, f.Line)
			fmt.Fprintf(w, "      %s\n", truncate(f.Msg, 80))
		}
		fmt.Fprintln(w)

		// Validations
		fmt.Fprintf(w, "Validations: %d\n", len(att.Validations))
		for _, v := range att.Validations {
			status := "PASS"
			if v.Confidence < agent.ConfidenceThreshold {
				status = "REJECT"
			}
			fmt.Fprintf(w, "  F%d → %s (conf=%.2f)\n", v.FindingID+1, status, v.Confidence)
			if len(v.Gaps) > 0 {
				fmt.Fprintf(w, "      gaps: %s\n", truncate(fmt.Sprint(v.Gaps), 80))
			}
		}
		fmt.Fprintln(w)

		// Critiques
		if len(att.Critiques) > 0 {
			fmt.Fprintf(w, "Critiques: %d\n", len(att.Critiques))
			for _, c := range att.Critiques {
				fmt.Fprintf(w, "  F%d: %s\n", c.FindingID+1, truncate(c.Reason, 80))
				fmt.Fprintf(w, "      suggestion: %s\n", truncate(c.Suggestion, 80))
			}
			fmt.Fprintln(w)
		}

		if att.Error != "" {
			fmt.Fprintf(w, "Error: %s\n\n", att.Error)
		}
	}

	// Layer 3: Runtime Metrics
	fmt.Fprintln(w, "\n=== Runtime Metrics ===")

	var totalTools int
	toolCount := make(map[string]int)
	for _, att := range result.Attempts {
		totalTools += len(att.ToolCalls)
		for _, tc := range att.ToolCalls {
			toolCount[tc.Tool]++
		}
	}

	fmt.Fprintf(w, "Rounds:        %d\n", len(result.Attempts))
	fmt.Fprintf(w, "Converged:     %v (%s)\n", result.Converged, result.Reason)
	fmt.Fprintf(w, "Total Time:    %v\n", result.TotalDuration)
	fmt.Fprintf(w, "Tool Calls:    %d\n", totalTools)
	if len(toolCount) > 0 {
		fmt.Fprintln(w, "Tool Usage:")
		for tool, count := range toolCount {
			fmt.Fprintf(w, "  %-20s %d\n", tool, count)
		}
	}
	fmt.Fprintln(w)

	// Layer 1: Summary
	fmt.Fprintln(w, "\n=== Summary ===")

	baseMetrics := Compute(bugs, baselineFindings, tol)
	reflexMetrics := Compute(bugs, result.FinalFindings, tol)

	fmt.Fprintf(w, "Bug Count:     %d\n\n", len(bugs))

	fmt.Fprintln(w, "Baseline:")
	fmt.Fprintf(w, "  Findings:    %d\n", baseMetrics.Findings)
	fmt.Fprintf(w, "  Hit:         %d\n", baseMetrics.Found)
	fmt.Fprintf(w, "  FP:          %d\n", baseMetrics.False)
	fmt.Fprintf(w, "  Recall:      %.0f%%\n", pct(baseMetrics.Recall()))
	fmt.Fprintf(w, "  Precision:   %.0f%%\n\n", pct(baseMetrics.Precision()))

	fmt.Fprintln(w, "Reflexion:")
	fmt.Fprintf(w, "  Findings:    %d\n", reflexMetrics.Findings)
	fmt.Fprintf(w, "  Hit:         %d\n", reflexMetrics.Found)
	fmt.Fprintf(w, "  FP:          %d\n", reflexMetrics.False)
	fmt.Fprintf(w, "  Recall:      %.0f%%\n", pct(reflexMetrics.Recall()))
	fmt.Fprintf(w, "  Precision:   %.0f%%\n\n", pct(reflexMetrics.Precision()))

	fmt.Fprintln(w, "Final Findings:")
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
		fmt.Fprintf(w, "  [%s] F%d %s:%d - %s\n", marker, i+1, f.File, f.Line, truncate(f.Msg, 60))
	}
	fmt.Fprintln(w)
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
