package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MUYI-luyu/codecritic/internal/diff"
	"github.com/MUYI-luyu/codecritic/internal/graph"
	"github.com/MUYI-luyu/codecritic/internal/recall"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

type Workflow struct {
	llm      LLMClient
	repo     string
	maxSteps int
	model    string
	logger   *slog.Logger
}

func New(llm LLMClient, repo string) (*Workflow, error) {
	if llm == nil {
		return nil, fmt.Errorf("nil LLM")
	}
	if repo == "" {
		return nil, fmt.Errorf("empty repository")
	}
	return &Workflow{llm: llm, repo: repo, maxSteps: 8, model: "claude-sonnet-5", logger: slog.Default()}, nil
}
func (w *Workflow) SetLogger(logger *slog.Logger) {
	if logger != nil {
		w.logger = logger
	}
}

// SetMaxSteps bounds investigator tool calls for predictable cost.
func (w *Workflow) SetMaxSteps(n int) {
	if n > 0 {
		w.maxSteps = n
	}
}

// SetInvestigatorModel overrides the investigator model for this workflow.
func (w *Workflow) SetInvestigatorModel(model string) {
	if strings.TrimSpace(model) != "" {
		w.model = model
	}
}

type Result struct{ Trace *Trace }

func (w *Workflow) Run(ctx context.Context, req Request) (*Result, error) {
	started := time.Now()
	id := traceID()
	tr := &Trace{ID: id, Request: req}
	obs := &observer{trace: tr}
	ctx = review.WithLLMObserver(ctx, obs)
	obs.setStage("normalize")
	changes, err := diff.Parse(req.Diff)
	if err != nil {
		return w.fail(tr, started, StopStageError, fmt.Errorf("parse diff: %w", err))
	}
	tr.Plan = Plan{Concern: "审查变更中的真实缺陷，重点关注并发、错误处理、边界和资源生命周期"}
	for _, c := range changes {
		if c.File != "" && c.File != "/dev/null" {
			tr.Plan.TargetFiles = append(tr.Plan.TargetFiles, c.File)
		}
		src, er := readRepoFile(w.repo, c.File)
		if er == nil {
			c.Annotate(src)
			for _, s := range c.Symbols {
				tr.Plan.Symbols = append(tr.Plan.Symbols, s.Name)
			}
		}
	}
	obs.setStage("plan")
	points, _, planErr := w.llm.Plan(ctx, string(req.Diff))
	if planErr == nil {
		for _, p := range points {
			tr.Plan.Questions = append(tr.Plan.Questions, p.Desc)
			tr.Plan.Keywords = append(tr.Plan.Keywords, p.Kw...)
		}
	} else {
		tr.Errors = append(tr.Errors, fmt.Sprintf("plan: %v", planErr))
		// Degrade to the deterministic file/symbol plan and continue.
	}
	idx, _ := graph.Build(w.repo)
	ts := &toolset{repo: w.repo, index: idx, store: recall.New(w.repo, idx)}
	obs.setStage("investigate")
	if err := w.investigate(ctx, tr, ts); err != nil {
		reason := tr.StopReason
		if ctx.Err() != nil {
			reason = StopContextCanceled
		}
		return w.fail(tr, started, reason, err)
	}
	prompt := buildReviewPrompt(req.Diff, tr.Plan, tr.Evidence)
	obs.setStage("review")
	findings, _, err := w.llm.Review(ctx, prompt)
	if err != nil {
		reason := StopStageError
		if ctx.Err() != nil {
			reason = StopContextCanceled
		}
		return w.fail(tr, started, reason, fmt.Errorf("review: %w", err))
	}
	tr.Findings = findings
	tr.Validations = validateFindings(findings, tr.Evidence)
	if tr.StopReason == "" {
		tr.StopReason = StopStageError
	}
	tr.Duration = time.Since(started)
	return &Result{Trace: tr}, nil
}

func (w *Workflow) fail(tr *Trace, started time.Time, reason string, err error) (*Result, error) {
	if reason == "" {
		reason = StopStageError
	}
	tr.StopReason = reason
	tr.Duration = time.Since(started)
	if err != nil {
		tr.Errors = append(tr.Errors, err.Error())
	}
	return &Result{Trace: tr}, err
}

func (w *Workflow) investigate(ctx context.Context, tr *Trace, ts *toolset) error {
	history := map[string]bool{}
	for step := 1; step <= w.maxSteps; step++ {
		prompt := buildDecisionPrompt(tr)
		text, _, err := w.llm.ChatWithUsage(ctx, "你是代码审查调查员，只返回 JSON。", prompt, w.model)
		if err != nil {
			if ctx.Err() != nil {
				tr.StopReason = StopContextCanceled
			} else {
				tr.StopReason = StopStageError
			}
			return err
		}
		var a struct {
			Done bool                   `json:"done"`
			Tool string                 `json:"tool"`
			Args map[string]interface{} `json:"args"`
		}
		if err := json.Unmarshal([]byte(stripJSON(text)), &a); err != nil {
			tr.StopReason = StopInvalidDecision
			return fmt.Errorf("decision JSON: %w", err)
		}
		if a.Done {
			tr.StopReason = StopAgentDone
			return nil
		}
		if strings.TrimSpace(a.Tool) == "" {
			tr.StopReason = StopInvalidDecision
			return fmt.Errorf("decision JSON: missing tool")
		}
		key := fmt.Sprintf("%s:%v", a.Tool, a.Args)
		if history[key] {
			continue
		}
		history[key] = true
		tc, ev := toolCall(a.Tool, a.Args, func() ([]*Evidence, error) { return ts.Execute(ctx, a.Tool, a.Args) })
		tc.Step = step
		for i, e := range ev {
			e.ID = fmt.Sprintf("e%d", len(tr.Evidence)+i+1)
			tc.EvidenceIDs = append(tc.EvidenceIDs, e.ID)
		}
		tr.ToolCalls = append(tr.ToolCalls, tc)
		tr.Evidence = append(tr.Evidence, ev...)
		w.logger.Info("workflow tool", "trace_id", tr.ID, "step", step, "tool", a.Tool, "evidence", len(ev), "error", tc.Error)
		if tc.Error != "" {
			tr.StopReason = StopToolError
			return fmt.Errorf("tool %s: %s", a.Tool, tc.Error)
		}
	}
	tr.StopReason = StopMaxSteps
	return nil
}

func buildDecisionPrompt(tr *Trace) string {
	return fmt.Sprintf("假设：%s\n目标文件：%s\n问题：%s\n已有证据：%s\n工具历史：%s\n可用工具：read_code(file), search_code(keyword), find_callers(symbol,file), run_static_rules()。先调查目标文件，证据足够时返回 {\"done\":true}，否则只返回 {\"done\":false,\"tool\":\"...\",\"args\":{}}。", tr.Plan.Concern, strings.Join(tr.Plan.TargetFiles, ", "), strings.Join(tr.Plan.Questions, "; "), encodeEvidence(tr.Evidence), summarizeCalls(tr.ToolCalls))
}
func buildReviewPrompt(d []byte, p Plan, e []*Evidence) string {
	return fmt.Sprintf("审查 diff 并只输出 JSON {\"findings\":[{\"file\":\"...\",\"line\":0,\"severity\":\"error|warning|info\",\"msg\":\"...\",\"evidence\":\"...\"}]}。计划：%+v\n证据：%s\nDiff：\n%s", p, encodeEvidence(e), d)
}
func validateFindings(fs []review.Finding, e []*Evidence) []Validation {
	out := make([]Validation, 0, len(fs))
	for i, f := range fs {
		ok := false
		for _, x := range e {
			if x.File == f.File && (x.Line == 0 || f.Line == 0 || abs(x.Line-f.Line) <= 3) {
				ok = true
				break
			}
		}
		out = append(out, Validation{FindingIndex: i, Accepted: ok, Confidence: map[bool]float64{true: 0.8, false: 0.2}[ok], Reason: map[bool]string{true: "evidence matches finding", false: "no matching evidence"}[ok]})
	}
	return out
}
func summarizeCalls(cs []ToolCall) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "%d:%s(%v) ", c.Step, c.Tool, c.Args)
	}
	return b.String()
}
func readRepoFile(repo, file string) ([]byte, error) { return os.ReadFile(filepath.Join(repo, file)) }
func stripJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
func traceID() string {
	b := make([]byte, 6)
	if _, e := rand.Read(b); e != nil {
		return fmt.Sprintf("review-%d", time.Now().UnixNano())
	}
	return "review-" + hex.EncodeToString(b)
}
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
