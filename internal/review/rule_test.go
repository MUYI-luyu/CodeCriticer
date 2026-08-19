package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRules(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/bugs\n\ngo 1.22\n")
	write(t, dir, "main.go", `package main

import "fmt"

func main() {
	fmt.Printf("%d\n", "not a number") // printf 格式不匹配
	fmt.Errorf("ignored error")        // unusedresult 忽略错误
}
`)

	fs, err := Rules(dir)
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}

	byRule := map[string]int{}
	for _, f := range fs {
		byRule[f.Symbol]++
	}
	if byRule["printf"] == 0 {
		t.Fatalf("未命中 printf 规则: %+v", fs)
	}
	if byRule["unusedresult"] == 0 {
		t.Fatalf("未命中 unusedresult 规则: %+v", fs)
	}

	// 行号与文件应可定位。
	for _, f := range fs {
		if f.File == "" || f.Line <= 0 {
			t.Fatalf("finding 缺定位: %+v", f)
		}
		if !strings.Contains(f.File, "main.go") {
			t.Fatalf("finding 文件路径异常: %q", f.File)
		}
	}
}

func write(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
