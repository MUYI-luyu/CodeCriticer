package diff

import (
	"fmt"
	"strings"
)

// DiffSummary 是 diff 的结构化摘要。
type DiffSummary struct {
	TotalFiles     int             // 变更文件数
	TotalAdds      int             // 新增行数
	TotalDels      int             // 删除行数
	ChangedSymbols []ChangedSymbol // 变更的符号列表
}

// ChangedSymbol 是一个变更符号及其变更行。
type ChangedSymbol struct {
	File      string   // 文件路径
	Symbol    Symbol   // 符号信息（含完整签名）
	AddLines  []int    // 新增的行号
	DelLines  []int    // 删除的行号
	ImpactMsg string   // 影响描述
}

// Summarize 生成 diff 的结构化摘要。
func Summarize(changes []Change) DiffSummary {
	s := DiffSummary{TotalFiles: len(changes)}
	for _, c := range changes {
		s.TotalAdds += len(c.Adds)
		s.TotalDels += len(c.Dels)
		for _, sym := range c.Symbols {
			cs := ChangedSymbol{File: c.File, Symbol: sym}
			for _, l := range c.Adds {
				if l.No >= sym.Line && l.No <= sym.EndLine {
					cs.AddLines = append(cs.AddLines, l.No)
				}
			}
			for _, l := range c.Dels {
				if l.No >= sym.Line && l.No <= sym.EndLine {
					cs.DelLines = append(cs.DelLines, l.No)
				}
			}
			cs.ImpactMsg = impactMessage(cs)
			s.ChangedSymbols = append(s.ChangedSymbols, cs)
		}
	}
	return s
}

// impactMessage 生成影响描述（如 "方法 *Calculator.Multiply, 参数: 1, 返回: 1, +1/-1 行"）。
func impactMessage(cs ChangedSymbol) string {
	name := cs.Symbol.Name
	if cs.Symbol.Receiver != "" {
		name = cs.Symbol.Receiver + "." + name
	}
	return fmt.Sprintf("%s %s, 参数: %d, 返回: %d, +%d/-%d 行",
		cs.Symbol.Kind, name, len(cs.Symbol.Params), len(cs.Symbol.Returns),
		len(cs.AddLines), len(cs.DelLines))
}

// GetSymbolContext 获取符号的完整上下文（用于 LLM 审查）。
func GetSymbolContext(sym Symbol, adds, dels []Line) string {
	var b strings.Builder
	fmt.Fprintf(&b, "符号: %s (%s)\n", sym.Name, sym.Kind)
	fmt.Fprintf(&b, "位置: 行 %d-%d\n", sym.Line, sym.EndLine)
	if sym.Signature != "" {
		fmt.Fprintf(&b, "签名: %s\n", sym.Signature)
	}
	for _, l := range adds {
		fmt.Fprintf(&b, "新增行: %d: %s\n", l.No, l.Text)
	}
	for _, l := range dels {
		fmt.Fprintf(&b, "删除行: %d: %s\n", l.No, l.Text)
	}
	if sym.Body != "" {
		fmt.Fprintf(&b, "完整定义:\n%s\n", sym.Body)
	}
	return strings.TrimRight(b.String(), "\n")
}
