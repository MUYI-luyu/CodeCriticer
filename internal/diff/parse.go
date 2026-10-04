// Package diff 解析 unified diff，产出文件级变更。
package diff

import (
	"bytes"
	"fmt"
	"strings"

	sgd "github.com/sourcegraph/go-diff/diff"
)

// Line 是一行变更，No 为该行在新文件（增）或旧文件（删）的行号。
type Line struct {
	No   int
	Text string
}

// Change 是单个文件的变更。
type Change struct {
	File    string   // 新文件路径
	Old     string   // 旧文件路径
	Adds    []Line   // 新增行
	Dels    []Line   // 删除行
	Symbols []Symbol // Annotate 填充
	Hunks   []Hunk   // 完整 unified diff hunk，保留上下文和新旧行号
}

// Hunk 是可独立提供给 Agent 的完整变更块。ID 在单个文件内稳定。
type Hunk struct {
	ID       string
	OldStart int
	OldLines int
	NewStart int
	NewLines int
	Section  string
	Lines    []HunkLine
}

// HunkLine 同时保存旧、新文件坐标。不存在的一侧行号为 0。
type HunkLine struct {
	Kind    string // context / add / delete / metadata
	OldLine int
	NewLine int
	Text    string
	Symbol  string
}

// Parse 把 unified diff 解析为变更列表，并自动标注符号信息。
// 如果提供了 repoPath，会尝试读取文件内容进行 AST 解析。
func Parse(data []byte) ([]Change, error) {
	fds, err := sgd.ParseMultiFileDiff(data)
	if err != nil {
		return nil, err
	}
	cs := make([]Change, 0, len(fds))
	for _, fd := range fds {
		c := Change{File: stripPath(fd.NewName), Old: stripPath(fd.OrigName)}
		for i, h := range fd.Hunks {
			c.fill(h, i+1)
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// stripPath 去掉 git diff 的 a/ b/ 前缀；/dev/null 原样保留。
func stripPath(p string) string {
	for _, pre := range []string{"a/", "b/"} {
		if strings.HasPrefix(p, pre) {
			return p[len(pre):]
		}
	}
	return p
}

// fill 按 hunk 头行号把正文逐行归入新增/删除。
func (c *Change) fill(h *sgd.Hunk, index int) {
	oldN, newN := int(h.OrigStartLine), int(h.NewStartLine)
	hunk := Hunk{
		ID:       fmt.Sprintf("h%d", index),
		OldStart: oldN,
		OldLines: int(h.OrigLines),
		NewStart: newN,
		NewLines: int(h.NewLines),
		Section:  h.Section,
	}
	for _, raw := range bytes.Split(h.Body, []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		switch raw[0] {
		case ' ':
			hunk.Lines = append(hunk.Lines, HunkLine{Kind: "context", OldLine: oldN, NewLine: newN, Text: string(raw[1:])})
			oldN++
			newN++
		case '-':
			c.Dels = append(c.Dels, Line{No: oldN, Text: string(raw[1:])})
			hunk.Lines = append(hunk.Lines, HunkLine{Kind: "delete", OldLine: oldN, Text: string(raw[1:])})
			oldN++
		case '+':
			c.Adds = append(c.Adds, Line{No: newN, Text: string(raw[1:])})
			hunk.Lines = append(hunk.Lines, HunkLine{Kind: "add", NewLine: newN, Text: string(raw[1:])})
			newN++
		case '\\':
			hunk.Lines = append(hunk.Lines, HunkLine{Kind: "metadata", Text: string(raw)})
		}
	}
	c.Hunks = append(c.Hunks, hunk)
}
