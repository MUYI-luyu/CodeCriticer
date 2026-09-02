package workflow

import (
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
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

func (t *toolset) readCode(args map[string]interface{}) ([]*Evidence, error) {
	file, _ := args["file"].(string)
	if file == "" {
		return nil, fmt.Errorf("read_code requires file")
	}
	if filepath.IsAbs(file) {
		rel, err := filepath.Rel(t.repo, file)
		if err != nil {
			return nil, err
		}
		file = rel
	}
	path := filepath.Join(t.repo, filepath.Clean(file))
	if !strings.HasPrefix(path, filepath.Clean(t.repo)+string(os.PathSeparator)) {
		return nil, fmt.Errorf("file outside repository")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return []*Evidence{{Source: "read_code", Type: "code", File: file, Content: string(b)}}, nil
}

func (t *toolset) searchCode(args map[string]interface{}) ([]*Evidence, error) {
	word, _ := args["keyword"].(string)
	if word == "" {
		return nil, fmt.Errorf("search_code requires keyword")
	}
	if t.store == nil {
		return nil, nil
	}
	docs := t.store.Keyword(word)
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
		return nil, nil
	}
	callers := t.index.Callers(graph.SymbolRef{Name: name, File: file})
	out := make([]*Evidence, 0, len(callers))
	for _, c := range callers {
		out = append(out, &Evidence{Source: "find_callers", Type: "call_chain", File: c.File, Line: c.Line, Content: c.Func})
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
