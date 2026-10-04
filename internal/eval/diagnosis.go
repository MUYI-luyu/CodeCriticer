package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

type FailureDiagnosis struct {
	Name              string                 `json:"name"`
	BaselineLocation  bool                   `json:"baseline_location_match"`
	BaselineSemantic  string                 `json:"baseline_semantic,omitempty"`
	Probe             ToolProbeResult        `json:"tool_probe"`
	EvidenceInjection DiagnosticIntervention `json:"evidence_injection"`
	FullContext       DiagnosticIntervention `json:"full_context_injection"`
	LikelyLayer       string                 `json:"likely_layer"`
	Reason            string                 `json:"reason"`
	Limitation        string                 `json:"limitation"`
}

type ToolProbeResult struct {
	Calls          int                  `json:"calls"`
	Evidence       []*workflow.Evidence `json:"evidence,omitempty"`
	Errors         []string             `json:"errors,omitempty"`
	PrimaryCovered bool                 `json:"primary_covered"`
	RelatedCovered int                  `json:"related_covered"`
	RelatedTotal   int                  `json:"related_total"`
}

type DiagnosticIntervention struct {
	Claims        []review.CandidateClaim `json:"claims,omitempty"`
	LocationMatch bool                    `json:"location_match"`
	Semantic      *SemanticEvaluation     `json:"semantic,omitempty"`
	Usage         review.LLMUsage         `json:"usage"`
	Error         string                  `json:"error,omitempty"`
}

type probeLLM struct {
	calls []review.ToolCall
}

func (p *probeLLM) CompleteToolsWithUsage(context.Context, string, []review.ToolMessage, []review.ToolDefinition, string) (review.ToolMessage, review.LLMUsage, error) {
	if len(p.calls) == 0 {
		return diagnosticAssistantCall("probe-submit", "submit_claims", `{"claims":[]}`), review.LLMUsage{}, nil
	}
	call := p.calls[0]
	p.calls = p.calls[1:]
	return review.ToolMessage{Role: "assistant", ToolCalls: []review.ToolCall{call}}, review.LLMUsage{}, nil
}

func diagnoseFailures(ctx context.Context, llm *review.LLM, judge *semanticJudge, datasetDir, traceDir string, results []caseResult) error {
	cases, err := Load(datasetDir)
	if err != nil {
		return err
	}
	byName := make(map[string]*Case, len(cases))
	for _, c := range cases {
		byName[c.Name] = c
	}
	diagnoses := make([]FailureDiagnosis, 0)
	for _, result := range results {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !result.completed || acceptedHit(result.metrics) > 0 {
			continue
		}
		c := byName[result.name]
		if c == nil {
			continue
		}
		diagnosis := diagnoseCase(ctx, llm, judge, c, result)
		diagnoses = append(diagnoses, diagnosis)
		if traceDir != "" {
			if err := saveDiagnosis(filepath.Join(traceDir, "diagnosis"), diagnosis); err != nil {
				return err
			}
		}
	}
	printDiagnoses(os.Stdout, diagnoses)
	return nil
}

func diagnoseCase(ctx context.Context, llm *review.LLM, judge *semanticJudge, c *Case, baseline caseResult) FailureDiagnosis {
	diagnosis := FailureDiagnosis{
		Name:             c.Name,
		BaselineLocation: acceptedHit(baseline.metrics) > 0,
		Limitation:       "Single-run interventions are directional evidence only. They change the prompt and model sample, and the oracle-guided probe establishes location retrieval rather than semantic fact coverage.",
	}
	if baseline.semantic != nil && baseline.semantic.Error == "" {
		diagnosis.BaselineSemantic = baseline.semantic.ExpectedIssue
		if baseline.semantic.ExpectedIssue == "found" {
			diagnosis.LikelyLayer = "eval_or_judge_disagreement"
			diagnosis.Reason = "The semantic judge and positional scorer disagree; this flags the evaluation boundary but does not establish which evaluator is correct."
			return diagnosis
		}
	}
	diagnosis.Probe = runToolProbe(ctx, c)
	if len(diagnosis.Probe.Evidence) > 0 {
		diagnosis.EvidenceInjection = runDiagnosticIntervention(ctx, llm, judge, c, "evidence", diagnosis.Probe.Evidence)
	}
	diagnosis.FullContext = runDiagnosticIntervention(ctx, llm, judge, c, "full_context", nil)
	diagnosis.LikelyLayer, diagnosis.Reason = inferFailureLayer(baseline, diagnosis)
	return diagnosis
}

func runToolProbe(ctx context.Context, c *Case) ToolProbeResult {
	probe := ToolProbeResult{RelatedTotal: len(c.GT.Related)}
	repo, err := materialize(c)
	if err != nil {
		probe.Errors = append(probe.Errors, err.Error())
		return probe
	}
	defer os.RemoveAll(repo)
	calls := diagnosticProbeCalls(c)
	probe.Calls = len(calls)
	run, err := workflow.NewReviewRun(&probeLLM{calls: calls}, repo)
	if err != nil {
		probe.Errors = append(probe.Errors, err.Error())
		return probe
	}
	run.SetMaxSteps(len(calls))
	runResult, err := run.Run(ctx, workflow.Request{Repo: repo, Diff: c.Diff})
	if err != nil {
		probe.Errors = append(probe.Errors, err.Error())
	}
	if runResult == nil || runResult.Trace == nil {
		return probe
	}
	for _, step := range runResult.Trace.Investigation {
		if step.Error != "" {
			probe.Errors = append(probe.Errors, fmt.Sprintf("%s: %s", step.Tool, step.Error))
		}
	}
	for _, evidence := range runResult.Trace.Evidence {
		if evidence != nil && evidence.Source != "diff" {
			probe.Evidence = append(probe.Evidence, evidence)
		}
	}
	probe.PrimaryCovered = evidenceSourceCovers(probe.Evidence, c.GT.Primary, tol, false)
	for _, related := range c.GT.Related {
		if evidenceSourceCovers(probe.Evidence, related, tol, false) {
			probe.RelatedCovered++
		}
	}
	return probe
}

func diagnosticProbeCalls(c *Case) []review.ToolCall {
	locations := append(acceptableLocations(c.GT), c.GT.Related...)
	seen := make(map[string]bool)
	calls := make([]review.ToolCall, 0, 8)
	for _, location := range locations {
		if location.File == "" || len(calls) >= 8 {
			continue
		}
		key := fmt.Sprintf("%s:%d", location.File, location.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		selector := map[string]any{"kind": "file_start"}
		if location.Line > 0 {
			selector = map[string]any{"kind": "line", "line": location.Line, "context_lines": 12}
		}
		arguments, _ := json.Marshal(map[string]any{"file": location.File, "selector": selector})
		calls = append(calls, diagnosticToolCall(fmt.Sprintf("probe-read-%d", len(calls)+1), "read_code", string(arguments)))
	}
	for _, symbol := range c.GT.Symbols {
		if strings.TrimSpace(symbol) == "" || c.GT.Primary.File == "" || len(calls) >= 8 {
			continue
		}
		arguments, _ := json.Marshal(map[string]any{"symbol": symbol, "file": c.GT.Primary.File, "line": nil, "relation": "definition"})
		calls = append(calls, diagnosticToolCall(fmt.Sprintf("probe-symbol-%d", len(calls)+1), "inspect_symbol", string(arguments)))
	}
	return calls
}

func runDiagnosticIntervention(ctx context.Context, llm *review.LLM, judge *semanticJudge, c *Case, mode string, evidence []*workflow.Evidence) DiagnosticIntervention {
	result := DiagnosticIntervention{}
	payload := map[string]any{"mode": mode, "diff": string(c.Diff)}
	if mode == "evidence" {
		payload["evidence"] = evidence
	} else {
		payload["repository_files"] = c.Repo
	}
	encoded, _ := json.Marshal(payload)
	system := "This is an isolated code-review diagnostic, not the production agent loop. Review only the supplied diff and context. Submit supported issues; do not assume hidden ground truth."
	message, usage, err := llm.CompleteToolsWithUsage(ctx, system, []review.ToolMessage{{Role: "user", Content: string(encoded)}}, []review.ToolDefinition{diagnosticSubmitTool()}, llm.AgentModel())
	result.Usage = usage
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Name != "submit_diagnostic_claims" {
		result.Error = "diagnostic model did not submit exactly one claim set"
		return result
	}
	var submitted struct {
		Claims []review.CandidateClaim `json:"claims"`
	}
	if err := decodeDiagnosticArgs(message.ToolCalls[0].Function.Arguments, &submitted); err != nil {
		result.Error = err.Error()
		return result
	}
	for i := range submitted.Claims {
		submitted.Claims[i].ID = fmt.Sprintf("d%d", i+1)
	}
	result.Claims = submitted.Claims
	result.LocationMatch = computeOneIssueAtLocations(acceptableLocations(c.GT), result.Claims, tol).Found > 0
	if judge != nil {
		verdicts := make([]workflow.Verdict, 0, len(result.Claims))
		for _, claim := range result.Claims {
			verdicts = append(verdicts, workflow.Verdict{ClaimID: claim.ID, Status: workflow.VerdictAccepted, Reason: "diagnostic intervention output"})
		}
		trace := &workflow.Trace{Evidence: evidence, FinalReport: workflow.FinalReport{Claims: result.Claims, Verdicts: verdicts}}
		graded := judge.Grade(ctx, c, trace)
		result.Semantic = &graded
	}
	return result
}

func diagnosticSubmitTool() review.ToolDefinition {
	stringValue := map[string]any{"type": "string", "pattern": `\S`}
	return review.ToolDefinition{Type: "function", Function: review.ToolFunction{
		Name: "submit_diagnostic_claims", Description: "Submit the complete diagnostic claim set.", Strict: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{"claims": map[string]any{
				"type": "array", "items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{"type": []string{"string", "null"}}, "file": stringValue,
						"line": map[string]any{"type": "integer", "minimum": 1}, "severity": map[string]any{"type": "string", "enum": []string{"error", "warning", "info"}},
						"msg": stringValue, "evidence_ids": map[string]any{"type": "array", "items": stringValue},
					},
					"required": []string{"id", "file", "line", "severity", "msg", "evidence_ids"}, "additionalProperties": false,
				},
			}},
			"required": []string{"claims"}, "additionalProperties": false,
		},
	}}
}

func decodeDiagnosticArgs(arguments string, dst any) error {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("diagnostic claim arguments contain trailing JSON")
	}
	return nil
}

func inferFailureLayer(baseline caseResult, diagnosis FailureDiagnosis) (string, string) {
	if len(diagnosis.Probe.Errors) > 0 && len(diagnosis.Probe.Evidence) == 0 {
		return "diagnostic_infrastructure", "The forced tool probe failed before producing evidence, so no layer attribution is justified."
	}
	if !diagnosis.Probe.PrimaryCovered {
		return "probe_retrieval_gap", "The bounded oracle-guided probe did not recover the annotated primary location; this may reflect tool coverage or the probe plan."
	}
	if interventionFound(diagnosis.EvidenceInjection) {
		investigated := len(baseline.attrs) > 0 && baseline.attrs[0].InvestigationHit
		if investigated {
			return "evidence_use_or_sampling", "The baseline had location-covering evidence and one isolated-evidence run succeeded; evidence presentation is implicated, but model sampling also changed."
		}
		return "tool_selection_or_sampling", "The probe recovered the location and one direct-evidence run succeeded; tool selection is implicated, but the prompt and model sample also changed."
	}
	if interventionFound(diagnosis.FullContext) {
		return "context_or_sampling", "One full-context run succeeded after the baseline and evidence-only run missed; context is implicated, but a single unseeded run cannot establish causality."
	}
	if diagnosis.EvidenceInjection.Error != "" || diagnosis.FullContext.Error != "" {
		return "diagnostic_infrastructure", "At least one controlled model intervention failed operationally, so reasoning cannot be isolated."
	}
	return "unresolved_after_interventions", "The probe exposed the annotated location, but one evidence-only and one full-context sample both missed; this does not uniquely distinguish reasoning failure from model variance or missing semantic facts."
}

func interventionFound(result DiagnosticIntervention) bool {
	if result.Semantic != nil && result.Semantic.Error == "" {
		return result.Semantic.ExpectedIssue == "found"
	}
	return result.LocationMatch
}

func saveDiagnosis(dir string, diagnosis FailureDiagnosis) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(diagnosis, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, safeName(diagnosis.Name)+".json"), data, 0o600)
}

func printDiagnoses(w io.Writer, diagnoses []FailureDiagnosis) {
	if len(diagnoses) == 0 {
		fmt.Fprintln(w, "\n=== Failure Diagnosis ===\nNo completed misses to diagnose.")
		return
	}
	sort.Slice(diagnoses, func(i, j int) bool { return diagnoses[i].Name < diagnoses[j].Name })
	fmt.Fprintln(w, "\n=== Failure Diagnosis (controlled interventions) ===")
	for _, diagnosis := range diagnoses {
		fmt.Fprintf(w, "%-24s layer=%-20s probe=%t evidence_injection=%t full_context=%t\n",
			diagnosis.Name, diagnosis.LikelyLayer, diagnosis.Probe.PrimaryCovered,
			interventionFound(diagnosis.EvidenceInjection), interventionFound(diagnosis.FullContext))
	}
}

func diagnosticToolCall(id, name, arguments string) review.ToolCall {
	return review.ToolCall{ID: id, Type: "function", Function: review.FunctionCall{Name: name, Arguments: arguments}}
}

func diagnosticAssistantCall(id, name, arguments string) review.ToolMessage {
	return review.ToolMessage{Role: "assistant", ToolCalls: []review.ToolCall{diagnosticToolCall(id, name, arguments)}}
}
