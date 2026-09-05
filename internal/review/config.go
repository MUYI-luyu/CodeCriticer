package review

import (
	"fmt"
	"strings"
)

// Config 存储 LLM 配置，支持分级模型。
type Config struct {
	APIKey  string
	BaseURL string

	// 分级模型：不同任务使用不同能力的模型
	PlanModel   string // 规划任务（便宜模型即可）
	ReviewModel string // 审查任务（需要强模型）
}

// DefaultConfig 返回默认配置。
func DefaultConfig() *Config {
	return &Config{
		// CodeCritic_URL is required because Claude may be reached through an
		// OpenAI-compatible gateway; the native Anthropic endpoint is not the
		// same protocol as this client.
		BaseURL:     "",
		PlanModel:   "gpt-5.4",
		ReviewModel: "gpt-5.4",
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

// WithPlanModel 设置规划模型。
func WithPlanModel(model string) Option {
	return func(c *Config) { c.PlanModel = model }
}

// WithReviewModel 设置审查模型。
func WithReviewModel(model string) Option {
	return func(c *Config) { c.ReviewModel = model }
}

// InvestigatorModel 返回调查阶段使用的模型。
func (l *LLM) InvestigatorModel() string {
	return l.config.ReviewModel
}
