package eval

import (
	"strings"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/diff"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

func TestLoadCases(t *testing.T) {
	cases, err := Load("testdata/cases")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 15 {
		t.Fatalf("期望 15 个用例，得到 %d", len(cases))
	}
	for _, c := range cases {
		if len(c.Bugs()) == 0 {
			t.Fatalf("%s: 未提取到 bug", c.Name)
		}
		if strings.Contains(c.Repo["main.go"], "// BUG") {
			t.Fatalf("%s: 标记未剥离", c.Name)
		}
		if _, err := diff.Parse(c.Diff); err != nil {
			t.Fatalf("%s: diff 解析失败: %v", c.Name, err)
		}
	}
}

// 确定性规则应命中标注为 rule 的用例，且定位在 bug 附近。
func TestRuleCatches(t *testing.T) {
	cases, err := Load("testdata/cases")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.Rule == "" {
			continue
		}
		// Go 1.22+ 修复了循环变量捕获问题，loopclosure 不再触发
		if c.Rule == "loopclosure" {
			t.Logf("%s: 跳过（Go 1.22+ 已修复循环变量问题）", c.Name)
			continue
		}
		repo, err := materialize(c)
		if err != nil {
			t.Fatal(err)
		}
		fs, err := review.Rules(repo)
		if err != nil {
			t.Fatalf("%s: Rules: %v", c.Name, err)
		}
		if !hitRule(fs, c.Rule, c.Bugs(), 3) {
			t.Fatalf("%s: 规则 %s 未命中 bug %+v（findings=%+v）", c.Name, c.Rule, c.Bugs(), fs)
		}
	}
}

// hitRule 判断 findings 里存在指定规则且位置命中任一 bug。
func hitRule(fs []review.Finding, rule string, bugs []Bug, tol int) bool {
	for _, f := range fs {
		if f.Symbol != rule {
			continue
		}
		for _, b := range bugs {
			if f.File == b.File && abs(f.Line-b.Line) <= tol {
				return true
			}
		}
	}
	return false
}
