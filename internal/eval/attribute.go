package eval

import (
	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

// BugStage 表示 Ground Truth 首个未闭合的流水线阶段。
type BugStage string

const (
	StageInputMiss          BugStage = "input_miss"
	StageInvestigationMiss  BugStage = "investigation_miss"
	StageReviewMiss         BugStage = "review_miss"
	StageEvaluationSelfHarm BugStage = "evaluation_self_harm"
	StageSuccess            BugStage = "success"
)

// BugAttribution 记录 Primary 在四个阶段的命中状态和 Related 的调查覆盖。
type BugAttribution struct {
	Bug                    Bug      `json:"bug"`
	Stage                  BugStage `json:"stage"`
	InputHit               bool     `json:"input_hit"`
	InvestigationHit       bool     `json:"investigation_hit"`
	RawFindingHit          bool     `json:"raw_finding_hit"`
	AcceptedFindingHit     bool     `json:"accepted_finding_hit"`
	RelatedEvidenceCovered int      `json:"related_evidence_covered"`
	RelatedEvidenceTotal   int      `json:"related_evidence_total"`
}

// Attribute 对一个 Case 的 Primary 做阶段归因，Related 仅用于调查覆盖统计。
func Attribute(c *Case, trace *workflow.Trace, tol int) []BugAttribution {
	primary := c.GT.Primary
	bug := Bug{File: primary.File, Line: primary.Line, Desc: c.GT.Description}
	if trace == nil {
		return []BugAttribution{{
			Bug:                  bug,
			Stage:                StageInputMiss,
			RelatedEvidenceTotal: len(c.GT.Related),
		}}
	}

	inputHit := evidenceSourceCovers(trace.Evidence, primary, tol, true)
	investigationHit := evidenceSourceCovers(trace.Evidence, primary, tol, false)
	rawFindingHit := findingsCover(trace.Findings, primary, tol)
	acceptedFindingHit := findingsCover(acceptedFindings(trace), primary, tol)
	relatedCovered := 0
	for _, related := range c.GT.Related {
		if evidenceSourceCovers(trace.Evidence, related, tol, false) {
			relatedCovered++
		}
	}

	return []BugAttribution{{
		Bug:                    bug,
		Stage:                  classify(inputHit, investigationHit, rawFindingHit, acceptedFindingHit),
		InputHit:               inputHit,
		InvestigationHit:       investigationHit,
		RawFindingHit:          rawFindingHit,
		AcceptedFindingHit:     acceptedFindingHit,
		RelatedEvidenceCovered: relatedCovered,
		RelatedEvidenceTotal:   len(c.GT.Related),
	}}
}

func classify(inputHit, investigationHit, rawFindingHit, acceptedFindingHit bool) BugStage {
	switch {
	case acceptedFindingHit:
		return StageSuccess
	case rawFindingHit:
		return StageEvaluationSelfHarm
	case investigationHit:
		return StageReviewMiss
	case inputHit:
		return StageInvestigationMiss
	default:
		return StageInputMiss
	}
}

func evidenceSourceCovers(evidence []*workflow.Evidence, location Location, tol int, input bool) bool {
	for _, item := range evidence {
		if item == nil || (item.Source == "diff") != input {
			continue
		}
		if evidenceCovers(item, location, tol) {
			return true
		}
	}
	return false
}

func evidenceCovers(evidence *workflow.Evidence, location Location, tol int) bool {
	if evidence.File != location.File {
		return false
	}
	if location.Line <= 0 {
		return true
	}
	end := evidence.EndLine
	if end < evidence.Line {
		end = evidence.Line
	}
	return location.Line >= evidence.Line-tol && location.Line <= end+tol
}

func findingsCover(findings []review.Finding, location Location, tol int) bool {
	for _, finding := range findings {
		if finding.File != location.File {
			continue
		}
		if location.Line <= 0 || abs(location.Line-finding.Line) <= tol {
			return true
		}
	}
	return false
}
