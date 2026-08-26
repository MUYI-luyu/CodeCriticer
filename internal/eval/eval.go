package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MUYI-luyu/codecritic/internal/agent"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

const tol = 3 // 定位容差（行）

// Run 跑全量评测，对比 Baseline（无 Reflexion）vs Reflexion Agent。
func Run(ctx context.Context, llm *review.LLM, datasetDir string) error {
	cases, err := Load(datasetDir)
	if err != nil {
		return err
	}
	if len(cases) == 0 {
		return fmt.Errorf("数据集为空: %s", datasetDir)
	}

	var base, reflexion Metrics
	for _, c := range cases {
		repo, err := materialize(c)
		if err != nil {
			return err
		}
		defer os.RemoveAll(repo)

		// Baseline: 使用旧的单次 Plan-and-Execute（无 Reflexion）
		baseRes, err := review.Analyze(ctx, llm, repo, c.Diff)
		if err != nil {
			return fmt.Errorf("%s baseline: %w", c.Name, err)
		}
		baseFindings := append(append([]review.Finding{}, baseRes.Rules...), baseRes.LLM...)
		b := Compute(c.Bugs, baseFindings, tol)

		// Reflexion Agent: 完整的 Reflexion Loop
		reflexAgent, err := agent.New(llm, repo, agent.WithMaxAttempts(3))
		if err != nil {
			return fmt.Errorf("%s agent: %w", c.Name, err)
		}

		// 从 Impact 提取 Sym 列表
		syms := make([]review.Sym, len(baseRes.Impact))
		for i, imp := range baseRes.Impact {
			syms[i] = review.Sym{Name: imp.Symbol, File: ""} // File 从 Caller 中提取
			if len(imp.Callers) > 0 {
				syms[i].File = imp.Callers[0].File
			}
		}

		reflexResult, err := reflexAgent.Review(ctx, c.Diff, syms)
		if err != nil {
			return fmt.Errorf("%s reflexion: %w", c.Name, err)
		}
		r := Compute(c.Bugs, reflexResult.FinalFindings, tol)

		base = base.Add(b)
		reflexion = reflexion.Add(r)

		fmt.Printf("%-16s bug=%d  baseline[F=%d hit=%d fp=%d]  reflexion[F=%d hit=%d fp=%d rounds=%d]\n",
			c.Name, len(c.Bugs),
			b.Findings, b.Found, b.False,
			r.Findings, r.Found, r.False,
			len(reflexResult.Attempts))
	}

	fmt.Println()
	fmt.Printf("%-12s %10s %10s %8s %10s\n", "", "Recall", "Precision", "FP", "误报率")
	fmt.Printf("%-12s %9.0f%% %10.0f%% %8d %9.0f%%\n", "Baseline", pct(base.Recall()), pct(base.Precision()), base.False, pct(base.FPRate()))
	fmt.Printf("%-12s %9.0f%% %10.0f%% %8d %9.0f%%\n", "Reflexion", pct(reflexion.Recall()), pct(reflexion.Precision()), reflexion.False, pct(reflexion.FPRate()))
	return nil
}

// materialize 把用例写成临时仓库，返回仓库路径。
func materialize(c *Case) (string, error) {
	dir, err := os.MkdirTemp("", "cceval")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/eval\n\ngo 1.22\n"), 0o644); err != nil {
		return "", err
	}
	for name, content := range c.Repo {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func pct(f float64) float64 { return f * 100 }
