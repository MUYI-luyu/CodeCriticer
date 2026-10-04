// Package recall 提供多路代码召回：符号引用（查调用图）+ 关键词（rg）。
package recall

import (
	"bufio"
	"errors"
	"fmt"
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
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
	Src  string `json:"src"` // symbol / keyword
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

// Root 返回仓库根路径（用于外部访问）。
func (s *Store) Root() string {
	return s.root
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
	docs, _ := s.Search(word)
	return docs
}

// Search performs a literal Go-source search. It distinguishes a valid empty
// result from an operational failure so callers never turn tool failure into
// false absence evidence.
func (s *Store) Search(word string) ([]Doc, error) {
	if word == "" {
		return nil, fmt.Errorf("empty search keyword")
	}
	ms, err := rgSearch(s.root, word)
	if err != nil {
		return nil, err
	}
	docs := make([]Doc, 0, len(ms))
	for _, m := range ms {
		docs = append(docs, Doc{File: m.file, Line: m.no, Text: ReadLines(s.root, m.file, m.no), Src: "keyword"})
	}
	return docs, nil
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

func rgSearch(root, word string) ([]match, error) {
	out, err := exec.Command("rg", "-n", "--fixed-strings", "--no-heading", "--glob", "*.go", "--glob", "!.git/**", "--glob", "!logs/**", "--glob", "!参考项目/**", "--glob", "!文档/**", "--glob", "!重构codecritic/**", "--max-count", "200", "--", word, root).Output()
	if err == nil {
		return parseRg(out), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("rg search failed: %w", err)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return walkSearch(root, word), nil
	}
	return nil, fmt.Errorf("start rg: %w", err)
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
