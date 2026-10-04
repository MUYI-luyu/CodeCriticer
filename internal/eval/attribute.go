package eval

import (
	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

// BugStage 表示 Ground Truth 首个未闭合的流水线阶段。
type BugStage string

const (
	StageInputMiss         BugStage = "input_miss"
	StageInvestigationMiss BugStage = "investigation_miss"
	StageClaimMiss         BugStage = "claim_miss"
	StageVerdictSelfHarm   BugStage = "verdict_self_harm"
	StageSuccess           BugStage = "success"
)

// BugAttribution 记录 Primary 在四个阶段的命中状态和 Related 的调查覆盖。
type BugAttribution struct {
	Bug                    Bug      `json:"bug"`
	Stage                  BugStage `json:"stage"`
	InputHit               bool     `json:"input_hit"`
	InvestigationHit       bool     `json:"investigation_hit"`
	RawClaimHit            bool     `json:"raw_claim_hit"`
	AcceptedClaimHit       bool     `json:"accepted_claim_hit"`
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
	rawClaimHit := claimsCoverAny(trace.FinalReport.Claims, acceptableLocations(c.GT), tol)
	acceptedClaimHit := claimsCoverAny(acceptedClaims(trace), acceptableLocations(c.GT), tol)
	relatedCovered := 0
	for _, related := range c.GT.Related {
		if evidenceSourceCovers(trace.Evidence, related, tol, false) {
			relatedCovered++
		}
	}

	return []BugAttribution{{
		Bug:                    bug,
		Stage:                  classify(inputHit, investigationHit, rawClaimHit, acceptedClaimHit),
		InputHit:               inputHit,
		InvestigationHit:       investigationHit,
		RawClaimHit:            rawClaimHit,
		AcceptedClaimHit:       acceptedClaimHit,
		RelatedEvidenceCovered: relatedCovered,
		RelatedEvidenceTotal:   len(c.GT.Related),
	}}
}

func claimsCoverAny(claims []review.CandidateClaim, locations []Location, tol int) bool {
	for _, location := range locations {
		if claimsCover(claims, location, tol) {
			return true
		}
	}
	return false
}

func classify(inputHit, investigationHit, rawClaimHit, acceptedClaimHit bool) BugStage {
	switch {
	case acceptedClaimHit:
		return StageSuccess
	case rawClaimHit:
		return StageVerdictSelfHarm
	case investigationHit:
		return StageClaimMiss
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

func claimsCover(claims []review.CandidateClaim, location Location, tol int) bool {
	for _, claim := range claims {
		if claim.File != location.File {
			continue
		}
		if location.Line <= 0 || abs(location.Line-claim.Line) <= tol {
			return true
		}
	}
	return false
}
