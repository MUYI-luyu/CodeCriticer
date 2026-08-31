package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/agent"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

// TestEndToEndDimensionAndCost 端到端测试：验证从 case 到 trace 的完整流程。
func TestEndToEndDimensionAndCost(t *testing.T) {
	// 创建一个简单的 case
	c := &Case{
		Name: "e2e-test",
		Repo: map[string]string{
			"main.go": `package main

import "fmt"

var counter int

func increment() {
	counter++
}

func main() {
	go increment()
	go increment()
	fmt.Println(counter)
}
`,
		},
		Diff: []byte(`diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -5,6 +5,7 @@ import "fmt"
 var counter int

 func increment() {
+	// race condition here
 	counter++
 }
`),
		GT: GroundTruth{
			Primary:  Location{File: "main.go", Line: 8},
			BugTypes: []string{"data-race"},
		},
		Metadata: &Metadata{
			OriginalRepoLOC: 500,
		},
	}

	// 1. 验证 Dimension 计算
	dim := ComputeDimension(c)
	if dim.RepoLOC <= 0 {
		t.Errorf("RepoLOC = %d, want > 0", dim.RepoLOC)
	}
	if dim.ScaleLabel == "" {
		t.Error("ScaleLabel is empty")
	}
	if dim.ScopeLabel == "" {
		t.Error("ScopeLabel is empty")
	}
	t.Logf("Dimension: Scale=%s (RepoLOC=%d), Scope=%s (Files=%d, Packages=%d)",
		dim.ScaleLabel, dim.RepoLOC, dim.ScopeLabel, dim.Files, dim.Packages)

	// 2. 验证 Cost 计算（模拟 agent.Result）
	mockResult := &agent.Result{
		Attempts: []agent.Attempt{
			{
				Round: 1,
				LLMUsage: agent.LLMUsage{
					PromptTokens:     100,
					CompletionTokens: 50,
					TotalTokens:      150,
				},
			},
		},
	}

	cost := ComputeCost(mockResult)
	if cost.Rounds != 1 {
		t.Errorf("Rounds = %d, want 1", cost.Rounds)
	}
	if cost.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150", cost.TotalTokens)
	}
	t.Logf("Cost: %d tokens (%d prompt + %d completion) in %d rounds",
		cost.TotalTokens, cost.TotalPromptTokens, cost.TotalCompletionTokens, cost.Rounds)

	// 3. 验证 EvalTrace 序列化
	trace := EvalTrace{
		Name:         c.Name,
		Bugs:         c.Bugs(),
		Reflex:       mockResult,
		Dimension:    &dim,
		CostSummary:  cost,
		Attributions: []BugAttribution{},
	}

	tmpDir := t.TempDir()
	if err := SaveTrace(tmpDir, trace); err != nil {
		t.Fatalf("SaveTrace failed: %v", err)
	}

	// 4. 验证 trace 文件存在且包含正确字段
	tracePath := filepath.Join(tmpDir, "e2e-test.json")
	data, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	// 简单验证 JSON 包含关键字段
	content := string(data)
	requiredFields := []string{
		`"dimension"`,
		`"cost_summary"`,
		`"scale_label"`,
		`"scope_label"`,
		`"total_tokens"`,
		`"rounds"`,
	}
	for _, field := range requiredFields {
		if !contains(content, field) {
			t.Errorf("Trace JSON missing field: %s", field)
		}
	}

	t.Logf("✅ Trace saved successfully with dimension and cost")
}

// TestComputeMultiLocationIntegration 验证多位置 GT 的集成。
func TestComputeMultiLocationIntegration(t *testing.T) {
	c := &Case{
		Name: "multi-loc-test",
		GT: GroundTruth{
			Primary: Location{File: "main.go", Line: 10},
			Related: []Location{
				{File: "main.go", Line: 20},
				{File: "util.go", Line: 5},
			},
		},
	}

	// Primary 命中 -> 覆盖率 50%
	findings1 := []review.Finding{
		{File: "main.go", Line: 10},
	}
	m1 := ComputeMultiLocation(c, findings1, 3, 0.5)
	if m1.Found != 1 {
		t.Errorf("Primary hit: Found=%d, want 1", m1.Found)
	}

	// Primary + 1 Related 命中 -> 覆盖率 75%
	findings2 := []review.Finding{
		{File: "main.go", Line: 10},
		{File: "main.go", Line: 20},
	}
	m2 := ComputeMultiLocation(c, findings2, 3, 0.5)
	if m2.Found != 1 {
		t.Errorf("Primary + 1 Related: Found=%d, want 1", m2.Found)
	}
	if m2.True != 2 {
		t.Errorf("True findings=%d, want 2", m2.True)
	}

	t.Logf("✅ Multi-location GT attribution works correctly")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
