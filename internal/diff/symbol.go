package diff

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
)

// Symbol 是被改位置所属的声明，携带完整语义信息。
type Symbol struct {
	Name      string   // 函数名 / 方法名 / 类型名
	Kind      string   // func / method / type / var / const
	Line      int      // 声明起始行（1-based）
	EndLine   int      // 声明结束行（1-based）
	Receiver  string   // 方法接收者类型（仅 method）
	Params    []string // 参数列表（函数/方法）
	Returns   []string // 返回值列表（函数/方法）
	Body      string   // 完整函数体源码
	Signature string   // 完整签名（用于精确匹配）
}

// Locate 找出包含行号 line（1-based）的最内层声明符号。
func Locate(src []byte, line int) (Symbol, bool) {
	best := locateNode(src, line)
	if best == nil {
		return Symbol{}, false
	}
	sym := symbolAt(best, src)
	if sym.Name == "" {
		return Symbol{}, false
	}
	return sym, true
}

// locateNode 找出包含行号 line 的最内层声明节点。
func locateNode(src []byte, line int) *sitter.Node {
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
	return best
}

// symbolAt 从声明节点提取增强符号信息。
func symbolAt(n *sitter.Node, src []byte) Symbol {
	sym := Symbol{
		Name:      declName(n, src),
		Kind:      declKind(n.Type()),
		Line:      int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		Receiver:  extractReceiver(n, src),
		Params:    extractParams(n, src),
		Returns:   extractReturns(n, src),
		Body:      extractBody(n, src),
		Signature: extractSignature(n, src),
	}
	return sym
}

// declName 提取声明名字：函数/方法/type_spec 取直接 name 字段，type/var/const 取首个子节点的 name。
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

// extractReceiver 提取方法接收者类型（如 *T / T）。
func extractReceiver(n *sitter.Node, src []byte) string {
	recv := n.ChildByFieldName("receiver")
	if recv == nil || recv.NamedChildCount() == 0 {
		return ""
	}
	pd := recv.NamedChild(0)
	if t := pd.ChildByFieldName("type"); t != nil {
		return t.Content(src)
	}
	return pd.Content(src)
}

// extractParams 提取参数列表，每个参数声明作为一项。
func extractParams(n *sitter.Node, src []byte) []string {
	params := n.ChildByFieldName("parameters")
	if params == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(params.NamedChildCount()); i++ {
		if c := params.NamedChild(i); c != nil {
			out = append(out, c.Content(src))
		}
	}
	return out
}

// extractReturns 提取返回值列表；单返回值是直接 type 节点，多返回值是 parameter_list。
func extractReturns(n *sitter.Node, src []byte) []string {
	result := n.ChildByFieldName("result")
	if result == nil {
		return nil
	}
	if result.Type() == "parameter_list" {
		var out []string
		for i := 0; i < int(result.NamedChildCount()); i++ {
			if c := result.NamedChild(i); c != nil {
				out = append(out, c.Content(src))
			}
		}
		return out
	}
	return []string{result.Content(src)}
}

// extractBody 提取函数体源码。
func extractBody(n *sitter.Node, src []byte) string {
	if body := n.ChildByFieldName("body"); body != nil {
		return body.Content(src)
	}
	return ""
}

// extractSignature 提取完整签名（不含 body）。
func extractSignature(n *sitter.Node, src []byte) string {
	if body := n.ChildByFieldName("body"); body != nil {
		return strings.TrimSpace(string(src[n.StartByte():body.StartByte()]))
	}
	return strings.TrimSpace(n.Content(src))
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
	case "type_declaration", "type_spec":
		return "type"
	case "var_declaration", "var_spec":
		return "var"
	case "const_declaration", "const_spec":
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
