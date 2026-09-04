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

// Bug 是一处真实缺陷的定位（向后兼容的简单视图）。
type Bug struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Desc string `json:"desc"`
}

// Location 是代码中的一个位置点。
type Location struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Symbol string `json:"symbol,omitempty"` // 函数名/变量名
}

// GroundTruth 是缺陷的完整标注（单一事实来源）。
type GroundTruth struct {
	Primary  Location   `json:"primary"`             // 主缺陷位置
	Related  []Location `json:"related,omitempty"`   // 相关位置（多位置证据链）
	Symbols  []string   `json:"symbols,omitempty"`   // 涉及的符号（函数/变量名）
	BugTypes []string   `json:"bug_types,omitempty"` // Bug 类型（可多标签）
	Evidence string     `json:"evidence,omitempty"`  // 人工标注的关键证据
}

// Metadata 是数据集原始提供的元信息（不可变事实）。
type Metadata struct {
	Project         string `json:"project,omitempty"`           // "kubernetes" / "etcd" / ...
	IssueURL        string `json:"issue_url,omitempty"`         // 原始 issue 链接
	CommitSHA       string `json:"commit,omitempty"`            // buggy commit
	OriginalRepoLOC int    `json:"original_repo_loc,omitempty"` // 原始 repo 总行数（如果已知）
}

// Case 是单个评测用例：buggy 为含缺陷代码，fixed 为修复后代码。
type Case struct {
	Name   string
	Source string
	Rule   string            // 能确定性命中的规则名，无则 LLM 兜底
	Repo   map[string]string // 文件名 → buggy 内容（写入仓库）
	Diff   []byte            // 引入 bug 的 unified diff

	// === 单一事实来源：GroundTruth ===
	GT GroundTruth `json:"ground_truth"`

	// === 原始元信息 ===
	Metadata *Metadata `json:"metadata,omitempty"`
}

// Bugs 返回向后兼容的 Bug[] 视图（从 GT 转换）。
func (c *Case) Bugs() []Bug {
	var bugs []Bug
	bugs = append(bugs, Bug{
		File: c.GT.Primary.File,
		Line: c.GT.Primary.Line,
	})
	for _, loc := range c.GT.Related {
		bugs = append(bugs, Bug{
			File: loc.File,
			Line: loc.Line,
		})
	}
	return bugs
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
	// 无论哪种方式，都要 strip 标记（避免污染测试代码）。
	clean, extractedBugs := stripMarks(string(buggyRaw))
	bugs := m.Bugs
	if len(bugs) == 0 {
		bugs = extractedBugs // 使用从注释提取的 bugs
	}
	lineCount := len(strings.Split(clean, "\n"))
	for i := range bugs {
		if bugs[i].Line > lineCount {
			bugs[i].Line = 0
		}
	}
	file := m.File
	if file == "" {
		file = "main.go"
	}
	diff, err := unifiedDiff(string(fixed), clean, file)
	if err != nil {
		return nil, err
	}

	// 转换 Bug[] → GroundTruth（单一事实来源）
	gt := GroundTruth{}
	if len(bugs) > 0 {
		gt.Primary = Location{
			File: bugs[0].File,
			Line: bugs[0].Line,
		}
		for i := 1; i < len(bugs); i++ {
			gt.Related = append(gt.Related, Location{
				File: bugs[i].File,
				Line: bugs[i].Line,
			})
		}
	}

	// 从 Rule 推断 BugType（不假设）
	gt.BugTypes = inferBugTypesFromRule(m.Rule, m.Name)

	return &Case{
		Name:   m.Name,
		Source: m.Source,
		Rule:   m.Rule,
		Repo:   map[string]string{file: clean},
		Diff:   diff,
		GT:     gt,
	}, nil
}

// inferBugTypesFromRule 从 Rule 字段推断 BugType（不假设，无法推断时返回 unknown）。
func inferBugTypesFromRule(rule, name string) []string {
	if rule != "" {
		switch rule {
		case "copylock", "lostcancel", "loopclosure":
			return []string{"concurrency"}
		case "printf", "unusedresult":
			return []string{"local_logic"}
		}
	}

	// 从 name 关键词推断
	text := strings.ToLower(name)
	if containsAny(text, "blocking", "deadlock", "race", "goroutine", "channel", "mutex") {
		return []string{"concurrency"}
	}
	if containsAny(text, "nil", "bounds", "panic") {
		return []string{"local_logic"}
	}

	// 无法推断
	return []string{"unknown"}
}

func containsAny(text string, keywords ...string) bool {
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
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
// fixed 为空时生成"整个文件新增"的 diff，用于 GoKer 这类只有 buggy 无 fixed 的数据源。
func unifiedDiff(fixed, buggy, file string) ([]byte, error) {
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

	out, err := exec.Command("diff", "-u", "--label", "a/"+file, "--label", "b/"+file, a, b).Output()
	if err != nil {
		// diff 返回 1 表示有差异，属正常；>1 才是错误。
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return out, nil
		}
		return nil, err
	}
	return out, nil
}
