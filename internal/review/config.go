package review

import (
	"fmt"
	"strings"
)

// Config 存储 Agent 主模型和可选降级模型。
type Config struct {
	APIKey  string
	BaseURL string
	Model   string
}

// DefaultConfig 返回默认配置。
func DefaultConfig() *Config {
	return &Config{
		// CodeCritic_URL is required because Claude may be reached through an
		// OpenAI-compatible gateway; the native Anthropic endpoint is not the
		// same protocol as this client.
		BaseURL: "",
		Model:   "gpt-5.4",
	}
}

// Option 是配置选项函数。
type Option func(*Config)

func (c *Config) Validate() error {
	if strings.TrimSpace(c.APIKey) == "" {
		return fmt.Errorf("missing API key")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return fmt.Errorf("missing CodeCritic_URL (OpenAI-compatible base URL)")
	}
	return nil
}

// WithAPIKey 设置 API Key。
func WithAPIKey(key string) Option {
	return func(c *Config) { c.APIKey = key }
}

// WithBaseURL 设置 API 基础 URL。
func WithBaseURL(url string) Option {
	return func(c *Config) { c.BaseURL = strings.TrimRight(url, "/") }
}

// WithModel 设置 Agent 主模型。
func WithModel(model string) Option {
	return func(c *Config) { c.Model = model }
}

// AgentModel 返回调查阶段使用的主模型。
func (l *LLM) AgentModel() string {
	return l.config.Model
}
