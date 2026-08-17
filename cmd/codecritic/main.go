// CodeCriticer 是一个 Go 代码审查 agent。
// 第 1 天：项目骨架 + 核心数据模型 + unified diff 解析。
// 目前只支持把 diff 解析为结构化变更并打印，规则与 LLM 审查逻辑后续迭代补齐。
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/MUYI-luyu/codecritic/internal/diff"
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
	fmt.Fprintln(os.Stderr, "  codecritic review <diff文件>")
}

func cmdReview(args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	raw, err := os.ReadFile(args[0])
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
}
