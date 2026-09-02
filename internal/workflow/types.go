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
	Plan(context.Context, string) ([]review.Point, review.LLMUsage, error)
	Review(context.Context, string) ([]review.Finding, review.LLMUsage, error)
	ChatWithUsage(context.Context, string, string, string) (string, review.LLMUsage, error)
}

type Request struct {
	Repo string
	Diff []byte
}

type Plan struct {
	TargetFiles []string `json:"target_files"`
	Symbols     []string `json:"symbols"`
	Concern     string   `json:"concern"`
	Questions   []string `json:"questions"`
	Keywords    []string `json:"keywords"`
}

type Evidence struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Type    string `json:"type"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Content string `json:"content"`
}

type ToolCall struct {
	Step        int                    `json:"step"`
	Tool        string                 `json:"tool"`
	Args        map[string]interface{} `json:"args,omitempty"`
	EvidenceIDs []string               `json:"evidence_ids,omitempty"`
	Error       string                 `json:"error,omitempty"`
	Duration    time.Duration          `json:"duration"`
}

type Validation struct {
	FindingIndex int     `json:"finding_index"`
	Accepted     bool    `json:"accepted"`
	Confidence   float64 `json:"confidence"`
	Reason       string  `json:"reason"`
}

type Trace struct {
	ID          string           `json:"id"`
	Request     Request          `json:"request"`
	Plan        Plan             `json:"plan"`
	Evidence    []*Evidence      `json:"evidence"`
	ToolCalls   []ToolCall       `json:"tool_calls"`
	Findings    []review.Finding `json:"findings"`
	Validations []Validation     `json:"validations"`
	LLMCalls    []review.LLMCall `json:"llm_calls"`
	StopReason  string           `json:"stop_reason"`
	Usage       review.LLMUsage  `json:"usage"`
	Duration    time.Duration    `json:"duration"`
	Errors      []string         `json:"errors,omitempty"`
}

const (
	StopToolError       = "tool_error"
	StopAgentDone       = "agent_done"
	StopMaxSteps        = "max_steps"
	StopInvalidDecision = "invalid_decision"
	StopContextCanceled = "context_canceled"
	StopStageError      = "stage_error"
)

// Save writes a complete workflow trace with owner-only permissions.
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
