package diff

import (
	"fmt"
	"strings"
)

// DiffSummary 提供 diff 的结构化摘要。
type DiffSummary struct {
	TotalFiles    int              // 变更文件数
	TotalAdds     int              // 新增行数
	TotalDels     int              // 删除行数
	ChangedSymbols []ChangedSymbol // 变更的符号列表
}

// ChangedSymbol 表示一个被修改的符号及其变更详情。
type ChangedSymbol struct {
	File      string   // 文件路径
	Symbol    Symbol   // 符号信息（含完整签名）
	AddLines  []int    // 新增的行号
	DelLines  []int    // 删除的行号
	ImpactMsg string   // 影响描述
}

// Summarize 生成 diff 的结构化摘要。
func Summarize(changes []Change) DiffSummary {
	summary := DiffSummary{
		TotalFiles: len(changes),
	}

	for _, c := range changes {
		summary.TotalAdds += len(c.Adds)
		summary.TotalDels += len(c.Dels)

		for _, sym := range c.Symbols {
			cs := ChangedSymbol{
				File:   c.File,
				Symbol: sym,
			}

			for _, add := range c.Adds {
				if add.No >= sym.Line && add.No <= sym.EndLine {
					cs.AddLines = append(cs.AddLines, add.No)
				}
			}

			for _, del := range c.Dels {
				if del.No >= sym.Line && del.No <= sym.EndLine {
					cs.DelLines = append(cs.DelLines, del.No)
				}
			}

			cs.ImpactMsg = formatImpact(sym, len(cs.AddLines), len(cs.DelLines))
			summary.ChangedSymbols = append(summary.ChangedSymbols, cs)
		}
	}

	return summary
}

// formatImpact 生成影响描述。
func formatImpact(sym Symbol, adds, dels int) string {
	parts := []string{}

	if sym.Kind == "func" || sym.Kind == "method" {
		if sym.Kind == "method" && sym.Receiver != "" {
			parts = append(parts, fmt.Sprintf("方法 %s.%s", sym.Receiver, sym.Name))
		} else {
			parts = append(parts, fmt.Sprintf("函数 %s", sym.Name))
		}

		if len(sym.Params) > 0 {
			parts = append(parts, fmt.Sprintf("参数: %d", len(sym.Params)))
		}

		if len(sym.Returns) > 0 {
			parts = append(parts, fmt.Sprintf("返回: %d", len(sym.Returns)))
		}
	} else {
		parts = append(parts, fmt.Sprintf("%s %s", sym.Kind, sym.Name))
	}

	if adds > 0 || dels > 0 {
		parts = append(parts, fmt.Sprintf("+%d/-%d 行", adds, dels))
	}

	return strings.Join(parts, ", ")
}
