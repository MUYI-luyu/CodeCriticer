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

召回的证据:
%s

请输出 0-1 的置信度分数（0=完全不可信，1=证据确凿），并说明：
1. 支持该 finding 的证据
2. 缺失或矛盾的证据（gaps）

只输出 JSON：
{"confidence": 0.0-1.0, "evidence": "支持的证据摘要", "gaps": ["缺失的证据1", "缺失的证据2"]}`

const critiquePrompt = `你是代码审查反思器。给出一个低置信度的 finding 及其 validation 结果，生成具体的反驳理由和改进建议。

Finding:
文件: %s
行号: %d
严重程度: %s
问题: %s
声称证据: %s

Validation 结果:
置信度: %.2f
证据: %s
缺失项: %s

请生成：
1. Reason: 为什么这个 finding 置信度低？具体的反驳理由
2. Evidence: 从 validation 证据中提取关键部分
3. Suggestion: 下次审查时应该做什么？（具体的行动建议）

只输出 JSON：
{
  "reason": "反驳理由（一句话说明为什么置信度低）",
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

// LLM 是 OpenAI 兼容的聊天客户端。
type LLM struct {
	key    string
	base   string
	model  string
	client *http.Client
}

func NewLLM(key, base, model string) *LLM {
	if base == "" {
		base = "https://api.deepseek.com/v1"
	}
	if model == "" {
		model = "deepseek-v4-flash"
	}
	return &LLM{key: key, base: base, model: model, client: &http.Client{}}
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

// chat 发送一轮对话，返回助手文本。
func (l *LLM) chat(ctx context.Context, system, user string) (string, error) {
	body := chatReq{
		Model:    l.model,
		Messages: []chatMsg{{Role: "system", Content: system}, {Role: "user", Content: user}},
	}
	body.Format.Type = "json_object"

	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.base+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+l.key)

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

// CompleteJSON 执行一次 JSON 约束的对话，供编排器复用。
func (l *LLM) CompleteJSON(ctx context.Context, system, user string) (string, error) {
	return l.chat(ctx, system, user)
}

// Review 一次性审查 diff，返回 findings。
func (l *LLM) Review(ctx context.Context, diffText string) ([]Finding, error) {
	text, err := l.chat(ctx, reviewPrompt, diffText)
	if err != nil {
		return nil, err
	}
	return parseFindings(text)
}

// Plan 规划审查要点。
func (l *LLM) Plan(ctx context.Context, diffText string) ([]Point, error) {
	text, err := l.chat(ctx, planPrompt, diffText)
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
	text, err := l.chat(ctx, sys, user)
	if err != nil {
		return nil, err
	}
	return parseFindings(text)
}

// Check 复核一条 finding 是否真实，返回是否保留。
func (l *LLM) Check(ctx context.Context, f Finding, code string) (bool, error) {
	sys := `你是代码审查复核员。给出一条 finding 与它所在位置的真实代码，判断它是否是必须修复的真 bug。
判定为 drop（误报）：风格/可读性建议、代码其实已正确处理、无法从代码证实、与本次 diff 无关。
判定为 keep（真 bug）：会导致 panic、数据错误、并发错误、资源泄漏等真实缺陷。
只输出 JSON：{"verdict":"keep|drop","reason":"一句话理由"}`
	user := fmt.Sprintf("finding: [%s] %s\n声称证据:\n%s\n位置代码:\n%s", f.Severity, f.Msg, f.Evidence, code)
	text, err := l.chat(ctx, sys, user)
	if err != nil {
		return false, err
	}
	var out struct {
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal([]byte(stripFence(text)), &out); err != nil {
		return false, err
	}
	return out.Verdict == "keep", nil
}

// ValidationResult 是 ValidateFinding 的返回结果。
type ValidationResult struct {
	Confidence float64  `json:"confidence"`
	Evidence   string   `json:"evidence"`
	Gaps       []string `json:"gaps"`
}

// ValidateFinding 对单个 finding 进行证据校验，返回置信度评分。
func (l *LLM) ValidateFinding(ctx context.Context, f Finding, evidence interface{}) (ValidationResult, error) {
	// 将 evidence 格式化为字符串
	evidenceText := fmt.Sprintf("%+v", evidence)

	sys := fmt.Sprintf(validatePrompt, f.File, f.Line, f.Severity, f.Msg, f.Evidence, evidenceText)
	text, err := l.chat(ctx, sys, "请评估上述 finding 的置信度。")
	if err != nil {
		return ValidationResult{}, err
	}

	var result ValidationResult
	if err := json.Unmarshal([]byte(stripFence(text)), &result); err != nil {
		return ValidationResult{}, fmt.Errorf("解析 validation 结果: %w", err)
	}

	return result, nil
}

// CritiqueResult 是 GenerateCritique 的返回结果。
type CritiqueResult struct {
	Reason     string `json:"reason"`
	Evidence   string `json:"evidence"`
	Suggestion string `json:"suggestion"`
}

// ValidationInput 是传给 GenerateCritique 的 Validation 信息。
type ValidationInput struct {
	Confidence float64
	Evidence   string
	Gaps       []string
}

// GenerateCritique 为低置信度的 finding 生成反驳理由 + 改进建议。
func (l *LLM) GenerateCritique(ctx context.Context, f Finding, val ValidationInput) (CritiqueResult, error) {
	gapsText := strings.Join(val.Gaps, "; ")
	sys := fmt.Sprintf(critiquePrompt, f.File, f.Line, f.Severity, f.Msg, f.Evidence,
		val.Confidence, val.Evidence, gapsText)

	text, err := l.chat(ctx, sys, "请生成 Critique。")
	if err != nil {
		return CritiqueResult{}, err
	}

	var result CritiqueResult
	if err := json.Unmarshal([]byte(stripFence(text)), &result); err != nil {
		return CritiqueResult{}, fmt.Errorf("解析 critique 结果: %w", err)
	}

	return result, nil
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
