// Package agent 实现基于 Reflexion 的代码审查 Agent。
package agent

import (
	"time"

	"github.com/MUYI-luyu/codecritic/internal/recall"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

// Validation 是对一个 finding 的证据校验结果。
// 从二元判断（keep/drop）升级为定量评分（0-1 confidence）。
type Validation struct {
	FindingID  int      `json:"finding_id"` // 对应 findings 数组的索引
	Confidence float64  `json:"confidence"` // 可信度评分 0.0-1.0
	Evidence   string   `json:"evidence"`   // 支持该 finding 的关键证据（代码片段）
	Gaps       []string `json:"gaps"`       // 证据缺口（当 confidence < 阈值时）
}

// Critique 是对一个低可信度 finding 的结构化批评。
// 用于指导下一轮审查（Reflexion 的核心）。
type Critique struct {
	FindingID  int    `json:"finding_id"` // 对应 findings 数组的索引
	Reason     string `json:"reason"`     // 为什么可信度低（一句话）
	Evidence   string `json:"evidence"`   // 反驳的证据（从 validation 中提取）
	Suggestion string `json:"suggestion"` // 下次审查时应该做什么（具体行动建议）
}

// Attempt 是一次审查尝试的完整记录。
// 包含 Execute → Validate → Reflect 三个阶段的产出。
type Attempt struct {
	Round        int                  `json:"round"`       // 第几轮（1-based）
	Findings     []review.Finding     `json:"findings"`    // Execute 阶段产出
	Validations  []Validation         `json:"validations"` // Validate 阶段产出
	Critiques    []Critique           `json:"critiques"`   // Reflect 阶段产出
	ToolCalls    []ToolCall           `json:"tool_calls"`  // 工具调用记录（仅 orchestration 模式）
	EvidencePool *recall.EvidencePool `json:"-"`           // Plan 阶段召回的证据池（传递给 Validate）
	StartedAt    time.Time            `json:"started_at"`
	Duration     time.Duration        `json:"duration"`
	Error        string               `json:"error,omitempty"` // 如果本轮失败
}

// Result 是完整的审查结果，包含所有尝试轨迹。
type Result struct {
	Attempts       []Attempt        `json:"attempts"`                  // 所有尝试的完整记录
	StaticFindings []review.Finding `json:"static_findings,omitempty"` // 静态规则命中（确定性，不进 Reflexion 循环）
	FinalFindings  []review.Finding `json:"final_findings"`            // 最终输出（静态规则 + LLM 高置信度，去重后）
	Converged      bool             `json:"converged"`                 // 是否收敛
	Reason         string           `json:"reason"`                    // 收敛原因或 max_attempts
	TotalDuration  time.Duration    `json:"total_duration"`
}

// ConfidenceThreshold 是 finding 可信度阈值，低于此值需要 reflect。
const ConfidenceThreshold = 0.7

// MaxAttempts 是默认最大尝试轮数。
const MaxAttempts = 3
