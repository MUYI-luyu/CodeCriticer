package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

// staticBugSrc 含一个确定性问题：printf 动词 %d 用于 string。
const staticBugSrc = `package m

import "fmt"

func Greet(name string) {
	fmt.Printf("%d", name)
}
`

// writeStaticRepo 构造一个含确定性问题的最小 Go 仓库，返回其路径。
func writeStaticRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(staticBugSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStaticRulesTool(t *testing.T) {
	repo := writeStaticRepo(t)
	res, err := runStaticRules(repo, "", "all")
	if err != nil {
		t.Fatalf("runStaticRules: %v", err)
	}
	if res.Count == 0 {
		t.Fatal("expected at least 1 finding, got 0")
	}
	if len(res.Findings) != res.Count {
		t.Fatalf("count=%d != len(findings)=%d", res.Count, len(res.Findings))
	}

	found := false
	for _, f := range res.Findings {
		if f.Rule != "printf" {
			continue
		}
		found = true
		if f.File == "" || f.Line == 0 {
			t.Fatalf("printf finding 缺位置: %+v", f)
		}
		if f.Hint == "" {
			t.Fatalf("printf finding 缺规则说明: %+v", f)
		}
		if f.Symbol != "Greet" {
			t.Fatalf("printf finding 符号应为 Greet: %+v", f)
		}
	}
	if !found {
		t.Fatalf("未发现 printf 规则命中: %+v", res.Findings)
	}
}

func TestStaticRulesToolExecute(t *testing.T) {
	repo := writeStaticRepo(t)
	tool := NewStaticRulesTool(repo, "")
	got, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m, ok := got.(map[string]interface{})
	if !ok {
		t.Fatalf("Execute 返回类型: %T", got)
	}
	findings, ok := m["findings"].([]review.Finding)
	if !ok || len(findings) == 0 {
		t.Fatalf("确定性发现未并入 findings: %+v", m)
	}
	hasPrintf := false
	for _, f := range findings {
		if f.Symbol == "printf" && f.Msg != "" {
			hasPrintf = true
		}
	}
	if !hasPrintf {
		t.Fatalf("findings 缺 printf: %+v", findings)
	}
}

func TestStaticRulesToolNoRepo(t *testing.T) {
	tool := NewStaticRulesTool("", "")
	if _, err := tool.Execute(context.Background(), nil); err == nil {
		t.Fatal("缺少 repo 应报错")
	}
}

func TestStaticRulesToolIntegration(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(NewStaticRulesTool("/tmp/repo", ""))
	if _, ok := reg.Get("static_rules"); !ok {
		t.Fatal("static_rules 未注册")
	}

	names := map[string]bool{}
	for _, s := range reg.List() {
		names[s.Name()] = true
	}
	if !names["static_rules"] {
		t.Fatalf("registry 列表缺少 static_rules: %+v", reg.List())
	}
}

func TestStaticScope(t *testing.T) {
	if staticScope(nil) != "all" {
		t.Fatal("默认应为 all")
	}
	if staticScope(map[string]interface{}{"scope": "diff"}) != "diff" {
		t.Fatal("diff 未识别")
	}
	if staticScope(map[string]interface{}{"scope": "DIFF"}) != "diff" {
		t.Fatal("大小写不敏感失败")
	}
	if staticScope(map[string]interface{}{"scope": "unknown"}) != "all" {
		t.Fatal("未知值应回退到 all")
	}
}

func TestStaticSeverity(t *testing.T) {
	if staticSeverity("") != "warning" {
		t.Fatal("空 severity 应归一化为 warning")
	}
	if staticSeverity("  ") != "warning" {
		t.Fatal("空白 severity 应归一化为 warning")
	}
	if staticSeverity("error") != "error" {
		t.Fatal("error 应保留")
	}
}

func TestFilterByDiff(t *testing.T) {
	findings := []review.Finding{
		{File: "a.go", Line: 4, Symbol: "printf", Severity: "warning", Msg: "bad"},
		{File: "b.go", Line: 2, Symbol: "printf", Severity: "warning", Msg: "bad"},
	}
	raw := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n"
	got := filterByDiff(findings, raw)
	if len(got) != 1 || got[0].File != "a.go" {
		t.Fatalf("filterByDiff = %+v", got)
	}

	// diff 只涉及 b.go 时，只保留 b.go。
	rawB := "diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n"
	got = filterByDiff(findings, rawB)
	if len(got) != 1 || got[0].File != "b.go" {
		t.Fatalf("filterByDiff(b.go) = %+v", got)
	}
}

func TestSortStaticFindings(t *testing.T) {
	fs := []StaticFinding{
		{File: "b.go", Line: 9, Severity: "warning"},
		{File: "a.go", Line: 2, Severity: "error"},
		{File: "a.go", Line: 1, Severity: "warning"},
	}
	sortStaticFindings(fs)

	// error 应排最前。
	if fs[0].Severity != "error" || fs[0].File != "a.go" || fs[0].Line != 2 {
		t.Fatalf("首条应为 a.go:2 (error): %+v", fs[0])
	}
	// 同 severity 按文件排序，再按行排序。
	if fs[1].File != "a.go" || fs[1].Line != 1 {
		t.Fatalf("第二条应为 a.go:1: %+v", fs[1])
	}
	if fs[2].File != "b.go" || fs[2].Line != 9 {
		t.Fatalf("第三条应为 b.go:9: %+v", fs[2])
	}
}

func TestFormatStaticResult(t *testing.T) {
	r := StaticRuleResult{Count: 2, Findings: []StaticFinding{
		{File: "a.go", Line: 4, Rule: "printf", Severity: "warning", Message: "bad verb", Symbol: "Greet", Context: "func Greet(name string)"},
		{File: "b.go", Line: 7, Rule: "copylock", Severity: "warning", Message: "passes lock"},
	}}
	text := formatStaticResult(r)
	for _, want := range []string{"count=2", "printf=1", "copylock=1", "Greet", "bad verb", "passes lock"} {
		if !strings.Contains(text, want) {
			t.Fatalf("formatStaticResult 缺 %q: %q", want, text)
		}
	}
}

func TestGroupStaticByRule(t *testing.T) {
	fs := []StaticFinding{
		{Rule: "printf"}, {Rule: "printf"}, {Rule: "copylock"},
	}
	got := groupStaticByRule(fs)
	for _, want := range []string{"printf=2", "copylock=1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("groupStaticByRule 缺 %q: %q", want, got)
		}
	}
}

func TestRuleHint(t *testing.T) {
	// 18 个规则都应有非空说明。
	for rule := range ruleHints {
		if ruleHint(rule) == "" {
			t.Fatalf("规则 %q 缺少说明", rule)
		}
	}
	// 辅助分析器不产生诊断，说明为空。
	if ruleHint("inspect") != "" {
		t.Fatal("inspect 不应有诊断说明")
	}
	// 未知规则给通用提示。
	if ruleHint("unknown_rule") == "" {
		t.Fatal("未知规则应有通用提示")
	}
}

func TestInDiffSpan(t *testing.T) {
	ranges := map[string][]lineRange{
		"a.go": {{start: 4, end: 8}},
	}
	if !inDiffSpan(ranges, "a.go", 5) {
		t.Fatal("第 5 行应在区间内")
	}
	if inDiffSpan(ranges, "a.go", 3) {
		t.Fatal("第 3 行不应在区间内")
	}
	if !inDiffSpan(ranges, "a.go", 8) {
		t.Fatal("边界第 8 行应在区间内")
	}
	if inDiffSpan(ranges, "b.go", 5) {
		t.Fatal("b.go 无区间")
	}
}

func TestDiffSymbolRanges(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package m\n\nfunc Foo() {\n\tx := 1\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

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
	ranges := diffSymbolRanges(dir, raw)
	if len(ranges["a.go"]) != 1 {
		t.Fatalf("ranges=%+v", ranges)
	}
	if ranges["a.go"][0].start != 3 || ranges["a.go"][0].end != 5 {
		t.Fatalf("Foo 区间应为 [3,5]: %+v", ranges["a.go"][0])
	}

	// 空 repo / 空 diff 时返回 nil。
	if diffSymbolRanges("", raw) != nil {
		t.Fatal("空 repo 应返回 nil")
	}
	if diffSymbolRanges(dir, "") != nil {
		t.Fatal("空 diff 应返回 nil")
	}
}
