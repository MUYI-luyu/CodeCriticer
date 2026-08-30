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
func (a *PlanAgent) Review(ctx context.Context, diffText string, syms []Sym) ([]Finding, *recall.EvidencePool, error) {
	return a.ReviewWithMemory(ctx, diffText, syms, "")
}

// ReviewWithMemory 支持注入历史批评（Reflexion 的 memory）。
// memory 是历史批评的文本形式，会注入到每个 ReviewPoint 的 prompt 中。
// 返回 findings 及其对应的召回证据池（供 Validate 复用）。
func (a *PlanAgent) ReviewWithMemory(ctx context.Context, diffText string, syms []Sym, memory string) ([]Finding, *recall.EvidencePool, error) {
	if a.store == nil {
		fs, err := a.llm.Review(ctx, diffText)
		return fs, nil, err
	}

	// 符号召回：被改符号的直接调用方，所有要点共享。
	var symDocs []recall.Doc
	for _, s := range syms {
		symDocs = append(symDocs, a.store.Symbol(s.Name, s.File)...)
	}

	points, err := a.llm.Plan(ctx, diffText)
	if err != nil || len(points) == 0 {
		fs, err := a.llm.Review(ctx, diffText)
		return fs, nil, err
	}

	seen := map[string]bool{}
	var all []Finding
	var allDocs [][]recall.Doc

	for _, p := range points {
		docs := append([]recall.Doc{}, symDocs...)
		for _, w := range p.Kw {
			docs = append(docs, a.store.Keyword(w)...)
		}
		docs = recall.Dedup(docs)

		// 如果召回过多，截断到 30 个（避免 token 溢出）
		const maxDocsPerFinding = 30
		if len(docs) > maxDocsPerFinding {
			docs = docs[:maxDocsPerFinding]
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
			key := fmt.Sprintf("%s:%d", f.File, f.Line) // 按 file:line 去重
			if !seen[key] {
				seen[key] = true
				all = append(all, f)
				allDocs = append(allDocs, docs)
			}
		}
	}

	// 转换为 recall.Finding 类型
	poolFindings := make([]recall.Finding, len(all))
	for i, f := range all {
		poolFindings[i] = recall.Finding{File: f.File, Line: f.Line, Msg: f.Msg}
	}

	pool := recall.NewEvidencePool(poolFindings, allDocs)
	return all, pool, nil
}

// formatDocs 把召回片段拼成文本。
func formatDocs(docs []recall.Doc) string {
	var b strings.Builder
	for _, d := range docs {
		fmt.Fprintf(&b, "// %s:%d (%s)\n%s\n", d.File, d.Line, d.Src, d.Text)
	}
	return b.String()
}
