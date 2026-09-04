package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

type scriptedLLM struct{ decisions []string }

type gapLoopLLM struct {
	decisions []string
	reviews   int
	prompts   []string
}

func (s *gapLoopLLM) Plan(context.Context, string) ([]review.Point, review.LLMUsage, error) {
	return []review.Point{{Desc: "检查修改函数的调用关系", Kw: []string{"Run"}}}, review.LLMUsage{}, nil
}

func (s *gapLoopLLM) ChatWithUsage(_ context.Context, _, prompt, _ string) (string, review.LLMUsage, error) {
	s.prompts = append(s.prompts, prompt)
	if len(s.decisions) == 0 {
		return `{"done":true}`, review.LLMUsage{}, nil
	}
	out := s.decisions[0]
	s.decisions = s.decisions[1:]
	return out, review.LLMUsage{}, nil
}

func (s *gapLoopLLM) Review(context.Context, string) ([]review.Finding, review.LLMUsage, error) {
	s.reviews++
	if s.reviews == 1 {
		return []review.Finding{{File: "main.go", Line: 4, Severity: "warning", Msg: "待补证据"}}, review.LLMUsage{}, nil
	}
	return []review.Finding{{File: "main.go", Line: 5, Severity: "warning", Msg: "证据充分", EvidenceIDs: []string{"e2"}}}, review.LLMUsage{}, nil
}

func (s *scriptedLLM) Plan(context.Context, string) ([]review.Point, review.LLMUsage, error) {
	return []review.Point{{Desc: "检查修改函数的调用关系", Kw: []string{"Run"}}}, review.LLMUsage{}, nil
}

func TestTraceSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "trace.json")
	trace := &Trace{ID: "trace-test", StopReason: "validated"}
	if err := trace.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("trace mode = %o, want 600", info.Mode().Perm())
	}
	var decoded Trace
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.ID != trace.ID {
		t.Fatalf("invalid trace JSON: %v", err)
	}
}
func (s *scriptedLLM) ChatWithUsage(context.Context, string, string, string) (string, review.LLMUsage, error) {
	out := s.decisions[0]
	s.decisions = s.decisions[1:]
	return out, review.LLMUsage{}, nil
}
func (s *scriptedLLM) Review(context.Context, string) ([]review.Finding, review.LLMUsage, error) {
	return []review.Finding{{File: "main.go", Line: 4, Severity: "warning", Msg: "reviewed change", Evidence: "func Run", EvidenceIDs: []string{"e2"}}}, review.LLMUsage{}, nil
}

func TestWorkflowUsesEvidenceBeforeFinishing(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n\nfunc Run() {\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/test\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	diff := []byte("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -2,3 +2,3 @@\n \n-func Run() {\n+func Run() {\n }\n")
	llm := &scriptedLLM{decisions: []string{`{"done":false,"tool":"read_code","args":{"file":"main.go"},"question_indexes":[0]}`, `{"done":true}`}}
	wf, err := New(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diff})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trace.Evidence) < 1 || result.Trace.Evidence[len(result.Trace.Evidence)-1].Type != "code" {
		t.Fatalf("unexpected evidence: %+v", result.Trace.Evidence)
	}
	if len(result.Trace.ToolCalls) != 1 || result.Trace.ToolCalls[0].Tool != "read_code" {
		t.Fatalf("unexpected calls: %+v", result.Trace.ToolCalls)
	}
	if len(result.Trace.Findings) != 1 || !result.Trace.Validations[0].Accepted {
		t.Fatalf("unexpected result: %+v", result.Trace)
	}
}

func TestInvestigatorContinuesAfterToolError(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n\nfunc Run() {\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/test\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	diff := []byte("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -2,3 +2,3 @@\n \n-func Run() {\n+func Run() {\n }\n")
	llm := &scriptedLLM{decisions: []string{
		`{"done":false,"tool":"dataflow","args":{"symbol":"Missing","file":"main.go"}}`,
		`{"done":false,"tool":"read_code","args":{"file":"main.go"}}`,
		`{"done":true}`,
	}}
	wf, err := New(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diff})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trace.ToolCalls) != 2 || result.Trace.ToolCalls[0].Error == "" || result.Trace.ToolCalls[1].Error != "" {
		t.Fatalf("工具失败后未继续决策: %+v", result.Trace.ToolCalls)
	}
	if len(result.Trace.Evidence) < 2 || result.Trace.Evidence[len(result.Trace.Evidence)-1].Source != "read_code" {
		t.Fatalf("后续证据缺失: %+v", result.Trace.Evidence)
	}
}

func TestInvestigatorReportsRepeatedDecision(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n\nfunc Run() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/test\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	diff := []byte("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -2,2 +2,2 @@\n-func Run() {}\n+func Run() { }\n")
	llm := &gapLoopLLM{decisions: []string{
		`{"done":false,"tool":"read_code","args":{"file":"main.go"}}`,
		`{"done":false,"tool":"read_code","args":{"file":"main.go"}}`,
		`{"done":true}`,
	}}
	wf, err := New(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diff})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trace.ToolCalls) < 2 || !strings.Contains(result.Trace.ToolCalls[1].Error, "重复调用") {
		t.Fatalf("重复决策未记录: %+v", result.Trace.ToolCalls)
	}
	if len(llm.prompts) < 3 || !strings.Contains(llm.prompts[2], "未产生新证据") {
		t.Fatalf("重复决策未反馈给下一轮: %+v", llm.prompts)
	}
}

func TestInvestigatorRecordsCoveredReadAsNoNewEvidence(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n\nfunc Run() {\n\tprintln(1)\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/test\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	diff := []byte("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -3,3 +3,3 @@\n func Run() {\n-\tprintln(0)\n+\tprintln(1)\n }\n")
	llm := &scriptedLLM{decisions: []string{
		`{"done":false,"tool":"read_code","args":{"file":"main.go","start_line":1,"end_line":5},"question_indexes":[0]}`,
		`{"done":false,"tool":"read_code","args":{"file":"main.go","start_line":3,"end_line":4},"question_indexes":[0]}`,
		`{"done":true}`,
	}}
	wf, err := New(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diff})
	if err != nil {
		t.Fatal(err)
	}
	tr := result.Trace
	if tr.Stats.SuccessfulToolCalls != 1 || tr.Stats.NoNewEvidenceCalls != 1 {
		t.Fatalf("无新证据调用统计异常: %+v", tr.Stats)
	}
	if len(tr.ToolCalls) != 2 || !strings.Contains(tr.ToolCalls[1].Error, "未产生新证据") {
		t.Fatalf("覆盖读取应记录为无新证据: %+v", tr.ToolCalls)
	}
	if got := tr.Evidence[len(tr.Evidence)-1]; got.Line != 1 || got.EndLine != 5 {
		t.Fatalf("较小读取不应覆盖完整证据: %+v", got)
	}
}

func TestInvestigatorStopsAfterCoveredQuestionsAndStaleResults(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n\nfunc Run() {\n\tprintln(1)\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/test\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	diff := []byte("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -3,3 +3,3 @@\n func Run() {\n-\tprintln(0)\n+\tprintln(1)\n }\n")
	llm := &scriptedLLM{decisions: []string{
		`{"done":false,"tool":"read_code","args":{"file":"main.go","start_line":1,"end_line":5},"question_indexes":[0]}`,
		`{"done":false,"tool":"read_code","args":{"file":"main.go","start_line":3,"end_line":4},"question_indexes":[0]}`,
		`{"done":false,"tool":"search_code","args":{"file":"main.go","keyword":"not-present"},"question_indexes":[0]}`,
	}}
	wf, err := New(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diff})
	if err != nil {
		t.Fatal(err)
	}
	if result.Trace.StopReason != StopEvidenceEnough || result.Trace.Stats.NoNewEvidenceCalls != 2 {
		t.Fatalf("证据覆盖后的无新增调用应触发收敛: stop=%s stats=%+v", result.Trace.StopReason, result.Trace.Stats)
	}
}

func TestEvaluateEvidenceContract(t *testing.T) {
	evidence := []*Evidence{{ID: "e1", File: "pkg/main.go", Line: 10}}
	tests := []struct {
		name    string
		finding review.Finding
		want    EvaluateStatus
	}{
		{name: "证据充分", finding: review.Finding{File: "pkg/main.go", Line: 11, EvidenceIDs: []string{"e1"}}, want: EvaluateSufficient},
		{name: "缺少引用", finding: review.Finding{File: "pkg/main.go", Line: 11}, want: EvaluateInsufficient},
		{name: "证据不存在", finding: review.Finding{File: "pkg/main.go", Line: 11, EvidenceIDs: []string{"e2"}}, want: EvaluateInsufficient},
		{name: "位置不同", finding: review.Finding{File: "other/main.go", Line: 11, EvidenceIDs: []string{"e1"}}, want: EvaluateInsufficient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got, _ := evaluateFindings([]review.Finding{tt.finding}, evidence)
			if got != tt.want {
				t.Fatalf("status=%s want=%s", got, tt.want)
			}
		})
	}
}

func TestEvaluateAcceptsCrossLocationSupportEvidence(t *testing.T) {
	evidence := []*Evidence{{ID: "e1", Source: "find_callers", Type: "call_chain", Relation: "supports", File: "caller.go", Line: 20, Content: "caller -> Changed"}}
	findings := []review.Finding{{File: "changed.go", Line: 10, EvidenceIDs: []string{"e1"}}}
	validations, status, _ := evaluateFindings(findings, evidence)
	if status != EvaluateSufficient || len(validations) != 1 || !validations[0].Accepted {
		t.Fatalf("跨位置支持证据应通过验收: status=%s validations=%+v", status, validations)
	}
}

func TestNormalizeEvidencePath(t *testing.T) {
	repo := t.TempDir()
	e := &Evidence{File: filepath.Join(repo, "pkg", "main.go"), Line: 1}
	normalizeEvidencePath(repo, e)
	if e.File != "pkg/main.go" {
		t.Fatalf("file=%q", e.File)
	}
}

func TestInvestigatorEvaluateGapLoop(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n\nfunc Load() error { return nil }\nfunc Start() error { return Load() }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/test\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	diff := []byte("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -2,3 +2,3 @@\n \n-func Run() {}\n+func Run() { }\n")
	llm := &gapLoopLLM{decisions: []string{
		`{"done":false,"tool":"dataflow","args":{"symbol":"Start","file":"main.go"},"question_indexes":[0]}`,
		`{"done":true}`,
		`{"done":false,"tool":"dataflow","args":{"symbol":"Load","file":"main.go"},"question_indexes":[0]}`,
		`{"done":true}`,
	}}
	wf, err := New(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diff})
	if err != nil {
		t.Fatal(err)
	}
	tr := result.Trace
	if llm.reviews != 2 {
		t.Fatalf("review calls=%d want 2", llm.reviews)
	}
	if len(tr.ToolCalls) != 2 || tr.ToolCalls[0].Tool != "dataflow" || tr.ToolCalls[1].Tool != "dataflow" {
		t.Fatalf("unexpected tool calls: %+v", tr.ToolCalls)
	}
	if len(llm.prompts) < 3 || !strings.Contains(llm.prompts[2], "未引用证据") {
		t.Fatalf("evidence gap not passed to investigator: %v", llm.prompts)
	}
	if tr.Evaluation != EvaluateSufficient || len(tr.Evidence) < 2 || !tr.Validations[0].Accepted {
		t.Fatalf("unexpected final evaluation: %+v", tr)
	}
}

func TestQuestionIndexesDoNotProveCoverage(t *testing.T) {
	tr := &Trace{Plan: Plan{Questions: []string{"检查调用关系"}}, Evidence: []*Evidence{{ID: "e1", File: "main.go", Line: 1, QuestionIndexes: []int{0}}}}
	if questionsCovered(tr) {
		t.Fatal("question_indexes must not prove coverage without substantive evidence")
	}
}

func TestDataflowRejectsFieldSymbol(t *testing.T) {
	tool := &toolset{repo: t.TempDir()}
	_, err := tool.dataflow(map[string]interface{}{"symbol": "Stopper.draining", "file": "main.go"})
	if err == nil || !strings.Contains(err.Error(), "字段符号") {
		t.Fatalf("字段符号应被拒绝: %v", err)
	}
}

func TestEvaluateRejectsDuplicateAndSpeculativeFindings(t *testing.T) {
	evidence := []*Evidence{{ID: "e1", File: "main.go", Line: 10, Content: "lock"}}
	findings := []review.Finding{
		{File: "main.go", Line: 10, Msg: "数据竞争：共享状态未同步", EvidenceIDs: []string{"e1"}},
		{File: "main.go", Line: 12, Msg: "数据竞争：同一问题的重复描述", EvidenceIDs: []string{"e1"}},
		{File: "main.go", Line: 20, Msg: "未来扩展可能导致死锁", EvidenceIDs: []string{"e1"}},
	}
	validations, status, gaps := evaluateFindings(findings, evidence)
	if status != EvaluatePartial || !validations[0].Accepted || validations[1].Accepted || validations[2].Accepted || len(gaps) < 2 {
		t.Fatalf("应拒绝重复和推测 Finding: status=%s validations=%+v gaps=%v", status, validations, gaps)
	}
}
