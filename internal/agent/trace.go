package agent

import (
	"encoding/json"
	"os"
)

// Trace 是审查轨迹的持久化格式。
type Trace struct {
	ID        string   `json:"id"`
	Diff      string   `json:"diff"`       // diff 文件路径
	Repo      string   `json:"repo"`       // 仓库路径
	Attempts  []Attempt `json:"attempts"`
	CreatedAt string   `json:"created_at"` // RFC3339
}

// Save 保存 trace 到磁盘。
func (t *Trace) Save(path string) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Load 从磁盘加载 trace。
func LoadTrace(path string) (*Trace, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var trace Trace
	if err := json.Unmarshal(data, &trace); err != nil {
		return nil, err
	}
	return &trace, nil
}
