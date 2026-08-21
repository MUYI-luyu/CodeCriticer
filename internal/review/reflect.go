package review

import (
	"context"

	"github.com/MUYI-luyu/codecritic/internal/recall"
)

// Reflector 二次校验 findings，回溯证据链，剔除误报。
type Reflector struct {
	llm  *LLM
	root string
}

func NewReflector(llm *LLM, root string) *Reflector {
	return &Reflector{llm: llm, root: root}
}

// Reflect 逐条复核，返回判定为真的 findings。复核失败时保留（保不漏）。
func (r *Reflector) Reflect(ctx context.Context, fs []Finding) []Finding {
	var out []Finding
	for _, f := range fs {
		code := recall.ReadLines(r.root, f.File, f.Line)
		keep, err := r.llm.Check(ctx, f, code)
		if err != nil || keep {
			out = append(out, f)
		}
	}
	return out
}
