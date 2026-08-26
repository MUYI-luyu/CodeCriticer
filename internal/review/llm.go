package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const reviewPrompt = `你是资深 Go 代码审查员。审查下面 unified diff，找出真实 bug 与风险（并发、错误处理、边界、资源泄漏、逻辑错误）。
只输出 JSON，不要任何其他文字，格式：
{"findings":[{"file":"文件路径","line":行号或0,"severity":"error|warning|info","msg":"问题描述","evidence":"相关代码片段"}]}
没有问题时输出 {"findings":[]}。`

const planPrompt = `你是代码审查规划者。读下面的 unified diff，规划 3-6 个审查要点，每个要点聚焦一类风险（并发、错误处理、边界、资源泄漏、逻辑）。
为每个要点给 1-3 个召回关键词（符号名/类型名/关键术语），用于检索相关代码。
只输出 JSON：
{"points":[{"desc":"要点描述","kw":["关键词"]}]}`

const validatePrompt = `你是代码审查证据校验员。给出一个 finding 与召回的证据，评估该 finding 的可信度。

Finding:
文件: %s
行号: %d
严重程度: %s
问题: %s
声称证据: %s

召回的证据：
函数体上下文:
%s

变量定义:
%s

调用关系:
%s

输出 JSON：
{
  "confidence": 0.0-1.0,
  "evidence": "支持该 finding 的关键证据（代码片段）",
  "gaps": ["证据缺口1", "证据缺口2"]
}

评分标准：
- 0.9-1.0：有明确代码证据，必然导致 bug（如：确实 nil check 缺失且后续解引用）
- 0.7-0.9：有间接证据，很可能是 bug（如：并发访问共享变量但无锁保护）
- 0.4-0.7：证据不足，可能是误报（如：声称 panic 但没看到触发路径）
- 0.0-0.4：明显误报（如：声称 nil panic 但代码已有 if x != nil 检查）

如果 confidence < 0.7，必须在 gaps 中列出缺失的证据。
只输出 JSON，不要其他文字。`

const critiquePrompt = `你是代码审查批评者。给出一个低可信度的 finding 及其 validation 结果，分析为什么可信度低，并给出改进建议。

Finding: [%s] %s:%d - %s

Validation 结果:
置信度: %.1f%%
证据: %s
缺口: %s

输出 JSON：
{
  "reason": "为什么可信度低（一句话）",
  "evidence": "反驳的证据（从 validation 的证据中提取关键部分）",
  "suggestion": "下次审查时应该做什么（具体的行动建议）"
}

示例：
Finding: "Variable x may be nil, potential panic"
Validation Confidence: 0.3
Validation Evidence: "Line 10: if x != nil { x.Method() }"
Validation Gaps: ["未检查所有 x 的使用点", "未追踪 x 的定义"]

输出：
{
  "reason": "声称 nil panic，但第 10 行已有 if x != nil 检查保护",
  "evidence": "if x != nil { x.Method() }",
  "suggestion": "下次审查前，先检索变量的所有赋值语句和 nil 检查，确认是否有未保护的使用点"
}

只输出 JSON，不要其他文字。`

// Config 返回 LLM 的配置（公开给 agent 包使用）。
func (l *LLM) Config() *Config {
	return l.config
}

// LLM 是 OpenAI 兼容的聊天客户端，支持分级模型配置。
type LLM struct {
	config *Config
	client *http.Client
}

// NewLLMWithConfig 创建 LLM 客户端，使用自定义配置。
func NewLLMWithConfig(opts ...Option) *LLM {
	cfg := DefaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	return &LLM{config: cfg, client: &http.Client{}}
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Model    string    `json:"model"`
	Messages []chatMsg `json:"messages"`
	Format   respFmt   `json:"response_format"`
}

type respFmt struct {
	Type string `json:"type"`
}

type chatResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Chat 发送一轮对话，返回助手文本（公开给 agent 包使用）。
func (l *LLM) Chat(ctx context.Context, system, user, model string) (string, error) {
	return l.chat(ctx, system, user, model)
}

// chat 发送一轮对话，返回助手文本。
func (l *LLM) chat(ctx context.Context, system, user, model string) (string, error) {
	body := chatReq{
		Model:    model,
		Messages: []chatMsg{{Role: "system", Content: system}, {Role: "user", Content: user}},
	}
	body.Format.Type = "json_object"

	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.config.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+l.config.APIKey)

	resp, err := l.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("llm: %s: %s", resp.Status, b)
	}
	var out chatResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("llm: 空响应")
	}
	return out.Choices[0].Message.Content, nil
}

// Review 一次性审查 diff，返回 findings。
func (l *LLM) Review(ctx context.Context, diffText string) ([]Finding, error) {
	text, err := l.chat(ctx, reviewPrompt, diffText, l.config.ReviewModel)
	if err != nil {
		return nil, err
	}
	return parseFindings(text)
}

// Plan 规划审查要点。
func (l *LLM) Plan(ctx context.Context, diffText string) ([]Point, error) {
	text, err := l.chat(ctx, planPrompt, diffText, l.config.PlanModel)
	if err != nil {
		return nil, err
	}
	var out struct {
		Points []Point `json:"points"`
	}
	if err := json.Unmarshal([]byte(stripFence(text)), &out); err != nil {
		return nil, err
	}
	return out.Points, nil
}

// ReviewPoint 针对单个要点审查，ctxText 是召回的代码。
func (l *LLM) ReviewPoint(ctx context.Context, diffText, desc, ctxText string) ([]Finding, error) {
	sys := fmt.Sprintf(`你是资深 Go 代码审查员。针对审查要点「%s」，结合 diff 与召回代码，找出该要点下的真实 bug。
只输出 JSON：{"findings":[{"file":"...","line":0,"severity":"error|warning|info","msg":"...","evidence":"..."}]}
没有则 {"findings":[]}。`, desc)
	user := "diff:\n" + diffText + "\n\n召回代码:\n" + ctxText
	text, err := l.chat(ctx, sys, user, l.config.ReviewModel)
	if err != nil {
		return nil, err
	}
	return parseFindings(text)
}

// ValidateFinding 对一个 finding 进行证据校验，返回置信度评分。
// 这是 Reflexion 的核心：从二元判断升级为定量评分。
// 返回：confidence, evidence, gaps, error
func (l *LLM) ValidateFinding(ctx context.Context, f Finding, funcBody, varDefs, callChain string) (float64, string, []string, error) {
	user := fmt.Sprintf(validatePrompt,
		f.File, f.Line, f.Severity, f.Msg, f.Evidence,
		funcBody, varDefs, callChain)

	text, err := l.chat(ctx, "", user, l.config.ReflectModel)
	if err != nil {
		return 0, "", nil, err
	}

	var result struct {
		Confidence float64  `json:"confidence"`
		Evidence   string   `json:"evidence"`
		Gaps       []string `json:"gaps"`
	}
	if err := json.Unmarshal([]byte(stripFence(text)), &result); err != nil {
		return 0, "", nil, fmt.Errorf("解析 validation: %w", err)
	}

	return result.Confidence, result.Evidence, result.Gaps, nil
}

// GenerateCritique 对低可信度 finding 生成结构化批评。
// 批评用于指导下一轮审查（Reflexion 的 memory）。
// 返回：reason, evidence, suggestion, error
func (l *LLM) GenerateCritique(ctx context.Context, f Finding, confidence float64, evidence string, gaps []string) (string, string, string, error) {
	user := fmt.Sprintf(critiquePrompt,
		f.Severity, f.File, f.Line, f.Msg,
		confidence*100, evidence, strings.Join(gaps, "; "))

	text, err := l.chat(ctx, "", user, l.config.ReflectModel)
	if err != nil {
		return "", "", "", err
	}

	var result struct {
		Reason     string `json:"reason"`
		Evidence   string `json:"evidence"`
		Suggestion string `json:"suggestion"`
	}
	if err := json.Unmarshal([]byte(stripFence(text)), &result); err != nil {
		return "", "", "", fmt.Errorf("解析 critique: %w", err)
	}

	return result.Reason, result.Evidence, result.Suggestion, nil
}

func parseFindings(text string) ([]Finding, error) {
	var out struct {
		Findings []Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(stripFence(text)), &out); err != nil {
		return nil, fmt.Errorf("解析 findings: %w", err)
	}
	return out.Findings, nil
}

// stripFence 去掉模型可能包在 JSON 外的 markdown 围栏。
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
