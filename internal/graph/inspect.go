package graph

import (
	"fmt"
	"go/types"
)

// SymbolFact is a compiler-derived navigation fact. Precision states whether
// the result is exact (types/static call) or a CHA over-approximation.
type SymbolFact struct {
	File      string
	Line      int
	Kind      string
	Detail    string
	Symbol    string
	Precision string
}

// InspectSymbol implements the small navigation surface needed by the Agent:
// declaration, semantic references, and incoming/outgoing call hierarchy.
func (idx *Index) InspectSymbol(ref SymbolRef, relation string) ([]SymbolFact, error) {
	switch relation {
	case "definition":
		obj, err := idx.ResolveObject(ref)
		if err != nil {
			return nil, err
		}
		pos := idx.prog.Fset.Position(obj.Pos())
		return []SymbolFact{{File: pos.Filename, Line: pos.Line, Kind: "definition", Detail: obj.String(), Symbol: obj.Name(), Precision: "exact_types"}}, nil
	case "references":
		return idx.references(ref)
	case "callers", "callees":
		return idx.callHierarchy(ref, relation)
	default:
		return nil, fmt.Errorf("unknown symbol relation %q", relation)
	}
}

func (idx *Index) references(ref SymbolRef) ([]SymbolFact, error) {
	objects, err := idx.resolveObjects(ref)
	if err != nil {
		return nil, err
	}
	targets := make(map[types.Object]bool, len(objects))
	for _, obj := range objects {
		targets[obj] = true
	}
	seen := map[string]bool{}
	var out []SymbolFact
	for _, pkg := range idx.pkgs {
		for ident, obj := range pkg.TypesInfo.Uses {
			if ident == nil || !targets[obj] {
				continue
			}
			pos := idx.prog.Fset.Position(ident.Pos())
			key := fmt.Sprintf("%s:%d", pos.Filename, pos.Line)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, SymbolFact{File: pos.Filename, Line: pos.Line, Kind: "reference", Detail: ident.Name, Symbol: obj.Name(), Precision: "exact_types"})
		}
	}
	return out, nil
}

func (idx *Index) callHierarchy(ref SymbolRef, relation string) ([]SymbolFact, error) {
	fn := idx.find(ref)
	if fn == nil {
		return nil, fmt.Errorf("找不到唯一函数 %s；请提供声明行号", ref.Name)
	}
	node := idx.cg.Nodes[fn]
	if node == nil {
		return nil, nil
	}
	edges := node.In
	if relation == "callees" {
		edges = node.Out
	}
	seen := map[string]bool{}
	var out []SymbolFact
	for _, edge := range edges {
		if edge == nil || edge.Site == nil {
			continue
		}
		file, line := idx.pos(edge.Site.Pos())
		dispatch := "dynamic"
		precision := "cha_overapproximation"
		if common := edge.Site.Common(); common != nil && common.StaticCallee() != nil {
			dispatch = "static"
			precision = "exact_static_call"
		}
		other := edge.Caller.Func
		if relation == "callees" {
			other = edge.Callee.Func
		}
		if other == nil {
			continue
		}
		key := fmt.Sprintf("%s:%d:%s", file, line, other.String())
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, SymbolFact{
			File: file, Line: line, Kind: relation,
			Detail: fmt.Sprintf("%s call (%s/CHA): %s", relation, dispatch, other.String()),
			Symbol: other.String(), Precision: precision,
		})
	}
	return out, nil
}
