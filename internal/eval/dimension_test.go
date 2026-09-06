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
