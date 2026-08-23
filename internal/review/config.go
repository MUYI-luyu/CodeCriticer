package review

// Config 存储 LLM 配置，支持分级模型。
type Config struct {
	APIKey  string
	BaseURL string

	// 分级模型：不同任务使用不同能力的模型
	PlanModel    string // 规划任务（便宜模型即可）
	ReviewModel  string // 审查任务（需要强模型）
	ReflectModel string // 反思任务（需要强模型）
}

// DefaultConfig 返回默认配置。
func DefaultConfig() *Config {
	return &Config{
		BaseURL:      "https://api.deepseek.com/v1",
		PlanModel:    "deepseek-chat",
		ReviewModel:  "deepseek-chat",
		ReflectModel: "deepseek-chat",
	}
}

// Option 是配置选项函数。
type Option func(*Config)

// WithAPIKey 设置 API Key。
func WithAPIKey(key string) Option {
	return func(c *Config) { c.APIKey = key }
}

// WithBaseURL 设置 API 基础 URL。
func WithBaseURL(url string) Option {
	return func(c *Config) { c.BaseURL = url }
}

// WithPlanModel 设置规划模型。
func WithPlanModel(model string) Option {
	return func(c *Config) { c.PlanModel = model }
}

// WithReviewModel 设置审查模型。
func WithReviewModel(model string) Option {
	return func(c *Config) { c.ReviewModel = model }
}

// WithReflectModel 设置反思模型。
func WithReflectModel(model string) Option {
	return func(c *Config) { c.ReflectModel = model }
}
