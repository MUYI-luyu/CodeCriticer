package diff

import "testing"

func TestLocateFunc(t *testing.T) {
	src := []byte("package p\n\nfunc Foo() {\n\tx := 1\n\t_ = x\n}\n")
	sym, ok := Locate(src, 4)
	if !ok || sym.Name != "Foo" || sym.Kind != "func" || sym.Line != 3 {
		t.Fatalf("got %+v ok=%v", sym, ok)
	}
}

func TestLocateMethod(t *testing.T) {
	src := []byte("package p\n\ntype T struct{}\n\nfunc (t *T) Bar() int {\n\treturn 1\n}\n")
	sym, ok := Locate(src, 6)
	if !ok || sym.Name != "Bar" || sym.Kind != "method" {
		t.Fatalf("got %+v ok=%v", sym, ok)
	}
}

func TestLocateType(t *testing.T) {
	src := []byte("package p\n\ntype Server struct {\n\tport int\n}\n")
	sym, ok := Locate(src, 4)
	if !ok || sym.Name != "Server" || sym.Kind != "type" {
		t.Fatalf("got %+v ok=%v", sym, ok)
	}
}

func TestLocateOutOfRange(t *testing.T) {
	src := []byte("package p\n\nfunc Foo() {}\n")
	if _, ok := Locate(src, 99); ok {
		t.Fatal("越界行不应定位到符号")
	}
}

func TestAnnotate(t *testing.T) {
	src := []byte("package p\n\nfunc Foo() {\n\tx := 1\n\t_ = x\n}\n")
	c := Change{File: "p.go", Adds: []Line{{No: 4}, {No: 5}}}
	c.Annotate(src)
	if len(c.Symbols) != 1 || c.Symbols[0].Name != "Foo" {
		t.Fatalf("Symbols=%+v", c.Symbols)
	}
}
