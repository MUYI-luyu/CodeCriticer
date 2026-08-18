package diff

import (
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
)

// Symbol 是被改位置所属的声明。
type Symbol struct {
	Name string // 函数名 / 方法名 / 类型名
	Kind string // func / method / type / var / const
	Line int    // 声明起始行（1-based）
}

// Locate 找出包含行号 line（1-based）的最内层声明符号。
func Locate(src []byte, line int) (Symbol, bool) {
	p := sitter.NewParser()
	defer p.Close()
	p.SetLanguage(golang.GetLanguage())
	tree := p.Parse(nil, src)

	var best *sitter.Node
	walk(tree.RootNode(), func(n *sitter.Node) {
		if !isDecl(n.Type()) || !contains(n, line) {
			return
		}
		if best == nil || span(n) < span(best) {
			best = n
		}
	})
	if best == nil {
		return Symbol{}, false
	}
	name := declName(best, src)
	if name == "" {
		return Symbol{}, false
	}
	return Symbol{
		Name: name,
		Kind: declKind(best.Type()),
		Line: int(best.StartPoint().Row) + 1,
	}, true
}

// declName 提取声明名字：函数/方法取直接 name 字段，type/var/const 取首个子节点的 name。
func declName(n *sitter.Node, src []byte) string {
	if name := n.ChildByFieldName("name"); name != nil {
		return name.Content(src)
	}
	if spec := n.NamedChild(0); spec != nil {
		if name := spec.ChildByFieldName("name"); name != nil {
			return name.Content(src)
		}
	}
	return ""
}

// Annotate 用文件内容给变更行标注所属符号，去重后写入 Symbols。
func (c *Change) Annotate(src []byte) {
	seen := map[string]bool{}
	for _, l := range c.Adds {
		sym, ok := Locate(src, l.No)
		if !ok {
			continue
		}
		key := sym.Kind + ":" + sym.Name
		if !seen[key] {
			seen[key] = true
			c.Symbols = append(c.Symbols, sym)
		}
	}
}

func walk(n *sitter.Node, fn func(*sitter.Node)) {
	fn(n)
	for i := 0; i < int(n.ChildCount()); i++ {
		if c := n.Child(i); c != nil {
			walk(c, fn)
		}
	}
}

func isDecl(t string) bool {
	switch t {
	case "function_declaration", "method_declaration", "type_declaration",
		"var_declaration", "const_declaration":
		return true
	}
	return false
}

func declKind(t string) string {
	switch t {
	case "function_declaration":
		return "func"
	case "method_declaration":
		return "method"
	case "type_declaration":
		return "type"
	case "var_declaration":
		return "var"
	case "const_declaration":
		return "const"
	}
	return t
}

// contains 判断 1-based 行号是否落在节点范围内。
func contains(n *sitter.Node, line int) bool {
	row := line - 1
	return int(n.StartPoint().Row) <= row && row <= int(n.EndPoint().Row)
}

// span 返回节点跨行数，越小越内层。
func span(n *sitter.Node) int {
	return int(n.EndPoint().Row) - int(n.StartPoint().Row)
}
