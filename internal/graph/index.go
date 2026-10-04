// Package graph 基于 x/tools 构建符号级调用图，做影响分析。
package graph

import (
	"fmt"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// SymbolRef 定位一个被改符号（名字 + 所在文件）。
type SymbolRef struct {
	Name string
	File string
	Line int
}

// Index 是仓库的调用图索引。
type Index struct {
	prog *ssa.Program
	cg   *callgraph.Graph
	pkgs []*packages.Package
	repo string

	// VTA is built lazily because ordinary symbol navigation only needs CHA.
	// Concurrency inspection uses it to narrow interface and function-value
	// targets without making every review pay that cost up front.
	vtaOnce sync.Once
	vtaCG   *callgraph.Graph
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
	prog, _ := ssautil.Packages(pkgs, ssa.InstantiateGenerics)
	prog.Build()
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, err
	}
	return &Index{prog: prog, cg: cha.CallGraph(prog), pkgs: pkgs, repo: filepath.Clean(absRepo)}, nil
}

func (idx *Index) refinedCallGraph() *callgraph.Graph {
	if idx == nil || idx.prog == nil {
		return nil
	}
	idx.vtaOnce.Do(func() {
		idx.vtaCG = vta.CallGraph(ssautil.AllFunctions(idx.prog), idx.cg)
	})
	if idx.vtaCG != nil {
		return idx.vtaCG
	}
	return idx.cg
}

// find 按名字 + 文件名在调用图里定位函数。
func (idx *Index) find(ref SymbolRef) *ssa.Function {
	target := filepath.Clean(ref.File)
	if filepath.IsAbs(target) {
		if rel, err := filepath.Rel(idx.repo, target); err == nil {
			target = filepath.Clean(rel)
		}
	}
	var matches []*ssa.Function
	for fn := range idx.cg.Nodes {
		if fn == nil {
			continue
		}
		if !functionNameMatches(fn, ref.Name) {
			continue
		}
		filename := filepath.Clean(idx.prog.Fset.Position(fn.Pos()).Filename)
		if rel, err := filepath.Rel(idx.repo, filename); err == nil && filepath.Clean(rel) == target {
			if ref.Line <= 0 || idx.prog.Fset.Position(fn.Pos()).Line == ref.Line {
				matches = append(matches, fn)
			}
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	if len(matches) > 1 {
		first := idx.prog.Fset.Position(matches[0].Pos())
		for _, match := range matches[1:] {
			pos := idx.prog.Fset.Position(match.Pos())
			if pos.Filename != first.Filename || pos.Line != first.Line {
				return nil
			}
		}
		return matches[0]
	}
	return nil
}

func functionNameMatches(fn *ssa.Function, requested string) bool {
	if fn == nil {
		return false
	}
	receiver := ""
	if dot := strings.LastIndex(requested, "."); dot >= 0 {
		receiver, requested = normalizeReceiver(requested[:dot]), requested[dot+1:]
	}
	if fn.Name() != requested || receiver == "" {
		return fn.Name() == requested
	}
	if fn.Signature == nil || fn.Signature.Recv() == nil {
		return false
	}
	return normalizeReceiver(fn.Signature.Recv().Type().String()) == receiver
}

// ResolveObject finds one compiler-resolved declaration. A line is strongly
// recommended because Go permits same-named methods with different receivers.
func (idx *Index) ResolveObject(ref SymbolRef) (types.Object, error) {
	matches, err := idx.resolveObjects(ref)
	if err != nil {
		return nil, err
	}
	return matches[0], nil
}

func (idx *Index) resolveObjects(ref SymbolRef) ([]types.Object, error) {
	if idx == nil || idx.prog == nil {
		return nil, fmt.Errorf("symbol index unavailable")
	}
	target := filepath.Clean(ref.File)
	name := ref.Name
	receiver := ""
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		receiver, name = normalizeReceiver(name[:dot]), name[dot+1:]
	}
	var matches []types.Object
	for _, pkg := range idx.pkgs {
		for ident, obj := range pkg.TypesInfo.Defs {
			if ident == nil || obj == nil || ident.Name != name {
				continue
			}
			if receiver != "" {
				fn, ok := obj.(*types.Func)
				if !ok || fn.Type().(*types.Signature).Recv() == nil || normalizeReceiver(fn.Type().(*types.Signature).Recv().Type().String()) != receiver {
					continue
				}
			}
			pos := idx.prog.Fset.Position(ident.Pos())
			rel, err := filepath.Rel(idx.repo, pos.Filename)
			if err != nil || filepath.Clean(rel) != target || (ref.Line > 0 && pos.Line != ref.Line) {
				continue
			}
			matches = append(matches, obj)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("找不到符号 %s", ref.Name)
	}
	first := idx.prog.Fset.Position(matches[0].Pos())
	for _, match := range matches[1:] {
		pos := idx.prog.Fset.Position(match.Pos())
		if pos.Filename != first.Filename || pos.Line != first.Line {
			return nil, fmt.Errorf("符号 %s 不唯一；请提供声明行号", ref.Name)
		}
	}
	return matches, nil
}

func normalizeReceiver(value string) string {
	value = strings.Trim(value, "*() ")
	if dot := strings.LastIndex(value, "."); dot >= 0 {
		value = value[dot+1:]
	}
	return value
}

// pos 把 SSA 指令位置转成 file:line。
func (idx *Index) pos(p token.Pos) (string, int) {
	pp := idx.prog.Fset.Position(p)
	if !pp.IsValid() {
		return "", 0
	}
	return pp.Filename, pp.Line
}
