package recall

import (
	"fmt"
)

// Finding 是 EvidencePool 需要的最小接口，避免循环依赖。
type Finding struct {
	File string
	Line int
	Msg  string
}

// EvidencePool 保存 Plan 阶段每个 finding 来源的召回证据，供 Validate 复用。
type EvidencePool struct {
	contexts map[string][]Doc // "file:line:msg" → docs
}

// NewEvidencePool 构建证据池，findings 与 docsPerFinding 一一对应。
func NewEvidencePool(findings []Finding, docsPerFinding [][]Doc) *EvidencePool {
	if len(findings) != len(docsPerFinding) {
		panic(fmt.Sprintf("NewEvidencePool: length mismatch: findings=%d, docs=%d", len(findings), len(docsPerFinding)))
	}

	pool := &EvidencePool{contexts: make(map[string][]Doc)}
	for i, f := range findings {
		key := fmt.Sprintf("%s:%d:%s", f.File, f.Line, f.Msg)
		pool.contexts[key] = docsPerFinding[i]
	}
	return pool
}

// AllDocs 返回池内全部召回片段（去重后），供离线观测/归因使用。
func (p *EvidencePool) AllDocs() []Doc {
	if p == nil {
		return nil
	}
	var all []Doc
	for _, docs := range p.contexts {
		all = append(all, docs...)
	}
	return Dedup(all)
}

// FindContext 根据 finding 的 file:line:msg 精确查找对应的召回证据。
func (p *EvidencePool) FindContext(file string, line int, msg string) []Doc {
	if p == nil {
		return nil
	}
	key := fmt.Sprintf("%s:%d:%s", file, line, msg)
	if docs, ok := p.contexts[key]; ok {
		return docs
	}
	return nil
}

// Dedup 按 file:line 去重，保留 Text 更长的（Symbol ±3行 > Keyword 单行）。
func Dedup(docs []Doc) []Doc {
	seen := make(map[string]int) // "file:line" → index in result
	var result []Doc

	for _, d := range docs {
		key := fmt.Sprintf("%s:%d", d.File, d.Line)
		if idx, ok := seen[key]; ok {
			// 保留 Text 更长的
			if len(d.Text) > len(result[idx].Text) {
				result[idx] = d
			}
		} else {
			seen[key] = len(result)
			result = append(result, d)
		}
	}
	return result
}

// FilterSameFile 优先保留同文件的 docs，截断到 max。
func FilterSameFile(docs []Doc, targetFile string, max int) []Doc {
	if len(docs) <= max {
		return docs
	}

	var sameFile, others []Doc
	for _, d := range docs {
		if d.File == targetFile {
			sameFile = append(sameFile, d)
		} else {
			others = append(others, d)
		}
	}

	if len(sameFile) >= max {
		return sameFile[:max]
	}

	result := append([]Doc{}, sameFile...)
	remaining := max - len(sameFile)
	if len(others) > remaining {
		others = others[:remaining]
	}
	result = append(result, others...)
	return result
}
