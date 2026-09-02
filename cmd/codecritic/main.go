package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

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
	case "eval":
		cmdEval(os.Args[2:])
	case "replay":
		cmdReplay(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法:")
	fmt.Fprintln(os.Stderr, "  codecritic review <diff文件> [选项]")
	fmt.Fprintln(os.Stderr, "    --repo <路径>           仓库路径（必需，用于召回和调用图）")
	fmt.Fprintln(os.Stderr, "    --workflow              兼容参数（Workflow 为默认路径）")
	fmt.Fprintln(os.Stderr, "    --trace <路径>          保存完整轨迹（JSON 文件）")
	fmt.Fprintln(os.Stderr, "    --plan-model <模型>     规划任务使用的模型")
	fmt.Fprintln(os.Stderr, "    --review-model <模型>   审查任务使用的模型")
	fmt.Fprintln(os.Stderr, "    --model <模型>          统一设置所有任务的模型")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  codecritic eval [选项]")
	fmt.Fprintln(os.Stderr, "    --dataset <目录>        评测数据集目录")
	fmt.Fprintln(os.Stderr, "    --trace-dir <目录>      每个用例落盘 EvalTrace(JSON)，末尾输出阶段归因分布")
	fmt.Fprintln(os.Stderr, "    --verbose, -v           显示详细的 Agent 执行轨迹")
	fmt.Fprintln(os.Stderr, "    --plan-model <模型>")
	fmt.Fprintln(os.Stderr, "    --review-model <模型>")
	fmt.Fprintln(os.Stderr, "    --model <模型>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  codecritic replay <trace文件> [选项]")
	fmt.Fprintln(os.Stderr, "    --output <路径>         保存分析结果（JSON 文件）")
	fmt.Fprintln(os.Stderr, "    --compare <trace2>      对比两个 trace")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "环境变量:")
	fmt.Fprintln(os.Stderr, "  CodeCritic_API_KEY      API 密钥（必需，兼容 DEEPSEEK_API_KEY）")
	fmt.Fprintln(os.Stderr, "  CodeCritic_URL          API 基础 URL（可选，兼容 DEEPSEEK_BASE_URL）")
}

func cmdReview(args []string) {
	repo, diffPath, tracePath, planModel, reviewModel, verbose := parseReviewArgs(args)
	if diffPath == "" || repo == "" {
		usage()
		os.Exit(2)
	}

	key := getEnv("CodeCritic_API_KEY", "DEEPSEEK_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "未设置 CodeCritic_API_KEY 或 DEEPSEEK_API_KEY")
		os.Exit(1)
	}

	raw, err := os.ReadFile(diffPath)
	if err != nil {
		log.Fatalf("读取 diff: %v", err)
	}

	ctx := context.Background()
	llm := buildLLM(key, planModel, reviewModel)
	wf, err := workflow.New(llm, repo)
	if err != nil {
		log.Fatalf("创建 Workflow: %v", err)
	}
	result, err := wf.Run(ctx, workflow.Request{Repo: repo, Diff: raw})
	if err != nil {
		if result != nil && result.Trace != nil && tracePath != "" {
			_ = result.Trace.Save(tracePath)
		}
		log.Fatalf("workflow 审查失败: %v", err)
	}
	if tracePath != "" {
		if err := result.Trace.Save(tracePath); err != nil {
			log.Printf("保存 workflow 轨迹失败: %v", err)
		}
	}
	printFindings(result.Trace.Findings)

	if verbose {
		printLLMMetrics(llm)
	}
}

func cmdEval(args []string) {
	dataset := "internal/eval/testdata/cases"
	var planModel, reviewModel string
	var verbose bool
	var traceDir string
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
		case args[i] == "--plan-model" && i+1 < len(args):
			planModel, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--plan-model="):
			planModel = strings.TrimPrefix(args[i], "--plan-model=")
		case args[i] == "--review-model" && i+1 < len(args):
			reviewModel, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--review-model="):
			reviewModel = strings.TrimPrefix(args[i], "--review-model=")
		case args[i] == "--model" && i+1 < len(args):
			planModel, reviewModel = args[i+1], args[i+1]
			i++
		case strings.HasPrefix(args[i], "--model="):
			m := strings.TrimPrefix(args[i], "--model=")
			planModel, reviewModel = m, m
		}
	}

	key := getEnv("CodeCritic_API_KEY", "DEEPSEEK_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "未设置 CodeCritic_API_KEY 或 DEEPSEEK_API_KEY")
		os.Exit(1)
	}

	llm := buildLLM(key, planModel, reviewModel)
	err := eval.RunConcurrent(context.Background(), llm, dataset, verbose, concurrency, traceDir)

	if err != nil {
		log.Fatalf("评测失败: %v", err)
	}

	if verbose {
		printLLMMetrics(llm)
	}
}

// parseReviewArgs 提取 review 命令的参数。
func parseReviewArgs(args []string) (repo, diff, tracePath, planModel, reviewModel string, verbose bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--repo" && i+1 < len(args):
			repo, i = args[i+1], i+1
		case strings.HasPrefix(a, "--repo="):
			repo = strings.TrimPrefix(a, "--repo=")
		case a == "--workflow":
			// Compatibility flag; Workflow is always used.
		case a == "--verbose" || a == "-v":
			verbose = true
		case a == "--trace" && i+1 < len(args):
			tracePath, i = args[i+1], i+1
		case strings.HasPrefix(a, "--trace="):
			tracePath = strings.TrimPrefix(a, "--trace=")
		case a == "--plan-model" && i+1 < len(args):
			planModel, i = args[i+1], i+1
		case strings.HasPrefix(a, "--plan-model="):
			planModel = strings.TrimPrefix(a, "--plan-model=")
		case a == "--review-model" && i+1 < len(args):
			reviewModel, i = args[i+1], i+1
		case strings.HasPrefix(a, "--review-model="):
			reviewModel = strings.TrimPrefix(a, "--review-model=")
		case a == "--model" && i+1 < len(args):
			planModel, reviewModel = args[i+1], args[i+1]
			i++
		case strings.HasPrefix(a, "--model="):
			m := strings.TrimPrefix(a, "--model=")
			planModel, reviewModel = m, m
		default:
			if diff == "" && !strings.HasPrefix(a, "--") {
				diff = a
			}
		}
	}
	return
}

// buildLLM 构建 LLM 客户端。
func buildLLM(key, planModel, reviewModel string) *review.LLM {
	baseURL := getEnv("CodeCritic_URL", "DEEPSEEK_BASE_URL")
	if baseURL == "" {
		baseURL = review.DefaultConfig().BaseURL
	}

	opts := []review.Option{
		review.WithAPIKey(key),
		review.WithBaseURL(baseURL),
	}

	if planModel != "" {
		opts = append(opts, review.WithPlanModel(planModel))
	}
	if reviewModel != "" {
		opts = append(opts, review.WithReviewModel(reviewModel))
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

func printFindings(fs []review.Finding) {
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
		src := ""
		if f.Symbol != "" {
			src = " @" + f.Symbol
		}
		fmt.Printf("[%s] %s%s — %s\n", sev, loc, src, f.Msg)
		if f.Evidence != "" {
			fmt.Printf("    证据: %s\n", f.Evidence)
		}
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
