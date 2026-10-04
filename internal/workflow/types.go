package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

type LLMClient interface {
	CompleteToolsWithUsage(context.Context, string, []review.ToolMessage, []review.ToolDefinition, string) (review.ToolMessage, review.LLMUsage, error)
}

type Request struct {
	Repo           string
	Diff           []byte
	AllowExecution bool
}

// ReviewScope 是由变更确定性导出的调查范围，不包含另一套 Agent 计划。
type ReviewScope struct {
	TargetFiles []string `json:"target_files"`
	Symbols     []string `json:"symbols"`
	Concern     string   `json:"concern"`
}

// TraceStats 记录调查阶段的可观测计数。
type TraceStats struct {
	DecisionCount       int `json:"decision_count"`
	SuccessfulToolCalls int `json:"successful_tool_calls"`
	FailedToolCalls     int `json:"failed_tool_calls"`
	DuplicateCalls      int `json:"duplicate_calls"`
	NoNewEvidenceCalls  int `json:"no_new_evidence_calls"`
}

// ContextProjectionStats describes only the latest model-facing projection.
// Conversation remains the complete, unmodified transcript.
type ContextProjectionStats struct {
	Enabled                    bool `json:"enabled"`
	Applications               int  `json:"applications"`
	FullBytes                  int  `json:"full_bytes"`
	ProjectedBytes             int  `json:"projected_bytes"`
	DuplicateResultsCompacted  int  `json:"duplicate_results_compacted"`
	DominatedReadCodeCompacted int  `json:"dominated_read_code_compacted"`
}

type Evidence struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Type    string `json:"type"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	EndLine int    `json:"end_line,omitempty"`
	Content string `json:"content"`
	Symbol  string `json:"symbol,omitempty"`
	StepID  string `json:"step_id,omitempty"`
}

// InvestigationStep 是一次完整的 action/result 记录。
// Evidence 通过 StepID 追溯到这里，不再另存一份 Observation 状态。
type InvestigationStep struct {
	ID       string        `json:"id"`
	Tool     string        `json:"tool"`
	Args     any           `json:"args,omitempty"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
}

type VerdictStatus string

const (
	VerdictAccepted   VerdictStatus = "accepted"
	VerdictRejected   VerdictStatus = "rejected"
	VerdictUnresolved VerdictStatus = "unresolved"
)

// Verdict 是系统对一个 CandidateClaim 的唯一裁决状态。
type Verdict struct {
	ClaimID         string        `json:"claim_id"`
	Status          VerdictStatus `json:"status"`
	Reason          string        `json:"reason"`
	MissingEvidence []string      `json:"missing_evidence,omitempty"`
}

// FinalReport 是候选结论和裁决的唯一持久化位置。
type FinalReport struct {
	Claims   []review.CandidateClaim `json:"claims"`
	Verdicts []Verdict               `json:"verdicts"`
}

type Trace struct {
	ID            string                 `json:"id"`
	Request       Request                `json:"request"`
	Snapshot      *ReviewSnapshot        `json:"snapshot,omitempty"`
	Scope         ReviewScope            `json:"scope"`
	Evidence      []*Evidence            `json:"evidence"`
	Investigation []InvestigationStep    `json:"investigation"`
	FinalReport   FinalReport            `json:"final_report,omitempty"`
	Conversation  []review.ToolMessage   `json:"conversation,omitempty"`
	LLMCalls      []review.LLMCall       `json:"llm_calls"`
	StopReason    string                 `json:"stop_reason"`
	Usage         review.LLMUsage        `json:"usage"`
	Duration      time.Duration          `json:"duration"`
	Errors        []string               `json:"errors,omitempty"`
	Stats         TraceStats             `json:"stats"`
	Projection    ContextProjectionStats `json:"context_projection"`
}

func (r FinalReport) ClaimsWithStatus(status VerdictStatus) []review.CandidateClaim {
	statuses := make(map[string]VerdictStatus, len(r.Verdicts))
	for _, verdict := range r.Verdicts {
		statuses[verdict.ClaimID] = verdict.Status
	}
	out := make([]review.CandidateClaim, 0)
	for _, claim := range r.Claims {
		if statuses[claim.ID] == status {
			out = append(out, claim)
		}
	}
	return out
}

const (
	StopAgentDone       = "agent_done"
	StopMaxSteps        = "max_steps"
	StopInvalidDecision = "invalid_decision"
	StopContextCanceled = "context_canceled"
	StopStageError      = "stage_error"
)

// 保存完整轨迹并限制文件权限。
func (t *Trace) Save(path string) error {
	if t == nil {
		return fmt.Errorf("nil trace")
	}
	if path == "" {
		return fmt.Errorf("empty trace path")
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("encode trace: %w", err)
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create trace directory: %w", err)
		}
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write trace: %w", err)
	}
	return nil
}

type observer struct {
	mu    sync.Mutex
	trace *Trace
	stage string
}

func (o *observer) setStage(stage string) {
	o.mu.Lock()
	o.stage = stage
	o.mu.Unlock()
}

func (o *observer) OnLLMCall(c review.LLMCall) {
	o.mu.Lock()
	defer o.mu.Unlock()
	c.TraceID = o.trace.ID
	c.Stage = o.stage
	o.trace.LLMCalls = append(o.trace.LLMCalls, c)
	o.trace.Usage.PromptTokens += c.Usage.PromptTokens
	o.trace.Usage.CompletionTokens += c.Usage.CompletionTokens
	o.trace.Usage.TotalTokens += c.Usage.TotalTokens
}
