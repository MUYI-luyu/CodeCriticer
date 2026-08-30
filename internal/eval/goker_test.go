package eval

import (
	"os"
	"strings"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/diff"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

// TestLoadGoker 验证 GoKer 生成的用例能被正确加载（diff 为新增文件格式、标注已剥离）。
func TestLoadGoker(t *testing.T) {
	cases, err := Load("testdata/goker")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 103 {
		t.Fatalf("期望 103 个用例，得到 %d", len(cases))
	}

	var exact, fileLevel int
	for _, c := range cases {
		if len(c.Bugs) == 0 {
			t.Fatalf("%s: 未提取到 bug", c.Name)
		}
		for _, b := range c.Bugs {
			if b.Line > 0 {
				exact++
			} else {
				fileLevel++
			}
		}

		changes, err := diff.Parse(c.Diff)
		if err != nil {
			t.Fatalf("%s: diff 解析失败: %v", c.Name, err)
		}
		if len(changes) != 1 || changes[0].File == "" {
			t.Fatalf("%s: diff 应含单个文件变更，得到 %+v", c.Name, changes)
		}
		if len(changes[0].Adds) == 0 {
			t.Fatalf("%s: 新增文件 diff 应有 Adds", c.Name)
		}

		if len(c.Repo) != 1 {
			t.Fatalf("%s: 期望单文件 repo，得到 %d", c.Name, len(c.Repo))
		}
		for name, content := range c.Repo {
			for _, mark := range []string{"// block here", "// Block here", "// Missing", "// G1", "// LockA"} {
				if strings.Contains(content, mark) {
					t.Fatalf("%s: 标注注释 %q 未剥离 (%s)", c.Name, mark, name)
				}
			}
		}
	}
	t.Logf("GoKer 用例加载正常: 精确行 %d, 文件级 %d", exact, fileLevel)
}

// TestComputeFileLevel 验证 line=0 的文件级 ground truth：同文件即命中，异文件即误报。
func TestComputeFileLevel(t *testing.T) {
	bugs := []Bug{{File: "a.go", Line: 0}}
	fs := []review.Finding{{File: "a.go", Line: 100}}
	if m := Compute(bugs, fs, 3); m.True != 1 || m.False != 0 || m.Found != 1 {
		t.Fatalf("文件级应命中: %+v", m)
	}

	m := Compute([]Bug{{File: "a.go", Line: 0}}, []review.Finding{{File: "b.go", Line: 100}}, 3)
	if m.True != 0 || m.False != 1 {
		t.Fatalf("文件不符应误报: %+v", m)
	}
}

// TestGokerCompilable 验证剥离后的代码可完整类型检查（packages.Load），确保 eval 能真正跑起来。
func TestGokerCompilable(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过编译验证（-short）")
	}
	cases, err := Load("testdata/goker")
	if err != nil {
		t.Fatal(err)
	}
	var failed int
	for _, c := range cases {
		repo, err := materialize(c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := review.Rules(repo); err != nil {
			t.Errorf("%s: 编译失败: %v", c.Name, err)
			failed++
		}
		os.RemoveAll(repo)
	}
	if failed > 0 {
		t.Fatalf("%d 个用例无法编译", failed)
	}
	t.Logf("103 个 GoKer 用例均通过编译检查")
}
