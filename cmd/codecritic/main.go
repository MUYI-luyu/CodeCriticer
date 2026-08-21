// CodeCriticer 是一个 Go 代码审查 agent。
// 第 5 天：组装完整流水线（规则 + 符号定位 + 依赖图 + 召回 + Plan-and-Execute + Reflection）。
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

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
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法:")
	fmt.Fprintln(os.Stderr, "  codecritic review <diff文件> [--repo 仓库路径] [--reflect]")
}

func cmdReview(args []string) {
	repo, diffPath, reflect := parseArgs(args)
	if diffPath == "" {
		usage()
		os.Exit(2)
	}

	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "未设置 DEEPSEEK_API_KEY")
		os.Exit(1)
	}

	raw, err := os.ReadFile(diffPath)
	if err != nil {
		log.Fatalf("读取 diff: %v", err)
	}

	ctx := context.Background()
	llm := review.NewLLM(key, os.Getenv("DEEPSEEK_BASE_URL"), os.Getenv("DEEPSEEK_MODEL"))

	res, err := review.Analyze(ctx, llm, repo, raw)
	if err != nil {
		log.Fatalf("审查失败: %v", err)
	}

	log.Printf("静态规则命中 %d 条", len(res.Rules))
	for _, im := range res.Impact {
		log.Printf("%s 波及面:", im.Symbol)
		for _, c := range im.Callers {
			log.Printf("  %s (%s:%d)", c.Func, filepath.Base(c.File), c.Line)
		}
	}

	// 规则结果确定性高，直接保留；Reflection 只二次校验 LLM 产出。
	llmFs := review.Dedup(res.LLM, 3)
	if reflect && repo != "" {
		refl := review.NewReflector(llm, repo)
		before := len(llmFs)
		llmFs = refl.Reflect(ctx, llmFs)
		log.Printf("Reflection: %d → %d 条", before, len(llmFs))
	}

	findings := review.Dedup(append(append([]review.Finding{}, res.Rules...), llmFs...), 3)
	printFindings(findings)
}

// parseArgs 提取 flag 与位置参数，flag 可出现在任意位置。
func parseArgs(args []string) (repo, diff string, reflect bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--repo" && i+1 < len(args):
			repo, i = args[i+1], i+1
		case strings.HasPrefix(a, "--repo="):
			repo = strings.TrimPrefix(a, "--repo=")
		case a == "--reflect":
			reflect = true
		default:
			if diff == "" {
				diff = a
			}
		}
	}
	return
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
