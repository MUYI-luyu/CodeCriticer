package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

// TestAttributeStages 覆盖四类归因：成功 / 自伤 / LLM漏 / 召回漏。
func TestAttributeStages(t *testing.T) {
	bugs := []Bug{
		{File: "a.go", Line: 10}, // 成功：召回+execute+validate 都命中
		{File: "b.go", Line: 20}, // 自伤：execute 命中但 conf<0.7 被丢
		{File: "c.go", Line: 30}, // LLM漏：召回覆盖但 execute 没报
		{File: "d.go", Line: 40}, // 召回漏：召回都没覆盖
	}

	att := &workflow.Trace{
		// 召回：a/b/c 覆盖，d 不覆盖
		Evidence: []*workflow.Evidence{
			{File: "a.go", Line: 11}, {File: "b.go", Line: 20}, {File: "c.go", Line: 31}, {File: "z.go", Line: 40},
		},
		// execute：a 命中(idx0)、b 命中(idx1)；c/d 未报
		Findings: []review.Finding{
			{File: "a.go", Line: 10},
			{File: "b.go", Line: 21},
		},
		// validate：a 高置信保留，b 低置信丢弃
		Validations: []workflow.Validation{
			{FindingIndex: 0, Accepted: true, Confidence: 0.9},
			{FindingIndex: 1, Accepted: false, Confidence: 0.4},
		},
	}

	got := Attribute(bugs, att, tol)
	want := []BugStage{StageSuccess, StageSelfHarm, StageLLMMiss, StageRecallMiss}
	if len(got) != len(want) {
		t.Fatalf("归因数量 = %d，期望 %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Stage != w {
			t.Errorf("bug %d: stage = %s，期望 %s (recall=%v exec=%v kept=%v)",
				i, got[i].Stage, w, got[i].RecallHit, got[i].ExecuteHit, got[i].ValidateKept)
		}
	}

	// 计数应四类相加等于 bug 总数
	counts := AttributionCounts{}.Add(got)
	if counts.Total() != len(bugs) {
		t.Errorf("counts.Total() = %d，期望 %d", counts.Total(), len(bugs))
	}
	if counts.Success != 1 || counts.SelfHarm != 1 || counts.LLMMiss != 1 || counts.RecallMiss != 1 {
		t.Errorf("counts = %+v，期望各 1", counts)
	}
}

// TestSaveTraceRoundtrip 验证 trace 落盘后能读回、且含召回全文。
func TestSaveTraceRoundtrip(t *testing.T) {
	dir := t.TempDir()
	tr := EvalTrace{
		Name:         "pkg/case-1", // 带分隔符，验证 safeName
		Bugs:         []Bug{{File: "a.go", Line: 10, Desc: "data race"}},
		Workflow:     &workflow.Trace{Evidence: []*workflow.Evidence{{File: "a.go", Line: 10, Content: "go func() { x++ }()"}}},
		Attributions: []BugAttribution{{Bug: Bug{File: "a.go", Line: 10}, Stage: StageSuccess}},
	}
	if err := SaveTrace(dir, tr); err != nil {
		t.Fatalf("SaveTrace: %v", err)
	}

	path := filepath.Join(dir, "pkg_case-1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回 trace: %v", err)
	}
	var back EvalTrace
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("反序列化: %v", err)
	}
	if len(back.Workflow.Evidence) != 1 {
		t.Fatalf("召回轨迹丢失: %+v", back.Workflow)
	}
	if txt := back.Workflow.Evidence[0].Content; txt != "go func() { x++ }()" {
		t.Errorf("召回全文未保留，got %q", txt)
	}
}
