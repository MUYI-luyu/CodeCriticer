package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	model := "gpt-5.4"
	if provider, ok := llm.(interface{ InvestigatorModel() string }); ok {
		if configured := strings.TrimSpace(provider.InvestigatorModel()); configured != "" {
			model = configured
		}
	}
	return &Workflow{llm: llm, repo: repo, maxSteps: 8, model: model, logger: slog.Default()}, nil
}
func (w *Workflow) SetLogger(logger *slog.Logger) {
	if logger != nil {
		w.logger = logger
	}
}

// 设置调查工具调用上限。
func (w *Workflow) SetMaxSteps(n int) {
	if n > 0 {
		w.maxSteps = n
	}
}

// 设置调查阶段模型。
func (w *Workflow) SetInvestigatorModel(model string) {
	if strings.TrimSpace(model) != "" {
		w.model = model
	}
}

type Result struct{ Trace *Trace }

func (w *Workflow) Run(ctx context.Context, req Request) (*Result, error) {
	started := time.Now()
	id := traceID()
	if req.Repo != "" && filepath.Clean(req.Repo) != filepath.Clean(w.repo) {
		tr := &Trace{ID: id, Request: req}
		return w.fail(tr, started, StopStageError, fmt.Errorf("request repository differs from workflow repository"))
	}
	req.Repo = w.repo
	tr := &Trace{ID: id, Request: req}
	obs := &observer{trace: tr}
	ctx = review.WithLLMObserver(ctx, obs)
	obs.setStage("normalize")
	changes, err := diff.Parse(req.Diff)
	if err != nil {
		return w.fail(tr, started, StopStageError, fmt.Errorf("parse diff: %w", err))
	}
	tr.Plan = Plan{Concern: "审查变更中的真实缺陷，重点关注并发、错误处理、边界和资源生命周期"}
	for i := range changes {
		c := &changes[i]
		if c.File != "" && c.File != "/dev/null" {
			file, er := repoRelativePath(w.repo, c.File)
			if er != nil {
				return w.fail(tr, started, StopStageError, fmt.Errorf("diff path: %w", er))
			}
			c.File = file
			tr.Plan.TargetFiles = append(tr.Plan.TargetFiles, file)
		}
		src, er := readRepoFile(w.repo, c.File)
		if er == nil {
			c.Annotate(src)
			for _, s := range c.Symbols {
				tr.Plan.Symbols = append(tr.Plan.Symbols, s.Name)
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
				tr.Evidence = append(tr.Evidence, &Evidence{ID: fmt.Sprintf("e%d", len(tr.Evidence)+1), Source: "diff", Type: "changed_line", Relation: "supports", File: c.File, Line: add.No, Content: lines[add.No-1], Symbol: symbol})
			}
		}
	}
	tr.RiskSeeds = buildRiskSeeds(changes)
	tr.Hypotheses = buildHypotheses(tr.RiskSeeds)
	obs.setStage("plan")
	points, _, planErr := w.llm.Plan(ctx, string(req.Diff))
	if planErr == nil {
		// 计划只保留少量高相关问题，避免调查阶段被泛化风险耗尽。
		if len(points) > 3 {
			points = points[:3]
		}
		seenQuestions := make(map[string]bool, len(points))
		for _, p := range points {
			question := strings.TrimSpace(p.Desc)
			if question == "" || seenQuestions[question] {
				continue
			}
			seenQuestions[question] = true
			tr.Plan.Questions = append(tr.Plan.Questions, question)
			tr.Plan.Keywords = append(tr.Plan.Keywords, p.Kw...)
		}
	} else {
		tr.Errors = append(tr.Errors, fmt.Sprintf("plan: %v", planErr))
		// 降级到确定性的文件和符号计划。
	}
	idx, graphErr := graph.Build(w.repo)
	if graphErr != nil {
		tr.Errors = append(tr.Errors, fmt.Sprintf("graph: %v", graphErr))
	}
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
	if err := normalizeFindingPaths(w.repo, findings); err != nil {
		return w.fail(tr, started, StopStageError, fmt.Errorf("review finding: %w", err))
	}
	tr.Findings = findings
	tr.Validations, tr.Evaluation, tr.EvidenceGaps = evaluateFindings(findings, tr.Evidence)
	// 仅允许一次补充调查，避免退化为多轮反思循环。
	if tr.Evaluation == EvaluateInsufficient && len(tr.ToolCalls) < w.maxSteps {
		tr.Plan.Questions = append(tr.Plan.Questions, tr.EvidenceGaps...)
		obs.setStage("investigate")
		if err := w.investigate(ctx, tr, ts); err != nil {
			reason := tr.StopReason
			if ctx.Err() != nil {
				reason = StopContextCanceled
			}
			return w.fail(tr, started, reason, err)
		}
		obs.setStage("review")
		findings, _, err = w.llm.Review(ctx, buildReviewPrompt(req.Diff, tr.Plan, tr.Evidence))
		if err != nil {
			reason := StopStageError
			if ctx.Err() != nil {
				reason = StopContextCanceled
			}
			return w.fail(tr, started, reason, fmt.Errorf("review: %w", err))
		}
		if err := normalizeFindingPaths(w.repo, findings); err != nil {
			return w.fail(tr, started, StopStageError, fmt.Errorf("review finding: %w", err))
		}
		tr.Findings = findings
		tr.Validations, tr.Evaluation, tr.EvidenceGaps = evaluateFindings(findings, tr.Evidence)
	}
	if tr.StopReason == "" {
		tr.StopReason = StopAgentDone
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
	history := make(map[string]bool, len(tr.ToolCalls))
	for _, c := range tr.ToolCalls {
		history[fmt.Sprintf("%s:%v", c.Tool, c.Args)] = true
	}
	// maxSteps 是有效工具预算；失败和重复决策最多额外占用同等尝试次数。
	step := 0
	validTools := 0
	staleResults := 0
	for step < w.maxSteps*2 && validTools < w.maxSteps {
		step++
		tr.Stats.DecisionCount++
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
		a, decisionErr := parseInvestigatorDecision(text)
		if decisionErr != nil {
			tr.Errors = append(tr.Errors, fmt.Sprintf("调查决策格式错误，已请求纠正: %v", decisionErr))
			repairPrompt := buildDecisionRepairPrompt(tr, text, decisionErr)
			text, _, err = w.llm.ChatWithUsage(ctx, "你是代码审查调查员，只纠正工具决策 JSON。", repairPrompt, w.model)
			if err != nil {
				if ctx.Err() != nil {
					tr.StopReason = StopContextCanceled
				} else {
					tr.StopReason = StopStageError
				}
				return err
			}
			a, decisionErr = parseInvestigatorDecision(text)
			if decisionErr != nil {
				tr.StopReason = StopInvalidDecision
				return fmt.Errorf("decision JSON: %w", decisionErr)
			}
		}
		if a.Done {
			if !questionsCovered(tr) {
				tr.EvidenceGaps = append(tr.EvidenceGaps, "计划问题尚未获得非 Diff 调查证据，请继续调用工具")
				continue
			}
			tr.StopReason = StopAgentDone
			return nil
		}
		key := fmt.Sprintf("%s:%v", a.Tool, a.Args)
		if history[key] {
			tr.Stats.DuplicateCalls++
			msg := fmt.Sprintf("工具 %s 使用相同参数重复调用，未产生新证据", a.Tool)
			tr.EvidenceGaps = append(tr.EvidenceGaps, msg)
			tr.Errors = append(tr.Errors, fmt.Sprintf("重复工具决策: %s", key))
			tr.ToolCalls = append(tr.ToolCalls, ToolCall{Step: step, Tool: a.Tool, Args: a.Args, Error: msg})
			staleResults++
			if staleResults >= 2 && questionsCovered(tr) {
				tr.StopReason = StopEvidenceEnough
				return nil
			}
			continue
		}
		history[key] = true
		tc, ev := toolCall(a.Tool, a.Args, func() ([]*Evidence, error) { return ts.Execute(ctx, a.Tool, a.Args) })
		tc.Step = step
		for i, e := range ev {
			if err := normalizeEvidencePath(w.repo, e); err != nil {
				tc.Error = err.Error()
				ev = nil
				tc.EvidenceIDs = nil
				break
			}
			e.ID = fmt.Sprintf("e%d", len(tr.Evidence)+i+1)
			e.QuestionIndexes = append([]int(nil), a.Questions...)
			tc.EvidenceIDs = append(tc.EvidenceIDs, e.ID)
		}
		tr.ToolCalls = append(tr.ToolCalls, tc)
		newEvidence := false
		for _, item := range ev {
			merged := false
			generatedID := item.ID
			for i, prior := range tr.Evidence {
				if prior != nil && prior.Source == item.Source && prior.File == item.File && prior.Line == item.Line && strings.TrimSpace(prior.Content) == strings.TrimSpace(item.Content) {
					item.ID = prior.ID
					for j := range tr.ToolCalls[len(tr.ToolCalls)-1].EvidenceIDs {
						if tr.ToolCalls[len(tr.ToolCalls)-1].EvidenceIDs[j] == generatedID {
							tr.ToolCalls[len(tr.ToolCalls)-1].EvidenceIDs[j] = item.ID
						}
					}
					merged = true
					break
				}
				if item.Source == "read_code" && prior != nil && prior.Source == "read_code" && prior.File == item.File && item.Line >= prior.Line && maxEvidenceLine(item) <= maxEvidenceLine(prior) {
					item.ID = prior.ID
					for j := range tr.ToolCalls[len(tr.ToolCalls)-1].EvidenceIDs {
						if tr.ToolCalls[len(tr.ToolCalls)-1].EvidenceIDs[j] == generatedID {
							tr.ToolCalls[len(tr.ToolCalls)-1].EvidenceIDs[j] = item.ID
						}
					}
					merged = true
					break
				}
				if item.Source == "read_code" && prior != nil && prior.Source == "read_code" && prior.File == item.File && item.Line <= prior.Line && maxEvidenceLine(item) >= maxEvidenceLine(prior) {
					item.ID = prior.ID
					tr.Evidence[i] = item
					for j := range tr.ToolCalls[len(tr.ToolCalls)-1].EvidenceIDs {
						if tr.ToolCalls[len(tr.ToolCalls)-1].EvidenceIDs[j] == generatedID {
							tr.ToolCalls[len(tr.ToolCalls)-1].EvidenceIDs[j] = item.ID
						}
					}
					newEvidence = true
					merged = true
					break
				}
			}
			if !merged {
				tr.Evidence = append(tr.Evidence, item)
				newEvidence = true
			}
		}
		if tc.Error == "" {
			if len(ev) == 0 || !newEvidence {
				tc.Error = "工具调用未产生新证据"
				tr.ToolCalls[len(tr.ToolCalls)-1].Error = tc.Error
				tr.EvidenceGaps = append(tr.EvidenceGaps, fmt.Sprintf("工具 %s 返回的证据与已有内容重叠", a.Tool))
				tr.Stats.NoNewEvidenceCalls++
				staleResults++
			} else {
				validTools++
				tr.Stats.SuccessfulToolCalls++
				staleResults = 0
			}
		} else {
			tr.Stats.FailedToolCalls++
		}
		w.logger.Info("workflow tool", "trace_id", tr.ID, "step", step, "tool", a.Tool, "evidence", len(ev), "error", tc.Error)
		if tc.Error != "" {
			if tc.Error == "工具调用未产生新证据" {
				if staleResults >= 2 && questionsCovered(tr) {
					tr.StopReason = StopEvidenceEnough
					return nil
				}
				continue
			}
			tr.EvidenceGaps = append(tr.EvidenceGaps, fmt.Sprintf("工具 %s 失败: %s", a.Tool, tc.Error))
			if a.Tool == "dataflow" {
				tr.EvidenceGaps = append(tr.EvidenceGaps, "DataFlow 失败时改用 read_code 查看函数范围，或用 search_code 搜索字段/局部变量；不要把字段名和局部变量名当作函数符号")
			}
			continue
		}
	}
	tr.StopReason = StopMaxSteps
	return nil
}

type investigatorDecision struct {
	Done      bool                   `json:"done"`
	Tool      string                 `json:"tool"`
	Args      map[string]interface{} `json:"args"`
	Questions []int                  `json:"question_indexes"`
}

func parseInvestigatorDecision(text string) (investigatorDecision, error) {
	var decision investigatorDecision
	decoder := json.NewDecoder(strings.NewReader(stripJSON(text)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decision); err != nil {
		return decision, err
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return decision, fmt.Errorf("multiple JSON values")
		}
		return decision, err
	}
	if !decision.Done && strings.TrimSpace(decision.Tool) == "" {
		return decision, fmt.Errorf("missing tool")
	}
	return decision, nil
}

func buildDecisionRepairPrompt(tr *Trace, response string, decisionErr error) string {
	const maxResponseRunes = 4000
	runes := []rune(response)
	if len(runes) > maxResponseRunes {
		runes = runes[:maxResponseRunes]
	}
	return fmt.Sprintf("上一次工具决策不符合协议。错误：%v\n目标文件：%s\n待调查问题：%s\n上一次响应：%s\n请只返回一个 JSON 对象，不要返回 findings、解释、Markdown 或多个对象。继续调查时必须从 read_code、search_code、find_callers、run_static_rules、dataflow 中选择一个工具，提供该工具必需的完整 args 和 question_indexes；确实完成时只返回 {\"done\":true}。", decisionErr, strings.Join(tr.Plan.TargetFiles, ", "), formatQuestions(tr.Plan.Questions), string(runes))
}

func maxEvidenceLine(e *Evidence) int {
	if e.EndLine > e.Line {
		return e.EndLine
	}
	return e.Line
}

func buildDecisionPrompt(tr *Trace) string {
	return fmt.Sprintf("%s\n目标文件：%s\n目标符号：%s\n风险摘要：%s\n调查假设：%s\n调查问题：%s\n变更附近代码：%s\n已获得的非 Diff 证据：%s\n已有发现：%s\n验收结果：%s\n证据缺口：%s\n工具历史：%s\n可用工具：read_code(file,start_line,end_line), search_code(keyword,file可选), find_callers(symbol,file), run_static_rules(), dataflow(symbol,file)。工具参数规则：dataflow 只接受函数或方法名；字段名、结构体字段和局部变量必须使用 search_code 或 read_code；调用关系使用 find_callers；DataFlow 失败后切换到 read_code/search_code，不要重复失败调用。必须优先调查目标文件和变更行，并围绕调查假设的 RequiredFacts 收集证据；返回 done 前，必须确认每个计划问题都有对应证据。工具决策可附 question_indexes 数组，只返回 {\"done\":false,\"tool\":\"...\",\"args\":{},\"question_indexes\":[0]}。", tr.Plan.Concern, strings.Join(tr.Plan.TargetFiles, ", "), strings.Join(tr.Plan.Symbols, ", "), buildInvestigatorContext(tr), encodeHypotheses(tr.Hypotheses), formatQuestions(tr.Plan.Questions), encodeChangedContext(tr), encodeInvestigatorEvidence(tr), encodeFindings(tr.Findings), encodeValidations(tr.Validations), strings.Join(tr.EvidenceGaps, "; "), summarizeCalls(tr.ToolCalls))
}

// buildInvestigatorContext 提取调查阶段首轮所需的紧凑风险摘要，完整证据仍保留在 Trace。
func buildInvestigatorContext(tr *Trace) string {
	type seedView struct {
		Category string `json:"category"`
		File     string `json:"file"`
		Line     int    `json:"line"`
		Symbol   string `json:"symbol,omitempty"`
		Trigger  string `json:"trigger,omitempty"`
	}
	seeds := make([]seedView, 0, len(tr.RiskSeeds))
	for _, seed := range tr.RiskSeeds {
		seeds = append(seeds, seedView{seed.Category, seed.File, seed.Line, seed.Symbol, seed.Trigger})
	}
	b, _ := json.Marshal(seeds)
	return string(b)
}

func encodeChangedContext(tr *Trace) string {
	type lineView struct {
		File string `json:"file"`
		Line int    `json:"line"`
		Text string `json:"text"`
	}
	lines := make([]lineView, 0, 24)
	seen := make(map[string]bool)
	for _, seed := range tr.RiskSeeds {
		for _, evidence := range tr.Evidence {
			if evidence == nil || evidence.Source != "diff" || evidence.File != seed.File || abs(evidence.Line-seed.Line) > 2 {
				continue
			}
			key := fmt.Sprintf("%s:%d", evidence.File, evidence.Line)
			if seen[key] {
				continue
			}
			seen[key] = true
			lines = append(lines, lineView{evidence.File, evidence.Line, evidence.Content})
		}
	}
	b, _ := json.Marshal(lines)
	return string(b)
}

func encodeInvestigatorEvidence(tr *Trace) string {
	filtered := make([]*Evidence, 0)
	for _, evidence := range tr.Evidence {
		if evidence != nil && evidence.Source != "diff" {
			filtered = append(filtered, evidence)
		}
	}
	return encodeEvidence(filtered)
}
func buildReviewPrompt(d []byte, p Plan, e []*Evidence) string {
	return fmt.Sprintf("审查 diff 并只输出 JSON {\"findings\":[{\"file\":\"...\",\"line\":0,\"severity\":\"error|warning|info\",\"msg\":\"...\",\"evidence\":\"...\",\"evidence_ids\":[\"e1\"]}]}。每条发现必须引用真正支持结论的证据编号。一个根因只输出一条发现；只报告当前代码可达、可复现的问题，不报告未来扩展风险或仅存在的编码风格问题。行号必须锚定引入根因的代码，不得只指向后续触发点或症状位置；例如锁初始化或配置错误应锚定初始化/配置行。无法定位或证据不足时不要输出。计划：%+v\n证据：%s\nDiff：\n%s", p, encodeEvidence(e), d)
}
func evaluateFindings(fs []review.Finding, e []*Evidence) ([]Validation, EvaluateStatus, []string) {
	out := make([]Validation, 0, len(fs))
	status := EvaluateSufficient
	var gaps []string
	byID := make(map[string]*Evidence, len(e))
	for _, x := range e {
		if x == nil || x.ID == "" {
			status = EvaluateConflict
			gaps = append(gaps, "证据缺少编号")
			continue
		}
		if _, exists := byID[x.ID]; exists {
			status = EvaluateConflict
			gaps = append(gaps, fmt.Sprintf("证据编号重复: %s", x.ID))
			continue
		}
		byID[x.ID] = x
	}
	accepted := make([]review.Finding, 0, len(fs))
	for i, f := range fs {
		ok := false
		conflict := false
		duplicate := false
		speculative := containsSpeculativeLanguage(f.Msg)
		for _, prior := range accepted {
			if sameFile(prior.File, f.File) && sameFindingTopic(prior.Msg, f.Msg) && sharedEvidence(prior, f) >= 2 {
				duplicate = true
				break
			}
		}
		if len(f.EvidenceIDs) == 0 {
			gaps = append(gaps, fmt.Sprintf("发现 %s:%d 未引用证据", f.File, f.Line))
		} else {
			seen := map[string]bool{}
			matched := false
			missing := false
			for _, id := range f.EvidenceIDs {
				if seen[id] {
					conflict = true
					continue
				}
				seen[id] = true
				x, found := byID[id]
				if !found {
					missing = true
					gaps = append(gaps, fmt.Sprintf("发现 %s:%d 引用不存在的证据 %s", f.File, f.Line, id))
					continue
				}
				if evidenceMatchesFinding(x, f) {
					matched = true
				}
			}
			factsSupported := requiredFactsSupportFinding(f, byID)
			anchorSupported := anchorSupportsClaim(f, byID)
			ok = matched && !missing && !conflict && !duplicate && !speculative && evidenceTextSupports(f, byID) && factsSupported && anchorSupported
			if !matched && !missing && !conflict {
				gaps = append(gaps, fmt.Sprintf("发现 %s:%d 缺少当前位置附近的支持证据", f.File, f.Line))
			}
			if matched && !evidenceTextSupports(f, byID) {
				gaps = append(gaps, fmt.Sprintf("发现 %s:%d 的证据描述未与引用事实形成实质对应", f.File, f.Line))
			}
			if !factsSupported {
				gaps = append(gaps, fmt.Sprintf("发现 %s:%d 缺少问题类型所需的参与路径或时序事实", f.File, f.Line))
			}
			if !anchorSupported {
				gaps = append(gaps, fmt.Sprintf("发现 %s:%d 的锚点未落在其声称的根因位置", f.File, f.Line))
			}
		}
		if conflict {
			status = EvaluateConflict
		}
		if duplicate {
			gaps = append(gaps, fmt.Sprintf("发现 %s:%d 与已有发现重复", f.File, f.Line))
		}
		if speculative {
			gaps = append(gaps, fmt.Sprintf("发现 %s:%d 包含未验证的推测风险", f.File, f.Line))
		}
		reason := "证据已关联"
		if conflict {
			reason = "证据与发现冲突"
		} else if duplicate {
			reason = "与已有发现重复"
		} else if speculative {
			reason = "结论包含未验证的推测风险"
		} else if !ok {
			reason = "证据不足"
		}
		out = append(out, Validation{FindingIndex: i, Accepted: ok, Confidence: map[bool]float64{true: 0.8, false: 0.0}[ok], Reason: reason})
		if ok {
			accepted = append(accepted, f)
		}
	}
	if len(gaps) > 0 && status != EvaluateConflict {
		if len(accepted) > 0 {
			status = EvaluatePartial
		} else {
			status = EvaluateInsufficient
		}
	}
	return out, status, gaps
}

func absLine(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

func containsSpeculativeLanguage(msg string) bool {
	for _, word := range []string{"未来扩展", "潜在", "可能导致", "理论上", "如果未来"} {
		if strings.Contains(msg, word) {
			return true
		}
	}
	return false
}

func sameFindingTopic(a, b string) bool {
	keywords := []string{"数据竞争", "竞态", "TOCTOU", "死锁", "泄漏", "重复关闭", "错误处理"}
	for _, keyword := range keywords {
		if strings.Contains(a, keyword) && strings.Contains(b, keyword) {
			return true
		}
	}
	return false
}

func sharedEvidence(a, b review.Finding) int {
	seen := make(map[string]bool, len(a.EvidenceIDs))
	for _, id := range a.EvidenceIDs {
		seen[id] = true
	}
	n := 0
	for _, id := range b.EvidenceIDs {
		if seen[id] {
			n++
		}
	}
	return n
}

func requiredFactsSupportFinding(f review.Finding, byID map[string]*Evidence) bool {
	msg := strings.ToLower(f.Msg)
	if !strings.Contains(msg, "死锁") && !strings.Contains(msg, "数据竞争") && !strings.Contains(msg, "竞态") && !strings.Contains(msg, "goroutine") {
		return true
	}
	symbols := make(map[string]bool)
	content := strings.Builder{}
	for _, id := range f.EvidenceIDs {
		e := byID[id]
		if e == nil {
			continue
		}
		if e.Symbol != "" {
			symbols[e.Symbol] = true
		}
		content.WriteString(strings.ToLower(e.Content))
		content.WriteByte('\n')
	}
	if strings.Contains(msg, "死锁") || strings.Contains(msg, "数据竞争") || strings.Contains(msg, "竞态") {
		return len(symbols) >= 2
	}
	text := content.String()
	started := strings.Contains(text, "go ") || strings.Contains(text, "runworker")
	blocked := strings.Contains(text, "<-") || strings.Contains(text, ".wait(") || strings.Contains(text, " <- ")
	return started && blocked
}

func anchorSupportsClaim(f review.Finding, byID map[string]*Evidence) bool {
	msg := strings.ToLower(f.Msg)
	requiredToken := ""
	if strings.Contains(msg, "rlocker") {
		requiredToken = "rlocker"
	}
	if requiredToken == "" {
		return true
	}
	for _, id := range f.EvidenceIDs {
		e := byID[id]
		if e != nil && maxEvidenceLine(e)-e.Line <= 10 && sameFile(e.File, f.File) && f.Line >= e.Line-3 && f.Line <= maxEvidenceLine(e)+3 && strings.Contains(strings.ToLower(e.Content), requiredToken) {
			return true
		}
	}
	return false
}

func evidenceTextSupports(f review.Finding, byID map[string]*Evidence) bool {
	if strings.TrimSpace(f.Evidence) == "" {
		for _, id := range f.EvidenceIDs {
			e := byID[id]
			if e != nil && sameFile(e.File, f.File) && absLine(e.Line, f.Line) <= 3 {
				return true
			}
			if e != nil && e.Relation == "supports" && (e.Type == "call_chain" || e.Type == "call" || e.Type == "return" || e.Type == "dataflow") {
				return true
			}
		}
		return false
	}
	text := strings.ToLower(strings.TrimSpace(f.Evidence))
	for _, id := range f.EvidenceIDs {
		e := byID[id]
		if e == nil {
			continue
		}
		content := strings.ToLower(strings.TrimSpace(e.Content))
		// 兼容旧 Trace 中没有内容的直接位置证据；新生成证据都会带内容。
		if content == "" && sameFile(e.File, f.File) && absLine(e.Line, f.Line) <= 3 {
			return true
		}
		if content != "" && (strings.Contains(text, content) || strings.Contains(content, text)) {
			return true
		}
		if content != "" && sameFile(e.File, f.File) && f.Line >= e.Line && f.Line <= maxEvidenceLine(e) {
			return true
		}
		for _, token := range strings.FieldsFunc(content, func(r rune) bool {
			return r == ' ' || r == '\n' || r == '\t' || r == ':' || r == '(' || r == ')' || r == ';'
		}) {
			if len([]rune(token)) >= 4 && strings.Contains(text, token) {
				return true
			}
		}
	}
	return false
}
func hasRejected(vs []Validation) bool {
	for _, v := range vs {
		if !v.Accepted {
			return true
		}
	}
	return false
}
func questionsCovered(tr *Trace) bool {
	if len(tr.Plan.Questions) == 0 {
		return hasSubstantiveEvidence(tr.Evidence)
	}
	covered := make(map[int]map[string]bool, len(tr.Plan.Questions))
	hasIndexes := false
	for _, e := range tr.Evidence {
		if !isSubstantiveEvidence(e) {
			continue
		}
		for _, q := range e.QuestionIndexes {
			hasIndexes = true
			if q >= 0 && q < len(tr.Plan.Questions) {
				if covered[q] == nil {
					covered[q] = make(map[string]bool)
				}
				covered[q][e.ID] = true
			}
		}
	}
	if !hasIndexes {
		count := 0
		for _, e := range tr.Evidence {
			if isSubstantiveEvidence(e) {
				count++
			}
		}
		return count >= len(tr.Plan.Questions)
	}
	required := 2
	if len(tr.Plan.Questions) == 1 {
		required = 1
	}
	for i := range tr.Plan.Questions {
		if len(covered[i]) < required {
			return false
		}
	}
	return true
}

func hasSubstantiveEvidence(evs []*Evidence) bool {
	for _, e := range evs {
		if isSubstantiveEvidence(e) {
			return true
		}
	}
	return false
}

func isSubstantiveEvidence(e *Evidence) bool {
	if e == nil || e.ID == "" || e.File == "" || e.Line <= 0 {
		return false
	}
	if e.Source == "diff" {
		return false
	}
	content := strings.TrimSpace(e.Content)
	if len([]rune(content)) < 4 {
		return false
	}
	return e.Type != "search_result" || len([]rune(content)) >= 12
}
func sameFile(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }
func evidenceMatchesFinding(e *Evidence, f review.Finding) bool {
	end := e.EndLine
	if end < e.Line {
		end = e.Line
	}
	if sameFile(e.File, f.File) && f.Line >= e.Line-3 && f.Line <= end+3 {
		return true
	}
	// 跨位置证据只能解释根因，不能替代 Finding 自身的代码锚点。
	return false
}
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
func formatQuestions(qs []string) string {
	var b strings.Builder
	for i, q := range qs {
		fmt.Fprintf(&b, "[%d] %s; ", i, q)
	}
	return b.String()
}
func normalizeFindingPaths(repo string, fs []review.Finding) error {
	for i := range fs {
		if fs[i].File == "" || fs[i].Line <= 0 {
			return fmt.Errorf("发现缺少有效位置")
		}
		p, err := repoRelativePath(repo, fs[i].File)
		if err != nil {
			return err
		}
		fs[i].File = p
	}
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
func encodeFindings(fs []review.Finding) string { b, _ := json.Marshal(fs); return string(b) }
func encodeValidations(vs []Validation) string  { b, _ := json.Marshal(vs); return string(b) }
func encodeRiskSeeds(rs []RiskSeed) string      { b, _ := json.Marshal(rs); return string(b) }
func encodeHypotheses(hs []Hypothesis) string   { b, _ := json.Marshal(hs); return string(b) }
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
