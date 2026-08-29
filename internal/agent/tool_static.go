package agent

import (
	"context"
	"fmt"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

// StaticRulesTool 运行静态分析规则（go vet 系列）。
type StaticRulesTool struct {
	repo string
}

func (t *StaticRulesTool) Name() string {
	return "static_rules"
}

func (t *StaticRulesTool) Description() string {
	return "运行静态分析规则（go vet、copylock、printf 等），返回确定性的 bug 发现"
}

func (t *StaticRulesTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	findings, err := review.Rules(t.repo)
	if err != nil {
		return nil, fmt.Errorf("运行静态规则: %w", err)
	}

	var results []map[string]interface{}
	for _, f := range findings {
		results = append(results, map[string]interface{}{
			"file":     f.File,
			"line":     f.Line,
			"rule":     f.Symbol, // 规则名（如 printf, copylock）
			"severity": f.Severity,
			"message":  f.Msg,
		})
	}

	return map[string]interface{}{
		"findings": results,
		"count":    len(results),
	}, nil
}
