package main

import (
	"encoding/json"
	"fmt"
	"os"

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
	fmt.Printf("Scope: files=%v symbols=%v\n", trace.Scope.TargetFiles, trace.Scope.Symbols)
	fmt.Printf("InvestigationSteps: %d\n", len(trace.Investigation))
	for i, c := range trace.Investigation {
		fmt.Printf("  %d. %s args=%v error=%s\n", i+1, c.Tool, c.Args, c.Error)
	}
	fmt.Printf("Evidence: %d\n", len(trace.Evidence))
	for _, e := range trace.Evidence {
		fmt.Printf("  %s %s:%d [%s]\n", e.ID, e.File, e.Line, e.Type)
	}
	accepted := trace.FinalReport.ClaimsWithStatus(workflow.VerdictAccepted)
	unresolved := trace.FinalReport.ClaimsWithStatus(workflow.VerdictUnresolved)
	fmt.Printf("Claims: %d (accepted: %d, unresolved: %d)\n", len(trace.FinalReport.Claims), len(accepted), len(unresolved))
	for i, f := range accepted {
		fmt.Printf("  %d. [%s] %s:%d %s\n", i+1, f.Severity, f.File, f.Line, f.Msg)
	}
	fmt.Printf("Verdicts: %d\n", len(trace.FinalReport.Verdicts))
	for _, v := range trace.FinalReport.Verdicts {
		fmt.Printf("  %s status=%s reason=%s\n", v.ClaimID, v.Status, v.Reason)
	}
	fmt.Printf("LLMCalls: %d tokens=%d (prompt=%d completion=%d)\n", len(trace.LLMCalls), trace.Usage.TotalTokens, trace.Usage.PromptTokens, trace.Usage.CompletionTokens)
}
