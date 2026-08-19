package review

import "sort"

// Dedup 合并同一文件内相距 ≤ window 行的 finding，保留最高严重度。
func Dedup(fs []Finding, window int) []Finding {
	byFile := map[string][]Finding{}
	var order []string
	for _, f := range fs {
		if _, ok := byFile[f.File]; !ok {
			order = append(order, f.File)
		}
		byFile[f.File] = append(byFile[f.File], f)
	}
	var out []Finding
	for _, file := range order {
		out = append(out, dedupFile(byFile[file], window)...)
	}
	return out
}

func dedupFile(fs []Finding, window int) []Finding {
	sort.SliceStable(fs, func(i, j int) bool {
		if sevRank(fs[i].Severity) != sevRank(fs[j].Severity) {
			return sevRank(fs[i].Severity) > sevRank(fs[j].Severity)
		}
		return fs[i].Line < fs[j].Line
	})
	var out []Finding
	for _, f := range fs {
		if i := nearLine(out, f.Line, window); i >= 0 {
			if sevRank(f.Severity) > sevRank(out[i].Severity) {
				out[i] = f
			}
			continue
		}
		out = append(out, f)
	}
	return out
}

// nearLine 返回与 line 相距 ≤ window 的已有 finding 下标。
func nearLine(fs []Finding, line, window int) int {
	for i := range fs {
		if absInt(fs[i].Line-line) <= window {
			return i
		}
	}
	return -1
}

func sevRank(s string) int {
	switch s {
	case "error":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	}
	return 0
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
