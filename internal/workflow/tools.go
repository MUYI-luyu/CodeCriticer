package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MUYI-luyu/codecritic/internal/graph"
	"github.com/MUYI-luyu/codecritic/internal/recall"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

type Tool interface {
	Name() string
	Execute(context.Context, map[string]interface{}) ([]*Evidence, error)
}

type toolset struct {
	repo  string
	store *recall.Store
	index *graph.Index
}

func (t *toolset) Execute(ctx context.Context, name string, args map[string]interface{}) ([]*Evidence, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	switch name {
	case "read_code":
		return t.readCode(args)
	case "search_code":
		return t.searchCode(args)
	case "find_callers":
		return t.findCallers(args)
	case "run_static_rules":
		return t.staticRules()
	case "dataflow":
		return t.dataflow(args)
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

func (t *toolset) dataflow(args map[string]interface{}) ([]*Evidence, error) {
	name, _ := args["symbol"].(string)
	file, _ := args["file"].(string)
	if name == "" || file == "" {
		return nil, fmt.Errorf("dataflow requires symbol and file")
	}
	if strings.Contains(name, ".") {
		return nil, fmt.Errorf("dataflow 只接受函数或方法名，不接受字段符号 %q", name)
	}
	if t.index == nil {
		return nil, fmt.Errorf("dataflow unavailable: graph index is nil")
	}
	var err error
	file, err = repoRelativePath(t.repo, file)
	if err != nil {
		return nil, err
	}
	steps, err := t.index.DataFlow(graph.SymbolRef{Name: name, File: file})
	if err != nil {
		return nil, err
	}
	out := make([]*Evidence, 0, len(steps))
	for _, step := range steps {
		path, err := repoRelativePath(t.repo, step.File)
		if err != nil {
			return nil, fmt.Errorf("dataflow result path: %w", err)
		}
		out = append(out, &Evidence{Source: "dataflow", Type: step.Kind, Relation: "supports", File: path, Line: step.Line, Content: step.Detail, Symbol: step.Function})
	}
	return out, nil
}

func (t *toolset) readCode(args map[string]interface{}) ([]*Evidence, error) {
	file, _ := args["file"].(string)
	if file == "" {
		return nil, fmt.Errorf("read_code requires file")
	}
	file, err := repoRelativePath(t.repo, file)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(t.repo, filepath.Clean(file))
	if !strings.HasPrefix(path, filepath.Clean(t.repo)+string(os.PathSeparator)) {
		return nil, fmt.Errorf("file outside repository")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	start, end := readRange(args, bytes.Count(b, []byte("\n"))+1)
	lines := bytes.Split(b, []byte("\n"))
	if start > len(lines) {
		return nil, fmt.Errorf("read_code 起始行超出文件范围")
	}
	if end > len(lines) {
		end = len(lines)
	}
	content := bytes.Join(lines[start-1:end], []byte("\n"))
	return []*Evidence{{Source: "read_code", Type: "code", File: file, Line: start, EndLine: end, Content: string(content)}}, nil
}

func readRange(args map[string]interface{}, total int) (int, int) {
	start := numberArg(args, "start_line", 1)
	end := numberArg(args, "end_line", 0)
	if end <= 0 {
		end = start + 199
	}
	if end-start > 200 {
		end = start + 200
	}
	if start < 1 {
		start = 1
	}
	if end > total {
		end = total
	}
	return start, end
}

func numberArg(args map[string]interface{}, key string, fallback int) int {
	v, ok := args[key]
	if !ok {
		return fallback
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return fallback
	}
}

func (t *toolset) searchCode(args map[string]interface{}) ([]*Evidence, error) {
	word, _ := args["keyword"].(string)
	if word == "" {
		return nil, fmt.Errorf("search_code requires keyword")
	}
	if t.store == nil {
		return nil, fmt.Errorf("search_code unavailable: recall store is nil")
	}
	docs := t.store.Keyword(word)
	if file, ok := args["file"].(string); ok && file != "" {
		file, err := repoRelativePath(t.repo, file)
		if err != nil {
			return nil, err
		}
		filtered := docs[:0]
		for _, d := range docs {
			if filepath.ToSlash(d.File) == file {
				filtered = append(filtered, d)
			}
		}
		docs = filtered
	}
	out := make([]*Evidence, 0, len(docs))
	for _, d := range docs {
		out = append(out, &Evidence{Source: "search_code", Type: "search_result", File: d.File, Line: d.Line, Content: d.Text})
	}
	return out, nil
}

func (t *toolset) findCallers(args map[string]interface{}) ([]*Evidence, error) {
	name, _ := args["symbol"].(string)
	file, _ := args["file"].(string)
	if name == "" {
		return nil, fmt.Errorf("find_callers requires symbol")
	}
	if t.index == nil {
		return nil, fmt.Errorf("find_callers unavailable: graph index is nil")
	}
	if file != "" {
		var err error
		file, err = repoRelativePath(t.repo, file)
		if err != nil {
			return nil, err
		}
	}
	callers := t.index.Callers(graph.SymbolRef{Name: name, File: file})
	out := make([]*Evidence, 0, len(callers))
	for _, c := range callers {
		out = append(out, &Evidence{Source: "find_callers", Type: "call_chain", Relation: "supports", File: c.File, Line: c.Line, Content: c.Func, Symbol: c.Func})
	}
	return out, nil
}

func (t *toolset) staticRules() ([]*Evidence, error) {
	findings, err := review.Rules(t.repo)
	if err != nil {
		return nil, err
	}
	out := make([]*Evidence, 0, len(findings))
	for _, f := range findings {
		out = append(out, &Evidence{Source: "run_static_rules", Type: "static_finding", File: f.File, Line: f.Line, Content: f.Msg})
	}
	return out, nil
}

func encodeEvidence(evs []*Evidence) string { b, _ := json.Marshal(evs); return string(b) }
func toolCall(name string, args map[string]interface{}, fn func() ([]*Evidence, error)) (ToolCall, []*Evidence) {
	started := time.Now()
	ev, err := fn()
	tc := ToolCall{Tool: name, Args: args, Duration: time.Since(started)}
	if err != nil {
		tc.Error = err.Error()
	}
	return tc, ev
}
