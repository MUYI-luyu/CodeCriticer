package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

type semanticJudgeClient interface {
	CompleteToolsWithUsage(context.Context, string, []review.ToolMessage, []review.ToolDefinition, string) (review.ToolMessage, review.LLMUsage, error)
	AgentModel() string
}

type SemanticEvaluation struct {
	ExpectedIssue string               `json:"expected_issue"`
	Reason        string               `json:"reason"`
	Claims        []SemanticClaimGrade `json:"claims"`
	Usage         review.LLMUsage      `json:"usage"`
	Error         string               `json:"error,omitempty"`
}

type SemanticClaimGrade struct {
	ClaimID        string `json:"claim_id"`
	Outcome        string `json:"outcome"`
	OutcomeReason  string `json:"outcome_reason"`
	Evidence       string `json:"evidence"`
	EvidenceReason string `json:"evidence_reason"`
}

type semanticJudge struct {
	client semanticJudgeClient
	model  string
}

func newSemanticJudge(client semanticJudgeClient, model string) *semanticJudge {
	if client == nil {
		return nil
	}
	if strings.TrimSpace(model) == "" {
		model = client.AgentModel()
	}
	return &semanticJudge{client: client, model: model}
}

func (j *semanticJudge) Grade(ctx context.Context, c *Case, trace *workflow.Trace) SemanticEvaluation {
	if j == nil || j.client == nil || trace == nil {
		return SemanticEvaluation{}
	}
	type claimInput struct {
		ID       string                 `json:"id"`
		File     string                 `json:"file"`
		Line     int                    `json:"line"`
		Message  string                 `json:"message"`
		Verdict  workflow.VerdictStatus `json:"verdict"`
		Evidence []*workflow.Evidence   `json:"cited_evidence"`
	}
	byID := make(map[string]*workflow.Evidence, len(trace.Evidence))
	for _, item := range trace.Evidence {
		if item != nil {
			byID[item.ID] = item
		}
	}
	verdicts := make(map[string]workflow.VerdictStatus, len(trace.FinalReport.Verdicts))
	for _, verdict := range trace.FinalReport.Verdicts {
		verdicts[verdict.ClaimID] = verdict.Status
	}
	claims := make([]claimInput, 0, len(trace.FinalReport.Claims))
	for _, claim := range trace.FinalReport.Claims {
		status := verdicts[claim.ID]
		if status == "" {
			status = workflow.VerdictUnresolved
		}
		input := claimInput{ID: claim.ID, File: claim.File, Line: claim.Line, Message: claim.Msg, Verdict: status}
		for _, id := range claim.EvidenceIDs {
			if item := byID[id]; item != nil {
				input.Evidence = append(input.Evidence, item)
			}
		}
		claims = append(claims, input)
	}
	payload := map[string]any{
		"expected_issue": map[string]any{
			"description": c.GT.Description,
			"evidence":    c.GT.Evidence,
			"symbols":     c.GT.Symbols,
			"locations":   acceptableLocations(c.GT),
		},
		"claims": claims,
	}
	encoded, _ := json.Marshal(payload)
	system := "You independently grade a code-review result. Compare semantic root cause, trigger and consequence; do not require an exact line match. Grade evidence using only each claim's cited_evidence. expected_issue is found only when an accepted claim matches the expected issue; rejected or unresolved claims may still receive an individual outcome grade. Return uncertain when the supplied ground truth or evidence is insufficient."
	message, usage, err := j.client.CompleteToolsWithUsage(ctx, system, []review.ToolMessage{{Role: "user", Content: string(encoded)}}, []review.ToolDefinition{semanticJudgeTool()}, j.model)
	result := SemanticEvaluation{Usage: usage}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Name != "submit_semantic_evaluation" {
		result.Error = fmt.Sprintf("judge returned %d unexpected tool calls", len(message.ToolCalls))
		return result
	}
	if err := decodeJudgeArgs(message.ToolCalls[0].Function.Arguments, &result); err != nil {
		result.Error = err.Error()
	} else if err := validateSemanticEvaluation(result, trace); err != nil {
		result.Error = err.Error()
	}
	result.Usage = usage
	return result
}

func validateSemanticEvaluation(result SemanticEvaluation, trace *workflow.Trace) error {
	if !oneOf(result.ExpectedIssue, "found", "missed", "uncertain") {
		return fmt.Errorf("judge returned invalid expected_issue %q", result.ExpectedIssue)
	}
	if trace == nil {
		return fmt.Errorf("semantic judge validation requires a trace")
	}
	claims := trace.FinalReport.Claims
	want := make(map[string]bool, len(claims))
	for _, claim := range claims {
		if claim.ID == "" || want[claim.ID] {
			return fmt.Errorf("trace contains missing or duplicate claim id %q", claim.ID)
		}
		want[claim.ID] = true
	}
	seen := make(map[string]bool, len(result.Claims))
	for _, grade := range result.Claims {
		if !want[grade.ClaimID] {
			return fmt.Errorf("judge graded unknown claim %q", grade.ClaimID)
		}
		if seen[grade.ClaimID] {
			return fmt.Errorf("judge graded claim %q more than once", grade.ClaimID)
		}
		if !oneOf(grade.Outcome, "same_issue", "different_issue", "uncertain") {
			return fmt.Errorf("judge returned invalid outcome %q for claim %q", grade.Outcome, grade.ClaimID)
		}
		if !oneOf(grade.Evidence, "supported", "insufficient", "uncertain") {
			return fmt.Errorf("judge returned invalid evidence grade %q for claim %q", grade.Evidence, grade.ClaimID)
		}
		seen[grade.ClaimID] = true
	}
	for id := range want {
		if !seen[id] {
			return fmt.Errorf("judge omitted claim %q", id)
		}
	}

	verdicts := make(map[string]workflow.VerdictStatus, len(trace.FinalReport.Verdicts))
	for _, verdict := range trace.FinalReport.Verdicts {
		verdicts[verdict.ClaimID] = verdict.Status
	}
	evidence := make(map[string]bool, len(trace.Evidence))
	for _, item := range trace.Evidence {
		if item != nil && item.ID != "" {
			evidence[item.ID] = true
		}
	}
	claimByID := make(map[string]review.CandidateClaim, len(claims))
	for _, claim := range claims {
		claimByID[claim.ID] = claim
	}
	acceptedSameIssue := false
	for _, grade := range result.Claims {
		claim := claimByID[grade.ClaimID]
		if grade.Evidence == "supported" {
			resolved := false
			for _, id := range claim.EvidenceIDs {
				if evidence[id] {
					resolved = true
					break
				}
			}
			if !resolved {
				return fmt.Errorf("judge marked claim %q supported without resolved cited evidence", grade.ClaimID)
			}
		}
		if grade.Outcome == "same_issue" && verdicts[grade.ClaimID] == workflow.VerdictAccepted {
			acceptedSameIssue = true
		}
	}
	if result.ExpectedIssue == "found" && !acceptedSameIssue {
		return fmt.Errorf("judge marked expected issue found without an accepted same-issue claim")
	}
	if result.ExpectedIssue == "missed" && acceptedSameIssue {
		return fmt.Errorf("judge marked expected issue missed despite an accepted same-issue claim")
	}
	return nil
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func semanticJudgeTool() review.ToolDefinition {
	stringEnum := func(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	return review.ToolDefinition{Type: "function", Function: review.ToolFunction{
		Name: "submit_semantic_evaluation", Description: "Submit independent outcome and cited-evidence grades.", Strict: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"expected_issue": stringEnum("found", "missed", "uncertain"),
				"reason":         map[string]any{"type": "string"},
				"claims": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"claim_id":        map[string]any{"type": "string"},
							"outcome":         stringEnum("same_issue", "different_issue", "uncertain"),
							"outcome_reason":  map[string]any{"type": "string"},
							"evidence":        stringEnum("supported", "insufficient", "uncertain"),
							"evidence_reason": map[string]any{"type": "string"},
						},
						"required":             []string{"claim_id", "outcome", "outcome_reason", "evidence", "evidence_reason"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"expected_issue", "reason", "claims"},
			"additionalProperties": false,
		},
	}}
}

func decodeJudgeArgs(arguments string, dst any) error {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode semantic judge: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode semantic judge: trailing JSON")
	}
	return nil
}
