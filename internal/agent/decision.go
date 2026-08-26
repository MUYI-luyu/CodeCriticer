package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

// NextAction 是 LLM 决策的下一步工具调用。
type NextAction struct {
	Tool   string                 `json:"tool"`
	Args   map[string]interface{} `json:"args"`
	Reason string                 `json:"reason"`
}

const decisionPrompt = `你是代码审查 Agent。根据当前证据，决定下一步应该调用哪个工具。

可用工具：
1. locate_symbols: 定位 diff 中的符号变更（函数/方法/类型）
   参数：无

2. static_rules: 运行静态分析规则（go vet、copylock、printf 等）
   参数：无

3. analyze_impact: 分析符号的影响范围（调用方）
   参数：{"symbol": "函数名", "file": "文件路径"}

4. search_code: 全文搜索关键词
   参数：{"keyword": "关键词"}

5. review_point: 针对审查要点执行审查
   参数：{"description": "审查要点描述", "context": "召回的代码上下文"}

6. done: 完成审查
   参数：无

决策原则：
- 先用 locate_symbols 定位变更
- 尽早调用 static_rules 获取确定性问题（go vet 系列）
- 对关键符号用 analyze_impact 分析调用方
- 用 search_code 召回相关代码
- 用 review_point 执行审查
- 所有必要工具都调用后，返回 done

历史调用：
%s

当前证据：
%s

只输出 JSON：
{
  "tool": "工具名",
  "args": {"参数": "值"},
  "reason": "为什么选择这个工具（一句话）"
}`

// DecideNextAction 让 LLM 决策下一步工具调用。
func DecideNextAction(ctx context.Context, llm *review.LLM, registry *ToolRegistry, evidence string, history []ToolCall) (*NextAction, error) {
	// 格式化历史调用
	historyText := formatHistory(history)

	// 调用 LLM 决策
	prompt := fmt.Sprintf(decisionPrompt, historyText, evidence)
	text, err := llm.Chat(ctx, "", prompt, llm.Config().PlanModel)
	if err != nil {
		return nil, fmt.Errorf("LLM 决策失败: %w", err)
	}

	// 解析决策
	var action NextAction
	if err := json.Unmarshal([]byte(stripFence(text)), &action); err != nil {
		return nil, fmt.Errorf("解析决策: %w", err)
	}

	return &action, nil
}

// formatHistory 格式化历史工具调用（包含结果摘要）。
func formatHistory(history []ToolCall) string {
	if len(history) == 0 {
		return "无"
	}

	var lines []string
	for i, call := range history {
		if call.Error != "" {
			lines = append(lines, fmt.Sprintf("%d. %s (失败: %s)", i+1, call.Tool, call.Error))
		} else {
			// 加入结果摘要（前 200 字符）
			resultSummary := truncateResult(call.Result, 200)
			lines = append(lines, fmt.Sprintf("%d. %s → %s", i+1, call.Tool, resultSummary))
		}
	}
	return strings.Join(lines, "\n")
}

// truncateResult 截断结果为摘要（前 maxLen 字符）。
func truncateResult(result interface{}, maxLen int) string {
	str := fmt.Sprintf("%v", result)
	if len(str) <= maxLen {
		return str
	}
	return str[:maxLen] + "..."
}

// stripFence 去掉 markdown 围栏。
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
