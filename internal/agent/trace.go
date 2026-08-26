package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Trace 是完整的审查轨迹，用于持久化和回放。
// 包含输入（diff + repo）、过程（attempts）、输出（result）。
type Trace struct {
	ID        string    `json:"id"`     // 唯一标识（用于回放）
	Diff      string    `json:"diff"`   // diff 内容或路径
	Repo      string    `json:"repo"`   // 仓库路径
	Result    *Result   `json:"result"` // 审查结果
	CreatedAt time.Time `json:"created_at"`
}

// Save 保存 trace 到磁盘。
// 格式为 JSON，人类可读，支持 jq 查询。
func (t *Trace) Save(path string) error {
	// 确保目录存在
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 格式化输出（缩进 2 空格）
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 trace 失败: %w", err)
	}

	// 写入文件
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}

	return nil
}

// Load 从磁盘加载 trace。
func Load(path string) (*Trace, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败: %w", err)
	}

	var trace Trace
	if err := json.Unmarshal(data, &trace); err != nil {
		return nil, fmt.Errorf("解析 trace 失败: %w", err)
	}

	return &trace, nil
}

// Replay 从 trace 中提取第 round 轮的 findings（用于评测复现）。
// round 从 1 开始。
func (t *Trace) Replay(round int) []Attempt {
	if t.Result == nil || round <= 0 || round > len(t.Result.Attempts) {
		return nil
	}
	// 返回前 round 轮的所有 attempts
	return t.Result.Attempts[:round]
}

// LastAttempt 返回最后一轮的 attempt（最常用）。
func (t *Trace) LastAttempt() *Attempt {
	if t.Result == nil || len(t.Result.Attempts) == 0 {
		return nil
	}
	last := t.Result.Attempts[len(t.Result.Attempts)-1]
	return &last
}

// HighConfidenceFindings 返回所有高可信度的 findings（从最后一轮）。
func (t *Trace) HighConfidenceFindings() []FindingWithConfidence {
	last := t.LastAttempt()
	if last == nil {
		return nil
	}

	var out []FindingWithConfidence
	for i, f := range last.Findings {
		if i < len(last.Validations) && last.Validations[i].Confidence >= ConfidenceThreshold {
			out = append(out, FindingWithConfidence{
				Finding:    f,
				Confidence: last.Validations[i].Confidence,
			})
		}
	}
	return out
}

// FindingWithConfidence 是带置信度的 finding（用于分析）。
type FindingWithConfidence struct {
	Finding    interface{} `json:"finding"` // review.Finding
	Confidence float64     `json:"confidence"`
}

// Stats 返回轨迹的统计信息（用于评测报告）。
func (t *Trace) Stats() TraceStats {
	if t.Result == nil {
		return TraceStats{}
	}

	stats := TraceStats{
		TotalAttempts:  len(t.Result.Attempts),
		Converged:      t.Result.Converged,
		ConvergeReason: t.Result.Reason,
		TotalDuration:  t.Result.TotalDuration,
	}

	// 统计每轮的 findings 数量和平均 confidence
	for _, att := range t.Result.Attempts {
		stats.FindingsPerRound = append(stats.FindingsPerRound, len(att.Findings))

		if len(att.Validations) > 0 {
			var sum float64
			for _, v := range att.Validations {
				sum += v.Confidence
			}
			stats.AvgConfidencePerRound = append(stats.AvgConfidencePerRound, sum/float64(len(att.Validations)))
		}
	}

	// 统计最终 findings
	stats.FinalFindings = len(t.Result.FinalFindings)

	return stats
}

// TraceStats 是轨迹统计信息。
type TraceStats struct {
	TotalAttempts         int           `json:"total_attempts"`
	Converged             bool          `json:"converged"`
	ConvergeReason        string        `json:"converge_reason"`
	TotalDuration         time.Duration `json:"total_duration"`
	FindingsPerRound      []int         `json:"findings_per_round"`
	AvgConfidencePerRound []float64     `json:"avg_confidence_per_round"`
	FinalFindings         int           `json:"final_findings"`
}
