// Package diff 解析 unified diff，产出文件级变更。
package diff

import (
	"bytes"
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
	File string // 新文件路径
	Old  string // 旧文件路径
	Adds []Line // 新增行
	Dels []Line // 删除行
}

// Parse 把 unified diff 解析为变更列表。
func Parse(data []byte) ([]Change, error) {
	fds, err := sgd.ParseMultiFileDiff(data)
	if err != nil {
		return nil, err
	}
	cs := make([]Change, 0, len(fds))
	for _, fd := range fds {
		c := Change{File: stripPath(fd.NewName), Old: stripPath(fd.OrigName)}
		for _, h := range fd.Hunks {
			c.fill(h)
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
func (c *Change) fill(h *sgd.Hunk) {
	oldN, newN := int(h.OrigStartLine), int(h.NewStartLine)
	for _, raw := range bytes.Split(h.Body, []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		switch raw[0] {
		case ' ':
			oldN++
			newN++
		case '-':
			c.Dels = append(c.Dels, Line{No: oldN, Text: string(raw[1:])})
			oldN++
		case '+':
			c.Adds = append(c.Adds, Line{No: newN, Text: string(raw[1:])})
			newN++
		}
	}
}
