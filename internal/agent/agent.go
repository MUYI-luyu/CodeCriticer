package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MUYI-luyu/codecritic/internal/diff"
	"github.com/MUYI-luyu/codecritic/internal/graph"
	"github.com/MUYI-luyu/codecritic/internal/recall"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

// Agent 是基于 Reflexion 的代码审查 Agent。
// 核心循环：Execute → Validate → Reflect → Retry（带 Memory）。
type Agent struct {
	planAgent        *review.PlanAgent // Plan-and-Execute 模式（固定 pipeline）
	validator        *Validator
	reflector        *Reflector
	memory           []memoryEntry // 历史批评（短期记忆，按 symbol 去重）
	maxAttempts      int
	useOrchestration bool // 是否启用动态工具编排
	repo             string
	llm              *review.LLM // 保存 llm 用于创建 Orchestrator
	store            *recall.Store
	staticFindings   []review.Finding // 静态规则命中（确定性，跳过 LLM 复核）
}

// memoryEntry 是一条历史批评记录。
type memoryEntry struct {
	Symbol     string // 符号名（用于去重）
	File       string
	Line       int
	Msg        string
	Reason     string
	Suggestion string
}

// New 创建 Reflexion Agent。
func New(llm *review.LLM, repo string, opts ...Option) (*Agent, error) {
	// 构建调用图和召回存储
	idx, err := graph.Build(repo)
	if err != nil {
		// 调用图构建失败时降级（不影响主流程）
		idx = nil
	}

	store := recall.New(repo, idx)

	// 静态规则：确定性 bug 发现，失败时降级为空（不影响主流程）
	var staticFindings []review.Finding
	if rf, err := review.Rules(repo); err == nil {
		staticFindings = rf
	}

	agent := &Agent{
		planAgent:        review.NewPlanAgent(llm, store),
		validator:        NewValidator(llm, store),
		reflector:        NewReflector(llm),
		maxAttempts:      MaxAttempts,
		useOrchestration: false, // 默认关闭，通过 Option 启用
		repo:             repo,
		llm:              llm,
		store:            store,
		staticFindings:   staticFindings,
	}

	// 应用配置选项
	for _, opt := range opts {
		opt(agent)
	}

	return agent, nil
}

// Option 是配置选项。
type Option func(*Agent)

// WithMaxAttempts 设置最大尝试轮数。
func WithMaxAttempts(n int) Option {
	return func(a *Agent) {
		a.maxAttempts = n
	}
}

// WithOrchestration 启用动态工具编排。
func WithOrchestration(enable bool) Option {
	return func(a *Agent) {
		a.useOrchestration = enable
	}
}

// Review 执行 Reflexion Loop 审查。
// 返回完整的审查结果（包含所有轮次的轨迹）。
func (a *Agent) Review(ctx context.Context, diff []byte, syms []review.Sym) (*Result, error) {
	startTime := time.Now()
	static := a.filterStaticToDiff(diff)
	result := &Result{
		Attempts:       make([]Attempt, 0, a.maxAttempts),
		StaticFindings: static,
	}

	var prevFindings []review.Finding

	for round := 1; round <= a.maxAttempts; round++ {
		attempt, err := a.executeRound(ctx, round, diff, syms, prevFindings)
		if err != nil {
			// 单轮失败时记录错误但继续
			attempt.Error = err.Error()
		}

		result.Attempts = append(result.Attempts, attempt)

		// 收敛检查
		if converged, reason := a.checkConvergence(result.Attempts, prevFindings); converged {
			result.Converged = true
			result.Reason = reason
			result.FinalFindings = mergeAndDedup(static, filterValidFindings(attempt.Findings, attempt.Validations))
			result.TotalDuration = time.Since(startTime)
			return result, nil
		}

		prevFindings = attempt.Findings
	}

	// 达到 max_attempts 仍未收敛
	lastAttempt := result.Attempts[len(result.Attempts)-1]
	result.Converged = false
	result.Reason = "max_attempts"
	result.FinalFindings = mergeAndDedup(static, filterValidFindings(lastAttempt.Findings, lastAttempt.Validations))
	result.TotalDuration = time.Since(startTime)

	return result, nil
}

// executeRound 执行单轮审查。
func (a *Agent) executeRound(ctx context.Context, round int, diff []byte, syms []review.Sym, prevFindings []review.Finding) (Attempt, error) {
	startTime := time.Now()
	attempt := Attempt{
		Round:     round,
		StartedAt: startTime,
	}

	// 1. Execute: 根据模式选择执行方式
	var findings []review.Finding
	var toolCalls []ToolCall
	var err error

	if a.useOrchestration {
		// 动态工具编排模式
		orchestrator := NewOrchestrator(a.llm, a.store, a.repo, string(diff))
		orchResult, orchErr := orchestrator.Execute(ctx)
		if orchErr != nil {
			attempt.Duration = time.Since(startTime)
			return attempt, fmt.Errorf("orchestration: %w", orchErr)
		}
		findings = orchResult.Findings
		toolCalls = orchResult.ToolCalls
	} else {
		// Plan-and-Execute 固定 pipeline 模式
		memoryText := a.formatMemory()
		var pool *recall.EvidencePool
		findings, pool, err = a.planAgent.ReviewWithMemory(ctx, string(diff), syms, memoryText)
		if err != nil {
			attempt.Duration = time.Since(startTime)
			return attempt, fmt.Errorf("execute: %w", err)
		}
		attempt.EvidencePool = pool
	}
	attempt.Findings = findings
	attempt.ToolCalls = toolCalls

	// 2. Validate: 证据校验（静态规则已命中的 finding 直接置信度 1.0，跳过 LLM 复核）
	validations, err := a.validate(ctx, findings, attempt.EvidencePool)
	if err != nil {
		attempt.Duration = time.Since(startTime)
		return attempt, fmt.Errorf("validate: %w", err)
	}
	attempt.Validations = validations

	// 3. Reflect: 生成批评（只对低可信度的）
	critiques, err := a.reflector.Reflect(ctx, findings, validations)
	if err != nil {
		// reflect 失败不影响主流程
		critiques = []Critique{}
	}
	attempt.Critiques = critiques

	// 4. 更新 memory
	a.updateMemory(findings, critiques)

	attempt.Duration = time.Since(startTime)
	return attempt, nil
}

// checkConvergence 检查是否收敛。
func (a *Agent) checkConvergence(attempts []Attempt, prevFindings []review.Finding) (bool, string) {
	if len(attempts) == 0 {
		return false, ""
	}

	latest := attempts[len(attempts)-1]

	// 条件1: 平均置信度 >= 0.75 且低置信度 findings <= 2
	if avgConfidence(latest.Validations) >= 0.75 && fewLowConfidence(latest.Validations, 2) {
		return true, "high_avg_with_bounded_low"
	}

	// 条件2: findings 集合稳定（与上一轮相比，Jaccard >= 0.8）
	if len(prevFindings) > 0 && findingsStable(prevFindings, latest.Findings, 0.8) {
		return true, "findings_stable"
	}

	return false, ""
}

// formatMemory 将批评转为文本形式（注入到下一轮 prompt）。
func (a *Agent) formatMemory() string {
	if len(a.memory) == 0 {
		return ""
	}
	var lines []string
	for i, m := range a.memory {
		entry := fmt.Sprintf(
			"[历史批评 #%d]\n位置: %s:%d\n问题: %s\n原因: %s\n建议: %s",
			i+1, m.File, m.Line, m.Msg, m.Reason, m.Suggestion,
		)
		lines = append(lines, entry)
	}
	return strings.Join(lines, "\n\n")
}

// updateMemory 更新短期记忆（按 symbol 去重）。
func (a *Agent) updateMemory(findings []review.Finding, critiques []Critique) {
	for _, c := range critiques {
		if c.FindingID >= len(findings) {
			continue
		}
		f := findings[c.FindingID]

		// 构造新条目
		entry := memoryEntry{
			Symbol:     f.Symbol,
			File:       f.File,
			Line:       f.Line,
			Msg:        f.Msg,
			Reason:     c.Reason,
			Suggestion: c.Suggestion,
		}

		// 按 symbol 去重：相同 symbol 的覆盖旧的
		updated := false
		for i, m := range a.memory {
			if m.Symbol != "" && m.Symbol == entry.Symbol {
				a.memory[i] = entry // 覆盖
				updated = true
				break
			}
		}

		// 新 symbol
		if !updated {
			a.memory = append(a.memory, entry)
		}
	}

	// 限制 memory 大小（避免 context 过长）
	const maxMemoryEntries = 10
	if len(a.memory) > maxMemoryEntries {
		a.memory = a.memory[len(a.memory)-maxMemoryEntries:]
	}
}

// avgConfidence 计算所有 validations 的平均置信度。
func avgConfidence(validations []Validation) float64 {
	if len(validations) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range validations {
		sum += v.Confidence
	}
	return sum / float64(len(validations))
}

// fewLowConfidence 检查低置信度 findings 的数量是否在限制内。
func fewLowConfidence(validations []Validation, maxLow int) bool {
	lowCount := 0
	for _, v := range validations {
		if v.Confidence < ConfidenceThreshold {
			lowCount++
		}
	}
	return lowCount <= maxLow
}

// allHighConfidence 检查所有 validations 的 confidence 是否都 >= 阈值。
// 已废弃：改用 avgConfidence + fewLowConfidence 组合策略。
func allHighConfidence(validations []Validation) bool {
	if len(validations) == 0 {
		return false
	}

	for _, v := range validations {
		if v.Confidence < ConfidenceThreshold {
			return false
		}
	}
	return true
}

// findingsStable 检查两轮 findings 是否稳定（Jaccard 相似度）。
func findingsStable(prev, curr []review.Finding, threshold float64) bool {
	if len(prev) == 0 && len(curr) == 0 {
		return true
	}

	// 构建 finding 的唯一键（file:line:msg）
	prevSet := make(map[string]bool)
	for _, f := range prev {
		key := fmt.Sprintf("%s:%d:%s", f.File, f.Line, f.Msg)
		prevSet[key] = true
	}

	currSet := make(map[string]bool)
	intersection := 0
	for _, f := range curr {
		key := fmt.Sprintf("%s:%d:%s", f.File, f.Line, f.Msg)
		currSet[key] = true
		if prevSet[key] {
			intersection++
		}
	}

	// Jaccard = |A ∩ B| / |A ∪ B|
	union := len(prevSet) + len(currSet) - intersection
	if union == 0 {
		return true
	}

	similarity := float64(intersection) / float64(union)
	return similarity >= threshold
}

// filterValidFindings 过滤出高可信度的 findings。
func filterValidFindings(findings []review.Finding, validations []Validation) []review.Finding {
	var result []review.Finding
	for _, v := range validations {
		if v.Confidence >= ConfidenceThreshold && v.FindingID < len(findings) {
			result = append(result, findings[v.FindingID])
		}
	}
	return result
}

// filterStaticToDiff 过滤静态规则 findings 到 diff 涉及的文件，避免报告无关的历史遗留问题。
func (a *Agent) filterStaticToDiff(diffData []byte) []review.Finding {
	if len(a.staticFindings) == 0 {
		return nil
	}
	changes, err := diff.Parse(diffData)
	if err != nil {
		return nil
	}
	files := make(map[string]bool)
	for i := range changes {
		if changes[i].File != "" && changes[i].File != "/dev/null" {
			files[changes[i].File] = true
		}
	}
	var out []review.Finding
	for _, f := range a.staticFindings {
		if files[f.File] {
			out = append(out, f)
		}
	}
	return out
}

// staticHit 判断 finding 是否已被静态规则命中（同文件同行）。
func (a *Agent) staticHit(file string, line int) bool {
	for _, f := range a.staticFindings {
		if f.File == file && f.Line == line {
			return true
		}
	}
	return false
}

// validate 逐条校验 finding；静态规则已命中的直接置信度 1.0，跳过 LLM 复核。
func (a *Agent) validate(ctx context.Context, findings []review.Finding, pool *recall.EvidencePool) ([]Validation, error) {
	validations := make([]Validation, len(findings))
	for i, f := range findings {
		if a.staticHit(f.File, f.Line) {
			validations[i] = Validation{FindingID: i, Confidence: 1.0, Evidence: f.Evidence}
			continue
		}
		v, err := a.validator.validateOne(ctx, f, i, pool)
		if err != nil {
			validations[i] = Validation{
				FindingID:  i,
				Confidence: 0.5,
				Evidence:   f.Evidence,
				Gaps:       []string{"validation failed: " + err.Error()},
			}
			continue
		}
		validations[i] = v
	}
	return validations, nil
}

// mergeAndDedup 合并静态规则与 LLM findings，按 file:line 去重（静态规则优先）。
func mergeAndDedup(static, llm []review.Finding) []review.Finding {
	seen := make(map[string]bool)
	out := make([]review.Finding, 0, len(static)+len(llm))
	for _, f := range static {
		key := fmt.Sprintf("%s:%d", f.File, f.Line)
		if !seen[key] {
			seen[key] = true
			out = append(out, f)
		}
	}
	for _, f := range llm {
		if f.Line == 0 {
			out = append(out, f) // 行号未定位，不参与去重
			continue
		}
		key := fmt.Sprintf("%s:%d", f.File, f.Line)
		if !seen[key] {
			seen[key] = true
			out = append(out, f)
		}
	}
	return out
}
