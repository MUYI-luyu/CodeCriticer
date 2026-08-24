package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func stripFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

const decisionPrompt = `你是一个代码审查编排器。根据当前证据，选择下一步工具。

可用工具：
%s

历史记录：
%s

当前证据：
%s

规则：
1. 优先先定位符号，再分析影响，再搜索相关代码，最后审查。
2. 如果证据已经足够，返回 done。
3. 只输出 JSON：{"tool":"工具名或done","args":{},"reason":"简短原因"}`

// DecisionClient 提供生成下一步动作的能力。
type DecisionClient interface {
	CompleteJSON(context.Context, string, string) (string, error)
}

// DecideNextAction 根据当前状态决定下一步动作。
func DecideNextAction(ctx context.Context, llm DecisionClient, tools []ToolSpec, st *State, history []Attempt) (Action, error) {
	if llm == nil {
		return heuristicAction(st, history), nil
	}

	toolText := formatToolSpecs(tools)
	historyText := formatAttempts(history)
	prompt := fmt.Sprintf(decisionPrompt, toolText, historyText, st.EvidenceText())
	text, err := llm.CompleteJSON(ctx, prompt, "请决定下一步工具。")
	if err != nil {
		return Action{}, err
	}
	return parseAction(text)
}

func parseAction(text string) (Action, error) {
	var out Action
	if err := json.Unmarshal([]byte(stripFence(text)), &out); err != nil {
		return Action{}, err
	}
	out.Tool = strings.TrimSpace(out.Tool)
	if out.Args == nil {
		out.Args = map[string]any{}
	}
	if out.Tool == "" {
		return Action{}, fmt.Errorf("empty tool")
	}
	return out, nil
}

func formatToolSpecs(tools []ToolSpec) string {
	if len(tools) == 0 {
		return "(无)"
	}
	var b strings.Builder
	for i, t := range tools {
		fmt.Fprintf(&b, "%d. %s - %s\n", i+1, t.Name, t.Description)
	}
	return strings.TrimSpace(b.String())
}

func formatAttempts(history []Attempt) string {
	if len(history) == 0 {
		return "(无)"
	}
	var b strings.Builder
	for _, a := range history {
		fmt.Fprintf(&b, "Round %d:\n", a.Round)
		for _, c := range a.ToolCalls {
			fmt.Fprintf(&b, "- %s => %s", c.Tool, strings.TrimSpace(c.Result))
			if c.Error != "" {
				fmt.Fprintf(&b, " (err=%s)", c.Error)
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func heuristicAction(st *State, history []Attempt) Action {
	steps := []string{"locate_symbols", "analyze_impact", "search_code", "review_point"}
	used := map[string]bool{}
	for _, a := range history {
		for _, c := range a.ToolCalls {
			used[c.Tool] = true
		}
	}
	for _, name := range steps {
		if !used[name] {
			args := map[string]any{}
			if name == "search_code" {
				args["keywords"] = symbolNames(st.Symbols)
			}
			return Action{Tool: name, Args: args, Reason: "按固定编排推进下一步"}
		}
	}
	return Action{Tool: "done", Args: map[string]any{}, Reason: "已完成基础编排"}
}
