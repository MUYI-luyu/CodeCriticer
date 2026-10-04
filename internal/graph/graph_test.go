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

func TestCallersDistinguishSameBasename(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/same\n\ngo 1.22\n")
	write(t, dir, "main.go", `package main

import (
    "example.com/same/left"
    "example.com/same/right"
)

func main() { left.Call(); right.Call() }
`)
	write(t, dir, "left/impl.go", `package left
func Call() { Same() }
func Same() {}
`)
	write(t, dir, "right/impl.go", `package right
func Call() { Same() }
func Same() {}
`)
	idx, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	callers := idx.Callers(SymbolRef{Name: "Same", File: "left/impl.go"})
	if !hasFunc(callers, "left.Call") || hasFunc(callers, "right.Call") {
		t.Fatalf("同名符号未按完整路径区分: %+v", callers)
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

func TestInspectSymbolReferencesUsesCompilerIdentity(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/refs\n\ngo 1.22\n")
	write(t, dir, "main.go", `package main

var shared = 1

func read() int { return shared }
func write() { shared = 2 }
`)
	idx, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := idx.InspectSymbol(SymbolRef{Name: "shared", File: "main.go", Line: 3}, "references")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Fatalf("references=%+v", facts)
	}
	for _, fact := range facts {
		if fact.Precision != "exact_types" || fact.Kind != "reference" {
			t.Fatalf("fact=%+v", fact)
		}
	}
}

func TestInspectSymbolCallHierarchyLabelsPrecision(t *testing.T) {
	dir := writeRepo(t)
	idx, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := idx.InspectSymbol(SymbolRef{Name: "Bar", File: "lib/lib.go", Line: 7}, "callers")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].Precision != "exact_static_call" {
		t.Fatalf("facts=%+v", facts)
	}
}

func TestInspectSymbolDisambiguatesReceiver(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/methods\n\ngo 1.22\n")
	write(t, dir, "main.go", `package main
type Left struct{}
type Right struct{}
func (Left) Close() {}
func (Right) Close() {}
func use() { Left{}.Close() }
`)
	idx, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := idx.InspectSymbol(SymbolRef{Name: "Left.Close", File: "main.go"}, "definition")
	if err != nil || len(facts) != 1 || !strings.Contains(facts[0].Detail, "Left") {
		t.Fatalf("facts=%+v err=%v", facts, err)
	}
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
