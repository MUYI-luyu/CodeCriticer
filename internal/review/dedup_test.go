package review

import "testing"

func TestDedupMergesNear(t *testing.T) {
	fs := []Finding{
		{File: "a.go", Line: 7, Severity: "warning", Msg: "w1"},
		{File: "a.go", Line: 8, Severity: "error", Msg: "e1"},
		{File: "a.go", Line: 20, Severity: "error", Msg: "far"},
	}
	out := Dedup(fs, 3)
	if len(out) != 2 {
		t.Fatalf("期望 2 条，得到 %d: %+v", len(out), out)
	}
	// 行 7/8 合并，保留更高严重度的 error（Msg=e1）。
	if out[0].Msg != "e1" {
		t.Fatalf("应保留 error 严重度: %+v", out[0])
	}
	if out[1].Msg != "far" {
		t.Fatalf("远处 finding 不应合并: %+v", out[1])
	}
}

func TestDedupKeepsSeverity(t *testing.T) {
	fs := []Finding{
		{File: "a.go", Line: 5, Severity: "info", Msg: "i"},
		{File: "a.go", Line: 7, Severity: "error", Msg: "e"},
	}
	out := Dedup(fs, 3)
	if len(out) != 1 || out[0].Severity != "error" {
		t.Fatalf("应保留 error: %+v", out)
	}
}

func TestDedupSeparateFiles(t *testing.T) {
	fs := []Finding{
		{File: "a.go", Line: 7, Msg: "a"},
		{File: "b.go", Line: 7, Msg: "b"},
	}
	if out := Dedup(fs, 3); len(out) != 2 {
		t.Fatalf("不同文件不应合并: %+v", out)
	}
}
