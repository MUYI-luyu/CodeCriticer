package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

const tol = 3 // 定位容差（行）

// Run 跑全量评测，输出逐用例与消融对比。
func Run(ctx context.Context, llm *review.LLM, datasetDir string) error {
	cases, err := Load(datasetDir)
	if err != nil {
		return err
	}
	if len(cases) == 0 {
		return fmt.Errorf("数据集为空: %s", datasetDir)
	}

	var base, ref Metrics
	for _, c := range cases {
		repo, err := materialize(c)
		if err != nil {
			return err
		}
		defer os.RemoveAll(repo)

		res, err := review.Analyze(ctx, llm, repo, c.Diff)
		if err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}

		// 基线：规则 + LLM 原始产出；Reflection：规则直保 + LLM 去重二次校验。
		raw := append(append([]review.Finding{}, res.Rules...), res.LLM...)
		b := Compute(c.Bugs, raw, tol)

		refl := review.NewReflector(llm, repo)
		llmFs := refl.Reflect(ctx, review.Dedup(res.LLM, tol))
		refined := review.Dedup(append(append([]review.Finding{}, res.Rules...), llmFs...), tol)
		r := Compute(c.Bugs, refined, tol)

		base = base.Add(b)
		ref = ref.Add(r)

		fmt.Printf("%-16s bug=%d  baseline[F=%d hit=%d fp=%d]  +reflect[F=%d hit=%d fp=%d]\n",
			c.Name, len(c.Bugs), b.Findings, b.Found, b.False, r.Findings, r.Found, r.False)
	}

	fmt.Println()
	fmt.Printf("%-12s %10s %10s %8s %10s\n", "", "Recall", "Precision", "FP", "误报率")
	fmt.Printf("%-12s %9.0f%% %10.0f%% %8d %9.0f%%\n", "Baseline", pct(base.Recall()), pct(base.Precision()), base.False, pct(base.FPRate()))
	fmt.Printf("%-12s %9.0f%% %10.0f%% %8d %9.0f%%\n", "+Reflection", pct(ref.Recall()), pct(ref.Precision()), ref.False, pct(ref.FPRate()))
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
