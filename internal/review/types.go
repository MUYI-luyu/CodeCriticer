package review

// CandidateClaim 是 Agent 基于已收集证据提出的候选问题。
// 它在 Verdict 产生前不代表系统已经确认问题成立。
type CandidateClaim struct {
	ID          string   `json:"id,omitempty"`
	File        string   `json:"file"`
	Line        int      `json:"line"`
	Severity    string   `json:"severity"`
	Msg         string   `json:"msg"`
	EvidenceIDs []string `json:"evidence_ids"`
}
