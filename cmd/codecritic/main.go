package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/MUYI-luyu/codecritic/internal/eval"
	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "review":
		cmdReview(os.Args[2:])
	case "reviewbench":
		cmdReviewBench(os.Args[2:])
	case "mcp":
		cmdMCP(os.Args[2:])
	case "eval":
		cmdEval(os.Args[2:])
	case "rescore":
		cmdRescore(os.Args[2:])
	case "replay":
		cmdReplay(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法:")
	fmt.Fprintln(os.Stderr, "  codecritic review --base <SHA> --head <SHA> [选项]")
	fmt.Fprintln(os.Stderr, "    --repo <路径>           仓库路径（必需，用于召回和调用图）")
	fmt.Fprintln(os.Stderr, "    --base <SHA>            固定审查的 base commit；必须与 --head 同时使用")
	fmt.Fprintln(os.Stderr, "    --head <SHA>            固定审查的 head commit；Diff 由 Git 生成")
	fmt.Fprintln(os.Stderr, "    --trace <路径>          保存完整轨迹（JSON 文件）")
	fmt.Fprintln(os.Stderr, "    --model <模型>          Agent 使用的主模型")
	fmt.Fprintln(os.Stderr, "    --memory <文件>         人工确认的项目规则记忆（JSON，可选）")
	fmt.Fprintln(os.Stderr, "    --max-tool-calls <n>    可选：工具调用安全上限（默认 32，不决定正常完成）")
	fmt.Fprintln(os.Stderr, "    --max-turns <n>         可选：模型决策安全上限（默认 64）")
	fmt.Fprintln(os.Stderr, "    --max-total-tokens <n>  可选：累计 provider token 安全上限（默认不限）")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  codecritic reviewbench")
	fmt.Fprintln(os.Stderr, "    ReviewBench 容器入口；从 RB_* 环境变量读取固定 PR 输入并写入 RB_OUT")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  codecritic mcp --repo <路径> --base <SHA> --head <SHA>")
	fmt.Fprintln(os.Stderr, "    通过 stdio 暴露绑定固定 Review Snapshot 的只读分析工具")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  codecritic eval [选项]")
	fmt.Fprintln(os.Stderr, "    --dataset <目录>        评测数据集目录")
	fmt.Fprintln(os.Stderr, "    --trace-dir <目录>      每个用例落盘 EvalTrace(JSON)，末尾输出阶段归因分布")
	fmt.Fprintln(os.Stderr, "    --verbose, -v           显示详细的 Agent 执行轨迹")
	fmt.Fprintln(os.Stderr, "    --model <模型>")
	fmt.Fprintln(os.Stderr, "    --judge-model <模型>    可选：独立评估 Claim 语义与引用 Evidence")
	fmt.Fprintln(os.Stderr, "    --ab-context            成对运行 Full Context 与 Context Checkpoint")
	fmt.Fprintln(os.Stderr, "    --context-checkpoint    在 eval 中启用 checkpoint 上下文")
	fmt.Fprintln(os.Stderr, "    --context-budget <byte> eval-only：覆盖 checkpoint 触发预算")
	fmt.Fprintln(os.Stderr, "    --diagnose              对已完成的漏报运行工具/证据/全上下文干预")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  codecritic rescore --dataset <目录> --trace-dir <目录>")
	fmt.Fprintln(os.Stderr, "    用当前已审计的标注离线重算已有 trace，不请求模型也不覆盖原轨迹")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  codecritic replay <trace文件> [选项]")
	fmt.Fprintln(os.Stderr, "    --output <路径>         保存分析结果（JSON 文件）")
	fmt.Fprintln(os.Stderr, "    --compare <trace2>      对比两个 trace")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "环境变量:")
	fmt.Fprintln(os.Stderr, "  CodeCritic_API_KEY      API 密钥（必需，兼容 DEEPSEEK_API_KEY）")
	fmt.Fprintln(os.Stderr, "  CodeCritic_URL          API 基础 URL（可选，兼容 DEEPSEEK_BASE_URL）")
}

func cmdMCP(args []string) {
	var repo, base, head string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--repo" && i+1 < len(args):
			repo, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--repo="):
			repo = strings.TrimPrefix(args[i], "--repo=")
		case args[i] == "--base" && i+1 < len(args):
			base, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--base="):
			base = strings.TrimPrefix(args[i], "--base=")
		case args[i] == "--head" && i+1 < len(args):
			head, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--head="):
			head = strings.TrimPrefix(args[i], "--head=")
		default:
			fmt.Fprintf(os.Stderr, "未知 MCP 参数: %s\n", args[i])
			usage()
			os.Exit(2)
		}
	}
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(base) == "" || strings.TrimSpace(head) == "" {
		usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	snapshot, diffData, err := workflow.CreateGitReviewSnapshot(ctx, repo, base, head, false)
	if err != nil {
		log.Fatalf("创建 MCP Review Snapshot 失败: %v", err)
	}
	err = workflow.ServeMCPStdio(ctx, snapshot, diffData)
	cleanupErr := snapshot.Close()
	if err != nil && ctx.Err() == nil {
		if cleanupErr != nil {
			log.Fatalf("MCP Server 失败: %v；清理 Review Snapshot 失败: %v", err, cleanupErr)
		}
		log.Fatalf("MCP Server 失败: %v", err)
	}
	if cleanupErr != nil {
		log.Printf("清理 MCP Review Snapshot 失败: %v", cleanupErr)
	}
}

func cmdReview(args []string) {
	repo, diffPath, tracePath, model, base, head, memoryPath, maxToolCalls, maxTurns, maxTotalTokens, verbose := parseReviewArgs(args)
	pinned := base != "" || head != ""
	if repo == "" || !pinned || base == "" || head == "" || diffPath != "" {
		usage()
		os.Exit(2)
	}

	key := getEnv("CodeCritic_API_KEY", "DEEPSEEK_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "未设置 CodeCritic_API_KEY 或 DEEPSEEK_API_KEY")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	llm := buildLLM(key, model)
	var raw []byte
	var run *workflow.ReviewRun
	var snapshot *workflow.ReviewSnapshot
	var diffData []byte
	var err error
	snapshot, diffData, err = workflow.CreateGitReviewSnapshot(ctx, repo, base, head, false)
	if err != nil {
		log.Fatalf("创建 Review Snapshot: %v", err)
	}
	raw = diffData
	var memoryEntries []workflow.MemoryEntry
	if memoryPath != "" {
		memoryEntries, err = workflow.LoadProjectMemory(memoryPath)
		if err != nil {
			_ = snapshot.Close()
			log.Fatalf("加载项目记忆失败: %v", err)
		}
	}
	run, err = workflow.NewWithSnapshot(llm, snapshot)
	if err != nil {
		if cleanupErr := snapshot.Close(); cleanupErr != nil {
			log.Printf("清理 Review Snapshot 失败: %v", cleanupErr)
		}
		log.Fatalf("创建 ReviewRun: %v", err)
	}
	// Ordinary review keeps the native conversation intact. Checkpointing is
	// only activated when the active context exceeds its conservative budget.
	if maxToolCalls > 0 {
		run.SetMaxToolCalls(maxToolCalls)
	}
	if maxTurns > 0 {
		run.SetMaxTurns(maxTurns)
	}
	if maxTotalTokens > 0 {
		run.SetMaxTotalTokens(maxTotalTokens)
	}
	if len(memoryEntries) > 0 {
		run.SetProjectMemory(memoryEntries)
	}
	result, err := run.Run(ctx, workflow.Request{Diff: raw})
	if tracePath == "" && result != nil && result.Trace != nil {
		tracePath = filepath.Join("logs", result.Trace.ID+".json")
	}
	if err != nil {
		if cleanupErr := run.Close(); cleanupErr != nil {
			log.Printf("重试清理 Review Snapshot 失败: %v", cleanupErr)
		}
		if result != nil && result.Trace != nil && tracePath != "" {
			if saveErr := result.Trace.Save(tracePath); saveErr != nil {
				log.Printf("保存失败 workflow 轨迹失败: %v", saveErr)
			}
		}
		log.Fatalf("ReviewRun 审查失败: %v", err)
	}
	if tracePath != "" {
		if err := result.Trace.Save(tracePath); err != nil {
			log.Printf("保存 workflow 轨迹失败: %v", err)
		}
	}
	if result.Trace.StopReason == workflow.StopPartial {
		fmt.Fprintln(os.Stderr, "审查以 partial 结束：运行安全阀触发，Trace 保留了当前证据与未完成结论；不能将空输出解释为未发现问题。")
	}
	printClaims(result.Trace.FinalReport.ClaimsWithStatus(workflow.VerdictAccepted))

	if verbose {
		printLLMMetrics(llm)
	}
}

func cmdRescore(args []string) {
	dataset := "internal/eval/testdata/cases"
	traceDir := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--dataset" && i+1 < len(args):
			dataset, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--dataset="):
			dataset = strings.TrimPrefix(args[i], "--dataset=")
		case args[i] == "--trace-dir" && i+1 < len(args):
			traceDir, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--trace-dir="):
			traceDir = strings.TrimPrefix(args[i], "--trace-dir=")
		default:
			fmt.Fprintf(os.Stderr, "未知 rescore 参数: %s\n", args[i])
			usage()
			return
		}
	}
	if traceDir == "" {
		fmt.Fprintln(os.Stderr, "rescore 需要 --trace-dir")
		os.Exit(2)
	}
	if err := eval.RescoreTraceDir(os.Stdout, dataset, traceDir); err != nil {
		log.Fatalf("重新评分失败: %v", err)
	}
}

func cmdEval(args []string) {
	dataset := "internal/eval/testdata/cases"
	var model string
	var judgeModel string
	var verbose bool
	var contextCheckpointing bool
	var contextBudget int
	var abContext bool
	var diagnose bool
	traceDir := "logs"
	concurrency := eval.DefaultConcurrency

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--dataset" && i+1 < len(args):
			dataset, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--dataset="):
			dataset = strings.TrimPrefix(args[i], "--dataset=")
		case args[i] == "--concurrency" && i+1 < len(args):
			concurrency, i = atoiOr(args[i+1], concurrency), i+1
		case strings.HasPrefix(args[i], "--concurrency="):
			concurrency = atoiOr(strings.TrimPrefix(args[i], "--concurrency="), concurrency)
		case args[i] == "--trace-dir" && i+1 < len(args):
			traceDir, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--trace-dir="):
			traceDir = strings.TrimPrefix(args[i], "--trace-dir=")
		case args[i] == "--verbose" || args[i] == "-v":
			verbose = true
		case args[i] == "--model" && i+1 < len(args):
			model, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--model="):
			model = strings.TrimPrefix(args[i], "--model=")
		case args[i] == "--judge-model" && i+1 < len(args):
			judgeModel, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--judge-model="):
			judgeModel = strings.TrimPrefix(args[i], "--judge-model=")
		case args[i] == "--context-checkpoint":
			contextCheckpointing = true
		case args[i] == "--context-budget" && i+1 < len(args):
			contextBudget, i = atoiOr(args[i+1], 0), i+1
		case strings.HasPrefix(args[i], "--context-budget="):
			contextBudget = atoiOr(strings.TrimPrefix(args[i], "--context-budget="), 0)
		case args[i] == "--ab-context":
			abContext = true
		case args[i] == "--diagnose":
			diagnose = true
		}
	}
	if abContext && contextCheckpointing {
		log.Fatal("--ab-context 已包含 checkpoint 组，不能与 --context-checkpoint 同时使用")
	}
	if abContext && judgeModel != "" {
		log.Fatal("--ab-context 暂不运行可选语义 judge；请单独运行 --judge-model")
	}
	if diagnose && (abContext || contextCheckpointing) {
		log.Fatal("--diagnose 只诊断基线运行，不能与上下文 checkpoint 选项同时使用")
	}

	key := getEnv("CodeCritic_API_KEY", "DEEPSEEK_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "未设置 CodeCritic_API_KEY 或 DEEPSEEK_API_KEY")
		os.Exit(1)
	}

	llm := buildLLM(key, model)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	if diagnose {
		var judgeLLM *review.LLM
		if judgeModel != "" {
			judgeLLM = buildLLM(key, judgeModel)
		}
		err = eval.RunConcurrentWithDiagnosis(ctx, llm, judgeLLM, judgeModel, dataset, verbose, concurrency, traceDir)
	} else if abContext {
		err = eval.RunContextCheckpointABWithBudget(ctx, llm, dataset, verbose, concurrency, traceDir, contextBudget)
	} else if judgeModel != "" && contextCheckpointing {
		judgeLLM := buildLLM(key, judgeModel)
		err = eval.RunConcurrentCheckpointedWithJudge(ctx, llm, judgeLLM, judgeModel, dataset, verbose, concurrency, traceDir)
	} else if judgeModel != "" {
		judgeLLM := buildLLM(key, judgeModel)
		err = eval.RunConcurrentWithJudge(ctx, llm, judgeLLM, judgeModel, dataset, verbose, concurrency, traceDir)
	} else if contextCheckpointing {
		if contextBudget > 0 {
			err = eval.RunConcurrentCheckpointedWithBudget(ctx, llm, dataset, verbose, concurrency, traceDir, contextBudget)
		} else {
			err = eval.RunConcurrentCheckpointed(ctx, llm, dataset, verbose, concurrency, traceDir)
		}
	} else {
		err = eval.RunConcurrent(ctx, llm, dataset, verbose, concurrency, traceDir)
	}

	if err != nil {
		log.Fatalf("评测失败: %v", err)
	}

	if verbose {
		printLLMMetrics(llm)
	}
}

// parseReviewArgs 提取 review 命令的参数。
func parseReviewArgs(args []string) (repo, diff, tracePath, model, base, head, memoryPath string, maxToolCalls, maxTurns, maxTotalTokens int, verbose bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--repo" && i+1 < len(args):
			repo, i = args[i+1], i+1
		case strings.HasPrefix(a, "--repo="):
			repo = strings.TrimPrefix(a, "--repo=")
		case a == "--base" && i+1 < len(args):
			base, i = args[i+1], i+1
		case strings.HasPrefix(a, "--base="):
			base = strings.TrimPrefix(a, "--base=")
		case a == "--head" && i+1 < len(args):
			head, i = args[i+1], i+1
		case strings.HasPrefix(a, "--head="):
			head = strings.TrimPrefix(a, "--head=")
		case a == "--verbose" || a == "-v":
			verbose = true
		case a == "--trace" && i+1 < len(args):
			tracePath, i = args[i+1], i+1
		case strings.HasPrefix(a, "--trace="):
			tracePath = strings.TrimPrefix(a, "--trace=")
		case a == "--model" && i+1 < len(args):
			model, i = args[i+1], i+1
		case strings.HasPrefix(a, "--model="):
			model = strings.TrimPrefix(a, "--model=")
		case a == "--memory" && i+1 < len(args):
			memoryPath, i = args[i+1], i+1
		case strings.HasPrefix(a, "--memory="):
			memoryPath = strings.TrimPrefix(a, "--memory=")
		case a == "--max-tool-calls" && i+1 < len(args):
			maxToolCalls, i = atoiOr(args[i+1], 0), i+1
		case strings.HasPrefix(a, "--max-tool-calls="):
			maxToolCalls = atoiOr(strings.TrimPrefix(a, "--max-tool-calls="), 0)
		case a == "--max-turns" && i+1 < len(args):
			maxTurns, i = atoiOr(args[i+1], 0), i+1
		case strings.HasPrefix(a, "--max-turns="):
			maxTurns = atoiOr(strings.TrimPrefix(a, "--max-turns="), 0)
		case a == "--max-total-tokens" && i+1 < len(args):
			maxTotalTokens, i = atoiOr(args[i+1], 0), i+1
		case strings.HasPrefix(a, "--max-total-tokens="):
			maxTotalTokens = atoiOr(strings.TrimPrefix(a, "--max-total-tokens="), 0)
		default:
			if diff == "" && !strings.HasPrefix(a, "--") {
				diff = a
			}
		}
	}
	return
}

// buildLLM 构建 LLM 客户端。
func buildLLM(key, model string) *review.LLM {
	baseURL := getEnv("CodeCritic_URL", "DEEPSEEK_BASE_URL")
	if baseURL == "" {
		baseURL = review.DefaultConfig().BaseURL
	}

	opts := []review.Option{
		review.WithAPIKey(key),
		review.WithBaseURL(baseURL),
	}

	if model != "" {
		opts = append(opts, review.WithModel(model))
	}
	return review.NewLLMWithConfig(opts...)
}

// getEnv 优先读取 primary，不存在则读取 fallback。
func getEnv(primary, fallback string) string {
	if v := os.Getenv(primary); v != "" {
		return v
	}
	return os.Getenv(fallback)
}

func printClaims(fs []review.CandidateClaim) {
	if len(fs) == 0 {
		fmt.Println("未发现问题")
		return
	}
	for _, f := range fs {
		sev := f.Severity
		if sev == "" {
			sev = "info"
		}
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		certainty := f.Certainty
		if certainty == "" {
			certainty = "certain"
		}
		fmt.Printf("[%s/%s] %s — %s\n", sev, certainty, loc, f.Msg)
	}
}

// printLLMMetrics 打印 LLM 调用统计（token、成功率、失败率）。
func printLLMMetrics(llm *review.LLM) {
	stats := llm.Metrics()
	if len(stats) == 0 {
		return
	}

	// 按模型名排序，保证输出稳定
	models := make([]string, 0, len(stats))
	for m := range stats {
		models = append(models, m)
	}
	sort.Strings(models)

	var totalCalls, totalSuccess, totalFail, totalRetries, totalIn, totalOut int
	fmt.Println("\n=== LLM Metrics ===")
	for _, m := range models {
		s := stats[m]
		fmt.Printf("%-28s calls=%-3d success=%-3d fail=%-2d retries=%-2d in=%-6d out=%-6d\n",
			m, s.Calls, s.Success, s.Fail, s.Retries, s.InputTokens, s.OutputTokens)
		totalCalls += s.Calls
		totalSuccess += s.Success
		totalFail += s.Fail
		totalRetries += s.Retries
		totalIn += s.InputTokens
		totalOut += s.OutputTokens
	}
	fmt.Println()
	fmt.Printf("合计:        calls=%d success=%d fail=%d retries=%d\n", totalCalls, totalSuccess, totalFail, totalRetries)
	if totalCalls > 0 {
		fmt.Printf("成功率:      %.1f%%\n", float64(totalSuccess)/float64(totalCalls)*100)
	}
	fmt.Printf("Token:       input=%d output=%d total=%d\n", totalIn, totalOut, totalIn+totalOut)
}

// atoiOr 解析整数，失败时返回 defaultVal。
func atoiOr(s string, defaultVal int) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err == nil {
		return n
	}
	return defaultVal
}
