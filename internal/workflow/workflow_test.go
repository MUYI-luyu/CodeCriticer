package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MUYI-luyu/codecritic/internal/diff"
	"github.com/MUYI-luyu/codecritic/internal/recall"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

type scriptedLLM struct {
	calls           []review.ToolCall
	transcripts     [][]review.ToolMessage
	configuredModel string
}

func (s *scriptedLLM) AgentModel() string { return s.configuredModel }

func (s *scriptedLLM) CompleteToolsWithUsage(_ context.Context, _ string, messages []review.ToolMessage, _ []review.ToolDefinition, _ string) (review.ToolMessage, review.LLMUsage, error) {
	s.transcripts = append(s.transcripts, append([]review.ToolMessage(nil), messages...))
	if len(s.calls) == 0 {
		return assistantCall("submit_claims", `{"claims":[]}`), review.LLMUsage{}, nil
	}
	out := s.calls[0]
	s.calls = s.calls[1:]
	return review.ToolMessage{Role: "assistant", ToolCalls: []review.ToolCall{out}}, review.LLMUsage{}, nil
}

func TestReviewRunIsOneShot(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {}\n", 3)
	run, err := NewReviewRun(&scriptedLLM{}, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Run(context.Background(), Request{Repo: repo, Diff: diffData}); err != nil {
		t.Fatal(err)
	}
	if result, err := run.Run(context.Background(), Request{Repo: repo, Diff: diffData}); !errors.Is(err, ErrReviewRunAlreadyStarted) || result != nil {
		t.Fatalf("second Run result=%+v err=%v", result, err)
	}
}

func TestEvidenceLedgerUsesSemanticIdentityAndDefensiveCopies(t *testing.T) {
	run := &ReviewRun{}
	first := &Evidence{Source: "read_code", Type: "code", File: "main.go", Line: 3, EndLine: 5, Content: "3 | func Run() {}", StepID: "s1"}
	if id := run.appendEvidence(first); id != "e1" {
		t.Fatalf("first id=%q", id)
	}

	first.Content = "mutated by caller"
	copyOne := run.evidenceSnapshot()
	if copyOne[0].Content != "3 | func Run() {}" {
		t.Fatalf("caller mutated ledger: %+v", copyOne[0])
	}
	copyOne[0].Content = "mutated snapshot"
	if got := run.evidenceSnapshot()[0].Content; got != "3 | func Run() {}" {
		t.Fatalf("snapshot mutated ledger: %q", got)
	}

	session := &ReviewSession{run: run}
	withSymbol := &Evidence{Source: "read_code", Type: "code", File: "main.go", Line: 3, EndLine: 5, Content: "3 | func Run() {}", Symbol: "Run"}
	observed, novel := session.appendEvidence("s2", []*Evidence{withSymbol})
	if !observed || novel || withSymbol.ID != "e2" {
		t.Fatalf("semantic variant observed=%v novel=%v evidence=%+v", observed, novel, withSymbol)
	}
	ledger := run.evidenceSnapshot()
	if len(ledger) != 2 || ledger[0].Symbol != "" || ledger[1].Symbol != "Run" {
		t.Fatalf("ledger=%+v", ledger)
	}
}

func assistantCall(name, arguments string) review.ToolMessage {
	return review.ToolMessage{Role: "assistant", ToolCalls: []review.ToolCall{{ID: "call-" + name, Type: "function", Function: review.FunctionCall{Name: name, Arguments: arguments}}}}
}

func toolCall(name, arguments string) review.ToolCall {
	return assistantCall(name, arguments).ToolCalls[0]
}

func TestWorkflowUsesConfiguredAgentModel(t *testing.T) {
	wf, err := NewReviewRun(&scriptedLLM{configuredModel: "agent-model"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if wf.model != "agent-model" {
		t.Fatalf("model=%q", wf.model)
	}
}

func TestTraceSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "trace.json")
	trace := &Trace{ID: "trace-test", StopReason: StopAgentDone}
	if err := trace.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	var decoded Trace
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.ID != trace.ID {
		t.Fatalf("invalid trace: %v", err)
	}
}

func TestAgentToolThenSubmit(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {\n}\n", 3)
	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"file_start"}}`),
		toolCall("submit_claims", `{"claims":[{"file":"main.go","line":3,"severity":"warning","msg":"Run has an issue","evidence_ids":["e2"]}]}`),
	}}
	wf, _ := NewReviewRun(llm, repo)
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err != nil {
		t.Fatal(err)
	}
	trace := result.Trace
	if len(trace.Investigation) != 1 || trace.Investigation[0].Tool != "read_code" {
		t.Fatalf("investigation=%+v", trace.Investigation)
	}
	if len(trace.FinalReport.Claims) != 1 || trace.FinalReport.Verdicts[0].Status != VerdictAccepted {
		t.Fatalf("report=%+v", trace.FinalReport)
	}
	if trace.Evidence[len(trace.Evidence)-1].StepID != trace.Investigation[0].ID {
		t.Fatalf("provenance not linked: evidence=%+v step=%+v", trace.Evidence, trace.Investigation[0])
	}
}

func TestWorkflowProjectsModelContextButKeepsFullConversation(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {\n\tprintln(1)\n}\n", 4)
	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"range","start":3,"max_lines":2}}`),
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"range","start":1,"max_lines":5}}`),
		toolCall("submit_claims", `{"claims":[]}`),
	}}
	wf, _ := NewReviewRun(llm, repo)
	wf.SetContextProjection(true)
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err != nil {
		t.Fatal(err)
	}
	if len(llm.transcripts) != 3 || !strings.Contains(llm.transcripts[2][2].Content, "read_code_covered") {
		t.Fatalf("third model request was not projected: %+v", llm.transcripts)
	}
	trace := result.Trace
	if len(trace.Conversation) != 6 || strings.Contains(trace.Conversation[2].Content, "context_projection") || !strings.Contains(trace.Conversation[2].Content, `"evidence"`) {
		t.Fatalf("full conversation was not preserved: %+v", trace.Conversation)
	}
	if !trace.Projection.Enabled || trace.Projection.Applications != 3 || trace.Projection.DominatedReadCodeCompacted != 1 {
		t.Fatalf("projection stats=%+v", trace.Projection)
	}
}

func TestAgentCanInspectConcurrencyFacts(t *testing.T) {
	repo, diffData := testRepo(t, `package main

import "sync"

var mu sync.Mutex

func Run() {
	mu.Lock()
	defer mu.Unlock()
}
`, 1)
	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("inspect_concurrency", `{"symbol":"Run","file":"main.go","line":null}`),
		toolCall("submit_claims", `{"claims":[]}`),
	}}
	wf, _ := NewReviewRun(llm, repo)
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err != nil {
		t.Fatal(err)
	}
	foundAcquire := false
	for _, evidence := range result.Trace.Evidence {
		if evidence.Source == "inspect_concurrency" && evidence.Type == "lock_acquire" && strings.Contains(evidence.Content, "Mutex.Lock") {
			foundAcquire = true
			break
		}
	}
	if !foundAcquire {
		t.Fatalf("concurrency evidence=%+v", result.Trace.Evidence)
	}
}

func TestRejectedSubmissionReturnsToSameAgentLoop(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {\n}\n", 3)
	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("submit_claims", `{"claims":[{"file":"main.go","line":3,"severity":"warning","msg":"needs evidence","evidence_ids":["e99"]}]}`),
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"file_start"}}`),
		toolCall("submit_claims", `{"claims":[{"file":"main.go","line":3,"severity":"warning","msg":"now supported","evidence_ids":["e2"]}]}`),
	}}
	wf, _ := NewReviewRun(llm, repo)
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err != nil {
		t.Fatal(err)
	}
	if len(llm.transcripts) != 3 || !strings.Contains(llm.transcripts[1][len(llm.transcripts[1])-1].Content, "Evidence e99 不存在") {
		t.Fatalf("verdict was not returned to same loop: %v", llm.transcripts)
	}
	if result.Trace.FinalReport.Verdicts[0].Status != VerdictAccepted || len(result.Trace.Investigation) != 1 {
		t.Fatalf("trace=%+v", result.Trace)
	}
}

func TestAgentContinuesAfterToolError(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {}\n", 3)
	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("inspect_symbol", `{"symbol":"Missing","file":"main.go","line":null,"relation":"references"}`),
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"file_start"}}`),
		toolCall("submit_claims", `{"claims":[]}`),
	}}
	wf, _ := NewReviewRun(llm, repo)
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err != nil {
		t.Fatal(err)
	}
	steps := result.Trace.Investigation
	if len(steps) != 2 || steps[0].Error == "" || steps[1].Error != "" {
		t.Fatalf("steps=%+v", steps)
	}
}

func TestAgentRecordsDuplicateAndNoNewEvidence(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {\n\tprintln(1)\n}\n", 4)
	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"range","start":1,"max_lines":5}}`),
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"range","start":1,"max_lines":5}}`),
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"range","start":3,"max_lines":2}}`),
		toolCall("submit_claims", `{"claims":[]}`),
	}}
	wf, _ := NewReviewRun(llm, repo)
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err != nil {
		t.Fatal(err)
	}
	trace := result.Trace
	if trace.Stats.DuplicateCalls != 1 || trace.Stats.NoNewEvidenceCalls != 1 || trace.Stats.SuccessfulToolCalls != 1 {
		t.Fatalf("stats=%+v", trace.Stats)
	}
	if !strings.Contains(trace.Investigation[1].Error, "重复调用") || !strings.Contains(trace.Investigation[2].Error, "未产生新证据") {
		t.Fatalf("steps=%+v", trace.Investigation)
	}
}

func TestEvidenceIdentityIsAppendOnlyForWiderRead(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {\n\tprintln(1)\n}\n\nfunc Other() {}\n", 3)
	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"range","start":3,"max_lines":2}}`),
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"file_start"}}`),
		toolCall("submit_claims", `{"claims":[]}`),
	}}
	run, err := NewReviewRun(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	result, err := run.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trace.Evidence) != 4 {
		t.Fatalf("expected two diff and two read Evidence entries, got %+v", result.Trace.Evidence)
	}
	firstRead, widerRead := result.Trace.Evidence[2], result.Trace.Evidence[3]
	if firstRead.ID != "e3" || firstRead.Line != 3 || firstRead.EndLine != 4 || firstRead.StepID != "s1" {
		t.Fatalf("first read Evidence was replaced: %+v", firstRead)
	}
	if widerRead.ID != "e4" || widerRead.Line != 1 || widerRead.StepID != "s2" {
		t.Fatalf("wider read Evidence=%+v", widerRead)
	}
}

func TestDecisionBudgetExhaustionIsNotReportedAsNoIssues(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {}\n", 3)
	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("submit_claims", `{"claims":[{"file":"main.go","line":3,"severity":"warning","msg":"needs evidence","evidence_ids":["missing"]}]}`),
		toolCall("submit_claims", `{"claims":[{"file":"main.go","line":3,"severity":"warning","msg":"needs evidence","evidence_ids":["missing"]}]}`),
		toolCall("submit_claims", `{"claims":[{"file":"main.go","line":3,"severity":"warning","msg":"needs evidence","evidence_ids":["missing"]}]}`),
		toolCall("submit_claims", `{"claims":[{"file":"main.go","line":3,"severity":"warning","msg":"needs evidence","evidence_ids":["missing"]}]}`),
	}}
	wf, _ := NewReviewRun(llm, repo)
	wf.SetMaxSteps(1)
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err == nil {
		t.Fatal("budget exhaustion must not be reported as a successful empty review")
	}
	if result.Trace.StopReason != StopMaxSteps || len(result.Trace.FinalReport.Claims) != 1 || result.Trace.FinalReport.Verdicts[0].Status != VerdictUnresolved {
		t.Fatalf("trace=%+v", result.Trace)
	}
}

func TestFailedToolExecutionConsumesToolBudget(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {}\n", 3)
	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("read_code", `{"file":"missing.go","selector":{"kind":"file_start"}}`),
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"file_start"}}`),
		toolCall("submit_claims", `{"claims":[]}`),
	}}
	run, err := NewReviewRun(llm, repo)
	if err != nil {
		t.Fatal(err)
	}
	run.SetMaxSteps(1)
	result, err := run.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err != nil {
		t.Fatal(err)
	}
	if result.Trace.Stats.FailedToolCalls != 1 || result.Trace.Stats.SuccessfulToolCalls != 0 {
		t.Fatalf("stats=%+v", result.Trace.Stats)
	}
	if len(result.Trace.Investigation) != 2 || !strings.Contains(result.Trace.Investigation[1].Error, "预算已耗尽") {
		t.Fatalf("investigation=%+v", result.Trace.Investigation)
	}
}

func TestAgentDoesNotRepairInvalidNativeResponse(t *testing.T) {
	repo, diffData := testRepo(t, "package main\n\nfunc Run() {}\n", 3)
	llm := &scriptedLLM{calls: []review.ToolCall{{}}}
	wf, _ := NewReviewRun(llm, repo)
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err == nil || result.Trace.StopReason != StopInvalidDecision || len(llm.transcripts) != 1 {
		t.Fatalf("trace=%+v", result.Trace)
	}
}

func TestAgentToolSchemasAreStrict(t *testing.T) {
	tools := agentTools(false)
	if len(tools) != 8 {
		t.Fatalf("tools=%d", len(tools))
	}
	for _, tool := range tools {
		if !tool.Function.Strict || tool.Function.Parameters["additionalProperties"] != false {
			t.Fatalf("tool is not strict: %+v", tool)
		}
		assertStrictSchemaObjects(t, tool.Function.Name, tool.Function.Parameters)
	}
}

func assertStrictSchemaObjects(t *testing.T, path string, schema map[string]any) {
	t.Helper()
	if schema["type"] == "object" {
		if schema["additionalProperties"] != false {
			t.Fatalf("%s object allows additional properties: %+v", path, schema)
		}
		properties, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]string)
		for name := range properties {
			if !schemaContainsString(required, name) {
				t.Fatalf("%s.%s is not required", path, name)
			}
		}
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		for name, raw := range properties {
			if child, ok := raw.(map[string]any); ok {
				assertStrictSchemaObjects(t, path+"."+name, child)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		assertStrictSchemaObjects(t, path+"[]", items)
	}
	if branches, ok := schema["anyOf"].([]any); ok {
		for i, raw := range branches {
			if child, ok := raw.(map[string]any); ok {
				assertStrictSchemaObjects(t, fmt.Sprintf("%s.anyOf[%d]", path, i), child)
			}
		}
	}
}

func schemaContainsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestReadCodeSchemaUsesExclusiveSelectorShapes(t *testing.T) {
	var schema map[string]any
	for _, tool := range agentTools(false) {
		if tool.Function.Name == "read_code" {
			schema = tool.Function.Parameters
			break
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	selector, _ := properties["selector"].(map[string]any)
	branches, _ := selector["anyOf"].([]any)
	if len(branches) != 4 {
		t.Fatalf("selector schema=%+v", selector)
	}
	for _, raw := range branches {
		branch, _ := raw.(map[string]any)
		if branch["type"] != "object" || branch["additionalProperties"] != false {
			t.Fatalf("selector branch is not strict: %+v", branch)
		}
		branchProperties, _ := branch["properties"].(map[string]any)
		if _, exists := branchProperties["kind"]; !exists {
			t.Fatalf("selector branch has no discriminator: %+v", branch)
		}
	}
	for _, legacy := range []string{"start_line", "end_line", "symbol", "line", "context_lines"} {
		if _, exists := properties[legacy]; exists {
			t.Fatalf("legacy selector %q remains expressible", legacy)
		}
	}
}

func TestToolSchemasMatchConcreteArgumentTypes(t *testing.T) {
	types := map[string]reflect.Type{
		"read_diff":           reflect.TypeOf(readDiffArgs{}),
		"read_code":           reflect.TypeOf(readCodeEnvelope{}),
		"search_code":         reflect.TypeOf(searchCodeArgs{}),
		"inspect_symbol":      reflect.TypeOf(inspectSymbolArgs{}),
		"inspect_concurrency": reflect.TypeOf(inspectConcurrencyArgs{}),
		"run_static_rules":    reflect.TypeOf(emptyToolArgs{}),
		"run_go_validation":   reflect.TypeOf(goValidationArgs{}),
		"submit_claims":       reflect.TypeOf(submitClaimsArgs{}),
	}
	for _, tool := range agentTools(false) {
		argType, ok := types[tool.Function.Name]
		if !ok {
			t.Fatalf("missing argument type for %s", tool.Function.Name)
		}
		assertSchemaPropertiesMatchStruct(t, tool.Function.Name, tool.Function.Parameters, argType)
	}

	readSchema := toolSchema(t, "read_code")
	readProperties := readSchema["properties"].(map[string]any)
	selectorSchema := readProperties["selector"].(map[string]any)
	branches := selectorSchema["anyOf"].([]any)
	selectorTypes := []reflect.Type{
		reflect.TypeOf(fileStartSelector{}), reflect.TypeOf(rangeSelector{}),
		reflect.TypeOf(lineSelector{}), reflect.TypeOf(symbolSelector{}),
	}
	for i, selectorType := range selectorTypes {
		assertSchemaPropertiesMatchStruct(t, fmt.Sprintf("read_code.selector[%d]", i), branches[i].(map[string]any), selectorType)
	}

	submitSchema := toolSchema(t, "submit_claims")
	claimItems := submitSchema["properties"].(map[string]any)["claims"].(map[string]any)["items"].(map[string]any)
	assertSchemaPropertiesMatchStruct(t, "submit_claims.claims[]", claimItems, reflect.TypeOf(candidateClaimInput{}))
}

func toolSchema(t *testing.T, name string) map[string]any {
	t.Helper()
	for _, tool := range agentTools(false) {
		if tool.Function.Name == name {
			return tool.Function.Parameters
		}
	}
	t.Fatalf("tool %s not found", name)
	return nil
}

func assertSchemaPropertiesMatchStruct(t *testing.T, path string, schema map[string]any, typ reflect.Type) {
	t.Helper()
	properties, _ := schema["properties"].(map[string]any)
	required, _ := schema["required"].([]string)
	fields := make(map[string]bool, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			fields[name] = true
		}
	}
	if len(properties) != len(fields) || len(required) != len(fields) {
		t.Fatalf("%s schema fields=%v required=%v Go fields=%v", path, properties, required, fields)
	}
	for name := range fields {
		if _, ok := properties[name]; !ok || !schemaContainsString(required, name) {
			t.Fatalf("%s.%s missing from schema or required", path, name)
		}
	}
}

func TestToolArgumentsDecodeStrictly(t *testing.T) {
	tool := newToolRuntime(t.TempDir(), "", nil, false, toolSurfaceAgent)
	tests := []struct {
		name string
		tool string
		json string
	}{
		{"unknown top-level field", "search_code", `{"keyword":"x","file":null,"extra":true}`},
		{"unknown selector field", "read_code", `{"file":"main.go","selector":{"kind":"range","start":1,"max_lines":10,"line":2}}`},
		{"fractional range integer", "read_code", `{"file":"main.go","selector":{"kind":"range","start":1.5,"max_lines":10}}`},
		{"fractional timeout", "run_go_validation", `{"mode":"compile","package":".","test":null,"timeout_seconds":1.5}`},
		{"multiple JSON values", "run_static_rules", `{} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := tool.prepare(test.tool, test.json); err == nil {
				t.Fatalf("%s accepted invalid arguments", test.tool)
			}
		})
	}
}

func TestGatewayAcceptsAgentToolSchemas(t *testing.T) {
	if os.Getenv("CODECRITIC_LIVE_TEST") != "1" {
		t.Skip("set CODECRITIC_LIVE_TEST=1 to exercise the configured model gateway")
	}
	baseURL := os.Getenv("CodeCritic_URL")
	apiKey := os.Getenv("CodeCritic_API_KEY")
	if baseURL == "" || apiKey == "" {
		t.Skip("CodeCritic_URL and CodeCritic_API_KEY are required")
	}
	llm := review.NewLLMWithConfig(
		review.WithBaseURL(baseURL),
		review.WithAPIKey(apiKey),
		review.WithModel("gpt-5.4"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	message, _, err := llm.CompleteToolsWithUsage(ctx, agentSystemPrompt, []review.ToolMessage{{
		Role:    "user",
		Content: "Call submit_claims with an empty claims array.",
	}}, agentTools(false), "gpt-5.4")
	if err != nil {
		t.Fatal(err)
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("tool calls=%+v", message.ToolCalls)
	}
}

func TestAgentReceivesCompleteDiffHunkBeyondTwentyFourAddedLines(t *testing.T) {
	repo := t.TempDir()
	var source strings.Builder
	source.WriteString("package main\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&source, "var v%d = %d\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte(source.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/diff\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var patch strings.Builder
	patch.WriteString("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,31 @@\n package main\n-var old = 1\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&patch, "+var v%d = %d\n", i, i)
	}
	llm := &scriptedLLM{calls: []review.ToolCall{toolCall("submit_claims", `{"claims":[]}`)}}
	wf, _ := NewReviewRun(llm, repo)
	if _, err := wf.Run(context.Background(), Request{Repo: repo, Diff: []byte(patch.String())}); err != nil {
		t.Fatal(err)
	}
	prompt := llm.transcripts[0][0].Content
	for _, want := range []string{"var old = 1", "var v29 = 29", `"omitted_hunks":[]`, "old=2 new=0", "old=0 new=31"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
}

func TestLargeDiffListsOmittedHunkExplicitly(t *testing.T) {
	change := diff.Change{File: "main.go", Hunks: []diff.Hunk{
		{ID: "h1", OldStart: 1, NewStart: 1, NewLines: 1, Lines: []diff.HunkLine{{Kind: "add", NewLine: 1, Text: strings.Repeat("x", 70<<10)}}},
		{ID: "h2", OldStart: 2, NewStart: 2, NewLines: 1, Lines: []diff.HunkLine{{Kind: "add", NewLine: 2, Text: "small"}}},
	}}
	encoded := encodeChangedContext(&Trace{}, []diff.Change{change})
	if !strings.Contains(encoded, `"omitted_hunks":["main.go:h1"]`) || !strings.Contains(encoded, `"hunk_id":"h2"`) {
		t.Fatalf("encoded=%s", encoded)
	}
}

func TestAgentRecoversOmittedHunkThroughReadDiff(t *testing.T) {
	repo := t.TempDir()
	largeLine := "// " + strings.Repeat("x", 70<<10)
	source := "package main\n" + largeLine + "\n"
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/large\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diffData := []byte("diff --git a/main.go b/main.go\n--- /dev/null\n+++ b/main.go\n@@ -0,0 +1,2 @@\n+package main\n+" + largeLine + "\n")
	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("read_diff", `{"file":"main.go","hunk_id":"h1"}`),
		toolCall("submit_claims", `{"claims":[{"file":"main.go","line":2,"severity":"warning","msg":"large hunk was recovered","evidence_ids":["e3"]}]}`),
	}}
	wf, _ := NewReviewRun(llm, repo)
	result, err := wf.Run(context.Background(), Request{Repo: repo, Diff: diffData})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llm.transcripts[0][0].Content, `"omitted_hunks":["main.go:h1"]`) {
		t.Fatal("initial prompt did not disclose the omitted hunk")
	}
	if len(result.Trace.Investigation) != 1 || result.Trace.Investigation[0].Tool != "read_diff" {
		t.Fatalf("investigation=%+v", result.Trace.Investigation)
	}
	if len(result.Trace.Evidence) != 3 || result.Trace.Evidence[2].Source != "read_diff" || !strings.Contains(result.Trace.Evidence[2].Content, largeLine) {
		t.Fatalf("evidence=%+v", result.Trace.Evidence)
	}
	if result.Trace.FinalReport.Verdicts[0].Status != VerdictAccepted {
		t.Fatalf("report=%+v", result.Trace.FinalReport)
	}
}

func TestVerifierOnlyChecksGenericEvidenceContract(t *testing.T) {
	repo := t.TempDir()
	evidence := []*Evidence{
		{ID: "e1", Source: "read_code", File: "main.go", Line: 10, EndLine: 20, Content: "arbitrary code"},
		{ID: "e2", Source: "find_callers", File: "caller.go", Line: 30, Content: "caller"},
	}
	tests := []struct {
		name  string
		claim review.CandidateClaim
		want  VerdictStatus
	}{
		{"anchored", review.CandidateClaim{File: "main.go", Line: 12, Severity: "error", Msg: "any semantic claim", EvidenceIDs: []string{"e1", "e2"}}, VerdictAccepted},
		{"missing evidence", review.CandidateClaim{File: "main.go", Line: 12, Severity: "error", Msg: "claim", EvidenceIDs: []string{"missing"}}, VerdictUnresolved},
		{"cross location only", review.CandidateClaim{File: "main.go", Line: 12, Severity: "error", Msg: "claim", EvidenceIDs: []string{"e2"}}, VerdictUnresolved},
		{"bad shape", review.CandidateClaim{File: "main.go", Line: 0, Severity: "urgent", Msg: "", EvidenceIDs: []string{"e1"}}, VerdictRejected},
		{"duplicate reference", review.CandidateClaim{File: "main.go", Line: 12, Severity: "error", Msg: "claim", EvidenceIDs: []string{"e1", "e1"}}, VerdictRejected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := []review.CandidateClaim{test.claim}
			verdicts, _ := verifyClaims(repo, claims, evidence)
			if verdicts[0].Status != test.want {
				t.Fatalf("verdict=%+v", verdicts[0])
			}
		})
	}
}

func TestInspectSymbolRequiresIndex(t *testing.T) {
	tool := &ToolRuntime{repo: t.TempDir()}
	_, err := tool.inspectSymbol(inspectSymbolArgs{Symbol: "Run", File: "main.go", Relation: "references"})
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("err=%v", err)
	}
}

func TestInspectConcurrencyRequiresIndex(t *testing.T) {
	tool := &ToolRuntime{repo: t.TempDir()}
	_, err := tool.inspectConcurrency(inspectConcurrencyArgs{Symbol: "Run", File: "main.go"})
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("err=%v", err)
	}
}

func TestSearchCodeRecordsScopedAbsence(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := &ToolRuntime{repo: repo, store: recall.New(repo, nil)}
	file := "main.go"
	evidence, err := tool.searchCode(searchCodeArgs{File: &file, Keyword: "missing"})
	if err != nil || len(evidence) != 1 || evidence[0].Type != "search_absence" {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func TestReadCodeBySymbol(t *testing.T) {
	repo := t.TempDir()
	source := "package main\n\nfunc First() {}\n\nfunc Second() {\n\tprintln(2)\n}\n"
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ToolRuntime{repo: repo}
	evidence, err := tool.readCode(readCodeArgs{File: "main.go", Selector: symbolSelector{Kind: "symbol", Symbol: "Second"}})
	if err != nil || len(evidence) != 1 || evidence[0].Line != 5 || strings.Contains(evidence[0].Content, "First") {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func TestReadCodeSelectorKinds(t *testing.T) {
	repo := t.TempDir()
	source := "package main\n\nfunc First() {}\n\nfunc Second() {\n\tprintln(2)\n}\n"
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ToolRuntime{repo: repo}
	tests := []struct {
		name     string
		selector readCodeSelector
		line     int
		endLine  int
	}{
		{name: "file start", selector: fileStartSelector{Kind: "file_start"}, line: 1, endLine: 8},
		{name: "range", selector: rangeSelector{Kind: "range", Start: 3, MaxLines: 2}, line: 3, endLine: 4},
		{name: "focus line", selector: lineSelector{Kind: "line", Line: 5, ContextLines: 1}, line: 4, endLine: 6},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence, err := tool.readCode(readCodeArgs{File: "main.go", Selector: test.selector})
			if err != nil || len(evidence) != 1 || evidence[0].Line != test.line || evidence[0].EndLine != test.endLine {
				t.Fatalf("evidence=%+v err=%v", evidence, err)
			}
		})
	}
}

func TestReadCodeRejectsSymlinkOutsideRepository(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.go")
	if err := os.WriteFile(outside, []byte("package secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "linked.go")); err != nil {
		t.Fatal(err)
	}
	tool := &ToolRuntime{repo: repo}
	_, err := tool.readCode(readCodeArgs{File: "linked.go", Selector: fileStartSelector{Kind: "file_start"}})
	if err == nil || !strings.Contains(err.Error(), "超出仓库") {
		t.Fatalf("err=%v", err)
	}
}

func TestGoValidationExecutionPermission(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/validation\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ToolRuntime{repo: repo, changes: []diff.Change{{File: "main.go"}}, surface: toolSurfaceAgent}
	evidence, err := tool.runGoValidation(context.Background(), goValidationArgs{Mode: "compile", Package: ".", TimeoutSeconds: 30})
	if err != nil || len(evidence) != 1 || evidence[0].Type != "validation_passed" {
		if len(evidence) > 0 {
			t.Fatalf("evidence=%+v content=%s err=%v", evidence, evidence[0].Content, err)
		}
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	_, err = tool.prepare("run_go_validation", `{"mode":"test","package":".","test":null,"timeout_seconds":30}`)
	if err == nil || !strings.Contains(err.Error(), "--allow-exec") {
		t.Fatalf("err=%v", err)
	}
}

func TestGoValidationAcceptsCurrentDirectorySlashPattern(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/slash\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ToolRuntime{repo: repo, changes: []diff.Change{{File: "main.go"}}}
	evidence, err := tool.runGoValidation(context.Background(), goValidationArgs{Mode: "compile", Package: "./", TimeoutSeconds: 30})
	if err != nil || len(evidence) != 1 || evidence[0].Type != "validation_passed" {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func TestGoPackagePatternMatchesToolContract(t *testing.T) {
	tests := map[string]bool{
		".": true, "./": true, "./...": true, "./pkg": true, "./pkg/sub": true, "./pkg/...": true,
		"./.../cmd": true, "./foo...bar": true, "./模块": true, "./pkg name": true,
		"": false, "../pkg": false, "/tmp/pkg": false, `./pkg\\sub`: false, "./../pkg": false,
		"./pkg//sub": false, "./pkg/./sub": false, "./pkg/../sub": false, "./pkg\nsub": false,
	}
	for pattern, valid := range tests {
		err := validatePackagePattern(pattern)
		if (err == nil) != valid {
			t.Errorf("pattern %q: valid=%v err=%v", pattern, valid, err)
		}
	}
}

func TestCompilePackageDirectoryCannotEscapeRepository(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(repo, "linked-package")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := ensureDirectoryWithinRepo(repo, link); err == nil || !strings.Contains(err.Error(), "escapes repository") {
		t.Fatalf("err=%v", err)
	}
}

func TestGoCompileValidationMatrix(t *testing.T) {
	t.Run("library dot and slash", func(t *testing.T) {
		repo := t.TempDir()
		writeTestFile(t, repo, "go.mod", "module example.com/library\n\ngo 1.22\n")
		writeTestFile(t, repo, "library.go", "package library\nfunc Value() int { return 1 }\n")
		for _, pattern := range []string{".", "./"} {
			assertCompileEvidence(t, repo, pattern, "validation_passed")
		}
	})

	t.Run("main package", func(t *testing.T) {
		repo := t.TempDir()
		writeTestFile(t, repo, "go.mod", "module example.com/mainapp\n\ngo 1.22\n")
		writeTestFile(t, repo, "main.go", "package main\nfunc main() {}\n")
		assertCompileEvidence(t, repo, "./", "validation_passed")
		if _, err := os.Stat(filepath.Join(repo, filepath.Base(repo))); !os.IsNotExist(err) {
			t.Fatalf("compile wrote an executable into repository: %v", err)
		}
	})

	t.Run("recursive mixed packages", func(t *testing.T) {
		repo := t.TempDir()
		writeTestFile(t, repo, "go.mod", "module example.com/mixed\n\ngo 1.22\n")
		writeTestFile(t, repo, "library.go", "package mixed\nfunc Value() int { return 1 }\n")
		writeTestFile(t, repo, "pkg/sub/sub.go", "package sub\nfunc Value() int { return 2 }\n")
		writeTestFile(t, repo, "cmd/app/main.go", "package main\nfunc main() {}\n")
		assertCompileEvidence(t, repo, "./...", "validation_passed")
	})

	t.Run("compile failure is evidence", func(t *testing.T) {
		repo := t.TempDir()
		writeTestFile(t, repo, "go.mod", "module example.com/broken\n\ngo 1.22\n")
		writeTestFile(t, repo, "broken.go", "package broken\nfunc Broken( {\n")
		assertCompileEvidence(t, repo, "./", "validation_failed")
	})

	t.Run("tool start failure is error", func(t *testing.T) {
		repo := t.TempDir()
		writeTestFile(t, repo, "go.mod", "module example.com/startfailure\n\ngo 1.22\n")
		writeTestFile(t, repo, "library.go", "package startfailure\n")
		t.Setenv("PATH", t.TempDir())
		tool := &ToolRuntime{repo: repo, changes: []diff.Change{{File: "library.go"}}}
		evidence, err := tool.runGoValidation(context.Background(), goValidationArgs{Mode: "compile", Package: "./", TimeoutSeconds: 30})
		if err == nil || len(evidence) != 0 || !strings.Contains(err.Error(), "start go list") {
			t.Fatalf("evidence=%+v err=%v", evidence, err)
		}
	})
}

func assertCompileEvidence(t *testing.T, repo, pattern, wantType string) {
	t.Helper()
	tool := &ToolRuntime{repo: repo, changes: []diff.Change{{File: "main.go"}}}
	evidence, err := tool.runGoValidation(context.Background(), goValidationArgs{Mode: "compile", Package: pattern, TimeoutSeconds: 30})
	if err != nil || len(evidence) != 1 || evidence[0].Type != wantType {
		if len(evidence) > 0 {
			t.Fatalf("pattern=%q evidence=%+v content=%s err=%v", pattern, evidence, evidence[0].Content, err)
		}
		t.Fatalf("pattern=%q evidence=%+v err=%v", pattern, evidence, err)
	}
}

func writeTestFile(t *testing.T, repo, name, content string) {
	t.Helper()
	path := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeEvidencePath(t *testing.T) {
	repo := t.TempDir()
	evidence := &Evidence{File: filepath.Join(repo, "pkg", "main.go"), Line: 1}
	if err := normalizeEvidencePath(repo, evidence); err != nil || evidence.File != "pkg/main.go" {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func testRepo(t *testing.T, source string, changedLine int) (string, []byte) {
	t.Helper()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/test\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	diffData := []byte("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,1 +1,1 @@\n-package main\n+package main\n")
	if changedLine > 1 {
		diffData = []byte("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -" + string(rune('0'+changedLine)) + ",1 +" + string(rune('0'+changedLine)) + ",1 @@\n-old\n+new\n")
	}
	return repo, diffData
}
