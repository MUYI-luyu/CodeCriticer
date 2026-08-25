package diff

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
)

// ExtractBoundary 提取包含指定行的完整符号定义。
func ExtractBoundary(src []byte, line int) (Symbol, string) {
	best := locateNode(src, line)
	if best == nil {
		return Symbol{}, ""
	}
	return symbolAt(best, src), best.Content(src)
}

// ExtractWithContext 提取符号定义及其前后 N 行上下文。
func ExtractWithContext(src []byte, line, contextLines int) (Symbol, string) {
	best := locateNode(src, line)
	if best == nil {
		return Symbol{}, ""
	}
	start := int(best.StartPoint().Row) - contextLines
	if start < 0 {
		start = 0
	}
	end := int(best.EndPoint().Row) + 1 + contextLines
	lines := strings.Split(string(src), "\n")
	if end > len(lines) {
		end = len(lines)
	}
	return symbolAt(best, src), strings.Join(lines[start:end], "\n")
}

// FindAllSymbols 提取文件中所有顶层符号声明。
func FindAllSymbols(src []byte) []Symbol {
	p := sitter.NewParser()
	defer p.Close()
	p.SetLanguage(golang.GetLanguage())
	tree := p.Parse(nil, src)
	root := tree.RootNode()

	var syms []Symbol
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		switch n.Type() {
		case "type_declaration":
			for j := 0; j < int(n.NamedChildCount()); j++ {
				if c := n.NamedChild(j); c.Type() == "type_spec" {
					syms = append(syms, symbolAt(c, src))
				}
			}
		case "function_declaration", "method_declaration", "var_declaration", "const_declaration":
			syms = append(syms, symbolAt(n, src))
		}
	}
	return syms
}
