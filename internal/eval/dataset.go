// Package eval 用真实 bug→fix 用例评测审查器，产出 Recall/Precision/FP 指标。
package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Bug 是一处真实缺陷的定位。
type Bug struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Desc string `json:"desc"`
}

// Case 是单个评测用例：buggy 为含缺陷代码，fixed 为修复后代码。
type Case struct {
	Name   string
	Source string
	Rule   string            // 能确定性命中的规则名，无则 LLM 兜底
	Repo   map[string]string // 文件名 → buggy 内容（写入仓库）
	Diff   []byte            // 引入 bug 的 unified diff
	Bugs   []Bug
}

// Load 加载数据集目录，每个子目录是一个用例。
func Load(dir string) ([]*Case, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*Case
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		c, err := loadCase(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out = append(out, c)
	}
	return out, nil
}

type meta struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Rule   string `json:"rule,omitempty"`
	File   string `json:"file,omitempty"`
	Bugs   []Bug  `json:"bugs,omitempty"`
}

func loadCase(dir string) (*Case, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "case.json"))
	if err != nil {
		return nil, err
	}
	var m meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}

	buggyRaw, err := os.ReadFile(filepath.Join(dir, "buggy.go"))
	if err != nil {
		return nil, err
	}
	fixed, err := os.ReadFile(filepath.Join(dir, "fixed.go"))
	if err != nil {
		return nil, err
	}

	// 真实 PR 用例在 case.json 里显式标注 bug；否则回退扫 // BUG 标记。
	clean := string(buggyRaw)
	bugs := m.Bugs
	if len(bugs) == 0 {
		clean, bugs = stripMarks(clean)
	}
	diff, err := unifiedDiff(string(fixed), clean)
	if err != nil {
		return nil, err
	}

	file := m.File
	if file == "" {
		file = "main.go"
	}
	return &Case{
		Name:   m.Name,
		Source: m.Source,
		Rule:   m.Rule,
		Repo:   map[string]string{file: clean},
		Diff:   diff,
		Bugs:   bugs,
	}, nil
}

// stripMarks 移除行尾 // BUG: desc 注释，返回干净代码与缺陷列表。
func stripMarks(src string) (string, []Bug) {
	var clean []string
	var bugs []Bug
	for i, line := range strings.Split(src, "\n") {
		if k := strings.Index(line, "// BUG"); k >= 0 {
			desc := strings.TrimSpace(line[k+len("// BUG"):])
			desc = strings.TrimPrefix(desc, ":")
			desc = strings.TrimSpace(desc)
			bugs = append(bugs, Bug{File: "main.go", Line: i + 1, Desc: desc})
			line = strings.TrimRight(line[:k], " \t")
		}
		clean = append(clean, line)
	}
	return strings.Join(clean, "\n"), bugs
}

// unifiedDiff 生成 fixed → buggy 的 unified diff（即引入 bug 的 diff）。
func unifiedDiff(fixed, buggy string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "ccdiff")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	a := filepath.Join(dir, "a.go")
	b := filepath.Join(dir, "b.go")
	if err := os.WriteFile(a, []byte(fixed), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(b, []byte(buggy), 0o644); err != nil {
		return nil, err
	}

	out, err := exec.Command("diff", "-u", "--label", "a/main.go", "--label", "b/main.go", a, b).Output()
	if err != nil {
		// diff 返回 1 表示有差异，属正常；>1 才是错误。
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return out, nil
		}
		return nil, err
	}
	return out, nil
}
