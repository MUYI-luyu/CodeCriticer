package eval

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

const tol = 3
const DefaultConcurrency = 4

type caseResult struct {
	metrics      TraceMetrics
	attrs        []BugAttribution
	completed    bool
	failureStage string
	output       string
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

	var total TraceMetrics
	var attrs AttributionCounts
	completed := 0
	failures := make(map[string]int)
	done := 0
	for r := range results {
		done++
		total = total.Add(r.metrics)
		if r.completed {
			completed++
			attrs = attrs.Add(r.attrs)
		} else {
			failures[r.failureStage]++
		}
		fmt.Printf("[%d/%d] %s", done, len(cases), r.output)
	}

	fmt.Printf("\n完成率: %.0f%% (%d/%d)\n", ratio(completed, len(cases))*100, completed, len(cases))
	printMetrics(os.Stdout, "行级 GT / Raw", total.RawLine)
	printMetrics(os.Stdout, "行级 GT / Accepted", total.AcceptedLine)
	printMetrics(os.Stdout, "文件级 GT / Raw", total.RawFile)
	printMetrics(os.Stdout, "文件级 GT / Accepted", total.AcceptedFile)
	if len(failures) > 0 {
		fmt.Fprintln(os.Stdout, "\n=== Workflow 失败阶段 ===")
		for _, stage := range []string{"input", "normalize", "plan", "investigate", "review", "evaluate", "unknown"} {
			if failures[stage] > 0 {
				fmt.Fprintf(os.Stdout, "%-12s %8d\n", stage, failures[stage])
			}
		}
	}
	attrs.Print(os.Stdout)
	return nil
}

func runCase(ctx context.Context, llm *review.LLM, c *Case, verbose bool, traceDir string) caseResult {
	repo, err := materialize(c)
	if err != nil {
		return failedCase(c, nil, "input", err, traceDir)
	}
	defer os.RemoveAll(repo)
	wf, err := workflow.New(llm, repo)
	if err != nil {
		return failedCase(c, nil, "normalize", err, traceDir)
	}
	res, err := wf.Run(ctx, workflow.Request{Repo: repo, Diff: c.Diff})
	if err != nil || res == nil || res.Trace == nil {
		var trace *workflow.Trace
		if res != nil {
			trace = res.Trace
		}
		return failedCase(c, trace, detectFailureStage(trace, err), err, traceDir)
	}

	trace := res.Trace
	metrics := ComputeTrace(c, trace, tol)
	attrs := Attribute(c, trace, tol)
	cost := ComputeCost(trace)
	dim := ComputeDimension(c)
	if traceDir != "" {
		_ = SaveTrace(traceDir, EvalTrace{
			Name:             c.Name,
			Bugs:             c.Bugs(),
			GroundTruth:      c.GT,
			BaselineFindings: trace.Findings,
			Workflow:         trace,
			Attributions:     attrs,
			Metrics:          metrics,
			Dimension:        &dim,
			CostSummary:      cost,
		})
	}

	var output bytes.Buffer
	if verbose {
		printWorkflowTrace(&output, c.Name, trace, cost)
	} else {
		scope := "line"
		m := metrics.AcceptedLine
		if c.GT.Primary.Line <= 0 {
			scope = "file"
			m = metrics.AcceptedFile
		}
		fmt.Fprintf(&output, "%-20s scope=%s raw=%d accepted=%d hit=%d fp=%d cost=%dtok\n",
			c.Name, scope, len(trace.Findings), m.Findings, m.Found, m.False, cost.TotalTokens)
	}
	return caseResult{metrics: metrics, attrs: attrs, completed: true, output: output.String()}
}

func failedCase(c *Case, trace *workflow.Trace, stage string, err error, traceDir string) caseResult {
	metrics := FailureMetrics(c)
	if trace != nil {
		diagnostic := ComputeTrace(c, trace, tol)
		diagnostic.AcceptedLine = metrics.AcceptedLine
		diagnostic.AcceptedFile = metrics.AcceptedFile
		metrics = diagnostic
	}
	attrs := Attribute(c, trace, tol)
	message := "未知错误"
	if err != nil {
		message = err.Error()
	}
	if stage == "" {
		stage = "unknown"
	}
	if traceDir != "" {
		dim := ComputeDimension(c)
		var rawFindings []review.Finding
		if trace != nil {
			rawFindings = trace.Findings
		}
		_ = SaveTrace(traceDir, EvalTrace{
			Name:             c.Name,
			Bugs:             c.Bugs(),
			GroundTruth:      c.GT,
			BaselineFindings: rawFindings,
			Workflow:         trace,
			Attributions:     attrs,
			Metrics:          metrics,
			FailureStage:     stage,
			Failure:          message,
			Dimension:        &dim,
			CostSummary:      ComputeCost(trace),
		})
	}
	return caseResult{
		metrics:      metrics,
		attrs:        attrs,
		failureStage: stage,
		output:       fmt.Sprintf("%-20s workflow 失败 stage=%s: %s\n", c.Name, stage, message),
	}
}

func detectFailureStage(trace *workflow.Trace, err error) string {
	if trace == nil {
		return "unknown"
	}
	if err != nil && strings.HasPrefix(err.Error(), "review:") {
		return "review"
	}
	for i := len(trace.LLMCalls) - 1; i >= 0; i-- {
		if trace.LLMCalls[i].Error != "" {
			return normalizeFailureStage(trace.LLMCalls[i].Stage)
		}
	}
	if len(trace.Findings) > 0 && len(trace.Validations) == 0 {
		return "evaluate"
	}
	if len(trace.LLMCalls) > 0 {
		return normalizeFailureStage(trace.LLMCalls[len(trace.LLMCalls)-1].Stage)
	}
	if len(trace.Plan.TargetFiles) == 0 {
		return "normalize"
	}
	return "unknown"
}

func normalizeFailureStage(stage string) string {
	switch stage {
	case "normalize", "plan", "investigate", "review", "evaluate":
		return stage
	default:
		return "unknown"
	}
}

func printMetrics(w io.Writer, label string, metrics Metrics) {
	fmt.Fprintf(w, "\n%-24s Recall=%6.0f%% Precision=%6.0f%% Bugs=%d Findings=%d TP=%d FP=%d FN=%d\n",
		label, pct(metrics.Recall()), pct(metrics.Precision()), metrics.Bugs, metrics.Findings,
		metrics.TP, metrics.FP, metrics.FN)
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

func printWorkflowTrace(w io.Writer, name string, trace *workflow.Trace, cost CostSummary) {
	fmt.Fprintf(w, "\n=== Case: %s ===\nStopReason: %s\nDuration: %v\nLLM Calls: %d\nTool Calls: %d\nTokens: %d\nPlan files=%v symbols=%v questions=%d keywords=%d\nFindings: %d Validations: %d\n", name, trace.StopReason, trace.Duration, len(trace.LLMCalls), len(trace.ToolCalls), cost.TotalTokens, trace.Plan.TargetFiles, trace.Plan.Symbols, len(trace.Plan.Questions), len(trace.Plan.Keywords), len(trace.Findings), len(trace.Validations))
	for i, finding := range trace.Findings {
		fmt.Fprintf(w, "  F%d [%s] %s:%d %s\n", i+1, finding.Severity, finding.File, finding.Line, finding.Msg)
	}
	fmt.Fprintln(w)
}
