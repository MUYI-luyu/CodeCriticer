package eval

import "github.com/MUYI-luyu/codecritic/internal/workflow"

// CostSummary 是单个 case 的 token 成本汇总。
type CostSummary struct {
	TotalPromptTokens     int `json:"total_prompt_tokens"`
	TotalCompletionTokens int `json:"total_completion_tokens"`
	TotalTokens           int `json:"total_tokens"`
	Rounds                int `json:"rounds"`
}

// ComputeCost 从 Workflow trace 计算总 token 成本。
func ComputeCost(trace *workflow.Trace) CostSummary {
	var summary CostSummary
	if trace == nil {
		return summary
	}
	summary.Rounds = 1
	summary.TotalPromptTokens = trace.Usage.PromptTokens
	summary.TotalCompletionTokens = trace.Usage.CompletionTokens
	summary.TotalTokens = trace.Usage.TotalTokens

	return summary
}
