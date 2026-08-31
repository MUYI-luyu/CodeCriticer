package eval

import "github.com/MUYI-luyu/codecritic/internal/agent"

// CostSummary 是单个 case 的 token 成本汇总。
type CostSummary struct {
	TotalPromptTokens     int `json:"total_prompt_tokens"`
	TotalCompletionTokens int `json:"total_completion_tokens"`
	TotalTokens           int `json:"total_tokens"`
	Rounds                int `json:"rounds"` // Reflexion 轮数
}

// ComputeCost 从 agent.Result 计算总 token 成本。
func ComputeCost(result *agent.Result) CostSummary {
	var summary CostSummary
	if result == nil {
		return summary
	}

	summary.Rounds = len(result.Attempts)
	for _, attempt := range result.Attempts {
		summary.TotalPromptTokens += attempt.LLMUsage.PromptTokens
		summary.TotalCompletionTokens += attempt.LLMUsage.CompletionTokens
		summary.TotalTokens += attempt.LLMUsage.TotalTokens
	}

	return summary
}
