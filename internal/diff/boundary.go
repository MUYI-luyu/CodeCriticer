package diff

import (
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
)

// ExtractBoundary 提取包含指定行的完整符号定义（含上下文）。
// 返回符号信息和完整源码片段。
func ExtractBoundary(src []byte, line int) (Symbol, string, bool) {
	sym, ok := Locate(src, line)
	if !ok {
		return Symbol{}, "", false
	}

	lines := splitLines(src)
	if sym.Line < 1 || sym.EndLine > len(lines) {
		return sym, sym.Body, true
	}

	startIdx := sym.Line - 1
	endIdx := sym.EndLine

	boundary := joinLines(lines[startIdx:endIdx])
	return sym, boundary, true
}

// FindAllSymbols 提取文件中所有顶层符号声明。
func FindAllSymbols(src []byte) []Symbol {
	p := sitter.NewParser()
	defer p.Close()
	p.SetLanguage(golang.GetLanguage())
	tree := p.Parse(nil, src)

	var symbols []Symbol
	root := tree.RootNode()

	for i := 0; i < int(root.ChildCount()); i++ {
		child := root.Child(i)
		if child == nil || !isDecl(child.Type()) {
			continue
		}

		name := declName(child, src)
		if name == "" {
			continue
		}

		sym := Symbol{
			Name:      name,
			Kind:      declKind(child.Type()),
			Line:      int(child.StartPoint().Row) + 1,
			EndLine:   int(child.EndPoint().Row) + 1,
			Body:      child.Content(src),
			Signature: extractSignature(child, src),
		}

		if sym.Kind == "method" {
			sym.Receiver = extractReceiver(child, src)
		}

		if sym.Kind == "func" || sym.Kind == "method" {
			sym.Params = extractParams(child, src)
			sym.Returns = extractReturns(child, src)
		}

		symbols = append(symbols, sym)
	}

	return symbols
}

// splitLines 按行分割源码（保留换行符）。
func splitLines(src []byte) []string {
	var lines []string
	start := 0
	for i, b := range src {
		if b == '\n' {
			lines = append(lines, string(src[start:i+1]))
			start = i + 1
		}
	}
	if start < len(src) {
		lines = append(lines, string(src[start:]))
	}
	return lines
}

// joinLines 合并行切片为单个字符串。
func joinLines(lines []string) string {
	result := ""
	for _, line := range lines {
		result += line
	}
	return result
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
