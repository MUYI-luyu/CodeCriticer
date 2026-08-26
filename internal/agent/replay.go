package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ReplayAnalysis 是 trace 回放的分析结果。
type ReplayAnalysis struct {
	TraceID     string
	TotalRounds int
	Convergence ConvergenceInfo
	Performance PerformanceInfo
	Quality     QualityInfo
	ToolUsage   ToolUsageInfo
}

// ConvergenceInfo 收敛信息。
type ConvergenceInfo struct {
	Converged        bool
	Reason           string
	RoundsToConverge int
}

// PerformanceInfo 性能信息。
type PerformanceInfo struct {
	TotalDuration time.Duration
	AvgRoundTime  time.Duration
	FastestRound  time.Duration
	SlowestRound  time.Duration
}

// QualityInfo 质量信息。
type QualityInfo struct {
	FindingsPerRound      []int
	AvgConfidencePerRound []float64
	FinalFindingsCount    int
	HighConfidenceCount   int
	LowConfidenceCount    int
}

// ToolUsageInfo 工具使用统计（orchestration 模式）。
type ToolUsageInfo struct {
	TotalCalls       int
	ToolCallCounts   map[string]int // 工具名 -> 调用次数
	AvgCallsPerRound float64
}

// AnalyzeTrace 分析一个 trace 文件，返回详细分析结果。
func AnalyzeTrace(tracePath string) (*ReplayAnalysis, error) {
	trace, err := Load(tracePath)
	if err != nil {
		return nil, fmt.Errorf("加载 trace: %w", err)
	}

	if trace.Result == nil {
		return nil, fmt.Errorf("trace 没有结果")
	}

	analysis := &ReplayAnalysis{
		TraceID:     trace.ID,
		TotalRounds: len(trace.Result.Attempts),
	}

	// 1. 收敛分析
	analysis.Convergence = ConvergenceInfo{
		Converged:        trace.Result.Converged,
		Reason:           trace.Result.Reason,
		RoundsToConverge: len(trace.Result.Attempts),
	}

	// 2. 性能分析
	if len(trace.Result.Attempts) > 0 {
		var totalDuration time.Duration
		var fastest, slowest time.Duration
		fastest = trace.Result.Attempts[0].Duration
		slowest = trace.Result.Attempts[0].Duration

		for _, att := range trace.Result.Attempts {
			totalDuration += att.Duration
			if att.Duration < fastest {
				fastest = att.Duration
			}
			if att.Duration > slowest {
				slowest = att.Duration
			}
		}

		analysis.Performance = PerformanceInfo{
			TotalDuration: totalDuration,
			AvgRoundTime:  totalDuration / time.Duration(len(trace.Result.Attempts)),
			FastestRound:  fastest,
			SlowestRound:  slowest,
		}
	}

	// 3. 质量分析
	highConf := 0
	lowConf := 0
	var findingsPerRound []int
	var avgConfPerRound []float64

	for _, att := range trace.Result.Attempts {
		findingsPerRound = append(findingsPerRound, len(att.Findings))

		if len(att.Validations) > 0 {
			var sum float64
			for _, v := range att.Validations {
				sum += v.Confidence
				if v.Confidence >= ConfidenceThreshold {
					highConf++
				} else {
					lowConf++
				}
			}
			avgConfPerRound = append(avgConfPerRound, sum/float64(len(att.Validations)))
		}
	}

	analysis.Quality = QualityInfo{
		FindingsPerRound:      findingsPerRound,
		AvgConfidencePerRound: avgConfPerRound,
		FinalFindingsCount:    len(trace.Result.FinalFindings),
		HighConfidenceCount:   highConf,
		LowConfidenceCount:    lowConf,
	}

	// 4. 工具使用分析（orchestration 模式）
	toolCounts := make(map[string]int)
	totalCalls := 0

	for _, att := range trace.Result.Attempts {
		for _, tc := range att.ToolCalls {
			toolCounts[tc.Tool]++
			totalCalls++
		}
	}

	avgCallsPerRound := 0.0
	if len(trace.Result.Attempts) > 0 {
		avgCallsPerRound = float64(totalCalls) / float64(len(trace.Result.Attempts))
	}

	analysis.ToolUsage = ToolUsageInfo{
		TotalCalls:       totalCalls,
		ToolCallCounts:   toolCounts,
		AvgCallsPerRound: avgCallsPerRound,
	}

	return analysis, nil
}

// CompareTraces 对比多个 trace 的执行情况。
func CompareTraces(tracePaths []string) ([]ReplayAnalysis, error) {
	var analyses []ReplayAnalysis

	for _, path := range tracePaths {
		analysis, err := AnalyzeTrace(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		analyses = append(analyses, *analysis)
	}

	return analyses, nil
}

// FormatAnalysis 格式化分析结果为人类可读的文本。
func FormatAnalysis(analysis *ReplayAnalysis) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("=== Trace Analysis: %s ===\n\n", analysis.TraceID))

	// 收敛信息
	sb.WriteString("收敛信息:\n")
	sb.WriteString(fmt.Sprintf("  收敛: %v\n", analysis.Convergence.Converged))
	sb.WriteString(fmt.Sprintf("  原因: %s\n", analysis.Convergence.Reason))
	sb.WriteString(fmt.Sprintf("  轮次: %d\n", analysis.Convergence.RoundsToConverge))
	sb.WriteString("\n")

	// 性能信息
	sb.WriteString("性能信息:\n")
	sb.WriteString(fmt.Sprintf("  总耗时: %v\n", analysis.Performance.TotalDuration))
	sb.WriteString(fmt.Sprintf("  平均每轮: %v\n", analysis.Performance.AvgRoundTime))
	sb.WriteString(fmt.Sprintf("  最快轮次: %v\n", analysis.Performance.FastestRound))
	sb.WriteString(fmt.Sprintf("  最慢轮次: %v\n", analysis.Performance.SlowestRound))
	sb.WriteString("\n")

	// 质量信息
	sb.WriteString("质量信息:\n")
	sb.WriteString(fmt.Sprintf("  每轮 findings: %v\n", analysis.Quality.FindingsPerRound))
	sb.WriteString(fmt.Sprintf("  每轮平均置信度: %.2f\n", avgFloat(analysis.Quality.AvgConfidencePerRound)))
	sb.WriteString(fmt.Sprintf("  最终 findings: %d\n", analysis.Quality.FinalFindingsCount))
	sb.WriteString(fmt.Sprintf("  高置信度: %d\n", analysis.Quality.HighConfidenceCount))
	sb.WriteString(fmt.Sprintf("  低置信度: %d\n", analysis.Quality.LowConfidenceCount))
	sb.WriteString("\n")

	// 工具使用（仅 orchestration 模式）
	if analysis.ToolUsage.TotalCalls > 0 {
		sb.WriteString("工具使用:\n")
		sb.WriteString(fmt.Sprintf("  总调用次数: %d\n", analysis.ToolUsage.TotalCalls))
		sb.WriteString(fmt.Sprintf("  平均每轮: %.1f\n", analysis.ToolUsage.AvgCallsPerRound))
		sb.WriteString("  工具分布:\n")

		// 按调用次数排序
		type toolCount struct {
			name  string
			count int
		}
		var tools []toolCount
		for name, count := range analysis.ToolUsage.ToolCallCounts {
			tools = append(tools, toolCount{name, count})
		}
		sort.Slice(tools, func(i, j int) bool {
			return tools[i].count > tools[j].count
		})

		for _, tc := range tools {
			pct := float64(tc.count) / float64(analysis.ToolUsage.TotalCalls) * 100
			sb.WriteString(fmt.Sprintf("    %-20s %3d (%.1f%%)\n", tc.name, tc.count, pct))
		}
	}

	return sb.String()
}

// SaveAnalysis 保存分析结果到 JSON 文件。
func SaveAnalysis(analysis *ReplayAnalysis, outputPath string) error {
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	data, err := json.MarshalIndent(analysis, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化分析结果失败: %w", err)
	}

	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}

	return nil
}

func avgFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}
