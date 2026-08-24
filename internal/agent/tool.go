package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/diff"
	"github.com/MUYI-luyu/codecritic/internal/graph"
	"github.com/MUYI-luyu/codecritic/internal/recall"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

// Tool 定义编排器可调用的能力。
type Tool interface {
	Name() string
	Description() string
	Execute(context.Context, *State, map[string]any) (string, error)
}

// ToolRegistry 负责注册和查找工具。
type ToolRegistry struct {
	tools map[string]Tool
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: map[string]Tool{}}
}

func (r *ToolRegistry) Register(t Tool) {
	if t == nil {
		return
	}
	if r.tools == nil {
		r.tools = map[string]Tool{}
	}
	r.tools[t.Name()] = t
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	t, ok := r.tools[name]
	return t, ok
}

func (r *ToolRegistry) List() []ToolSpec {
	if r == nil {
		return nil
	}
	out := make([]ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, ToolSpec{Name: t.Name(), Description: t.Description()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LocateSymbolsTool 解析 diff 并标注变更符号。
type LocateSymbolsTool struct{}

func (LocateSymbolsTool) Name() string { return "locate_symbols" }

func (LocateSymbolsTool) Description() string { return "提取 diff 中的变更符号" }

func (LocateSymbolsTool) Execute(ctx context.Context, st *State, _ map[string]any) (string, error) {
	_ = ctx
	if st == nil {
		return "", errors.New("state 为空")
	}
	changes, err := diff.Parse([]byte(st.RawDiff))
	if err != nil {
		return "", err
	}

	seen := map[string]bool{}
	var names []string
	for i := range changes {
		c := &changes[i]
		if st.Repo != "" && c.File != "/dev/null" {
			src, err := os.ReadFile(filepath.Join(st.Repo, c.File))
			if err == nil {
				c.Annotate(src)
			}
		}
		for _, sym := range c.Symbols {
			key := sym.Kind + ":" + sym.Name + ":" + c.File
			if seen[key] {
				continue
			}
			seen[key] = true
			st.Symbols = append(st.Symbols, review.Sym{Name: sym.Name, File: c.File})
			names = append(names, sym.Name)
		}
	}
	if len(names) == 0 {
		return fmt.Sprintf("files=%d symbols=0", len(changes)), nil
	}
	st.AppendEvidence(NameOf(LocateSymbolsTool{}), strings.Join(names, ", "))
	return fmt.Sprintf("files=%d symbols=%d %s", len(changes), len(names), strings.Join(names, ", ")), nil
}

// AnalyzeImpactTool 计算被改符号的调用方波及面。
type AnalyzeImpactTool struct{}

func (AnalyzeImpactTool) Name() string { return "analyze_impact" }

func (AnalyzeImpactTool) Description() string { return "分析变更符号的调用图影响" }

func (AnalyzeImpactTool) Execute(ctx context.Context, st *State, _ map[string]any) (string, error) {
	_ = ctx
	if st == nil {
		return "", errors.New("state 为空")
	}
	if st.Repo == "" {
		return "", errors.New("缺少 repo 路径")
	}
	if st.Index == nil {
		idx, err := graph.Build(st.Repo)
		if err != nil {
			return "", err
		}
		st.Index = idx
	}
	if st.Store == nil {
		st.Store = recall.New(st.Repo, st.Index)
	}
	if len(st.Symbols) == 0 {
		return "no symbols", nil
	}

	var parts []string
	for _, sym := range st.Symbols {
		callers := st.Index.Impact([]graph.SymbolRef{{Name: sym.Name, File: sym.File}})
		parts = append(parts, fmt.Sprintf("%s=%d callers", sym.Name, len(callers)))
		for _, c := range callers {
			st.AppendEvidence(NameOf(AnalyzeImpactTool{}), fmt.Sprintf("%s <- %s:%d", c.Func, c.File, c.Line))
		}
	}
	if len(parts) == 0 {
		return "no impact", nil
	}
	return strings.Join(parts, "; "), nil
}

// SearchCodeTool 用关键词召回仓库中的相关代码片段。
type SearchCodeTool struct{}

func (SearchCodeTool) Name() string { return "search_code" }

func (SearchCodeTool) Description() string { return "使用关键词召回相关代码" }

func (SearchCodeTool) Execute(ctx context.Context, st *State, args map[string]any) (string, error) {
	_ = ctx
	if st == nil {
		return "", errors.New("state 为空")
	}
	if st.Repo == "" {
		return "", errors.New("缺少 repo 路径")
	}
	if st.Store == nil {
		st.Store = recall.New(st.Repo, st.Index)
	}

	keywords := parseKeywords(args)
	if len(keywords) == 0 {
		keywords = symbolNames(st.Symbols)
	}
	if len(keywords) == 0 {
		return "no keywords", nil
	}

	var docs []string
	for _, kw := range keywords {
		for _, d := range st.Store.Keyword(kw) {
			docs = append(docs, fmt.Sprintf("%s:%d %s", d.File, d.Line, strings.TrimSpace(d.Text)))
			if len(docs) >= 10 {
				break
			}
		}
		if len(docs) >= 10 {
			break
		}
	}
	if len(docs) == 0 {
		return "no matches", nil
	}
	joined := strings.Join(docs, "\n")
	st.AppendEvidence(NameOf(SearchCodeTool{}), joined)
	return joined, nil
}

// ReviewPointTool 调用现有 review 逻辑生成 findings。
type ReviewPointTool struct {
	llm *review.LLM
}

func NewReviewPointTool(llm *review.LLM) ReviewPointTool {
	return ReviewPointTool{llm: llm}
}

func (ReviewPointTool) Name() string { return "review_point" }

func (ReviewPointTool) Description() string { return "对当前证据执行一次审查" }

func (t ReviewPointTool) Execute(ctx context.Context, st *State, _ map[string]any) (string, error) {
	if st == nil {
		return "", errors.New("state 为空")
	}
	if t.llm == nil {
		return "", errors.New("缺少 LLM")
	}
	fs, err := t.llm.Review(ctx, st.RawDiff)
	if err != nil {
		return "", err
	}
	st.AddFindings(fs...)
	return fmt.Sprintf("findings=%d", len(fs)), nil
}

func parseKeywords(args map[string]any) []string {
	if len(args) == 0 {
		return nil
	}
	if v, ok := args["keywords"]; ok {
		return normalizeStrings(v)
	}
	if v, ok := args["keyword"]; ok {
		return normalizeStrings(v)
	}
	return nil
}

func normalizeStrings(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		parts := strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == ' ' || r == ';' || r == '\n' || r == '\t' })
		return cleanStrings(parts)
	case []string:
		return cleanStrings(x)
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return cleanStrings(out)
	default:
		return cleanStrings(strings.Fields(fmt.Sprint(v)))
	}
}

func cleanStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func symbolNames(syms []review.Sym) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range syms {
		if s.Name == "" || seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		out = append(out, s.Name)
	}
	return out
}

// NameOf 是给空结构体工具生成稳定名字的便捷函数。
func NameOf(t Tool) string {
	if t == nil {
		return ""
	}
	return t.Name()
}
