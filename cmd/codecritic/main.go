package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MUYI-luyu/codecritic/internal/agent"
	"github.com/MUYI-luyu/codecritic/internal/diff"
	"github.com/MUYI-luyu/codecritic/internal/eval"
	"github.com/MUYI-luyu/codecritic/internal/review"
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
	fmt.Fprintln(os.Stderr, "    --max-attempts <数字>   最大尝试轮数（默认 3）")
	fmt.Fprintln(os.Stderr, "    --orchestration         启用动态工具编排模式（默认 Plan-and-Execute）")
	fmt.Fprintln(os.Stderr, "    --trace <路径>          保存完整轨迹（JSON 文件）")
	fmt.Fprintln(os.Stderr, "    --plan-model <模型>     规划任务使用的模型")
	fmt.Fprintln(os.Stderr, "    --review-model <模型>   审查任务使用的模型")
	fmt.Fprintln(os.Stderr, "    --reflect-model <模型>  Reflection 使用的模型")
	fmt.Fprintln(os.Stderr, "    --model <模型>          统一设置所有任务的模型")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  codecritic eval [选项]")
	fmt.Fprintln(os.Stderr, "    --dataset <目录>        评测数据集目录")
	fmt.Fprintln(os.Stderr, "    --orchestration         评测 orchestration 模式")
	fmt.Fprintln(os.Stderr, "    --verbose, -v           显示详细的 Agent 执行轨迹")
	fmt.Fprintln(os.Stderr, "    --plan-model <模型>")
	fmt.Fprintln(os.Stderr, "    --review-model <模型>")
	fmt.Fprintln(os.Stderr, "    --reflect-model <模型>")
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
	repo, diffPath, maxAttempts, tracePath, planModel, reviewModel, reflectModel, useOrchestration, verbose := parseReviewArgs(args)
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
	llm := buildLLM(key, planModel, reviewModel, reflectModel)

	// 使用 Reflexion Agent
	ag, err := agent.New(llm, repo,
		agent.WithMaxAttempts(maxAttempts),
		agent.WithOrchestration(useOrchestration))
	if err != nil {
		log.Fatalf("创建 Agent: %v", err)
	}

	// 提取 diff 中被修改的符号
	syms := extractSymbols(raw, repo)
	log.Printf("提取到 %d 个符号: %v", len(syms), symbolNames(syms))

	result, err := ag.Review(ctx, raw, syms)
	if err != nil {
		log.Fatalf("审查失败: %v", err)
	}

	// 保存轨迹
	if tracePath != "" {
		trace := &agent.Trace{
			ID:        filepath.Base(diffPath),
			Diff:      diffPath,
			Repo:      repo,
			Result:    result,
			CreatedAt: time.Now(),
		}
		if err := trace.Save(tracePath); err != nil {
			log.Printf("保存轨迹失败: %v", err)
		} else {
			log.Printf("轨迹已保存: %s", tracePath)
		}
	}

	// 打印结果
	log.Printf("总轮数: %d, 收敛: %v (%s), 耗时: %v",
		len(result.Attempts), result.Converged, result.Reason, result.TotalDuration)

	for i, att := range result.Attempts {
		log.Printf("第 %d 轮: %d findings, 平均置信度 %.2f, 耗时 %v",
			i+1, len(att.Findings), avgConfidence(att.Validations), att.Duration)
		if att.Error != "" {
			log.Printf("  第 %d 轮错误: %s", i+1, att.Error)
		}
	}

	if len(result.StaticFindings) > 0 {
		log.Printf("静态规则命中 %d 个问题（确定性，直接进入结果）", len(result.StaticFindings))
	}

	printFindings(result.FinalFindings)

	if verbose {
		printLLMMetrics(llm)
	}
}

func cmdEval(args []string) {
	dataset := "internal/eval/testdata/cases"
	var useOrchestration bool
	var planModel, reviewModel, reflectModel string
	var verbose bool

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--dataset" && i+1 < len(args):
			dataset, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--dataset="):
			dataset = strings.TrimPrefix(args[i], "--dataset=")
		case args[i] == "--orchestration":
			useOrchestration = true
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
		case args[i] == "--reflect-model" && i+1 < len(args):
			reflectModel, i = args[i+1], i+1
		case strings.HasPrefix(args[i], "--reflect-model="):
			reflectModel = strings.TrimPrefix(args[i], "--reflect-model=")
		case args[i] == "--model" && i+1 < len(args):
			planModel, reviewModel, reflectModel = args[i+1], args[i+1], args[i+1]
			i++
		case strings.HasPrefix(args[i], "--model="):
			m := strings.TrimPrefix(args[i], "--model=")
			planModel, reviewModel, reflectModel = m, m, m
		}
	}

	key := getEnv("CodeCritic_API_KEY", "DEEPSEEK_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "未设置 CodeCritic_API_KEY 或 DEEPSEEK_API_KEY")
		os.Exit(1)
	}

	llm := buildLLM(key, planModel, reviewModel, reflectModel)

	var err error
	if useOrchestration {
		err = eval.RunOrchestration(context.Background(), llm, dataset, verbose)
	} else {
		err = eval.Run(context.Background(), llm, dataset, verbose)
	}

	if err != nil {
		log.Fatalf("评测失败: %v", err)
	}

	if verbose {
		printLLMMetrics(llm)
	}
}

// parseReviewArgs 提取 review 命令的参数。
func parseReviewArgs(args []string) (repo, diff string, maxAttempts int, tracePath, planModel, reviewModel, reflectModel string, useOrchestration, verbose bool) {
	maxAttempts = 3 // 默认值
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--repo" && i+1 < len(args):
			repo, i = args[i+1], i+1
		case strings.HasPrefix(a, "--repo="):
			repo = strings.TrimPrefix(a, "--repo=")
		case a == "--max-attempts" && i+1 < len(args):
			fmt.Sscanf(args[i+1], "%d", &maxAttempts)
			i++
		case strings.HasPrefix(a, "--max-attempts="):
			fmt.Sscanf(strings.TrimPrefix(a, "--max-attempts="), "%d", &maxAttempts)
		case a == "--orchestration":
			useOrchestration = true
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
		case a == "--reflect-model" && i+1 < len(args):
			reflectModel, i = args[i+1], i+1
		case strings.HasPrefix(a, "--reflect-model="):
			reflectModel = strings.TrimPrefix(a, "--reflect-model=")
		case a == "--model" && i+1 < len(args):
			planModel, reviewModel, reflectModel = args[i+1], args[i+1], args[i+1]
			i++
		case strings.HasPrefix(a, "--model="):
			m := strings.TrimPrefix(a, "--model=")
			planModel, reviewModel, reflectModel = m, m, m
		default:
			if diff == "" && !strings.HasPrefix(a, "--") {
				diff = a
			}
		}
	}
	return
}

// buildLLM 构建 LLM 客户端。
func buildLLM(key, planModel, reviewModel, reflectModel string) *review.LLM {
	baseURL := getEnv("CodeCritic_URL", "DEEPSEEK_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.deepseek.com/v1"
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
	if reflectModel != "" {
		opts = append(opts, review.WithReflectModel(reflectModel))
	}

	return review.NewLLMWithConfig(opts...)
}

// extractSymbols 从 diff 中提取被修改的符号。
func extractSymbols(diffData []byte, repo string) []review.Sym {
	changes, err := diff.Parse(diffData)
	if err != nil {
		return nil
	}

	var syms []review.Sym
	for i := range changes {
		c := &changes[i]
		if c.File == "" || c.File == "/dev/null" {
			continue
		}

		fullPath := filepath.Join(repo, c.File)
		src, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		c.Annotate(src)
		for _, s := range c.Symbols {
			syms = append(syms, review.Sym{Name: s.Name, File: c.File})
		}
	}

	return syms
}

// symbolNames 提取符号名称列表（用于日志）。
func symbolNames(syms []review.Sym) []string {
	names := make([]string, len(syms))
	for i, s := range syms {
		names[i] = s.Name
	}
	return names
}

// getEnv 优先读取 primary，不存在则读取 fallback。
func getEnv(primary, fallback string) string {
	if v := os.Getenv(primary); v != "" {
		return v
	}
	return os.Getenv(fallback)
}

// avgConfidence 计算平均置信度。
func avgConfidence(validations []agent.Validation) float64 {
	if len(validations) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range validations {
		sum += v.Confidence
	}
	return sum / float64(len(validations))
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
