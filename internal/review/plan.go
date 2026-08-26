package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/recall"
)

// Point 是一个审查要点。
type Point struct {
	Desc string   `json:"desc"`
	Kw   []string `json:"kw"`
}

// Sym 是被改符号（名字 + 文件）。
type Sym struct {
	Name string
	File string
}

// PlanAgent 是 Plan-and-Execute 审查器。
// 先规划审查要点，再逐点召回代码并审查。
type PlanAgent struct {
	llm   *LLM
	store *recall.Store
}

func NewPlanAgent(llm *LLM, store *recall.Store) *PlanAgent {
	return &PlanAgent{llm: llm, store: store}
}

// Review 先规划要点，再逐点召回 + 审查。
func (a *PlanAgent) Review(ctx context.Context, diffText string, syms []Sym) ([]Finding, error) {
	return a.ReviewWithMemory(ctx, diffText, syms, "")
}

// ReviewWithMemory 支持注入历史批评（Reflexion 的 memory）。
// memory 是历史批评的文本形式，会注入到每个 ReviewPoint 的 prompt 中。
func (a *PlanAgent) ReviewWithMemory(ctx context.Context, diffText string, syms []Sym, memory string) ([]Finding, error) {
	if a.store == nil {
		return a.llm.Review(ctx, diffText)
	}

	// 符号召回：被改符号的直接调用方，所有要点共享。
	var symDocs []recall.Doc
	for _, s := range syms {
		symDocs = append(symDocs, a.store.Symbol(s.Name, s.File)...)
	}

	points, err := a.llm.Plan(ctx, diffText)
	if err != nil || len(points) == 0 {
		return a.llm.Review(ctx, diffText)
	}

	seen := map[string]bool{}
	var all []Finding
	for _, p := range points {
		docs := append([]recall.Doc{}, symDocs...)
		for _, w := range p.Kw {
			docs = append(docs, a.store.Keyword(w)...)
		}

		// 将 memory 注入到召回上下文中
		ctxText := formatDocs(docs)
		if memory != "" {
			ctxText = fmt.Sprintf("历史批评（避免重犯）：\n%s\n\n召回代码：\n%s", memory, ctxText)
		}

		fs, err := a.llm.ReviewPoint(ctx, diffText, p.Desc, ctxText)
		if err != nil {
			continue
		}
		for _, f := range fs {
			key := f.File + ":" + f.Msg
			if !seen[key] {
				seen[key] = true
				all = append(all, f)
			}
		}
	}
	return all, nil
}

// formatDocs 把召回片段拼成文本。
func formatDocs(docs []recall.Doc) string {
	var b strings.Builder
	for _, d := range docs {
		fmt.Fprintf(&b, "// %s:%d (%s)\n%s\n", d.File, d.Line, d.Src, d.Text)
	}
	return b.String()
}
