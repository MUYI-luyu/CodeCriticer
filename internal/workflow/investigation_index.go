package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// investigationIndex is a request-local view derived from Trace. It records
// delivery only: returned code is not thereby understood, safe, or complete.
// It is not persisted as a second source of review truth.
type investigationIndex struct {
	DiffHunksReturned  []returnedDiffHunk  `json:"diff_hunks_returned,omitempty"`
	CodeRangesReturned []returnedCodeRange `json:"code_ranges_returned,omitempty"`
	ToolOutcomes       []toolOutcome       `json:"tool_outcomes,omitempty"`
}

type returnedDiffHunk struct {
	File        string   `json:"file"`
	HunkID      string   `json:"hunk_id"`
	StepID      string   `json:"step_id"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
}

type returnedCodeRange struct {
	File        string   `json:"file"`
	StartLine   int      `json:"start_line"`
	EndLine     int      `json:"end_line"`
	StepID      string   `json:"step_id"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
}

type toolOutcome struct {
	StepID string `json:"step_id"`
	Tool   string `json:"tool"`
	Target string `json:"target,omitempty"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

func investigationIndexPrompt(trace *Trace) string {
	return "Deterministic investigation index, derived from actual tool calls. " +
		"`returned` means only that a result was delivered to you; it is not a review verdict. " +
		"Listed Evidence IDs are stable Claim references and are not a re-read checklist. " +
		"Call read_evidence only when an exact source detail is necessary and absent from the recent raw tool results.\n" + mustJSON(deriveInvestigationIndex(trace))
}

func deriveInvestigationIndex(trace *Trace) investigationIndex {
	if trace == nil {
		return investigationIndex{}
	}
	evidenceByStep := make(map[string][]string)
	codeByStep := make(map[string][]returnedCodeRange)
	for _, item := range trace.Evidence {
		if item == nil || item.StepID == "" {
			continue
		}
		if item.ID != "" {
			evidenceByStep[item.StepID] = append(evidenceByStep[item.StepID], item.ID)
		}
		if item.Source == "read_code" {
			codeByStep[item.StepID] = append(codeByStep[item.StepID], returnedCodeRange{File: item.File, StartLine: item.Line, EndLine: item.EndLine, StepID: item.StepID})
		}
	}
	for stepID := range evidenceByStep {
		sort.Strings(evidenceByStep[stepID])
	}

	index := investigationIndex{}
	for _, step := range trace.Investigation {
		status := investigationOutcomeStatus(step.Error)
		index.ToolOutcomes = append(index.ToolOutcomes, toolOutcome{StepID: step.ID, Tool: step.Tool, Target: investigationTarget(step.Args), Status: status, Reason: truncateStateText(step.Error, 160)})
		if step.Error != "" {
			continue
		}
		if args, ok := indexReadDiffArgs(step.Args); ok {
			index.DiffHunksReturned = append(index.DiffHunksReturned, returnedDiffHunk{File: args.File, HunkID: args.HunkID, StepID: step.ID, EvidenceIDs: evidenceByStep[step.ID]})
		}
		for _, code := range codeByStep[step.ID] {
			code.EvidenceIDs = evidenceByStep[step.ID]
			index.CodeRangesReturned = append(index.CodeRangesReturned, code)
		}
	}
	return index
}

func investigationOutcomeStatus(err string) string {
	switch {
	case err == "":
		return "returned"
	case strings.Contains(err, "重复调用"):
		return "duplicate"
	case strings.Contains(err, "未产生新证据"):
		return "returned_no_new_evidence"
	default:
		return "error"
	}
}

// InvestigationStep.Args becomes map[string]any after a Trace JSON replay.
// Keep this derivation usable both during a live review and when a caller
// reconstructs the index from the persisted trace.
func indexReadDiffArgs(args any) (readDiffArgs, bool) {
	if value, ok := args.(readDiffArgs); ok {
		return value, value.File != "" && value.HunkID != ""
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return readDiffArgs{}, false
	}
	var value readDiffArgs
	if err := json.Unmarshal(encoded, &value); err != nil || value.File == "" || value.HunkID == "" {
		return readDiffArgs{}, false
	}
	return value, true
}

func investigationTarget(args any) string {
	switch value := args.(type) {
	case readDiffArgs:
		return value.File + ":" + value.HunkID
	case readCodeArgs:
		return value.File
	case searchCodeArgs:
		if value.File != nil {
			return *value.File + ":" + value.Keyword
		}
		return value.Keyword
	case readEvidenceArgs:
		return fmt.Sprintf("%d evidence IDs", len(value.EvidenceIDs))
	default:
		return ""
	}
}
