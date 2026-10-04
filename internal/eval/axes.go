package eval

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

// EvaluationAxes keeps independent diagnostic dimensions. It intentionally has
// no aggregate score: a run may investigate well but still produce a bad claim.
type EvaluationAxes struct {
	Outcome       OutcomeAxis       `json:"outcome"`
	Evidence      EvidenceAxis      `json:"evidence"`
	Investigation InvestigationAxis `json:"investigation"`
	Efficiency    EfficiencyAxis    `json:"efficiency"`
	Infra         InfraAxis         `json:"infra"`
}

// EvaluationAxesSummary aggregates raw counts only. Ratios can be derived by
// consumers without collapsing the independent axes into a composite score.
type EvaluationAxesSummary struct {
	Cases         int               `json:"cases"`
	Outcome       OutcomeAxis       `json:"outcome"`
	Evidence      EvidenceAxis      `json:"evidence"`
	Investigation InvestigationAxis `json:"investigation"`
	Efficiency    EfficiencyAxis    `json:"efficiency"`
	InfraFailures int               `json:"infra_failures"`
}

func (s EvaluationAxesSummary) Add(axes EvaluationAxes) EvaluationAxesSummary {
	s.Cases++
	s.Outcome.RawClaims += axes.Outcome.RawClaims
	s.Outcome.AcceptedClaims += axes.Outcome.AcceptedClaims
	s.Evidence.ClaimsWithEvidence += axes.Evidence.ClaimsWithEvidence
	s.Evidence.ClaimsWithResolvedRefs += axes.Evidence.ClaimsWithResolvedRefs
	s.Evidence.ClaimsWithUnresolvedRefs += axes.Evidence.ClaimsWithUnresolvedRefs
	s.Evidence.CitedEvidence += axes.Evidence.CitedEvidence
	s.Evidence.Sources = addCounts(s.Evidence.Sources, axes.Evidence.Sources)
	s.Investigation.Steps += axes.Investigation.Steps
	s.Investigation.SuccessfulTools += axes.Investigation.SuccessfulTools
	s.Investigation.FailedTools += axes.Investigation.FailedTools
	s.Investigation.NoNewEvidenceCalls += axes.Investigation.NoNewEvidenceCalls
	s.Investigation.Evidence += axes.Investigation.Evidence
	s.Investigation.ToolEvidence += axes.Investigation.ToolEvidence
	s.Investigation.EvidenceSources = addCounts(s.Investigation.EvidenceSources, axes.Investigation.EvidenceSources)
	s.Efficiency.DecisionCount += axes.Efficiency.DecisionCount
	s.Efficiency.DuplicateCalls += axes.Efficiency.DuplicateCalls
	s.Efficiency.PromptTokens += axes.Efficiency.PromptTokens
	s.Efficiency.OutputTokens += axes.Efficiency.OutputTokens
	s.Efficiency.TotalTokens += axes.Efficiency.TotalTokens
	s.Efficiency.Duration += axes.Efficiency.Duration
	if !axes.Infra.Completed {
		s.InfraFailures++
	}
	return s
}

func (s EvaluationAxesSummary) Print(w io.Writer) {
	fmt.Fprintln(w, "\n=== Trace Eval 独立指标 ===")
	fmt.Fprintf(w, "Outcome       raw_claims=%d accepted_claims=%d\n", s.Outcome.RawClaims, s.Outcome.AcceptedClaims)
	fmt.Fprintf(w, "Evidence      cited=%d resolved_claims=%d unresolved_claims=%d\n",
		s.Evidence.CitedEvidence, s.Evidence.ClaimsWithResolvedRefs, s.Evidence.ClaimsWithUnresolvedRefs)
	fmt.Fprintf(w, "Investigation tools=%d failed=%d no_new_evidence=%d tool_evidence=%d\n",
		s.Investigation.SuccessfulTools, s.Investigation.FailedTools, s.Investigation.NoNewEvidenceCalls, s.Investigation.ToolEvidence)
	fmt.Fprintf(w, "Efficiency    decisions=%d duplicate_calls=%d tokens=%d duration=%s\n",
		s.Efficiency.DecisionCount, s.Efficiency.DuplicateCalls, s.Efficiency.TotalTokens, s.Efficiency.Duration)
	fmt.Fprintf(w, "Infra         completed=%d failed=%d\n", s.Cases-s.InfraFailures, s.InfraFailures)
}

func addCounts(dst, src map[string]int) map[string]int {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]int)
	}
	for key, value := range src {
		dst[key] += value
	}
	return dst
}

type SemanticSummary struct {
	Cases       int
	JudgeErrors int
	JudgeTokens int
	Expected    map[string]int
	Outcomes    map[string]int
	Evidence    map[string]int
}

func (s SemanticSummary) Add(result *SemanticEvaluation) SemanticSummary {
	if result == nil {
		return s
	}
	s.Cases++
	s.JudgeTokens += result.Usage.TotalTokens
	if result.Error != "" {
		s.JudgeErrors++
		return s
	}
	s.Expected = increment(s.Expected, result.ExpectedIssue)
	for _, claim := range result.Claims {
		s.Outcomes = increment(s.Outcomes, claim.Outcome)
		s.Evidence = increment(s.Evidence, claim.Evidence)
	}
	return s
}

func (s SemanticSummary) Print(w io.Writer) {
	if s.Cases == 0 {
		return
	}
	fmt.Fprintln(w, "\n=== 可选语义评审（独立于 Agent）===")
	fmt.Fprintf(w, "Cases=%d judge_errors=%d judge_tokens=%d\n", s.Cases, s.JudgeErrors, s.JudgeTokens)
	fmt.Fprintf(w, "Expected issue found=%d missed=%d uncertain=%d\n", s.Expected["found"], s.Expected["missed"], s.Expected["uncertain"])
	fmt.Fprintf(w, "Claim outcome same=%d different=%d uncertain=%d\n", s.Outcomes["same_issue"], s.Outcomes["different_issue"], s.Outcomes["uncertain"])
	fmt.Fprintf(w, "Cited evidence supported=%d insufficient=%d uncertain=%d\n", s.Evidence["supported"], s.Evidence["insufficient"], s.Evidence["uncertain"])
}

func increment(counts map[string]int, key string) map[string]int {
	if counts == nil {
		counts = make(map[string]int)
	}
	counts[key]++
	return counts
}

type OutcomeAxis struct {
	RawClaims      int `json:"raw_claims"`
	AcceptedClaims int `json:"accepted_claims"`
}

type EvidenceAxis struct {
	ClaimsWithEvidence       int            `json:"claims_with_evidence"`
	ClaimsWithResolvedRefs   int            `json:"claims_with_resolved_refs"`
	ClaimsWithUnresolvedRefs int            `json:"claims_with_unresolved_refs"`
	CitedEvidence            int            `json:"cited_evidence"`
	Sources                  map[string]int `json:"sources,omitempty"`
}

type InvestigationAxis struct {
	Steps              int            `json:"steps"`
	SuccessfulTools    int            `json:"successful_tools"`
	FailedTools        int            `json:"failed_tools"`
	NoNewEvidenceCalls int            `json:"no_new_evidence_calls"`
	Evidence           int            `json:"evidence"`
	ToolEvidence       int            `json:"tool_evidence"`
	EvidenceSources    map[string]int `json:"evidence_sources,omitempty"`
}

type EfficiencyAxis struct {
	DecisionCount  int           `json:"decision_count"`
	DuplicateCalls int           `json:"duplicate_calls"`
	PromptTokens   int           `json:"prompt_tokens"`
	OutputTokens   int           `json:"output_tokens"`
	TotalTokens    int           `json:"total_tokens"`
	Duration       time.Duration `json:"duration"`
}

type InfraAxis struct {
	Completed   bool     `json:"completed"`
	StopReason  string   `json:"stop_reason,omitempty"`
	LLMFailures int      `json:"llm_failures"`
	Errors      []string `json:"errors,omitempty"`
}

func ComputeAxes(trace *workflow.Trace, completed bool) EvaluationAxes {
	var axes EvaluationAxes
	axes.Infra.Completed = completed
	if trace == nil {
		return axes
	}
	axes.Outcome.RawClaims = len(trace.FinalReport.Claims)
	axes.Outcome.AcceptedClaims = len(trace.FinalReport.ClaimsWithStatus(workflow.VerdictAccepted))
	axes.Evidence.Sources = make(map[string]int)
	axes.Investigation.EvidenceSources = make(map[string]int)
	byID := make(map[string]*workflow.Evidence, len(trace.Evidence))
	for _, evidence := range trace.Evidence {
		if evidence == nil {
			continue
		}
		byID[evidence.ID] = evidence
		axes.Investigation.Evidence++
		axes.Investigation.EvidenceSources[evidence.Source]++
		if evidence.Source != "diff" {
			axes.Investigation.ToolEvidence++
		}
	}
	for _, claim := range trace.FinalReport.Claims {
		if len(claim.EvidenceIDs) == 0 {
			continue
		}
		axes.Evidence.ClaimsWithEvidence++
		resolved := true
		for _, id := range claim.EvidenceIDs {
			evidence := byID[id]
			if evidence == nil {
				resolved = false
				continue
			}
			axes.Evidence.CitedEvidence++
			axes.Evidence.Sources[evidence.Source]++
		}
		if resolved {
			axes.Evidence.ClaimsWithResolvedRefs++
		} else {
			axes.Evidence.ClaimsWithUnresolvedRefs++
		}
	}
	axes.Investigation.Steps = len(trace.Investigation)
	axes.Investigation.SuccessfulTools = trace.Stats.SuccessfulToolCalls
	axes.Investigation.FailedTools = trace.Stats.FailedToolCalls
	axes.Investigation.NoNewEvidenceCalls = trace.Stats.NoNewEvidenceCalls
	axes.Efficiency.DecisionCount = trace.Stats.DecisionCount
	axes.Efficiency.DuplicateCalls = trace.Stats.DuplicateCalls
	axes.Efficiency.PromptTokens = trace.Usage.PromptTokens
	axes.Efficiency.OutputTokens = trace.Usage.CompletionTokens
	axes.Efficiency.TotalTokens = trace.Usage.TotalTokens
	axes.Efficiency.Duration = trace.Duration
	axes.Infra.StopReason = trace.StopReason
	axes.Infra.Errors = append([]string(nil), trace.Errors...)
	for _, call := range trace.LLMCalls {
		if strings.TrimSpace(call.Error) != "" {
			axes.Infra.LLMFailures++
		}
	}
	return axes
}
