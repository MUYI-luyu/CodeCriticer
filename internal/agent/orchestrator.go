package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

// Orchestrator 负责动态工具编排。
type Orchestrator struct {
	llm      DecisionClient
	registry *ToolRegistry
	maxSteps int
}

func NewOrchestrator(llm DecisionClient, registry *ToolRegistry) *Orchestrator {
	return &Orchestrator{llm: llm, registry: registry, maxSteps: 10}
}

func (o *Orchestrator) SetMaxSteps(n int) {
	if n > 0 {
		o.maxSteps = n
	}
}

// Execute 运行编排循环。
func (o *Orchestrator) Execute(ctx context.Context, st *State) (*Result, error) {
	if st == nil {
		return nil, errors.New("state 为空")
	}
	if o.registry == nil {
		return nil, errors.New("tool registry 为空")
	}
	start := time.Now()
	res := &Result{}
	var history []Attempt

	for step := 0; step < o.maxSteps; step++ {
		action, err := DecideNextAction(ctx, o.llm, o.registry.List(), st, history)
		if err != nil {
			return nil, err
		}
		if action.Tool == "done" {
			res.Converged = true
			res.Reason = action.Reason
			res.Attempts = history
			res.Findings = append([]review.Finding{}, st.Findings...)
			res.TotalDuration = time.Since(start)
			return res, nil
		}

		tool, ok := o.registry.Get(action.Tool)
		if !ok {
			return nil, fmt.Errorf("未知工具: %s", action.Tool)
		}

		call := ToolCall{Tool: action.Tool, Args: action.Args}
		callStart := time.Now()
		result, err := tool.Execute(ctx, st, action.Args)
		call.Duration = time.Since(callStart)
		if err != nil {
			call.Error = err.Error()
		} else {
			call.Result = result
		}

		st.AppendEvidence(action.Tool, result)
		if err != nil {
			st.AppendEvidence(action.Tool+"-error", err.Error())
		}

		history = append(history, Attempt{
			Round:     step + 1,
			ToolCalls: []ToolCall{call},
			Evidence:  st.EvidenceText(),
			Findings:  append([]review.Finding{}, st.Findings...),
		})
	}

	res.Attempts = history
	res.Findings = append([]review.Finding{}, st.Findings...)
	res.Reason = "达到最大步数"
	res.TotalDuration = time.Since(start)
	return res, nil
}
