package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/recall"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

// Validator 负责证据校验，将 finding 从二元判断升级为定量评分。
// 核心思想：不是问"这个 finding 对不对"，而是问"证据充分性如何"。
type Validator struct {
	llm   *review.LLM
	store *recall.Store
}

// NewValidator 创建证据校验器。
func NewValidator(llm *review.LLM, store *recall.Store) *Validator {
	return &Validator{
		llm:   llm,
		store: store,
	}
}

// Validate 对每个 finding 进行证据校验。
// 返回 0-1 的置信度评分，而非二元的 keep/drop。
func (v *Validator) Validate(ctx context.Context, findings []review.Finding) ([]Validation, error) {
	validations := make([]Validation, len(findings))

	for i, f := range findings {
		validation, err := v.validateOne(ctx, f, i)
		if err != nil {
			// 校验失败时给默认中等置信度（保守策略）
			validations[i] = Validation{
				FindingID:  i,
				Confidence: 0.5,
				Evidence:   f.Evidence,
				Gaps:       []string{"validation failed: " + err.Error()},
			}
			continue
		}
		validations[i] = validation
	}

	return validations, nil
}

// validateOne 校验单个 finding。
func (v *Validator) validateOne(ctx context.Context, f review.Finding, id int) (Validation, error) {
	// 1. 收集证据
	evidence := v.collectEvidence(f)

	// 2. 调用 LLM 生成置信度评分
	result, err := v.llm.ValidateFinding(ctx, f, evidence)
	if err != nil {
		return Validation{}, err
	}

	return Validation{
		FindingID:  id,
		Confidence: result.Confidence,
		Evidence:   result.Evidence,
		Gaps:       result.Gaps,
	}, nil
}

// Evidence 是提交给 LLM 的完整证据包。
type Evidence struct {
	ClaimedEvidence string   // finding 自己声称的证据
	FunctionBody    string   // 函数体上下文
	SymbolInfo      string   // 符号信息
	CallChain       string   // 调用关系
	RelatedSymbols  []string // 相关符号
}

// collectEvidence 从 recall.Store 提取多源证据。
func (v *Validator) collectEvidence(f review.Finding) Evidence {
	evidence := Evidence{
		ClaimedEvidence: f.Evidence,
	}

	// 1. 函数体上下文：使用 ±10 行（完整符号提取将在后续迭代添加）
	if v.store != nil {
		evidence.FunctionBody = v.readContext(f.File, f.Line, 10)
		evidence.SymbolInfo = "上下文 ±10 行"
	} else {
		evidence.FunctionBody = v.readContext(f.File, f.Line, 10)
	}

	// 2. 调用关系（如果 finding 涉及符号）
	if f.Symbol != "" && v.store != nil {
		callers := v.store.Symbol(f.Symbol, f.File)
		if len(callers) > 0 {
			evidence.CallChain = formatCallChain(callers)
		}
	}

	// 3. 相关符号（通过关键词召回）
	if v.store != nil {
		// 从 finding msg 提取关键词
		keywords := messageKeywords(f.Msg)
		for _, kw := range keywords {
			syms := v.store.Keyword(kw)
			if len(syms) > 0 {
				for _, s := range syms {
					evidence.RelatedSymbols = append(evidence.RelatedSymbols,
						fmt.Sprintf("%s:%d %s", s.File, s.Line, s.Text))
				}
			}
		}
	}

	return evidence
}

// readContext 读取文件的 ±N 行上下文。
func (v *Validator) readContext(file string, line, n int) string {
	if v.store == nil {
		return ""
	}

	fullPath := v.store.Root() + "/" + file
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return ""
	}

	lines := strings.Split(string(content), "\n")
	start := max(0, line-n-1)
	end := min(len(lines), line+n)

	return strings.Join(lines[start:end], "\n")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func formatCallChain(callers []recall.Doc) string {
	var parts []string
	for _, c := range callers {
		parts = append(parts, fmt.Sprintf("%s:%d %s", c.File, c.Line, c.Text))
	}
	return strings.Join(parts, "\n")
}

func messageKeywords(msg string) []string {
	// 简单提取：分词 + 过滤停用词
	words := strings.Fields(strings.ToLower(msg))
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true,
		"but": true, "is": true, "are": true, "was": true, "were": true,
	}

	var keywords []string
	for _, w := range words {
		w = strings.Trim(w, ",.!?;:")
		if len(w) > 3 && !stopWords[w] {
			keywords = append(keywords, w)
		}
	}
	return keywords
}
