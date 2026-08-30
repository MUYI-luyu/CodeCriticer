package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/diff"
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
		validation, err := v.validateOne(ctx, f, i, nil)
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
// 优先使用 pool 中保存的 Plan 阶段召回结果，回退到 gatherEvidence。
func (v *Validator) validateOne(ctx context.Context, f review.Finding, findingID int, pool *recall.EvidencePool) (Validation, error) {
	var contextInfo, callChain string

	// 1. 尝试从 pool 复用 Plan 阶段的召回结果
	if pool != nil {
		docs := pool.FindContext(f.File, f.Line, f.Msg)
		if len(docs) > 0 {
			// 过滤同文件的 docs，最多取 20 个
			sameDocs := recall.FilterSameFile(docs, f.File, 20)
			contextInfo = formatPoolDocs(sameDocs)
		}
	}

	// 2. 回退到 gatherEvidence（pool 为空或未命中）
	if contextInfo == "" {
		evidence := v.gatherEvidence(f)
		contextInfo = evidence.FunctionBody
		if evidence.SymbolInfo != "" {
			contextInfo = fmt.Sprintf("[%s]\n\n%s", evidence.SymbolInfo, evidence.FunctionBody)
		}
		callChain = evidence.CallChain
	}

	// 3. 调用 LLM 评估置信度
	confidence, evidenceText, gaps, err := v.llm.ValidateFinding(ctx, f,
		contextInfo,
		"", // 变量定义召回已移除
		callChain)
	if err != nil {
		return Validation{}, err
	}

	return Validation{
		FindingID:  findingID,
		Confidence: confidence,
		Evidence:   evidenceText,
		Gaps:       gaps,
	}, nil
}

// gatherEvidence 召回 finding 相关的证据。
// 包括：函数体上下文、变量定义、调用关系。
func (v *Validator) gatherEvidence(f review.Finding) Evidence {
	evidence := Evidence{}

	// 1. 函数体上下文：优先使用完整符号体，回退到 ±10 行
	if v.store != nil {
		fileContent := v.readFileContent(f.File)
		if len(fileContent) > 0 {
			// 尝试提取完整符号边界
			if sym, body, ok := diff.ExtractBoundary(fileContent, f.Line); ok {
				// 限制超长符号体（避免 token 溢出）
				const maxBodyLines = 150
				lines := strings.Split(body, "\n")
				if len(lines) > maxBodyLines {
					// 保留前 50 + 后 50 行
					head := strings.Join(lines[:50], "\n")
					tail := strings.Join(lines[len(lines)-50:], "\n")
					evidence.FunctionBody = fmt.Sprintf("%s\n\n... [省略 %d 行] ...\n\n%s",
						head, len(lines)-100, tail)
					evidence.SymbolInfo = fmt.Sprintf("符号: %s (完整定义 %d 行，已截断)", sym.Name, len(lines))
				} else {
					evidence.FunctionBody = body
					evidence.SymbolInfo = fmt.Sprintf("符号: %s (完整定义 %d 行)", sym.Name, len(lines))
				}
			} else {
				// 回退到 ±10 行
				evidence.FunctionBody = v.readContext(f.File, f.Line, 10)
				evidence.SymbolInfo = "无法定位符号边界，回退到 ±10 行"
			}
		}
	}

	// 2. 调用关系（如果 finding 涉及符号）
	if f.Symbol != "" && v.store != nil {
		callers := v.store.Symbol(f.Symbol, f.File)
		if len(callers) > 0 {
			evidence.CallChain = formatCallChain(callers)
		}
	}

	return evidence
}

// readFileContent 读取完整文件内容。
func (v *Validator) readFileContent(file string) []byte {
	if v.store == nil {
		return nil
	}
	// 构造绝对路径
	fullPath := file
	if !strings.HasPrefix(file, "/") {
		fullPath = v.store.Root() + "/" + file
	}
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return nil
	}
	return content
}

// readContext 读取文件指定行的上下文（±lines 行）。
func (v *Validator) readContext(file string, line, lines int) string {
	if v.store == nil {
		return ""
	}
	return recall.ReadLines(v.store.Root(), file, line)
}

// Evidence 是召回的证据。
type Evidence struct {
	FunctionBody    string // 函数体上下文（完整符号体或 ±10 行）
	SymbolInfo      string // 符号信息（提取方式和长度）
	CallChain       string // 调用关系
}

// formatCallChain 格式化调用链。
func formatCallChain(callers []recall.Doc) string {
	if len(callers) == 0 {
		return "无调用方"
	}

	var lines []string
	for i, c := range callers {
		if i >= 5 { // 最多显示 5 个调用方
			lines = append(lines, fmt.Sprintf("... 及其他 %d 个调用方", len(callers)-5))
			break
		}
		lines = append(lines, fmt.Sprintf("- %s:%d", c.File, c.Line))
	}
	return strings.Join(lines, "\n")
}

// formatPoolDocs 格式化从 EvidencePool 复用的 docs。
func formatPoolDocs(docs []recall.Doc) string {
	var parts []string
	for _, d := range docs {
		parts = append(parts, fmt.Sprintf("// %s:%d\n%s", d.File, d.Line, d.Text))
	}
	return strings.Join(parts, "\n\n")
}
