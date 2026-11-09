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
	if len(cases) != 17 {
		t.Fatalf("期望 17 个用例，得到 %d", len(cases))
	}
	for _, c := range cases {
		if c.Category == CategorySemanticPositive && len(c.Bugs()) == 0 {
			t.Fatalf("%s: 未提取到 bug", c.Name)
		}
		if c.Category == CategoryConvention && c.PolicyID == "" {
			t.Fatalf("%s: convention 缺少 policy_id", c.Name)
		}
		if strings.Contains(c.Repo["main.go"], "// BUG") {
			t.Fatalf("%s: 标记未剥离", c.Name)
		}
		if _, err := diff.Parse(c.Diff); err != nil {
			t.Fatalf("%s: diff 解析失败: %v", c.Name, err)
		}
	}
}

func TestLoadPreservesCategoryAndLanguageMetadata(t *testing.T) {
	cases, err := Load("testdata/cases")
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]*Case, len(cases))
	for _, c := range cases {
		byName[c.Name] = c
	}
	if got := byName["range-capture-go122"]; got == nil || got.Category != CategoryNegative || got.GoVersion != "1.22" || len(got.Bugs()) != 0 {
		t.Fatalf("negative metadata=%+v", got)
	}
	if got := byName["context-leak"]; got == nil || got.Category != CategoryConvention || got.PolicyID != "context_cancel" {
		t.Fatalf("convention metadata=%+v", got)
	}
}

func TestLoadCuratedGoKerManifest(t *testing.T) {
	cases, err := Load("testdata/goker-curated")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 20 {
		t.Fatalf("期望 20 个精选 GoKer 用例，得到 %d", len(cases))
	}
	positives, negatives := 0, 0
	for _, c := range cases {
		switch c.Category {
		case CategorySemanticPositive:
			positives++
			if len(c.Bugs()) != 1 {
				t.Fatalf("%s: 正例缺失 ground truth", c.Name)
			}
		case CategoryNegative:
			negatives++
			if len(c.Bugs()) != 0 {
				t.Fatalf("%s: 负例不应带 ground truth", c.Name)
			}
		default:
			t.Fatalf("%s: 非法精选分类 %q", c.Name, c.Category)
		}
		if c.GoVersion != "1.22" {
			t.Fatalf("%s: Go 版本=%q，期望 1.22", c.Name, c.GoVersion)
		}
	}
	if positives != 17 || negatives != 3 {
		t.Fatalf("精选分布=%d positive, %d negative，期望 17/3", positives, negatives)
	}
	if got := byCaseName(cases, "blocking-etcd-6708"); got == nil || len(got.GT.AcceptableLocations) != 4 {
		t.Fatalf("manifest ground truth override=%+v", got)
	}
	if got := byCaseName(cases, "blocking-grpc-795"); got == nil || len(got.GT.AcceptableLocations) != 4 {
		t.Fatalf("grpc-795 alternate deadlock anchors=%+v", got)
	}
}

func byCaseName(cases []*Case, name string) *Case {
	for _, c := range cases {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// 确定性规则应命中标注为 rule 的用例，且定位在 bug 附近。
func TestRuleCatches(t *testing.T) {
	cases, err := Load("testdata/cases")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.Rule == "" || c.Category != CategorySemanticPositive {
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

// hitRule 判断 diagnostics 里存在指定规则且位置命中任一 bug。
func hitRule(fs []review.StaticDiagnostic, rule string, bugs []Bug, tol int) bool {
	for _, f := range fs {
		if f.Analyzer != rule {
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
