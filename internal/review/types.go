package review

// Finding 是一条审查意见。
type Finding struct {
	File     string `json:"file"`               // 哪个文件
	Line     int    `json:"line"`               // 哪一行（0 表示未定位）
	Symbol   string `json:"symbol,omitempty"`   // 命中的规则名/符号
	Severity string `json:"severity"`           // error / warning / info
	Msg      string `json:"msg"`                // 问题描述
	Evidence string `json:"evidence,omitempty"` // 证据片段
}
