package graph

import (
	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/ssa"
)

// Caller 是一个调用点。
type Caller struct {
	Func string // 调用方函数全名
	File string
	Line int
}

// Callers 返回所有直接调用符号 ref 的函数。
func (idx *Index) Callers(ref SymbolRef) []Caller {
	fn := idx.find(ref)
	if fn == nil {
		return nil
	}
	return idx.edgesToCallers(idx.callersOf(fn))
}

// callersOf 返回指向 fn 的入边（调用关系）。
func (idx *Index) callersOf(fn *ssa.Function) []*callgraph.Edge {
	node := idx.cg.Nodes[fn]
	if node == nil {
		return nil
	}
	return node.In
}

// edgesToCallers 把入边转成去重后的调用方列表。
func (idx *Index) edgesToCallers(edges []*callgraph.Edge) []Caller {
	seen := map[string]bool{}
	var out []Caller
	for _, e := range edges {
		c := Caller{Func: e.Caller.Func.String()}
		if e.Site != nil {
			c.File, c.Line = idx.pos(e.Site.Pos())
		}
		if !seen[c.Func] {
			seen[c.Func] = true
			out = append(out, c)
		}
	}
	return out
}
