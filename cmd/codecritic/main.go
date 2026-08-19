// CodeCriticer 是一个 Go 代码审查 agent。
// 第 3 天：接入静态规则引擎，`review --repo` 可离线跑出确定性 findings；LLM 客户端待后续流水线接入。
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/MUYI-luyu/codecritic/internal/diff"
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
	fmt.Fprintln(os.Stderr, "  codecritic review <diff文件> [--repo 仓库路径]")
}

func cmdReview(args []string) {
	repo, diffPath := parseArgs(args)
	if diffPath == "" {
		usage()
		os.Exit(2)
	}

	raw, err := os.ReadFile(diffPath)
	if err != nil {
		log.Fatalf("读取 diff: %v", err)
	}

	cs, err := diff.Parse(raw)
	if err != nil {
		log.Fatalf("解析 diff: %v", err)
	}

	for _, c := range cs {
		fmt.Printf("%s  +%d -%d\n", c.File, len(c.Adds), len(c.Dels))
		for _, l := range c.Dels {
			fmt.Printf("  -%s\n", l.Text)
		}
		for _, l := range c.Adds {
			fmt.Printf("  +%s\n", l.Text)
		}
	}

	if repo == "" {
		return
	}

	fs, err := review.Rules(repo)
	if err != nil {
		log.Fatalf("静态规则失败: %v", err)
	}
	fmt.Printf("\n静态规则命中 %d 条:\n", len(fs))
	for _, f := range fs {
		fmt.Printf("  [%s] %s:%d @%s — %s\n", f.Severity, f.File, f.Line, f.Symbol, f.Msg)
	}
}

func parseArgs(args []string) (repo, diff string) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--repo" && i+1 < len(args):
			repo, i = args[i+1], i+1
		default:
			if diff == "" {
				diff = args[i]
			}
		}
	}
	return
}
