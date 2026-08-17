package review

// Finding 是一条审查意见，是规则引擎与 LLM 共享的数据模型。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Symbol   string `json:"symbol,omitempty"`
	Severity string `json:"severity"` // error / warning / info
	Msg      string `json:"msg"`
	Evidence string `json:"evidence,omitempty"`
}
