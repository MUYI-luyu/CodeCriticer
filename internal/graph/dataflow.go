package graph

import (
	"fmt"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// FlowStep 表示 SSA 中可定位的值传播事实。
type FlowStep struct {
	Function string
	File     string
	Line     int
	Kind     string
	Detail   string
}

// DataFlow 沿调用边提取有限的调用和返回事实，不进行通用数据流求解。
func (idx *Index) DataFlow(ref SymbolRef) ([]FlowStep, error) {
	if idx == nil || idx.prog == nil || idx.cg == nil {
		return nil, fmt.Errorf("dataflow: SSA 不可用")
	}
	root := idx.find(ref)
	if root == nil {
		return nil, fmt.Errorf("dataflow: 找不到符号 %s", ref.Name)
	}
	seen := map[*ssa.Function]bool{}
	var out []FlowStep
	var visit func(*ssa.Function, int)
	visit = func(fn *ssa.Function, depth int) {
		if fn == nil || seen[fn] || depth > 2 {
			return
		}
		seen[fn] = true
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				pos := idx.prog.Fset.Position(instr.Pos())
				if !pos.IsValid() {
					continue
				}
				line := FlowStep{Function: fn.String(), File: pos.Filename, Line: pos.Line, Detail: instr.String()}
				switch x := instr.(type) {
				case *ssa.Return:
					line.Kind = "return"
					if hasErrorValue(x.Results) {
						line.Detail = "返回 error/value: " + instr.String()
					}
					out = append(out, line)
				case *ssa.Call:
					line.Kind = "call"
					out = append(out, line)
					if common := x.Common(); common != nil {
						if callee := common.StaticCallee(); callee != nil {
							visit(callee, depth+1)
						}
					}
				}
			}
		}
	}
	visit(root, 0)
	if len(out) == 0 {
		return nil, fmt.Errorf("dataflow: 无法确定传播关系")
	}
	return out, nil
}

func hasErrorValue(values []ssa.Value) bool {
	for _, v := range values {
		if v != nil && strings.Contains(v.Type().String(), "error") {
			return true
		}
	}
	return false
}
