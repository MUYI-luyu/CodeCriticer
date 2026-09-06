package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
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

	// 2. 验证 Cost 计算（模拟 Workflow trace）
	mockResult := &workflow.Trace{Usage: review.LLMUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}}

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
		Workflow:     mockResult,
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
