package workflow

import (
	"fmt"
	"strings"
)

// agentState is short-term memory, not a host-owned plan. The model remains
// free to choose its next investigation tool and to decide when to submit.
type agentState struct {
	review ReviewState
}

func newAgentState(tr *Trace) *agentState {
	return &agentState{review: ReviewState{
		Objective: fmt.Sprintf("审查固定 Diff 中的真实缺陷：%s", strings.Join(tr.Scope.TargetFiles, ", ")),
		Phase:     "investigate",
	}}
}

func (s *agentState) observe(tool string, evidence []*Evidence, err string) {
	result := tool
	if err != "" {
		result += ": failed (" + truncateStateText(err, 120) + ")"
	} else {
		result += fmt.Sprintf(": %d evidence", len(evidence))
	}
	s.review.RecentActions = appendBounded(s.review.RecentActions, result, 4)

	for _, item := range evidence {
		if item == nil || item.ID == "" {
			continue
		}
		s.review.Facts = appendFact(s.review.Facts, ReviewFact{
			EvidenceID: item.ID,
			Summary:    evidenceSummary(item),
		}, 12)
	}

}

func (s *agentState) finish() {
	s.review.Phase = "done"
}

func (s *agentState) snapshot() ReviewState {
	copy := s.review
	copy.Facts = append([]ReviewFact(nil), s.review.Facts...)
	copy.RecentActions = append([]string(nil), s.review.RecentActions...)
	return copy
}

func (s *agentState) prompt() string {
	// The conversation already contains complete tool results for normal-sized
	// reviews. Re-injecting lossy host summaries alongside them made the model
	// attend to bookkeeping rather than source. ReviewState is therefore trace
	// metadata, not an extra prompt-side memory channel. Long-context projection
	// remains an explicit experiment rather than the default path.
	return ""
}

func evidenceSummary(e *Evidence) string {
	location := e.File
	if e.Line > 0 {
		location = fmt.Sprintf("%s:%d", e.File, e.Line)
	}
	symbol := ""
	if e.Symbol != "" {
		symbol = " symbol=" + e.Symbol
	}
	return fmt.Sprintf("%s %s %s%s", e.Source, location, e.Type, symbol)
}

func appendFact(facts []ReviewFact, fact ReviewFact, limit int) []ReviewFact {
	if fact.EvidenceID == "" {
		return facts
	}
	for _, existing := range facts {
		if existing.EvidenceID == fact.EvidenceID {
			return facts
		}
	}
	facts = append(facts, fact)
	if len(facts) > limit {
		return append([]ReviewFact(nil), facts[len(facts)-limit:]...)
	}
	return facts
}

func appendBounded(values []string, value string, limit int) []string {
	if value == "" {
		return values
	}
	values = append(values, value)
	if len(values) > limit {
		return append([]string(nil), values[len(values)-limit:]...)
	}
	return values
}

func truncateStateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
