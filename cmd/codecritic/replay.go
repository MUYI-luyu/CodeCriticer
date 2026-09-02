package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

func cmdReplay(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "错误: 需要指定 trace 文件")
		usage()
		os.Exit(1)
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取失败: %v\n", err)
		os.Exit(1)
	}
	var trace workflow.Trace
	if err := json.Unmarshal(data, &trace); err != nil {
		fmt.Fprintf(os.Stderr, "解析失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Trace: %s\nStopReason: %s\nDuration: %v\n", trace.ID, trace.StopReason, trace.Duration)
	fmt.Printf("Plan:\n  files: %s\n  symbols: %s\n  questions: %s\n  keywords: %s\n", strings.Join(trace.Plan.TargetFiles, ", "), strings.Join(trace.Plan.Symbols, ", "), strings.Join(trace.Plan.Questions, " | "), strings.Join(trace.Plan.Keywords, ", "))
	fmt.Printf("ToolCalls: %d\n", len(trace.ToolCalls))
	for i, c := range trace.ToolCalls {
		fmt.Printf("  %d. %s args=%v evidence=%v error=%s\n", i+1, c.Tool, c.Args, c.EvidenceIDs, c.Error)
	}
	fmt.Printf("Evidence: %d\n", len(trace.Evidence))
	for _, e := range trace.Evidence {
		fmt.Printf("  %s %s:%d [%s]\n", e.ID, e.File, e.Line, e.Type)
	}
	fmt.Printf("Findings: %d\n", len(trace.Findings))
	for i, f := range trace.Findings {
		fmt.Printf("  %d. [%s] %s:%d %s\n", i+1, f.Severity, f.File, f.Line, f.Msg)
	}
	fmt.Printf("Validations: %d\n", len(trace.Validations))
	for _, v := range trace.Validations {
		fmt.Printf("  %d. accepted=%v confidence=%.2f reason=%s\n", v.FindingIndex+1, v.Accepted, v.Confidence, v.Reason)
	}
	fmt.Printf("LLMCalls: %d tokens=%d (prompt=%d completion=%d)\n", len(trace.LLMCalls), trace.Usage.TotalTokens, trace.Usage.PromptTokens, trace.Usage.CompletionTokens)
}
