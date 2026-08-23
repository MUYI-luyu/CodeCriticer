package agent

import (
	"context"
	"sync"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

// Reflector 负责生成 Critique（反驳理由 + 改进建议）。
// 只对低置信度的 finding 生成 Critique，供下一轮审查改进。
type Reflector struct {
	llm *review.LLM
}

// NewReflector 创建 Reflector。
func NewReflector(llm *review.LLM) *Reflector {
	return &Reflector{llm: llm}
}

// GenerateCritiques 批量生成 Critique。
// 只对置信度 < ConfidenceThreshold 的 finding 生成 Critique。
func (r *Reflector) GenerateCritiques(ctx context.Context, findings []review.Finding, validations []Validation) ([]Critique, error) {
	var critiques []Critique
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i, v := range validations {
		// 只对低置信度的 finding 生成 Critique
		if v.Confidence >= ConfidenceThreshold {
			continue
		}

		wg.Add(1)
		go func(idx int, val Validation) {
			defer wg.Done()

			if idx >= len(findings) {
				return
			}

			f := findings[idx]
			// 转换为 LLM 的输入格式
			valInput := review.ValidationInput{
				Confidence: val.Confidence,
				Evidence:   val.Evidence,
				Gaps:       val.Gaps,
			}
			critique, err := r.llm.GenerateCritique(ctx, f, valInput)
			if err != nil {
				// 生成失败时跳过（不阻塞整体流程）
				return
			}

			mu.Lock()
			critiques = append(critiques, Critique{
				FindingID:  idx,
				Reason:     critique.Reason,
				Evidence:   critique.Evidence,
				Suggestion: critique.Suggestion,
			})
			mu.Unlock()
		}(i, v)
	}

	wg.Wait()
	return critiques, nil
}
