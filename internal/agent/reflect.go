package agent

import (
	"context"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

// Reflector 负责生成结构化批评。
// 批评是 Reflexion 的核心：告诉下一轮"为什么这次错了，下次应该怎么做"。
type Reflector struct {
	llm *review.LLM
}

// NewReflector 创建批评生成器。
func NewReflector(llm *review.LLM) *Reflector {
	return &Reflector{llm: llm}
}

// Reflect 对低可信度的 findings 生成批评，返回批评 + token 用量。
// 只对 confidence < ConfidenceThreshold 的 findings 生成批评。
func (r *Reflector) Reflect(ctx context.Context, findings []review.Finding, validations []Validation) ([]Critique, review.LLMUsage, error) {
	var critiques []Critique
	var totalUsage review.LLMUsage

	for _, v := range validations {
		// 只批评低可信度的 findings
		if v.Confidence >= ConfidenceThreshold {
			continue
		}

		// 防止越界
		if v.FindingID >= len(findings) {
			continue
		}

		finding := findings[v.FindingID]

		// 调用 LLM 生成批评
		reason, evidence, suggestion, usage, err := r.llm.GenerateCritique(ctx, finding, v.Confidence, v.Evidence, v.Gaps)
		totalUsage.PromptTokens += usage.PromptTokens
		totalUsage.CompletionTokens += usage.CompletionTokens
		totalUsage.TotalTokens += usage.TotalTokens

		if err != nil {
			// 生成失败时，使用默认批评（保证流程不中断）
			critiques = append(critiques, Critique{
				FindingID:  v.FindingID,
				Reason:     "证据不足",
				Evidence:   v.Evidence,
				Suggestion: "收集更多证据后重新评估",
			})
			continue
		}

		critiques = append(critiques, Critique{
			FindingID:  v.FindingID,
			Reason:     reason,
			Evidence:   evidence,
			Suggestion: suggestion,
		})
	}

	return critiques, totalUsage, nil
}
