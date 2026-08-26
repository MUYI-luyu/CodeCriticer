package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/agent"
)

func cmdReplay(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "错误: 需要指定 trace 文件")
		usage()
		os.Exit(1)
	}

	tracePath := args[0]
	var outputPath string
	var comparePath string

	// 解析参数
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "--output" && i+1 < len(args):
			outputPath, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--output="):
			outputPath = strings.TrimPrefix(args[i], "--output=")
		case args[i] == "--compare" && i+1 < len(args):
			comparePath, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--compare="):
			comparePath = strings.TrimPrefix(args[i], "--compare=")
		}
	}

	// 对比模式
	if comparePath != "" {
		if err := runCompare(tracePath, comparePath); err != nil {
			fmt.Fprintf(os.Stderr, "对比失败: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// 单个 trace 分析
	analysis, err := agent.AnalyzeTrace(tracePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "分析失败: %v\n", err)
		os.Exit(1)
	}

	// 打印分析结果
	fmt.Print(agent.FormatAnalysis(analysis))

	// 保存到文件（可选）
	if outputPath != "" {
		if err := agent.SaveAnalysis(analysis, outputPath); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 保存分析结果失败: %v\n", err)
		} else {
			fmt.Printf("\n分析结果已保存: %s\n", outputPath)
		}
	}
}

func runCompare(trace1Path, trace2Path string) error {
	analyses, err := agent.CompareTraces([]string{trace1Path, trace2Path})
	if err != nil {
		return err
	}

	if len(analyses) != 2 {
		return fmt.Errorf("对比需要 2 个 trace，实际得到 %d 个", len(analyses))
	}

	fmt.Println("=== Trace 对比 ===")
	fmt.Println()

	// Trace 1
	fmt.Printf("Trace 1: %s\n", analyses[0].TraceID)
	fmt.Printf("  轮次: %d\n", analyses[0].TotalRounds)
	fmt.Printf("  收敛: %v (%s)\n", analyses[0].Convergence.Converged, analyses[0].Convergence.Reason)
	fmt.Printf("  耗时: %v\n", analyses[0].Performance.TotalDuration)
	fmt.Printf("  最终 findings: %d (高置信度: %d)\n",
		analyses[0].Quality.FinalFindingsCount,
		analyses[0].Quality.HighConfidenceCount)
	if analyses[0].ToolUsage.TotalCalls > 0 {
		fmt.Printf("  工具调用: %d 次\n", analyses[0].ToolUsage.TotalCalls)
	}
	fmt.Println()

	// Trace 2
	fmt.Printf("Trace 2: %s\n", analyses[1].TraceID)
	fmt.Printf("  轮次: %d\n", analyses[1].TotalRounds)
	fmt.Printf("  收敛: %v (%s)\n", analyses[1].Convergence.Converged, analyses[1].Convergence.Reason)
	fmt.Printf("  耗时: %v\n", analyses[1].Performance.TotalDuration)
	fmt.Printf("  最终 findings: %d (高置信度: %d)\n",
		analyses[1].Quality.FinalFindingsCount,
		analyses[1].Quality.HighConfidenceCount)
	if analyses[1].ToolUsage.TotalCalls > 0 {
		fmt.Printf("  工具调用: %d 次\n", analyses[1].ToolUsage.TotalCalls)
	}
	fmt.Println()

	// 差异对比
	fmt.Println("=== 差异分析 ===")
	roundsDiff := analyses[1].TotalRounds - analyses[0].TotalRounds
	if roundsDiff != 0 {
		fmt.Printf("轮次差异: %+d\n", roundsDiff)
	}

	timeDiff := analyses[1].Performance.TotalDuration - analyses[0].Performance.TotalDuration
	if timeDiff != 0 {
		fmt.Printf("耗时差异: %+v\n", timeDiff)
	}

	findingsDiff := analyses[1].Quality.FinalFindingsCount - analyses[0].Quality.FinalFindingsCount
	if findingsDiff != 0 {
		fmt.Printf("Findings 差异: %+d\n", findingsDiff)
	}

	confDiff := analyses[1].Quality.HighConfidenceCount - analyses[0].Quality.HighConfidenceCount
	if confDiff != 0 {
		fmt.Printf("高置信度差异: %+d\n", confDiff)
	}

	if analyses[0].ToolUsage.TotalCalls > 0 || analyses[1].ToolUsage.TotalCalls > 0 {
		toolDiff := analyses[1].ToolUsage.TotalCalls - analyses[0].ToolUsage.TotalCalls
		fmt.Printf("工具调用差异: %+d\n", toolDiff)
	}

	return nil
}
