package diff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocateWithEnhancedSymbol(t *testing.T) {
	src := []byte("package p\n\ntype T struct{}\n\nfunc (t *T) Bar(a, b int) int {\n\treturn a + b\n}\n")
	sym, ok := Locate(src, 6)
	if !ok {
		t.Fatal("未定位到符号")
	}
	if sym.Name != "Bar" || sym.Kind != "method" {
		t.Fatalf("got %+v", sym)
	}
	if sym.Receiver != "*T" {
		t.Fatalf("receiver = %q", sym.Receiver)
	}
	if len(sym.Params) == 0 {
		t.Fatal("params 为空")
	}
	if len(sym.Returns) != 1 {
		t.Fatalf("returns = %v", sym.Returns)
	}
	if sym.EndLine < sym.Line {
		t.Fatalf("EndLine %d < Line %d", sym.EndLine, sym.Line)
	}
	if sym.Signature == "" || sym.Body == "" {
		t.Fatalf("signature/body 为空: %+v", sym)
	}
}

func TestExtractBoundary(t *testing.T) {
	src := []byte("package p\n\nfunc Foo() {\n\tx := 1\n\t_ = x\n}\n")
	sym, text := ExtractBoundary(src, 4)
	if sym.Name != "Foo" {
		t.Fatalf("got %+v", sym)
	}
	if !strings.Contains(text, "func Foo") {
		t.Fatalf("boundary 不含函数定义: %q", text)
	}
}

func TestExtractWithContext(t *testing.T) {
	src := []byte("package p\n\nvar a = 1\nfunc Foo() {\n\tx := 1\n\t_ = x\n}\n")
	_, text := ExtractWithContext(src, 5, 1)
	if !strings.Contains(text, "func Foo") {
		t.Fatalf("context 不含函数定义: %q", text)
	}
	if !strings.Contains(text, "var a") {
		t.Fatalf("context 不含前文: %q", text)
	}
}

func TestFindAllSymbols(t *testing.T) {
	src := []byte("package p\n\ntype A struct{}\ntype B struct{}\n\nfunc Foo() {}\n")
	syms := FindAllSymbols(src)
	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
	}
	if !names["A"] || !names["B"] || !names["Foo"] {
		t.Fatalf("syms = %+v", syms)
	}
}

func TestParseWithRepo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module m\n\ngo 1.22\n")
	writeFile(t, dir, "a.go", "package m\n\nfunc Foo() {\n\tx := 1\n}\n")

	raw := "diff --git a/a.go b/a.go\n" +
		"--- a/a.go\n" +
		"+++ b/a.go\n" +
		"@@ -1,3 +1,4 @@\n" +
		" package m\n" +
		" \n" +
		"-func Foo() {}\n" +
		"+func Foo() {\n" +
		"+\tx := 1\n" +
		"+}\n"
	cs, err := ParseWithRepo([]byte(raw), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || len(cs[0].Symbols) != 1 || cs[0].Symbols[0].Name != "Foo" {
		t.Fatalf("cs=%+v", cs)
	}
}

func TestSummarize(t *testing.T) {
	cs := []Change{{
		File: "a.go",
		Adds: []Line{{No: 4, Text: "return a + b + 1"}},
		Dels: []Line{{No: 4, Text: "return a + b"}},
		Symbols: []Symbol{{
			Name: "Add", Kind: "func", Line: 3, EndLine: 5,
			Params: []string{"a, b int"}, Returns: []string{"int"},
		}},
	}}
	s := Summarize(cs)
	if s.TotalFiles != 1 || s.TotalAdds != 1 || s.TotalDels != 1 {
		t.Fatalf("summary=%+v", s)
	}
	if len(s.ChangedSymbols) != 1 {
		t.Fatalf("changed symbols=%+v", s.ChangedSymbols)
	}
	if s.ChangedSymbols[0].ImpactMsg == "" {
		t.Fatal("ImpactMsg 为空")
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
