package eval

import (
	"strings"
)

// CaseDimension 是运行时计算的派生维度（不存储在 Case 中）。
type CaseDimension struct {
	// Scale
	RepoLOC     int    `json:"repo_loc"`      // Case.Repo 的行数
	DiffLOC     int    `json:"diff_loc"`      // diff 修改行数（changed lines）
	RelevantLOC int    `json:"relevant_loc"`  // GT 相关代码行数
	ScaleLabel  string `json:"scale_label"`   // "100_LOC" / "1K_LOC" / ...

	// Scope
	Files      int    `json:"files"`
	Packages   int    `json:"packages"`
	ScopeLabel string `json:"scope_label"` // "single_function" / "single_file" / "multi_file" / "cross_package"
}

// ComputeDimension 从 Case 计算维度（每次 eval 都重新计算）。
func ComputeDimension(c *Case) CaseDimension {
	d := CaseDimension{}

	// 1. RepoLOC：Case.Repo 的实际内容
	for _, content := range c.Repo {
		d.RepoLOC += countLines(content)
	}

	// 2. DiffLOC：changed lines（+ 和 - 都算）
	d.DiffLOC = countDiffChangedLines(c.Diff)

	// 3. RelevantLOC：暂时用 RepoLOC（精确计算需要 tree-sitter，留到需要时再做）
	d.RelevantLOC = d.RepoLOC

	// 4. 分类 Scale（优先用 Metadata.OriginalRepoLOC）
	d.ScaleLabel = classifyScale(d.RepoLOC, c.Metadata)

	// 5. Scope：简化版（按文件数和 package 数分类）
	d.Files = len(c.Repo)
	d.Packages = inferPackageCount(c.Repo)
	d.ScopeLabel = classifyScope(d.Files, d.Packages)

	return d
}

// countLines 统计行数。
func countLines(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}

// countDiffChangedLines 统计 diff 中的修改行数（+ 和 - 行）。
func countDiffChangedLines(diff []byte) int {
	lines := 0
	for _, line := range strings.Split(string(diff), "\n") {
		if len(line) == 0 {
			continue
		}
		// 统计 + 和 - 行，但排除 +++ / --- 头部
		if (line[0] == '+' || line[0] == '-') && !strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") {
			lines++
		}
	}
	return lines
}

// inferPackageCount 推断 package 数量。
func inferPackageCount(repo map[string]string) int {
	packages := make(map[string]bool)
	for path := range repo {
		// 简化：按目录分 package
		dir := path
		if idx := strings.LastIndex(path, "/"); idx >= 0 {
			dir = path[:idx]
		} else {
			dir = "." // 根目录
		}
		packages[dir] = true
	}
	return len(packages)
}

// classifyScale 分类规模（优先用 OriginalRepoLOC）。
func classifyScale(repoLOC int, meta *Metadata) string {
	// 如果数据集提供了原始 repo 规模，用它
	if meta != nil && meta.OriginalRepoLOC > 0 {
		loc := meta.OriginalRepoLOC
		return categorizeLOC(loc)
	}

	// 否则用 Case.Repo 的实际行数
	return categorizeLOC(repoLOC)
}

func categorizeLOC(loc int) string {
	switch {
	case loc < 1000:
		return "100_LOC"
	case loc < 10000:
		return "1K_LOC"
	case loc < 100000:
		return "10K_LOC"
	case loc < 1000000:
		return "100K_LOC"
	default:
		return "1M_LOC"
	}
}

// classifyScope 分类依赖范围（简化版：按文件数和 package 数）。
func classifyScope(files, packages int) string {
	if packages > 1 {
		return "cross_package"
	}
	if files > 1 {
		return "multi_file"
	}
	return "single_file" // 单文件（暂不区分 single_function）
}
