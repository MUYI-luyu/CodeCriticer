package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCallersAndImpact(t *testing.T) {
	dir := writeRepo(t)
	idx, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}

	ref := SymbolRef{Name: "Bar", File: "lib/lib.go"}

	callers := idx.Callers(ref)
	if !hasFunc(callers, "Foo") {
		t.Fatalf("Callers 缺 Foo: %+v", callers)
	}

	impact := idx.Impact([]SymbolRef{ref})
	if !hasFunc(impact, "Foo") {
		t.Fatalf("Impact 缺 Foo: %+v", impact)
	}
	if !hasFunc(impact, "main") {
		t.Fatalf("Impact 缺 main: %+v", impact)
	}
}

func TestCallersUnknown(t *testing.T) {
	dir := writeRepo(t)
	idx, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.Callers(SymbolRef{Name: "Nope", File: "lib/lib.go"}); len(got) != 0 {
		t.Fatalf("未知符号应返回空: %+v", got)
	}
}

// interface 调用由 CHA 分发到实现，改实现内部函数应波及接口调用方。
func TestInterfaceDispatch(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/demo\n\ngo 1.22\n")
	write(t, dir, "main.go", `package main

import "example.com/demo/lib"

func main() {
	lib.Call(lib.Impl{})
}
`)

	write(t, dir, "lib/lib.go", `package lib

type Runner interface {
	Run()
}

type Impl struct{}

func (Impl) Run() {
	work()
}

func work() {}

func Call(r Runner) {
	r.Run()
}
`)
	idx, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := idx.Impact([]SymbolRef{{Name: "work", File: "lib/lib.go"}})
	if !hasFunc(out, "Run") {
		t.Fatalf("Impact 缺 Run（interface 分发失败）: %+v", out)
	}
	if !hasFunc(out, "Call") {
		t.Fatalf("Impact 缺 Call: %+v", out)
	}
}

func TestDataFlowFacts(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/flow\n\ngo 1.22\n")
	write(t, dir, "main.go", `package main

import "fmt"

func readConfig() error { return fmt.Errorf("bad config") }
func Load() error { return readConfig() }
func Start() error { err := Load(); return err }
func Wrap() error { return fmt.Errorf("wrapped: %w", Load()) }
func Ignore() error { Load(); return nil }
`)
	idx, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := idx.DataFlow(SymbolRef{Name: "Start", File: "main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasKind(steps, "call") || !hasKind(steps, "return") {
		t.Fatalf("缺少调用或返回事实: %+v", steps)
	}
	for _, name := range []string{"Wrap", "Ignore"} {
		if _, err := idx.DataFlow(SymbolRef{Name: name, File: "main.go"}); err != nil {
			t.Fatalf("%s 数据流失败: %v", name, err)
		}
	}
}

func TestDataFlowMissingSymbol(t *testing.T) {
	dir := writeRepo(t)
	idx, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := idx.DataFlow(SymbolRef{Name: "Missing", File: "main.go"}); err == nil || !strings.Contains(err.Error(), "找不到符号") {
		t.Fatalf("应明确报告目标不存在: %v", err)
	}
}

func TestDataFlowLoadsTestVariant(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/testflow\n\ngo 1.22\n")
	write(t, dir, "flow_test.go", `package testflow

import (
    "errors"
    "testing"
)

func Go() error { return errors.New("failed") }
func TestEntry(t *testing.T) { _ = Go() }
`)
	idx, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := idx.DataFlow(SymbolRef{Name: "Go", File: "flow_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasKind(steps, "return") {
		t.Fatalf("测试变体缺少返回事实: %+v", steps)
	}
}

func hasKind(steps []FlowStep, kind string) bool {
	for _, step := range steps {
		if step.Kind == kind && step.File != "" && step.Line > 0 && step.Detail != "" {
			return true
		}
	}
	return false
}

func hasFunc(cs []Caller, sub string) bool {
	for _, c := range cs {
		if strings.Contains(c.Func, sub) {
			return true
		}
	}
	return false
}

func writeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/demo\n\ngo 1.22\n")
	write(t, dir, "main.go", `package main

import "example.com/demo/lib"

func main() {
	lib.Foo()
}
`)
	write(t, dir, "lib/lib.go", `package lib

func Foo() {
	Bar()
}

func Bar() {}
`)
	return dir
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
