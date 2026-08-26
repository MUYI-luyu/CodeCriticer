package diff

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocateWithEnhancedSymbol(t *testing.T) {
	src := []byte(`package main

import "fmt"

// Add 计算两数之和
func Add(a, b int) int {
	return a + b
}

type Calculator struct {
	name string
}

// Multiply 计算乘积
func (c *Calculator) Multiply(x, y int) int {
	return x * y
}

func main() {
	fmt.Println(Add(1, 2))
}
`)

	tests := []struct {
		line         int
		wantName     string
		wantKind     string
		wantReceiver string
		wantParams   int
		wantReturns  int
	}{
		{line: 6, wantName: "Add", wantKind: "func", wantReceiver: "", wantParams: 1, wantReturns: 1},
		{line: 15, wantName: "Multiply", wantKind: "method", wantReceiver: "*Calculator", wantParams: 1, wantReturns: 1},
		{line: 19, wantName: "main", wantKind: "func", wantReceiver: "", wantParams: 0, wantReturns: 0},
	}

	for _, tt := range tests {
		sym, ok := Locate(src, tt.line)
		if !ok {
			t.Errorf("Locate(%d) failed", tt.line)
			continue
		}

		if sym.Name != tt.wantName {
			t.Errorf("Locate(%d).Name = %q, want %q", tt.line, sym.Name, tt.wantName)
		}

		if sym.Kind != tt.wantKind {
			t.Errorf("Locate(%d).Kind = %q, want %q", tt.line, sym.Kind, tt.wantKind)
		}

		if sym.Receiver != tt.wantReceiver {
			t.Errorf("Locate(%d).Receiver = %q, want %q", tt.line, sym.Receiver, tt.wantReceiver)
		}

		if len(sym.Params) != tt.wantParams {
			t.Errorf("Locate(%d).Params = %d, want %d", tt.line, len(sym.Params), tt.wantParams)
		}

		if len(sym.Returns) != tt.wantReturns {
			t.Errorf("Locate(%d).Returns = %d, want %d", tt.line, len(sym.Returns), tt.wantReturns)
		}

		if sym.Signature == "" {
			t.Errorf("Locate(%d).Signature is empty", tt.line)
		}

		if sym.Body == "" {
			t.Errorf("Locate(%d).Body is empty", tt.line)
		}

		if sym.Line <= 0 || sym.EndLine <= sym.Line {
			t.Errorf("Locate(%d) invalid line range: %d-%d", tt.line, sym.Line, sym.EndLine)
		}
	}
}

func TestExtractBoundary(t *testing.T) {
	src := []byte(`package main

func Add(a, b int) int {
	return a + b
}
`)

	sym, boundary, ok := ExtractBoundary(src, 4)
	if !ok {
		t.Fatal("ExtractBoundary failed")
	}

	if sym.Name != "Add" {
		t.Errorf("Symbol.Name = %q, want %q", sym.Name, "Add")
	}

	if boundary == "" {
		t.Error("Boundary is empty")
	}

	if sym.Line != 3 || sym.EndLine != 5 {
		t.Errorf("Line range = %d-%d, want 3-5", sym.Line, sym.EndLine)
	}
}

func TestFindAllSymbols(t *testing.T) {
	src := []byte(`package main

func Add(a, b int) int {
	return a + b
}

type Calculator struct {
	name string
}

func (c *Calculator) Multiply(x, y int) int {
	return x * y
}

const MaxValue = 100

var globalVar int
`)

	symbols := FindAllSymbols(src)

	if len(symbols) < 4 {
		t.Fatalf("FindAllSymbols returned %d symbols, want at least 4", len(symbols))
	}

	kinds := map[string]int{}
	for _, sym := range symbols {
		kinds[sym.Kind]++
	}

	if kinds["func"] < 1 {
		t.Error("No func declarations found")
	}

	if kinds["method"] < 1 {
		t.Error("No method declarations found")
	}

	if kinds["type"] < 1 {
		t.Error("No type declarations found")
	}

	if kinds["const"] < 1 {
		t.Error("No const declarations found")
	}
}

func TestParseWithRepo(t *testing.T) {
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "test.go")
	err := os.WriteFile(testFile, []byte(`package main

func Add(a, b int) int {
	return a + b
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	diffText := `diff --git a/test.go b/test.go
index 1234567..abcdefg 100644
--- a/test.go
+++ b/test.go
@@ -2,5 +2,5 @@ package main

 func Add(a, b int) int {
-	return a + b
+	return a + b + 1
 }
`

	// Parse diff and manually annotate symbols
	changes, err := Parse([]byte(diffText))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Manually annotate symbols (equivalent to old ParseWithRepo)
	for i := range changes {
		c := &changes[i]
		if c.File == "" || c.File == "/dev/null" {
			continue
		}

		fullPath := filepath.Join(tmpDir, c.File)
		src, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		c.Annotate(src)
	}

	if len(changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(changes))
	}

	if len(changes[0].Symbols) == 0 {
		t.Error("No symbols extracted from change")
	}

	sym := changes[0].Symbols[0]
	if sym.Name != "Add" {
		t.Errorf("Symbol.Name = %q, want %q", sym.Name, "Add")
	}

	if sym.Signature == "" {
		t.Error("Symbol signature is empty")
	}
}

func TestSummarize(t *testing.T) {
	changes := []Change{
		{
			File: "test.go",
			Adds: []Line{{No: 4, Text: "return a + b + 1"}},
			Dels: []Line{{No: 4, Text: "return a + b"}},
			Symbols: []Symbol{
				{
					Name:      "Add",
					Kind:      "func",
					Line:      3,
					EndLine:   5,
					Params:    []string{"a int", "b int"},
					Returns:   []string{"int"},
					Signature: "Add(a, b int) int",
				},
			},
		},
	}

	summary := Summarize(changes)

	if summary.TotalFiles != 1 {
		t.Errorf("TotalFiles = %d, want 1", summary.TotalFiles)
	}

	if summary.TotalAdds != 1 {
		t.Errorf("TotalAdds = %d, want 1", summary.TotalAdds)
	}

	if summary.TotalDels != 1 {
		t.Errorf("TotalDels = %d, want 1", summary.TotalDels)
	}

	if len(summary.ChangedSymbols) != 1 {
		t.Fatalf("ChangedSymbols count = %d, want 1", len(summary.ChangedSymbols))
	}

	cs := summary.ChangedSymbols[0]
	if cs.Symbol.Name != "Add" {
		t.Errorf("ChangedSymbol.Name = %q, want %q", cs.Symbol.Name, "Add")
	}

	if len(cs.AddLines) != 1 || len(cs.DelLines) != 1 {
		t.Errorf("AddLines = %d, DelLines = %d, want 1, 1", len(cs.AddLines), len(cs.DelLines))
	}
}
