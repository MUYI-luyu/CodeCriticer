package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MUYI-luyu/codecritic/internal/recall"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

// Orchestrator 是动态工具编排器。
// 核心：LLM 根据当前证据决定下一步调用哪个工具，而非固定 pipeline。
type Orchestrator struct {
	llm      *review.LLM
	registry *ToolRegistry
	diffText string
	maxSteps int
}

// NewOrchestrator 创建工具编排器。
func NewOrchestrator(llm *review.LLM, store *recall.Store, repo, diffText string) *Orchestrator {
	registry := NewToolRegistry()

	// 注册工具
	registry.Register(NewLocateSymbolsTool(diffText, repo))
	registry.Register(NewAnalyzeImpactTool(store))
	registry.Register(NewSearchCodeTool(store))
	registry.Register(NewReviewPointTool(llm, diffText))

	// 新增：静态规则工具
	registry.Register(&StaticRulesTool{repo: repo})

	return &Orchestrator{
		llm:      llm,
		registry: registry,
		diffText: diffText,
		maxSteps: 10, // 防止无限循环
	}
}

// OrchestrationResult 是编排结果。
type OrchestrationResult struct {
	Findings  []review.Finding
	ToolCalls []ToolCall
}

// Execute 执行动态工具编排。
func (o *Orchestrator) Execute(ctx context.Context) (*OrchestrationResult, error) {
	var toolCalls []ToolCall
	var findings []review.Finding
	evidence := fmt.Sprintf("Diff:\n%s", o.diffText)
	noProgressCount := 0 // 连续无进展次数

	for step := 1; step <= o.maxSteps; step++ {
		// 决策下一步
		action, err := DecideNextAction(ctx, o.llm, o.registry, evidence, toolCalls)
		if err != nil {
			return nil, fmt.Errorf("决策失败 (step %d): %w", step, err)
		}

		// 检查是否完成
		if action.Tool == "done" {
			break
		}

		// 检查是否重复调用（连续 3 次相同工具 + 无新 findings）
		if isRepeatedTool(action.Tool, toolCalls) {
			noProgressCount++
			if noProgressCount >= 3 {
				// 强制退出：连续 3 次重复工具调用
				break
			}
		} else {
			noProgressCount = 0 // 重置计数
		}

		// 执行工具
		tool, ok := o.registry.Get(action.Tool)
		if !ok {
			return nil, fmt.Errorf("未知工具: %s", action.Tool)
		}

		start := time.Now()
		result, err := tool.Execute(ctx, action.Args)
		toolCall := ToolCall{
			Tool:     action.Tool,
			Args:     action.Args,
			Duration: time.Since(start),
		}

		if err != nil {
			toolCall.Error = err.Error()
			toolCalls = append(toolCalls, toolCall)
			continue
		}

		toolCall.Result = result
		toolCalls = append(toolCalls, toolCall)

		// 更新证据
		evidence = o.updateEvidence(evidence, action, result)

		// 如果是 review_point，收集 findings
		if action.Tool == "review_point" {
			if resultMap, ok := result.(map[string]interface{}); ok {
				if findingsRaw, ok := resultMap["findings"].([]review.Finding); ok {
					findings = append(findings, findingsRaw...)
				}
			}
		}
	}

	return &OrchestrationResult{
		Findings:  findings,
		ToolCalls: toolCalls,
	}, nil
}

// updateEvidence 更新证据上下文。
func (o *Orchestrator) updateEvidence(currentEvidence string, action *NextAction, result interface{}) string {
	var update strings.Builder
	update.WriteString(currentEvidence)
	update.WriteString(fmt.Sprintf("\n\n--- Step: %s (理由: %s) ---\n", action.Tool, action.Reason))

	switch action.Tool {
	case "locate_symbols":
		if m, ok := result.(map[string]interface{}); ok {
			update.WriteString(fmt.Sprintf("定位到 %v 个符号\n", m["count"]))
		}
	case "analyze_impact":
		if m, ok := result.(map[string]interface{}); ok {
			update.WriteString(fmt.Sprintf("符号 %v 有 %v 个调用方\n", m["symbol"], m["count"]))
		}
	case "search_code":
		if m, ok := result.(map[string]interface{}); ok {
			update.WriteString(fmt.Sprintf("关键词 '%v' 匹配 %v 处\n", m["keyword"], m["count"]))
		}
	case "review_point":
		if m, ok := result.(map[string]interface{}); ok {
			update.WriteString(fmt.Sprintf("发现 %v 个问题\n", m["count"]))
		}
	}

	return update.String()
}

// isRepeatedTool 检查最近 3 次调用是否都是同一个工具。
func isRepeatedTool(tool string, history []ToolCall) bool {
	if len(history) < 2 {
		return false
	}
	// 检查最近 2 次
	recent := history[len(history)-2:]
	for _, tc := range recent {
		if tc.Tool != tool {
			return false
		}
	}
	return true
}
