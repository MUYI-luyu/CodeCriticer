package agent

import (
	"context"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

// Reviewer 是编排器需要的最小执行面。
type Reviewer interface {
	CompleteJSON(context.Context, string, string) (string, error)
}

// Agent 组合工具注册表与编排器，供 CLI 使用。
type Agent struct {
	orchestrator *Orchestrator
}

func New(llm Reviewer, registry *ToolRegistry) *Agent {
	if registry == nil {
		registry = NewToolRegistry()
	}
	return &Agent{orchestrator: NewOrchestrator(llm, registry)}
}

func NewWithDefaults(llm *review.LLM) *Agent {
	return New(llm, BuildDefaultRegistry(llm))
}

func (a *Agent) WithMaxSteps(n int) *Agent {
	if a != nil && a.orchestrator != nil {
		a.orchestrator.SetMaxSteps(n)
	}
	return a
}

// Review 执行一次编排审查。
func (a *Agent) Review(ctx context.Context, st *State) (*Result, error) {
	if a == nil || a.orchestrator == nil {
		return nil, nil
	}
	return a.orchestrator.Execute(ctx, st)
}

// BuildDefaultRegistry 注册默认审查工具。
func BuildDefaultRegistry(llm *review.LLM) *ToolRegistry {
	registry := NewToolRegistry()
	registry.Register(LocateSymbolsTool{})
	registry.Register(StaticRulesTool{})
	registry.Register(AnalyzeImpactTool{})
	registry.Register(SearchCodeTool{})
	if llm != nil {
		registry.Register(NewReviewPointTool(llm))
	}
	return registry
}
