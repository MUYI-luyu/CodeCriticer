// Package graph 基于 x/tools 构建符号级调用图，做影响分析。
package graph

import (
	"go/token"
	"path/filepath"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// SymbolRef 定位一个被改符号（名字 + 所在文件）。
type SymbolRef struct {
	Name string
	File string
}

// Index 是仓库的调用图索引。
type Index struct {
	prog *ssa.Program
	cg   *callgraph.Graph
}

// Build 加载 repoDir 下所有包并构建 CHA 调用图。
func Build(repoDir string) (*Index, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedTypesSizes,
		Dir:   repoDir,
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			return nil, p.Errors[0]
		}
	}
	prog, _ := ssautil.Packages(pkgs, ssa.BuilderMode(0))
	prog.Build()
	return &Index{prog: prog, cg: cha.CallGraph(prog)}, nil
}

// find 按名字 + 文件名在调用图里定位函数。
func (idx *Index) find(ref SymbolRef) *ssa.Function {
	base := filepath.Base(ref.File)
	for fn := range idx.cg.Nodes {
		if fn == nil {
			continue
		}
		if fn.Name() != ref.Name {
			continue
		}
		if filepath.Base(idx.prog.Fset.Position(fn.Pos()).Filename) == base {
			return fn
		}
	}
	return nil
}

// pos 把 SSA 指令位置转成 file:line。
func (idx *Index) pos(p token.Pos) (string, int) {
	pp := idx.prog.Fset.Position(p)
	if !pp.IsValid() {
		return "", 0
	}
	return pp.Filename, pp.Line
}
