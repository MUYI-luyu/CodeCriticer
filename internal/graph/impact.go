package graph

import "golang.org/x/tools/go/ssa"

// Impact 返回被改符号的传递波及面（调用方闭包）。
func (idx *Index) Impact(refs []SymbolRef) []Caller {
	seen := map[*ssa.Function]bool{}
	queue := make([]*ssa.Function, 0, len(refs))
	for _, r := range refs {
		if fn := idx.find(r); fn != nil && !seen[fn] {
			seen[fn] = true
			queue = append(queue, fn)
		}
	}

	var out []Caller
	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]
		for _, e := range idx.callersOf(fn) {
			c := Caller{Func: e.Caller.Func.String()}
			if e.Site != nil {
				c.File, c.Line = idx.pos(e.Site.Pos())
			}
			if !seen[e.Caller.Func] {
				seen[e.Caller.Func] = true
				out = append(out, c)
				queue = append(queue, e.Caller.Func)
			}
		}
	}
	return out
}
