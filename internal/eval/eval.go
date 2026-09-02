package eval

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

const tol = 3
const DefaultConcurrency = 4

type caseResult struct {
	name    string
	metrics Metrics
	attrs   []BugAttribution
	output  string
	err     error
}

func Run(ctx context.Context, llm *review.LLM, datasetDir string, verbose bool) error {
	return RunConcurrent(ctx, llm, datasetDir, verbose, DefaultConcurrency, "")
}

func RunConcurrent(ctx context.Context, llm *review.LLM, datasetDir string, verbose bool, concurrency int, traceDir string) error {
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
	if traceDir != "" {
		if err := os.MkdirAll(traceDir, 0755); err != nil {
			return err
		}
	}
	jobs := make(chan *Case)
	results := make(chan caseResult)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				results <- runCase(ctx, llm, c, verbose, traceDir)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, c := range cases {
			jobs <- c
		}
	}()
	go func() { wg.Wait(); close(results) }()
	var total Metrics
	var attrs AttributionCounts
	done := 0
	for r := range results {
		done++
		if r.err != nil {
			fmt.Printf("[%d/%d] %s\n", done, len(cases), r.output)
			continue
		}
		total = total.Add(r.metrics)
		attrs = attrs.Add(r.attrs)
		fmt.Printf("[%d/%d] %s", done, len(cases), r.output)
	}
	fmt.Printf("\n%-12s %10s %10s %8s %10s\n", "Workflow", "Recall", "Precision", "FP", "误报率")
	fmt.Printf("%-12s %9.0f%% %10.0f%% %8d %9.0f%%\n", "Workflow", pct(total.Recall()), pct(total.Precision()), total.False, pct(total.FPRate()))
	attrs.Print(os.Stdout)
	return nil
}

func runCase(ctx context.Context, llm *review.LLM, c *Case, verbose bool, traceDir string) caseResult {
	repo, err := materialize(c)
	if err != nil {
		return caseResult{name: c.Name, err: err, output: fmt.Sprintf("%-16s materialize 失败: %v", c.Name, err)}
	}
	defer os.RemoveAll(repo)
	wf, err := workflow.New(llm, repo)
	if err != nil {
		return caseResult{name: c.Name, err: err, output: fmt.Sprintf("%-16s workflow 创建失败: %v", c.Name, err)}
	}
	res, err := wf.Run(ctx, workflow.Request{Repo: repo, Diff: c.Diff})
	if err != nil || res == nil || res.Trace == nil {
		return caseResult{name: c.Name, err: err, output: fmt.Sprintf("%-16s workflow 失败: %v", c.Name, err)}
	}
	t := res.Trace
	m := Compute(c.Bugs(), t.Findings, tol)
	attrs := Attribute(c.Bugs(), t, tol)
	cost := ComputeCost(t)
	dim := ComputeDimension(c)
	if traceDir != "" {
		_ = SaveTrace(traceDir, EvalTrace{Name: c.Name, Bugs: c.Bugs(), BaselineFindings: t.Findings, Workflow: t, Attributions: attrs, Dimension: &dim, CostSummary: cost})
	}
	var b bytes.Buffer
	if verbose {
		printWorkflowTrace(&b, c.Name, t, cost)
	} else {
		fmt.Fprintf(&b, "%-20s bugs=%d findings=%d hit=%d fp=%d cost=%dtok\n", c.Name, len(c.Bugs()), m.Findings, m.Found, m.False, cost.TotalTokens)
	}
	return caseResult{name: c.Name, metrics: m, attrs: attrs, output: b.String()}
}

func materialize(c *Case) (string, error) {
	dir, err := os.MkdirTemp("", "cceval")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/eval\n\ngo 1.22\n"), 0644); err != nil {
		return "", err
	}
	for name, content := range c.Repo {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			return "", err
		}
	}
	return dir, nil
}
func pct(f float64) float64 { return f * 100 }
func printWorkflowTrace(w io.Writer, name string, t *workflow.Trace, cost CostSummary) {
	fmt.Fprintf(w, "\n=== Case: %s ===\nStopReason: %s\nDuration: %v\nLLM Calls: %d\nTool Calls: %d\nTokens: %d\nPlan files=%v symbols=%v questions=%d keywords=%d\nFindings: %d Validations: %d\n", name, t.StopReason, t.Duration, len(t.LLMCalls), len(t.ToolCalls), cost.TotalTokens, t.Plan.TargetFiles, t.Plan.Symbols, len(t.Plan.Questions), len(t.Plan.Keywords), len(t.Findings), len(t.Validations))
	for i, f := range t.Findings {
		fmt.Fprintf(w, "  F%d [%s] %s:%d %s\n", i+1, f.Severity, f.File, f.Line, f.Msg)
	}
	fmt.Fprintln(w)
}
