package review

import (
	"context"
	"os"
	"path/filepath"

	"github.com/MUYI-luyu/codecritic/internal/diff"
	"github.com/MUYI-luyu/codecritic/internal/graph"
	"github.com/MUYI-luyu/codecritic/internal/recall"
)

// Impact 是被改符号及其波及的调用方。
type Impact struct {
	Symbol  string
	Callers []graph.Caller
}

// Result 是一次审查的产出，规则与 LLM 结果分开保存。
type Result struct {
	Rules  []Finding
	LLM    []Finding
	Impact []Impact
}

// Analyze 跑完整审查流水线（规则 + 符号定位 + 依赖图 + LLM），不含 Reflection。
func Analyze(ctx context.Context, llm *LLM, repo string, raw []byte) (*Result, error) {
	cs, err := diff.Parse(raw)
	if err != nil {
		return nil, err
	}

	res := &Result{}
	var syms []Sym
	var idx *graph.Index
	if repo != "" {
		if rf, err := Rules(repo); err == nil {
			res.Rules = rf
		}
		syms = annotate(cs, repo)
		if idx, err = graph.Build(repo); err == nil {
			res.Impact = impactOf(cs, idx)
		} else {
			idx = nil
		}
	}

	var store *recall.Store
	if idx != nil {
		store = recall.New(repo, idx)
	}
	agent := NewAgent(llm, store)
	fs, err := agent.Review(ctx, string(raw), syms)
	if err != nil {
		return nil, err
	}
	res.LLM = fs
	return res, nil
}

// annotate 给每个变更标注符号，返回被改符号列表。
func annotate(cs []diff.Change, repo string) []Sym {
	var syms []Sym
	for i := range cs {
		c := &cs[i]
		if c.File == "/dev/null" {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo, c.File))
		if err != nil {
			continue
		}
		c.Annotate(src)
		for _, s := range c.Symbols {
			syms = append(syms, Sym{Name: s.Name, File: c.File})
		}
	}
	return syms
}

// impactOf 计算每个被改符号的波及面。
func impactOf(cs []diff.Change, idx *graph.Index) []Impact {
	var refs []graph.SymbolRef
	for _, c := range cs {
		for _, s := range c.Symbols {
			refs = append(refs, graph.SymbolRef{Name: s.Name, File: c.File})
		}
	}
	var out []Impact
	for _, r := range refs {
		if callers := idx.Impact([]graph.SymbolRef{r}); len(callers) > 0 {
			out = append(out, Impact{Symbol: r.Name, Callers: callers})
		}
	}
	return out
}
