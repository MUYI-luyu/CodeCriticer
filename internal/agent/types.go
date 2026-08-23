package agent

import "github.com/MUYI-luyu/codecritic/internal/review"

// ConfidenceThreshold 是判定 finding 可信的置信度阈值。
const ConfidenceThreshold = 0.7

// MaxAttempts 是 Reflexion Loop 的最大尝试轮数。
const MaxAttempts = 3

// Validation 是一个 finding 的证据验证结果。
type Validation struct {
	FindingID  int      `json:"finding_id"`
	Confidence float64  `json:"confidence"` // 0.0-1.0
	Evidence   string   `json:"evidence"`   // 支持证据
	Gaps       []string `json:"gaps"`       // 证据缺口
}

// Critique 是对低置信度 finding 的结构化批评。
type Critique struct {
	FindingID  int    `json:"finding_id"`
	Reason     string `json:"reason"`     // 为什么置信度低
	Evidence   string `json:"evidence"`   // 反驳的证据
	Suggestion string `json:"suggestion"` // 下次审查时应该做什么
}

// Attempt 是一次审查尝试的完整记录。
type Attempt struct {
	Round       int                `json:"round"`
	Findings    []review.Finding   `json:"findings"`
	Validations []Validation       `json:"validations,omitempty"`
	Critiques   []Critique         `json:"critiques,omitempty"`
	ToolCalls   []ToolCall         `json:"tool_calls,omitempty"`
	StartedAt   string             `json:"started_at"` // RFC3339
	Duration    string             `json:"duration"`   // "1.234s"
	Error       string             `json:"error,omitempty"`
}

// ToolCall 是工具调用记录。
type ToolCall struct {
	Tool   string                 `json:"tool"`
	Args   map[string]interface{} `json:"args"`
	Result interface{}            `json:"result,omitempty"`
	Error  string                 `json:"error,omitempty"`
}

// Result 是完整的审查结果。
type Result struct {
	Attempts      []Attempt        `json:"attempts"`
	FinalFindings []review.Finding `json:"final_findings"`
	Converged     bool             `json:"converged"`
	Reason        string           `json:"reason"`        // 收敛原因
	TotalDuration string           `json:"total_duration"` // "5.678s"
}
