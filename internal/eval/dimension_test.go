package eval

import (
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

func TestComputeDimension(t *testing.T) {
	c := &Case{
		Name: "test-dimension",
		Repo: map[string]string{
			"main.go": `package main

func main() {
	x := 1
	println(x)
}
`,
			"pkg/util.go": `package pkg

func Helper() int {
	return 42
}
`,
		},
		Diff: []byte(`diff --git a/main.go b/main.go
index 1234567..abcdefg 100644
--- a/main.go
+++ b/main.go
@@ -2,5 +2,6 @@ package main

 func main() {
 	x := 1
+	y := 2
 	println(x)
 }
`),
		GT: GroundTruth{
			Primary: Location{File: "main.go", Line: 5},
		},
	}

	dim := ComputeDimension(c)

	// RepoLOC: main.go 7 行 + pkg/util.go 6 行 = 13 行
	if dim.RepoLOC != 13 {
		t.Errorf("RepoLOC = %d, want 13", dim.RepoLOC)
	}

	// DiffLOC: 1 行添加（+	y := 2）
	if dim.DiffLOC != 1 {
		t.Errorf("DiffLOC = %d, want 1", dim.DiffLOC)
	}

	// ScaleLabel: 13 行 -> 100_LOC
	if dim.ScaleLabel != "100_LOC" {
		t.Errorf("ScaleLabel = %s, want 100_LOC", dim.ScaleLabel)
	}

	// Files: 2
	if dim.Files != 2 {
		t.Errorf("Files = %d, want 2", dim.Files)
	}

	// Packages: 2（main 目录和 pkg 目录）
	if dim.Packages != 2 {
		t.Errorf("Packages = %d, want 2", dim.Packages)
	}

	// ScopeLabel: 2 packages -> cross_package
	if dim.ScopeLabel != "cross_package" {
		t.Errorf("ScopeLabel = %s, want cross_package", dim.ScopeLabel)
	}
}

func TestComputeDimensionWithMetadata(t *testing.T) {
	c := &Case{
		Name: "test-large-project",
		Repo: map[string]string{
			"main.go": `package main
func main() {}
`,
		},
		Diff: []byte(`diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1 +1,2 @@
 package main
+func main() {}
`),
		GT: GroundTruth{
			Primary: Location{File: "main.go", Line: 2},
		},
		Metadata: &Metadata{
			OriginalRepoLOC: 50000, // 50K LOC 真实项目
		},
	}

	dim := ComputeDimension(c)

	// 应该使用 Metadata.OriginalRepoLOC
	if dim.ScaleLabel != "10K_LOC" {
		t.Errorf("ScaleLabel = %s, want 10K_LOC (应使用 OriginalRepoLOC=50000)", dim.ScaleLabel)
	}
}

func TestComputeCost(t *testing.T) {
	result := &workflow.Trace{Usage: review.LLMUsage{PromptTokens: 300, CompletionTokens: 150, TotalTokens: 450}}

	cost := ComputeCost(result)

	if cost.Rounds != 1 {
		t.Errorf("Rounds = %d, want 1", cost.Rounds)
	}
	if cost.TotalPromptTokens != 300 {
		t.Errorf("TotalPromptTokens = %d, want 300", cost.TotalPromptTokens)
	}
	if cost.TotalCompletionTokens != 150 {
		t.Errorf("TotalCompletionTokens = %d, want 150", cost.TotalCompletionTokens)
	}
	if cost.TotalTokens != 450 {
		t.Errorf("TotalTokens = %d, want 450", cost.TotalTokens)
	}
}

func TestComputeMultiLocation(t *testing.T) {
	c := &Case{
		Name: "multi-location-bug",
		GT: GroundTruth{
			Primary: Location{File: "main.go", Line: 10},
			Related: []Location{
				{File: "main.go", Line: 20},
				{File: "main.go", Line: 30},
			},
		},
	}

	// 命中 Primary（权重 1.0），覆盖率 1.0 / 2.0 = 0.5 >= 0.5 → 命中
	findings1 := []review.Finding{
		{File: "main.go", Line: 10},
	}
	m1 := ComputeMultiLocation(c, findings1, 3, 0.5)
	if m1.Found != 1 || m1.TP != 1 || m1.FN != 0 {
		t.Errorf("命中 Primary: Found=%d TP=%d FN=%d, want Found=1 TP=1 FN=0", m1.Found, m1.TP, m1.FN)
	}

	// 只命中 1 个 Related（权重 0.5），覆盖率 0.5 / 2.0 = 0.25 < 0.5 → 未命中
	findings2 := []review.Finding{
		{File: "main.go", Line: 20},
	}
	m2 := ComputeMultiLocation(c, findings2, 3, 0.5)
	if m2.Found != 0 || m2.TP != 0 || m2.FN != 1 {
		t.Errorf("只命中 1 Related: Found=%d TP=%d FN=%d, want Found=0 TP=0 FN=1", m2.Found, m2.TP, m2.FN)
	}

	// 命中 Primary + 1 Related，覆盖率 1.5 / 2.0 = 0.75 >= 0.5 → 命中
	findings3 := []review.Finding{
		{File: "main.go", Line: 10},
		{File: "main.go", Line: 20},
	}
	m3 := ComputeMultiLocation(c, findings3, 3, 0.5)
	if m3.Found != 1 || m3.TP != 1 || m3.FN != 0 {
		t.Errorf("命中 Primary + 1 Related: Found=%d TP=%d FN=%d, want Found=1 TP=1 FN=0", m3.Found, m3.TP, m3.FN)
	}

	// 无命中
	findings4 := []review.Finding{
		{File: "other.go", Line: 100},
	}
	m4 := ComputeMultiLocation(c, findings4, 3, 0.5)
	if m4.Found != 0 || m4.TP != 0 || m4.FN != 1 || m4.FP != 1 {
		t.Errorf("无命中: Found=%d TP=%d FN=%d FP=%d, want Found=0 TP=0 FN=1 FP=1", m4.Found, m4.TP, m4.FN, m4.FP)
	}
}
