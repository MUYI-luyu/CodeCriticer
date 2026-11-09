package eval

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

const tol = 3
const DefaultConcurrency = 4

type caseResult struct {
	name         string
	metrics      TraceMetrics
	attrs        []BugAttribution
	axes         EvaluationAxes
	semantic     *SemanticEvaluation
	trace        *workflow.Trace
	completed    bool
	failureStage string
	output       string
}

type runOptions struct {
	contextCheckpointing bool
	contextBudget        int
}

func Run(ctx context.Context, llm *review.LLM, datasetDir string, verbose bool) error {
	return RunConcurrent(ctx, llm, datasetDir, verbose, DefaultConcurrency, "")
}

func RunConcurrent(ctx context.Context, llm *review.LLM, datasetDir string, verbose bool, concurrency int, traceDir string) error {
	_, err := runConcurrent(ctx, llm, nil, datasetDir, verbose, concurrency, traceDir, runOptions{})
	return err
}

// RunConcurrentWithJudge adds independent semantic Claim/Evidence grading. The
// judge observes completed traces only and never participates in the Agent loop.
func RunConcurrentWithJudge(ctx context.Context, llm, judgeLLM *review.LLM, judgeModel, datasetDir string, verbose bool, concurrency int, traceDir string) error {
	_, err := runConcurrent(ctx, llm, newSemanticJudge(judgeLLM, judgeModel), datasetDir, verbose, concurrency, traceDir, runOptions{})
	return err
}

// RunConcurrentCheckpointed enables checkpoint-based active context.
func RunConcurrentCheckpointed(ctx context.Context, llm *review.LLM, datasetDir string, verbose bool, concurrency int, traceDir string) error {
	_, err := runConcurrent(ctx, llm, nil, datasetDir, verbose, concurrency, traceDir, runOptions{contextCheckpointing: true})
	return err
}

// RunConcurrentCheckpointedWithBudget is an eval-only pressure-test entry
// point. Production continues to use the conservative default budget.
func RunConcurrentCheckpointedWithBudget(ctx context.Context, llm *review.LLM, datasetDir string, verbose bool, concurrency int, traceDir string, contextBudget int) error {
	_, err := runConcurrent(ctx, llm, nil, datasetDir, verbose, concurrency, traceDir, runOptions{contextCheckpointing: true, contextBudget: contextBudget})
	return err
}

func RunConcurrentCheckpointedWithJudge(ctx context.Context, llm, judgeLLM *review.LLM, judgeModel, datasetDir string, verbose bool, concurrency int, traceDir string) error {
	_, err := runConcurrent(ctx, llm, newSemanticJudge(judgeLLM, judgeModel), datasetDir, verbose, concurrency, traceDir, runOptions{contextCheckpointing: true})
	return err
}

// RunContextCheckpointAB performs one paired experiment. Both arms use the
// same configured model, prompt, tools, verifier and budgets. Model sampling
// and analyzer iteration are not seeded, so deltas remain descriptive.
func RunContextCheckpointAB(ctx context.Context, llm *review.LLM, datasetDir string, verbose bool, concurrency int, traceDir string) error {
	return RunContextCheckpointABWithBudget(ctx, llm, datasetDir, verbose, concurrency, traceDir, 0)
}

func RunContextCheckpointABWithBudget(ctx context.Context, llm *review.LLM, datasetDir string, verbose bool, concurrency int, traceDir string, contextBudget int) error {
	baselineDir, checkpointDir := "", ""
	if traceDir != "" {
		baselineDir = filepath.Join(traceDir, "baseline")
		checkpointDir = filepath.Join(traceDir, "checkpoint")
	}
	fmt.Fprintln(os.Stdout, "\n========== A: Full context ==========")
	baseline, err := runConcurrent(ctx, llm, nil, datasetDir, verbose, concurrency, baselineDir, runOptions{contextBudget: contextBudget})
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "\n========== B: Context checkpoint ==========")
	checkpointed, err := runConcurrent(ctx, llm, nil, datasetDir, verbose, concurrency, checkpointDir, runOptions{contextCheckpointing: true, contextBudget: contextBudget})
	if err != nil {
		return err
	}
	printABComparison(os.Stdout, baseline, checkpointed)
	return nil
}

// RunConcurrentWithDiagnosis runs the normal baseline once, then applies
// eval-only interventions to completed misses. The production Agent is unchanged.
func RunConcurrentWithDiagnosis(ctx context.Context, llm, judgeLLM *review.LLM, judgeModel, datasetDir string, verbose bool, concurrency int, traceDir string) error {
	var judge *semanticJudge
	if judgeLLM != nil {
		judge = newSemanticJudge(judgeLLM, judgeModel)
	}
	results, err := runConcurrent(ctx, llm, judge, datasetDir, verbose, concurrency, traceDir, runOptions{})
	if err != nil {
		return err
	}
	return diagnoseFailures(ctx, llm, judge, datasetDir, traceDir, results)
}

func runConcurrent(ctx context.Context, llm *review.LLM, judge *semanticJudge, datasetDir string, verbose bool, concurrency int, traceDir string, options runOptions) ([]caseResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cases, err := Load(datasetDir)
	if err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("数据集为空: %s", datasetDir)
	}
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	if concurrency > len(cases) {
		concurrency = len(cases)
	}
	if traceDir != "" {
		if err := os.MkdirAll(traceDir, 0755); err != nil {
			return nil, err
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
				r := runCase(ctx, llm, judge, c, verbose, traceDir, options)
				select {
				case results <- r:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, c := range cases {
			select {
			case jobs <- c:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	var total TraceMetrics
	var attrs AttributionCounts
	var axes EvaluationAxesSummary
	var semantic SemanticSummary
	completed := 0
	failures := make(map[string]int)
	done := 0
	collected := make([]caseResult, 0, len(cases))
	for r := range results {
		done++
		collected = append(collected, r)
		axes = axes.Add(r.axes)
		semantic = semantic.Add(r.semantic)
		if r.completed {
			total = addCompletedMetrics(total, r)
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
	printConventionMetrics(os.Stdout, "规范项 / Raw", total.RawConvention)
	printConventionMetrics(os.Stdout, "规范项 / Accepted", total.AcceptedConvention)
	if len(failures) > 0 {
		fmt.Fprintln(os.Stdout, "\n=== Workflow 失败阶段 ===")
		for _, stage := range []string{"input", "normalize", "agent", "verifier", "trace", "unknown"} {
			if failures[stage] > 0 {
				fmt.Fprintf(os.Stdout, "%-12s %8d\n", stage, failures[stage])
			}
		}
	}
	attrs.Print(os.Stdout)
	axes.Print(os.Stdout)
	semantic.Print(os.Stdout)
	if err := ctx.Err(); err != nil {
		return collected, err
	}
	return collected, nil
}

func addCompletedMetrics(total TraceMetrics, result caseResult) TraceMetrics {
	if !result.completed {
		return total
	}
	return total.Add(result.metrics)
}

func runCase(ctx context.Context, llm *review.LLM, judge *semanticJudge, c *Case, verbose bool, traceDir string, options runOptions) caseResult {
	repo, err := materialize(c)
	if err != nil {
		return failedCase(c, nil, "input", err, traceDir)
	}
	defer os.RemoveAll(repo)
	run, err := workflow.NewReviewRun(llm, repo)
	if err != nil {
		return failedCase(c, nil, "normalize", err, traceDir)
	}
	run.SetContextCheckpointing(options.contextCheckpointing)
	if options.contextBudget > 0 {
		run.SetModelContextBudget(options.contextBudget)
	}
	res, err := run.Run(ctx, workflow.Request{Repo: repo, Diff: c.Diff})
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
	axes := ComputeAxes(trace, true)
	var semantic *SemanticEvaluation
	if judge != nil && c.Category == CategorySemanticPositive {
		graded := judge.Grade(ctx, c, trace)
		semantic = &graded
	}
	if traceDir != "" {
		if err := SaveTrace(traceDir, EvalTrace{
			Name:         c.Name,
			Category:     c.Category,
			GoVersion:    c.GoVersion,
			PolicyID:     c.PolicyID,
			Bugs:         c.Bugs(),
			GroundTruth:  c.GT,
			Workflow:     trace,
			Attributions: attrs,
			Metrics:      metrics,
			Axes:         axes,
			Semantic:     semantic,
			Dimension:    &dim,
			CostSummary:  cost,
		}); err != nil {
			trace.Errors = append(trace.Errors, fmt.Sprintf("save eval trace: %v", err))
			return caseResult{
				name: c.Name, metrics: metrics, attrs: attrs, axes: ComputeAxes(trace, false), semantic: semantic, trace: trace,
				failureStage: "trace", output: fmt.Sprintf("%-20s workflow 完成但 Trace 保存失败: %v\n", c.Name, err),
			}
		}
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
		if c.Category == CategoryConvention {
			fmt.Fprintf(&output, "%-20s category=%s policy=%s raw_claims=%d accepted_claims=%d policy_hit=%d cost=%dtok\n",
				c.Name, c.Category, c.PolicyID, metrics.RawConvention.Claims, metrics.AcceptedConvention.Claims, metrics.AcceptedConvention.Hits, cost.TotalTokens)
		} else {
			fmt.Fprintf(&output, "%-20s category=%s scope=%s raw=%d accepted=%d hit=%d fp=%d cost=%dtok\n",
				c.Name, c.Category, scope, len(trace.FinalReport.Claims), m.Claims, m.Found, m.False, cost.TotalTokens)
		}
	}
	return caseResult{name: c.Name, metrics: metrics, attrs: attrs, axes: axes, semantic: semantic, trace: trace, completed: true, output: output.String()}
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
		if saveErr := SaveTrace(traceDir, EvalTrace{
			Name:         c.Name,
			Category:     c.Category,
			GoVersion:    c.GoVersion,
			PolicyID:     c.PolicyID,
			Bugs:         c.Bugs(),
			GroundTruth:  c.GT,
			Workflow:     trace,
			Attributions: attrs,
			Metrics:      metrics,
			Axes:         ComputeAxes(trace, false),
			FailureStage: stage,
			Failure:      message,
			Dimension:    &dim,
			CostSummary:  ComputeCost(trace),
		}); saveErr != nil {
			if trace != nil {
				trace.Errors = append(trace.Errors, fmt.Sprintf("save failed eval trace: %v", saveErr))
			}
			message = fmt.Sprintf("%s; save eval trace: %v", message, saveErr)
		}
	}
	return caseResult{
		name:         c.Name,
		metrics:      metrics,
		attrs:        attrs,
		axes:         ComputeAxes(trace, false),
		trace:        trace,
		failureStage: stage,
		output:       fmt.Sprintf("%-20s workflow 失败 stage=%s: %s\n", c.Name, stage, message),
	}
}

func printABComparison(w io.Writer, baseline, projected []caseResult) {
	byName := make(map[string]caseResult, len(projected))
	for _, result := range projected {
		byName[result.name] = result
	}
	names := make([]string, 0, len(baseline))
	baseByName := make(map[string]caseResult, len(baseline))
	for _, result := range baseline {
		names = append(names, result.name)
		baseByName[result.name] = result
	}
	sort.Strings(names)
	fmt.Fprintln(w, "\n========== A/B comparison (B - A) ==========")
	fmt.Fprintln(w, "Descriptive only: model sampling is unseeded; do not attribute a single-run delta to checkpointing when no checkpoint occurred.")
	fmt.Fprintf(w, "%-24s %10s %10s %10s %12s %12s %10s\n", "case", "outcome", "claims", "tokens", "tool_calls", "context_B", "checkpoints")
	for _, name := range names {
		a := baseByName[name]
		b, ok := byName[name]
		if !ok || !a.completed || !b.completed {
			fmt.Fprintf(w, "%-24s %10s\n", name, "infra/n-a")
			continue
		}
		aHit, bHit := acceptedHit(a.metrics), acceptedHit(b.metrics)
		outcome := fmt.Sprintf("%d->%d", aHit, bHit)
		contextDelta, checkpoints := 0, 0
		if b.trace != nil {
			contextDelta = b.trace.Compaction.ActiveBytes - b.trace.Compaction.FullBytes
			checkpoints = b.trace.Compaction.Checkpoints
		}
		fmt.Fprintf(w, "%-24s %10s %+10d %+10d %+12d %+12d %10d\n", name, outcome,
			b.axes.Outcome.AcceptedClaims-a.axes.Outcome.AcceptedClaims,
			b.axes.Efficiency.TotalTokens-a.axes.Efficiency.TotalTokens,
			b.axes.Investigation.SuccessfulTools-a.axes.Investigation.SuccessfulTools,
			contextDelta, checkpoints)
	}
}

func acceptedHit(metrics TraceMetrics) int {
	if metrics.AcceptedLine.Bugs > 0 {
		return metrics.AcceptedLine.Found
	}
	return metrics.AcceptedFile.Found
}

func detectFailureStage(trace *workflow.Trace, err error) string {
	if trace == nil {
		return "unknown"
	}
	for i := len(trace.LLMCalls) - 1; i >= 0; i-- {
		if trace.LLMCalls[i].Error != "" {
			return normalizeFailureStage(trace.LLMCalls[i].Stage)
		}
	}
	if len(trace.FinalReport.Claims) > 0 && len(trace.FinalReport.Verdicts) == 0 {
		return "verifier"
	}
	if len(trace.LLMCalls) > 0 {
		return normalizeFailureStage(trace.LLMCalls[len(trace.LLMCalls)-1].Stage)
	}
	if len(trace.Scope.TargetFiles) == 0 {
		return "normalize"
	}
	return "unknown"
}

func normalizeFailureStage(stage string) string {
	switch stage {
	case "normalize", "agent", "verifier":
		return stage
	default:
		return "unknown"
	}
}

func printMetrics(w io.Writer, label string, metrics Metrics) {
	fmt.Fprintf(w, "\n%-24s Recall=%6.0f%% Precision=%6.0f%% Bugs=%d Claims=%d TP=%d FP=%d FN=%d\n",
		label, pct(metrics.Recall()), pct(metrics.Precision()), metrics.Bugs, metrics.Claims,
		metrics.TP, metrics.FP, metrics.FN)
}

func printConventionMetrics(w io.Writer, label string, metrics ConventionMetrics) {
	if metrics.Cases == 0 {
		return
	}
	fmt.Fprintf(w, "\n%-24s HitRate=%6.0f%% Cases=%d Hits=%d Claims=%d\n",
		label, pct(metrics.HitRate()), metrics.Cases, metrics.Hits, metrics.Claims)
}

func materialize(c *Case) (dir string, err error) {
	if c == nil {
		return "", fmt.Errorf("nil eval case")
	}
	dir, err = os.MkdirTemp("", "cceval")
	if err != nil {
		return "", err
	}
	defer func() {
		if err == nil {
			return
		}
		if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
			err = fmt.Errorf("%w; cleanup materialized case: %v", err, cleanupErr)
		}
	}()
	goVersion := strings.TrimSpace(c.GoVersion)
	if goVersion == "" {
		goVersion = "1.22"
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/eval\n\ngo "+goVersion+"\n"), 0644); err != nil {
		return "", err
	}
	for name, content := range c.Repo {
		rel := filepath.Clean(name)
		if rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", fmt.Errorf("eval repository path escapes case root: %q", name)
		}
		full := filepath.Join(dir, rel)
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
	accepted := trace.FinalReport.ClaimsWithStatus(workflow.VerdictAccepted)
	fmt.Fprintf(w, "\n=== Case: %s ===\nStopReason: %s\nDuration: %v\nLLM Calls: %d\nInvestigation Steps: %d\nTokens: %d\nScope files=%v symbols=%v\nCandidate claims: %d Accepted: %d Verdicts: %d\n", name, trace.StopReason, trace.Duration, len(trace.LLMCalls), len(trace.Investigation), cost.TotalTokens, trace.Scope.TargetFiles, trace.Scope.Symbols, len(trace.FinalReport.Claims), len(accepted), len(trace.FinalReport.Verdicts))
	for i, finding := range accepted {
		fmt.Fprintf(w, "  F%d [%s] %s:%d %s\n", i+1, finding.Severity, finding.File, finding.Line, finding.Msg)
	}
	fmt.Fprintln(w)
}
