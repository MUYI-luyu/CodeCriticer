package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/MUYI-luyu/codecritic/internal/diff"
	"github.com/MUYI-luyu/codecritic/internal/recall"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

// Tool 是工具接口，所有可被 Orchestrator 调用的工具都实现此接口。
type Tool interface {
	Name() string
	Description() string
	Execute(ctx context.Context, args map[string]interface{}) (interface{}, error)
}

// ToolRegistry 是工具注册表。
type ToolRegistry struct {
	tools map[string]Tool
}

// NewToolRegistry 创建工具注册表。
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]Tool),
	}
}

// Register 注册一个工具。
func (r *ToolRegistry) Register(tool Tool) {
	r.tools[tool.Name()] = tool
}

// Get 获取一个工具。
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

// List 列出所有工具。
func (r *ToolRegistry) List() []Tool {
	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}
	return tools
}

// ToolCall 是工具调用记录。
type ToolCall struct {
	Tool     string                 `json:"tool"`
	Args     map[string]interface{} `json:"args"`
	Result   interface{}            `json:"result"`
	Error    string                 `json:"error,omitempty"`
	Duration time.Duration          `json:"duration"` // 工具执行耗时
}

// LocateSymbolsTool 定位 diff 中的符号变更。
type LocateSymbolsTool struct {
	diffText string
	repo     string
}

func NewLocateSymbolsTool(diffText, repo string) *LocateSymbolsTool {
	return &LocateSymbolsTool{diffText: diffText, repo: repo}
}

func (t *LocateSymbolsTool) Name() string {
	return "locate_symbols"
}

func (t *LocateSymbolsTool) Description() string {
	return "定位 diff 中的函数/方法/类型符号变更，返回符号列表"
}

func (t *LocateSymbolsTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	changes, err := diff.Parse([]byte(t.diffText))
	if err != nil {
		return nil, fmt.Errorf("解析 diff: %w", err)
	}

	// 逐变更标注符号：diff.Parse 只提取变更结构，符号定位需要读取文件原文。
	var enrichedSymbols []map[string]interface{}
	for i := range changes {
		c := &changes[i]
		if t.repo != "" && c.File != "/dev/null" {
			if src, err := os.ReadFile(filepath.Join(t.repo, c.File)); err == nil {
				c.Annotate(src)
			}
		}
		for _, sym := range c.Symbols {
			enrichedSymbols = append(enrichedSymbols, map[string]interface{}{
				"name":      sym.Name,
				"kind":      sym.Kind,
				"file":      c.File,
				"line":      sym.Line,
				"end_line":  sym.EndLine,
				"receiver":  sym.Receiver,
				"signature": sym.Signature,
			})
		}
	}

	return map[string]interface{}{
		"symbols": enrichedSymbols,
		"count":   len(enrichedSymbols),
	}, nil
}

// AnalyzeImpactTool 分析符号的影响范围（调用方）。
type AnalyzeImpactTool struct {
	store *recall.Store
}

func NewAnalyzeImpactTool(store *recall.Store) *AnalyzeImpactTool {
	return &AnalyzeImpactTool{store: store}
}

func (t *AnalyzeImpactTool) Name() string {
	return "analyze_impact"
}

func (t *AnalyzeImpactTool) Description() string {
	return "分析符号的影响范围，通过调用图追踪跨包调用方"
}

func (t *AnalyzeImpactTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	symbol, ok := args["symbol"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'symbol' argument")
	}
	file, _ := args["file"].(string)

	if t.store == nil {
		return map[string]interface{}{"callers": []recall.Doc{}, "count": 0}, nil
	}

	callers := t.store.Symbol(symbol, file)
	return map[string]interface{}{
		"symbol":  symbol,
		"callers": callers,
		"count":   len(callers),
	}, nil
}

// SearchCodeTool 全文搜索关键词。
type SearchCodeTool struct {
	store *recall.Store
}

func NewSearchCodeTool(store *recall.Store) *SearchCodeTool {
	return &SearchCodeTool{store: store}
}

func (t *SearchCodeTool) Name() string {
	return "search_code"
}

func (t *SearchCodeTool) Description() string {
	return "全文搜索关键词（ripgrep），用于召回相关代码片段"
}

func (t *SearchCodeTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	keyword, ok := args["keyword"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'keyword' argument")
	}

	if t.store == nil {
		return map[string]interface{}{"matches": []recall.Doc{}, "count": 0}, nil
	}

	matches := t.store.Keyword(keyword)
	return map[string]interface{}{
		"keyword": keyword,
		"matches": matches,
		"count":   len(matches),
	}, nil
}

// ReviewPointTool 针对审查要点执行审查。
type ReviewPointTool struct {
	llm      *review.LLM
	diffText string
}

func NewReviewPointTool(llm *review.LLM, diffText string) *ReviewPointTool {
	return &ReviewPointTool{
		llm:      llm,
		diffText: diffText,
	}
}

func (t *ReviewPointTool) Name() string {
	return "review_point"
}

func (t *ReviewPointTool) Description() string {
	return "针对审查要点与召回代码执行审查，返回 findings"
}

func (t *ReviewPointTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	desc, ok := args["description"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'description' argument")
	}
	ctxText, _ := args["context"].(string)

	findings, err := t.llm.ReviewPoint(ctx, t.diffText, desc, ctxText)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"findings": findings,
		"count":    len(findings),
	}, nil
}
