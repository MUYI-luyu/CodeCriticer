// Package recall 提供多路代码召回：符号引用（查调用图）+ 关键词（rg）。
package recall

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/graph"
)

const limit = 20

// Doc 是召回到的代码片段。
type Doc struct {
	File string
	Line int
	Text string
	Src  string // symbol / keyword
}

// Store 是召回上下文：仓库根 + 调用图。
type Store struct {
	root string
	idx  *graph.Index
}

func New(root string, idx *graph.Index) *Store {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return &Store{root: abs, idx: idx}
}

// Symbol 召回被改符号的直接调用方代码。
func (s *Store) Symbol(name, file string) []Doc {
	if s.idx == nil {
		return nil
	}
	callers := s.idx.Callers(graph.SymbolRef{Name: name, File: file})
	docs := make([]Doc, 0, len(callers))
	for _, c := range callers {
		docs = append(docs, Doc{File: c.File, Line: c.Line, Text: s.readAt(c.File, c.Line), Src: "symbol"})
	}
	return docs
}

// Keyword 用 rg 搜索关键词，返回匹配片段。
func (s *Store) Keyword(word string) []Doc {
	if word == "" {
		return nil
	}
	ms := rgSearch(s.root, word)
	docs := make([]Doc, 0, len(ms))
	for _, m := range ms {
		docs = append(docs, Doc{File: m.file, Line: m.no, Text: m.text, Src: "keyword"})
		if len(docs) >= limit {
			break
		}
	}
	return docs
}

func (s *Store) readAt(file string, line int) string {
	return ReadLines(s.root, file, line)
}

// ReadLines 读文件第 line 行附近 ±3 行，file 可为相对或绝对路径。
func ReadLines(root, file string, line int) string {
	if !filepath.IsAbs(file) {
		file = filepath.Join(root, file)
	}
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var lines []string
	no := 0
	for sc.Scan() {
		no++
		if no >= line-3 && no <= line+3 {
			lines = append(lines, sc.Text())
		}
		if no > line+3 {
			break
		}
	}
	return strings.Join(lines, "\n")
}

type match struct {
	file string
	no   int
	text string
}

func rgSearch(root, word string) []match {
	out, err := exec.Command("rg", "-n", "--no-heading", word, root).Output()
	if err == nil {
		return parseRg(out)
	}
	return walkSearch(root, word)
}

func parseRg(out []byte) []match {
	var ms []match
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), ":", 3)
		if len(parts) != 3 {
			continue
		}
		no, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		ms = append(ms, match{file: parts[0], no: no, text: parts[2]})
	}
	return ms
}

// walkSearch 是 rg 不可用时的朴素扫描。
func walkSearch(root, word string) []match {
	var ms []match
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		no := 0
		for sc.Scan() {
			no++
			if strings.Contains(sc.Text(), word) {
				ms = append(ms, match{file: path, no: no, text: sc.Text()})
			}
		}
		return nil
	})
	return ms
}
