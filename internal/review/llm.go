package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Config 返回 LLM 的配置（公开给 agent 包使用）。
func (l *LLM) Config() *Config {
	return l.config
}

// LLM 是 OpenAI 兼容的聊天客户端。
type LLM struct {
	config   *Config
	client   *http.Client
	metrics  *Metrics
	observer LLMObserver
}

const llmRequestTimeout = 120 * time.Second

// LLMCall describes one completed HTTP attempt. It is intentionally a callback
// so callers can route it to slog, a trace, or both without coupling this
// package to a logging backend.
type LLMCall struct {
	TraceID      string `json:"trace_id,omitempty"`
	Stage        string `json:"stage,omitempty"`
	Model        string
	SystemPrompt string
	UserPrompt   string
	Response     string
	Usage        LLMUsage
	Attempt      int
	Status       int
	Duration     time.Duration
	RequestBytes int `json:"request_bytes,omitempty"`
	Error        string
}

type LLMObserver interface{ OnLLMCall(LLMCall) }

type observerContextKey struct{}

// WithLLMObserver scopes observation to a single invocation.
func WithLLMObserver(ctx context.Context, observer LLMObserver) context.Context {
	return context.WithValue(ctx, observerContextKey{}, observer)
}

func observerFromContext(ctx context.Context) LLMObserver {
	if observer, ok := ctx.Value(observerContextKey{}).(LLMObserver); ok {
		return observer
	}
	return nil
}

// SetObserver attaches an optional observer for request/response tracing.
func (l *LLM) SetObserver(observer LLMObserver) { l.observer = observer }

// NewLLMWithConfig 创建 LLM 客户端，使用自定义配置。
func NewLLMWithConfig(opts ...Option) *LLM {
	cfg := DefaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	return &LLM{
		config:  cfg,
		client:  &http.Client{Timeout: llmRequestTimeout},
		metrics: newMetrics(),
	}
}

type chatMsg struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type chatReq struct {
	Model             string           `json:"model"`
	Messages          []chatMsg        `json:"messages"`
	Tools             []ToolDefinition `json:"tools,omitempty"`
	ToolChoice        string           `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool            `json:"parallel_tool_calls,omitempty"`
}

type chatResp struct {
	Choices []struct {
		Message chatMsg `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// ToolCall and ToolDefinition mirror the OpenAI-compatible function-calling
// wire format. They are transport records, not a second agent protocol.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolDefinition struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Strict      bool           `json:"strict"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolMessage is one message in a native function-calling conversation.
type ToolMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// LLMUsage 是一次 LLM 调用的 token 用量（公开给 agent/eval 包使用）。
type LLMUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// CompleteToolsWithUsage performs one native tool-calling model turn. The
// caller owns the loop because it also owns tool safety, Evidence and Verdict.
func (l *LLM) CompleteToolsWithUsage(ctx context.Context, system string, messages []ToolMessage, tools []ToolDefinition, model string) (ToolMessage, LLMUsage, error) {
	wire := make([]chatMsg, 0, len(messages)+1)
	wire = append(wire, chatMsg{Role: "system", Content: system})
	for _, message := range messages {
		wire = append(wire, chatMsg{Role: message.Role, Content: message.Content, ToolCalls: message.ToolCalls, ToolCallID: message.ToolCallID})
	}
	message, totalUsage, err := l.completeWithUsage(ctx, system, encodeMessages(wire), model, wire, tools)
	return ToolMessage{Role: message.Role, Content: message.Content, ToolCalls: message.ToolCalls, ToolCallID: message.ToolCallID}, totalUsage, err
}

func (l *LLM) completeWithUsage(ctx context.Context, system, user, model string, messages []chatMsg, tools []ToolDefinition) (chatMsg, LLMUsage, error) {
	const maxRetries = 3
	var lastErr error
	var totalUsage LLMUsage
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			l.metrics.recordRetry(model)
			select {
			case <-ctx.Done():
				return chatMsg{}, totalUsage, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}

		message, usage, err := l.completeOnce(ctx, system, user, model, messages, tools, attempt+1)
		if err == nil {
			totalUsage.PromptTokens += usage.Input
			totalUsage.CompletionTokens += usage.Output
			totalUsage.TotalTokens = totalUsage.PromptTokens + totalUsage.CompletionTokens
			l.metrics.recordSuccess(model, usage)
			return message, totalUsage, nil
		}
		lastErr = err
		if !retryable(err) {
			break
		}
	}
	l.metrics.recordFail(model)
	return chatMsg{}, totalUsage, lastErr
}

func (l *LLM) completeOnce(ctx context.Context, system, user, model string, messages []chatMsg, tools []ToolDefinition, attempt int) (chatMsg, usage, error) {
	started := time.Now()
	requestBytes := 0
	observer := observerFromContext(ctx)
	if observer == nil {
		observer = l.observer
	}
	if err := l.config.Validate(); err != nil {
		if observer != nil {
			observer.OnLLMCall(LLMCall{Model: model, SystemPrompt: system, UserPrompt: user, Attempt: attempt, Duration: time.Since(started), Error: err.Error()})
		}
		return chatMsg{}, usage{}, err
	}
	record := func(response string, u usage, status int, err error) {
		if observer == nil {
			return
		}
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		observer.OnLLMCall(LLMCall{Model: model, SystemPrompt: system, UserPrompt: user, RequestBytes: requestBytes,
			Response: response, Usage: LLMUsage{PromptTokens: u.Input, CompletionTokens: u.Output, TotalTokens: u.Input + u.Output},
			Attempt: attempt, Status: status, Duration: time.Since(started), Error: msg})
	}
	body := chatReq{Model: model, Messages: messages, Tools: tools}
	if len(tools) > 0 {
		parallel := false
		body.ToolChoice = "required"
		body.ParallelToolCalls = &parallel
	}

	raw, err := json.Marshal(body)
	if err != nil {
		record("", usage{}, 0, err)
		return chatMsg{}, usage{}, err
	}
	requestBytes = len(raw)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.config.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		record("", usage{}, 0, err)
		return chatMsg{}, usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+l.config.APIKey)

	resp, err := l.client.Do(req)
	if err != nil {
		record("", usage{}, 0, err)
		return chatMsg{}, usage{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		err := &llmError{status: resp.StatusCode, body: string(b)}
		record(string(b), usage{}, resp.StatusCode, err)
		return chatMsg{}, usage{}, err
	}
	var out chatResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		decodeErr := fmt.Errorf("llm: 响应不是合法 JSON (content-type=%q): %w", resp.Header.Get("Content-Type"), err)
		record("", usage{}, resp.StatusCode, decodeErr)
		return chatMsg{}, usage{}, decodeErr
	}
	if len(out.Choices) == 0 {
		err := fmt.Errorf("llm: 空响应")
		record("", usage{}, resp.StatusCode, err)
		return chatMsg{}, usage{}, err
	}
	u := usage{
		Input:  out.Usage.PromptTokens,
		Output: out.Usage.CompletionTokens,
	}
	response, _ := json.Marshal(out.Choices[0].Message)
	record(string(response), u, resp.StatusCode, nil)
	return out.Choices[0].Message, u, nil
}

func encodeMessages(messages []chatMsg) string {
	b, _ := json.Marshal(messages)
	return string(b)
}

// backoff 返回第 attempt 次重试的退避时间（1s/2s/4s）。
func backoff(attempt int) time.Duration {
	return time.Duration(1<<(attempt-1)) * time.Second
}

// retryable 判断错误是否值得重试：429 限流、5xx 服务端错误、网络错误。
func retryable(err error) bool {
	if err == nil {
		return false
	}
	var le *llmError
	if errors.As(err, &le) {
		return le.status == http.StatusTooManyRequests || le.status >= 500
	}
	// 非 llmError 通常是网络错误（连接失败、超时等），可重试
	return true
}

// usage 是一次 LLM 调用的 token 用量。
type usage struct {
	Input  int
	Output int
}

// llmError 是带 HTTP 状态码的 LLM 错误，用于判断是否可重试。
type llmError struct {
	status int
	body   string
}

func (e *llmError) Error() string {
	return fmt.Sprintf("llm: %d: %s", e.status, e.body)
}

// ModelStat 是单个模型的调用统计。
type ModelStat struct {
	Calls        int
	Success      int
	Fail         int
	Retries      int
	InputTokens  int
	OutputTokens int
}

// Metrics 记录 LLM 调用统计，按模型分组，用于可观测性。
type Metrics struct {
	mu      sync.Mutex
	byModel map[string]*ModelStat
}

func newMetrics() *Metrics {
	return &Metrics{byModel: make(map[string]*ModelStat)}
}

func (m *Metrics) recordSuccess(model string, u usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stat(model)
	s.Calls++
	s.Success++
	s.InputTokens += u.Input
	s.OutputTokens += u.Output
}

func (m *Metrics) recordFail(model string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stat(model)
	s.Calls++
	s.Fail++
}

func (m *Metrics) recordRetry(model string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stat(model)
	s.Retries++
}

func (m *Metrics) stat(model string) *ModelStat {
	s, ok := m.byModel[model]
	if !ok {
		s = &ModelStat{}
		m.byModel[model] = s
	}
	return s
}

// Snapshot 返回指标快照（拷贝，避免并发读写）。
func (m *Metrics) Snapshot() map[string]ModelStat {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]ModelStat, len(m.byModel))
	for k, v := range m.byModel {
		out[k] = *v
	}
	return out
}

// Metrics 返回 LLM 的调用统计快照。
func (l *LLM) Metrics() map[string]ModelStat {
	return l.metrics.Snapshot()
}
