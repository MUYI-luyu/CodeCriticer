package workflow

import (
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/diff"
)

func TestBuildRiskSeedsUsesUnifiedCategories(t *testing.T) {
	src := []byte("package p\nimport \"context\"\nfunc Run(ctx context.Context) error {\n go func(){ <-ctx.Done() }()\n return nil\n}\n")
	changes, err := diff.Parse([]byte("--- a/main.go\n+++ b/main.go\n@@ -1,0 +1,6 @@\n+package p\n+import \"context\"\n+func Run(ctx context.Context) error {\n+ go func(){ <-ctx.Done() }()\n+ return nil\n+}\n"))
	if err != nil {
		t.Fatal(err)
	}
	changes[0].Annotate(src)
	seeds := buildRiskSeeds(changes)
	seen := map[string]bool{}
	for _, seed := range seeds {
		seen[seed.Category] = true
	}
	for _, category := range []string{"goroutine_channel", "context", "error"} {
		if !seen[category] {
			t.Fatalf("缺少风险类别 %q: %+v", category, seeds)
		}
	}
	for _, seed := range seeds {
		if seed.Category == "caller" {
			t.Fatalf("caller 不应作为独立风险类别")
		}
	}
}

func TestBuildHypothesesKeepsRequiredFacts(t *testing.T) {
	seeds := []RiskSeed{{Category: "boundary", File: "main.go", Line: 4, Symbol: "Run", Trigger: "m2 := m"}}
	hs := buildHypotheses(seeds)
	if len(hs) != 1 || len(hs[0].RequiredFacts) == 0 {
		t.Fatalf("假设缺少初始事实缺口: %+v", hs)
	}
}
