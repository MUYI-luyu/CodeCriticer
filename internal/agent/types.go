package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/MUYI-luyu/codecritic/internal/graph"
	"github.com/MUYI-luyu/codecritic/internal/recall"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

// Action 是编排器的一次决策结果。
type Action struct {
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args,omitempty"`
	Reason string         `json:"reason,omitempty"`
}

// ToolSpec 是给决策器展示的工具摘要。
type ToolSpec struct {
	Name        string
	Description string
}

// ToolCall 记录一次工具执行。
type ToolCall struct {
	Tool     string         `json:"tool"`
	Args     map[string]any `json:"args,omitempty"`
	Result   string         `json:"result,omitempty"`
	Error    string         `json:"error,omitempty"`
	Duration time.Duration  `json:"duration"`
}

// Attempt 记录一轮编排过程。
type Attempt struct {
	Round     int              `json:"round"`
	ToolCalls []ToolCall       `json:"tool_calls"`
	Evidence  string           `json:"evidence,omitempty"`
	Findings  []review.Finding `json:"findings,omitempty"`
}

// Result 是编排器的最终结果。
type Result struct {
	Findings      []review.Finding `json:"findings"`
	Attempts      []Attempt        `json:"attempts"`
	Converged     bool             `json:"converged"`
	Reason        string           `json:"reason,omitempty"`
	TotalDuration time.Duration    `json:"total_duration"`
}

// Validation 是证据校验结果。
type Validation struct {
	FindingID  int
	Confidence float64
	Evidence   string
	Gaps       []string
}

// Critique 是反思阶段产出的批评建议。
type Critique struct {
	FindingID  int
	Reason     string
	Evidence   string
	Suggestion string
}

// ConfidenceThreshold 是生成批评时的默认置信度阈值。
const ConfidenceThreshold = 0.7

// State 保存编排过程中的共享上下文。
type State struct {
	Repo     string
	RawDiff  string
	Evidence []string
	Symbols  []review.Sym
	Index    *graph.Index
	Store    *recall.Store
	Findings []review.Finding
}

// NewState 初始化编排状态。
func NewState(repo, raw string) *State {
	return &State{Repo: repo, RawDiff: raw}
}

// EvidenceText 拼接全部证据文本。
func (s *State) EvidenceText() string {
	return strings.Join(s.Evidence, "\n\n")
}

// AppendEvidence 追加一段证据摘要。
func (s *State) AppendEvidence(label, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if label != "" {
		s.Evidence = append(s.Evidence, fmt.Sprintf("[%s]\n%s", label, text))
		return
	}
	s.Evidence = append(s.Evidence, text)
}

// AddFindings 追加审查意见。
func (s *State) AddFindings(fs ...review.Finding) {
	s.Findings = append(s.Findings, fs...)
}
