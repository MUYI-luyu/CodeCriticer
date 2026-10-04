package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MUYI-luyu/codecritic/internal/diff"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

// ReviewRun owns one complete review lifecycle. It binds the input snapshot,
// trace and append-only Evidence collection to the session that investigates
// that input.
type ReviewRun struct {
	llm            LLMClient
	repo           string
	snapshot       *ReviewSnapshot
	maxSteps       int
	model          string
	logger         *slog.Logger
	projectContext bool
	trace          *Trace
	evidence       []*Evidence
	tools          *ToolRuntime
	mu             sync.Mutex
	started        bool
	finished       bool
	closed         bool
}

var ErrReviewRunAlreadyStarted = errors.New("review run already started")
var ErrReviewRunClosed = errors.New("review run closed before start")
var ErrReviewRunRunning = errors.New("review run is still running")

// NewWithSnapshot creates a ReviewRun whose complete analysis surface is
// rooted at one committed Git snapshot. Ownership is transferred to the
// ReviewRun: the supplied value is cleared after a successful transfer, and
// the run closes its private snapshot before Run returns.
func NewWithSnapshot(llm LLMClient, snapshot *ReviewSnapshot) (*ReviewRun, error) {
	if snapshot == nil || snapshot.Root == "" || snapshot.BaseSHA == "" || snapshot.HeadSHA == "" || snapshot.DiffHash == "" || snapshot.sourceRoot == "" || snapshot.tempRoot == "" {
		return nil, fmt.Errorf("invalid review snapshot")
	}
	w, err := NewReviewRun(llm, snapshot.Root)
	if err != nil {
		return nil, err
	}
	owned := *snapshot
	*snapshot = ReviewSnapshot{}
	w.snapshot = &owned
	return w, nil
}

// NewReviewRun creates the direct repository entry point used by Eval and
// focused tests. Production CLI review admission uses NewWithSnapshot.
func NewReviewRun(llm LLMClient, repo string) (*ReviewRun, error) {
	if llm == nil {
		return nil, fmt.Errorf("nil LLM")
	}
	if repo == "" {
		return nil, fmt.Errorf("empty repository")
	}
	model := "gpt-5.4"
	if provider, ok := llm.(interface{ AgentModel() string }); ok {
		if configured := strings.TrimSpace(provider.AgentModel()); configured != "" {
			model = configured
		}
	}
	return &ReviewRun{llm: llm, repo: repo, maxSteps: 8, model: model, logger: slog.Default()}, nil
}

func (w *ReviewRun) SetLogger(logger *slog.Logger) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started || w.closed {
		return
	}
	if logger != nil {
		w.logger = logger
	}
}

// 设置调查工具调用上限。
func (w *ReviewRun) SetMaxSteps(n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started || w.closed {
		return
	}
	if n > 0 {
		w.maxSteps = n
	}
}

// 设置 Agent 模型。
func (w *ReviewRun) SetAgentModel(model string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started || w.closed {
		return
	}
	if strings.TrimSpace(model) != "" {
		w.model = model
	}
}

// SetContextProjection enables deterministic removal of redundant tool-result
// content from model requests. The complete conversation is still kept in Trace.
func (w *ReviewRun) SetContextProjection(enabled bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started || w.closed {
		return
	}
	w.projectContext = enabled
}

type Result struct{ Trace *Trace }

// appendEvidence is the sole mutation point for run-owned Evidence. Existing
// entries are never replaced; a newly observed window receives a new identity.
func (w *ReviewRun) appendEvidence(e *Evidence) string {
	if e == nil {
		return ""
	}
	e.ID = fmt.Sprintf("e%d", len(w.evidence)+1)
	w.evidence = append(w.evidence, cloneEvidence(e))
	if w.trace != nil {
		w.trace.Evidence = w.evidenceSnapshot()
	}
	return e.ID
}

func (w *ReviewRun) evidenceSnapshot() []*Evidence {
	out := make([]*Evidence, len(w.evidence))
	for i, item := range w.evidence {
		out[i] = cloneEvidence(item)
	}
	return out
}

func cloneEvidence(e *Evidence) *Evidence {
	if e == nil {
		return nil
	}
	copy := *e
	return &copy
}

// Close releases an owned snapshot when a run is abandoned before Run starts.
// Once Run has started, Run itself is the only owner allowed to clean up.
func (w *ReviewRun) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	if w.started && !w.finished {
		return ErrReviewRunRunning
	}
	if w.snapshot != nil {
		if err := w.snapshot.Close(); err != nil {
			return err
		}
	}
	w.closed = true
	return nil
}

func (w *ReviewRun) Run(ctx context.Context, req Request) (result *Result, runErr error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil, ErrReviewRunClosed
	}
	if w.started {
		w.mu.Unlock()
		return nil, ErrReviewRunAlreadyStarted
	}
	w.started = true
	w.mu.Unlock()
	started := time.Now()
	id := traceID()
	defer func() {
		defer func() {
			w.mu.Lock()
			w.finished = true
			w.mu.Unlock()
		}()
		if w.snapshot == nil {
			return
		}
		if cleanupErr := w.snapshot.Close(); cleanupErr != nil {
			if result == nil {
				result = &Result{Trace: w.trace}
			}
			if result.Trace != nil {
				result.Trace.Errors = append(result.Trace.Errors, fmt.Sprintf("snapshot cleanup: %v", cleanupErr))
				if runErr == nil || result.Trace.StopReason == "" {
					result.Trace.StopReason = StopStageError
				}
				result.Trace.Duration = time.Since(started)
			}
			if runErr == nil {
				runErr = cleanupErr
			} else {
				runErr = fmt.Errorf("%w; snapshot cleanup: %v", runErr, cleanupErr)
			}
		}
	}()
	if req.Repo != "" && filepath.Clean(req.Repo) != filepath.Clean(w.repo) {
		tr := &Trace{ID: id, Request: req}
		w.trace = tr
		return w.fail(tr, started, StopStageError, fmt.Errorf("request repository differs from workflow repository"))
	}
	if w.snapshot != nil && hashBytes(req.Diff) != w.snapshot.DiffHash {
		tr := &Trace{ID: id, Request: req, Snapshot: w.snapshot.traceCopy()}
		w.trace = tr
		return w.fail(tr, started, StopStageError, fmt.Errorf("request diff differs from review snapshot"))
	}
	if w.snapshot != nil && req.AllowExecution && w.snapshot.executionRepo() == "" {
		tr := &Trace{ID: id, Request: req, Snapshot: w.snapshot.traceCopy()}
		w.trace = tr
		return w.fail(tr, started, StopStageError, fmt.Errorf("review snapshot was created without an execution worktree"))
	}
	req.Repo = w.repo
	tr := &Trace{ID: id, Request: req, Snapshot: w.snapshot.traceCopy()}
	w.trace = tr
	w.evidence = nil
	obs := &observer{trace: tr}
	ctx = review.WithLLMObserver(ctx, obs)
	obs.setStage("normalize")
	changes, err := diff.Parse(req.Diff)
	if err != nil {
		return w.fail(tr, started, StopStageError, fmt.Errorf("parse diff: %w", err))
	}
	tr.Scope = ReviewScope{Concern: "审查变更中的真实缺陷，重点关注并发、错误处理、边界和资源生命周期"}
	for i := range changes {
		c := &changes[i]
		if c.File != "" && c.File != "/dev/null" {
			file, er := repoRelativePath(w.repo, c.File)
			if er != nil {
				return w.fail(tr, started, StopStageError, fmt.Errorf("diff path: %w", er))
			}
			c.File = file
			if !containsString(tr.Scope.TargetFiles, file) {
				tr.Scope.TargetFiles = append(tr.Scope.TargetFiles, file)
			}
		}
		if c.Old != "" && c.Old != "/dev/null" {
			old, er := repoRelativePath(w.repo, c.Old)
			if er != nil {
				return w.fail(tr, started, StopStageError, fmt.Errorf("old diff path: %w", er))
			}
			c.Old = old
			if c.File == "/dev/null" && !containsString(tr.Scope.TargetFiles, old) {
				tr.Scope.TargetFiles = append(tr.Scope.TargetFiles, old)
			}
		}
		var src []byte
		var er error
		if c.File != "/dev/null" {
			src, er = readRepoFile(w.repo, c.File)
		}
		if er == nil {
			if len(src) > 0 {
				c.Annotate(src)
			}
			for _, s := range c.Symbols {
				if !containsString(tr.Scope.Symbols, s.Name) {
					tr.Scope.Symbols = append(tr.Scope.Symbols, s.Name)
				}
			}
			// 先把变更行放入证据，保证调查员从真实修改点开始。
			lines := strings.Split(string(src), "\n")
			for _, add := range c.Adds {
				if add.No < 1 || add.No > len(lines) {
					continue
				}
				symbol := ""
				for _, s := range c.Symbols {
					if add.No >= s.Line && add.No <= s.EndLine {
						symbol = s.Name
						break
					}
				}
				w.appendEvidence(&Evidence{Source: "diff", Type: "changed_line", File: c.File, Line: add.No, Content: lines[add.No-1], Symbol: symbol})
			}
		}
		for _, del := range c.Dels {
			file := changeReviewFile(*c)
			w.appendEvidence(&Evidence{Source: "diff", Type: "deleted_line", File: file, Line: del.No, Content: del.Text})
		}
	}
	validationRepo := ""
	if req.AllowExecution && w.snapshot != nil {
		validationRepo = w.snapshot.executionRepo()
	}
	w.tools = newToolRuntime(w.repo, validationRepo, changes, req.AllowExecution, toolSurfaceAgent)
	obs.setStage("agent")
	session := newReviewSession(w, tr, w.tools)
	claims, verdicts, err := session.Run(ctx)
	tr.StopReason = session.stopReason
	if err != nil {
		// 即使 Agent 未能正常收敛，也保留最后一次提交及裁决用于追踪；
		// 调用方仍会收到错误，不能把未完成审查解释成“未发现问题”。
		tr.FinalReport = FinalReport{Claims: claims, Verdicts: verdicts}
		reason := tr.StopReason
		if ctx.Err() != nil {
			reason = StopContextCanceled
		}
		return w.fail(tr, started, reason, err)
	}
	if tr.StopReason == "" {
		tr.StopReason = StopAgentDone
	}
	tr.FinalReport = FinalReport{Claims: claims, Verdicts: verdicts}
	tr.Duration = time.Since(started)
	return &Result{Trace: tr}, nil
}

func (w *ReviewRun) fail(tr *Trace, started time.Time, reason string, err error) (*Result, error) {
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

func decodeToolArgs(arguments string, dst any) error {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func toolResult(id string, value any) review.ToolMessage {
	b, _ := json.Marshal(value)
	return review.ToolMessage{Role: "tool", ToolCallID: id, Content: string(b)}
}

func maxEvidenceLine(e *Evidence) int {
	if e.EndLine > e.Line {
		return e.EndLine
	}
	return e.Line
}

const agentSystemPrompt = `You are a Go code review agent. Investigate the change with the provided tools. Relevant code is not by itself proof of a bug. Call exactly one tool per turn. When the review is complete, call submit_claims; submit an empty claims array if there are no supported issues.`

func buildAgentPrompt(tr *Trace, changes []diff.Change, allowExecution bool) string {
	diffContext := encodeChangedContext(tr, changes)
	execution := "compile only; test and race execution are disabled"
	if allowExecution {
		execution = "compile, test and race validation are enabled"
	}
	return fmt.Sprintf("%s\n目标文件：%s\n目标符号：%s\nDiff hunk（每个 included hunk 都是完整的；omitted hunks 必须用 read_diff 读取后才能声称已审查）：%s\nGo validation policy: %s.\n你负责理解代码、提出假设、选择工具并判断何时提交。read_code 可按行段或 symbol 读取；inspect_symbol 提供编译器语义 references 与带精度说明的 call hierarchy。Claim 必须描述单一根因、触发条件和实际后果，并引用足以支撑结论的 Evidence ID。", tr.Scope.Concern, strings.Join(tr.Scope.TargetFiles, ", "), strings.Join(tr.Scope.Symbols, ", "), diffContext, execution)
}

func encodeChangedContext(tr *Trace, changes []diff.Change) string {
	type hunkManifest struct {
		File     string `json:"file"`
		HunkID   string `json:"hunk_id"`
		OldRange string `json:"old_range"`
		NewRange string `json:"new_range"`
		Section  string `json:"section,omitempty"`
		Included bool   `json:"included"`
	}
	type includedHunk struct {
		File    string `json:"file"`
		HunkID  string `json:"hunk_id"`
		Content string `json:"content"`
	}
	const contentBudget = 64 << 10
	used := 0
	manifest := make([]hunkManifest, 0)
	included := make([]includedHunk, 0)
	omitted := make([]string, 0)
	for _, change := range changes {
		file := changeReviewFile(change)
		for _, hunk := range change.Hunks {
			content := formatPromptHunk(tr, file, hunk)
			fits := used+len(content) <= contentBudget
			manifest = append(manifest, hunkManifest{
				File: file, HunkID: hunk.ID,
				OldRange: fmt.Sprintf("%d,%d", hunk.OldStart, hunk.OldLines),
				NewRange: fmt.Sprintf("%d,%d", hunk.NewStart, hunk.NewLines),
				Section:  hunk.Section, Included: fits,
			})
			if fits {
				included = append(included, includedHunk{File: file, HunkID: hunk.ID, Content: content})
				used += len(content)
			} else {
				omitted = append(omitted, file+":"+hunk.ID)
			}
		}
	}
	payload := map[string]any{"manifest": manifest, "included_hunks": included, "omitted_hunks": omitted}
	b, _ := json.Marshal(payload)
	return string(b)
}

func formatPromptHunk(tr *Trace, file string, hunk diff.Hunk) string {
	content := formatHunk(hunk)
	for _, evidence := range tr.Evidence {
		if evidence == nil || evidence.Source != "diff" || evidence.File != file {
			continue
		}
		needle := fmt.Sprintf("old=%d new=0", evidence.Line)
		if evidence.Type == "changed_line" {
			needle = fmt.Sprintf("old=0 new=%d", evidence.Line)
		}
		content = strings.Replace(content, needle, needle+" evidence="+evidence.ID, 1)
	}
	return content
}

func verifyClaims(repo string, claims []review.CandidateClaim, evidence []*Evidence) ([]Verdict, bool) {
	byID := make(map[string]*Evidence, len(evidence))
	duplicateEvidenceIDs := make(map[string]bool)
	for _, item := range evidence {
		if item == nil || item.ID == "" {
			continue
		}
		if _, exists := byID[item.ID]; exists {
			duplicateEvidenceIDs[item.ID] = true
			continue
		}
		byID[item.ID] = item
	}

	verdicts := make([]Verdict, 0, len(claims))
	needsRevision := false
	for i := range claims {
		claim := &claims[i]
		claim.ID = fmt.Sprintf("c%d", i+1)
		var invalid []string
		var missing []string

		if strings.TrimSpace(claim.Msg) == "" {
			invalid = append(invalid, "问题描述为空")
		}
		if claim.Line <= 0 {
			invalid = append(invalid, "缺少有效行号")
		}
		if claim.Severity != "error" && claim.Severity != "warning" && claim.Severity != "info" {
			invalid = append(invalid, "severity 必须是 error、warning 或 info")
		}
		if normalized, err := repoRelativePath(repo, claim.File); err != nil || claim.File == "" {
			invalid = append(invalid, "文件路径无效或超出仓库")
		} else {
			claim.File = normalized
		}

		if len(claim.EvidenceIDs) == 0 {
			missing = append(missing, "至少引用一条 Evidence")
		}
		seen := make(map[string]bool, len(claim.EvidenceIDs))
		hasAnchor := false
		for _, id := range claim.EvidenceIDs {
			if seen[id] {
				invalid = append(invalid, fmt.Sprintf("重复引用 Evidence %s", id))
				continue
			}
			seen[id] = true
			if duplicateEvidenceIDs[id] {
				invalid = append(invalid, fmt.Sprintf("Evidence %s 的 provenance 不唯一", id))
				continue
			}
			item, exists := byID[id]
			if !exists {
				missing = append(missing, fmt.Sprintf("Evidence %s 不存在", id))
				continue
			}
			if evidenceAnchorsClaim(item, *claim) {
				hasAnchor = true
			}
		}
		if len(claim.EvidenceIDs) > 0 && !hasAnchor {
			missing = append(missing, "缺少覆盖 Claim 位置的 Evidence")
		}

		verdict := Verdict{ClaimID: claim.ID, Status: VerdictAccepted, Reason: "Claim 格式、provenance 和代码锚点完整"}
		switch {
		case len(invalid) > 0:
			verdict.Status = VerdictRejected
			verdict.Reason = strings.Join(uniqueStrings(invalid), "；")
			needsRevision = true
		case len(missing) > 0:
			verdict.Status = VerdictUnresolved
			verdict.Reason = "证据引用不完整"
			verdict.MissingEvidence = uniqueStrings(missing)
			needsRevision = true
		}
		verdicts = append(verdicts, verdict)
	}
	return verdicts, needsRevision
}

func evidenceAnchorsClaim(evidence *Evidence, claim review.CandidateClaim) bool {
	if evidence == nil || !sameFile(evidence.File, claim.File) {
		return false
	}
	return claim.Line >= evidence.Line && claim.Line <= maxEvidenceLine(evidence)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func sameFile(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }
func normalizeEvidencePath(repo string, e *Evidence) error {
	if e == nil || e.File == "" || e.Line <= 0 {
		return fmt.Errorf("证据缺少有效位置")
	}
	p, err := repoRelativePath(repo, e.File)
	if err != nil {
		return err
	}
	e.File = p
	return nil
}
func repoRelativePath(repo, file string) (string, error) {
	p := filepath.Clean(file)
	if filepath.IsAbs(p) {
		rel, err := filepath.Rel(repo, p)
		if err != nil {
			return "", err
		}
		p = rel
	}
	if p == "." || p == ".." || strings.HasPrefix(p, ".."+string(os.PathSeparator)) || filepath.IsAbs(p) {
		return "", fmt.Errorf("路径超出仓库: %s", file)
	}
	return filepath.ToSlash(p), nil
}
func readRepoFile(repo, file string) ([]byte, error) {
	path, err := secureRepoFile(repo, file)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func secureRepoFile(repo, file string) (string, error) {
	rel, err := repoRelativePath(repo, file)
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return "", err
	}
	realFile, err := filepath.EvalSymlinks(filepath.Join(repo, rel))
	if err != nil {
		return "", err
	}
	contained, err := filepath.Rel(realRoot, realFile)
	if err != nil || contained == ".." || strings.HasPrefix(contained, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("文件符号链接超出仓库: %s", file)
	}
	return realFile, nil
}
func traceID() string {
	b := make([]byte, 6)
	if _, e := rand.Read(b); e != nil {
		return fmt.Sprintf("review-%d", time.Now().UnixNano())
	}
	return "review-" + hex.EncodeToString(b)
}
