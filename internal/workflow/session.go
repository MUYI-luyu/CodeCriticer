package workflow

import (
	"context"
	"fmt"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

// ReviewSession owns one continuous model investigation inside a ReviewRun.
// Its message history, tool history, budget decisions, submitted claims and
// verifier feedback live here; the enclosing run owns the durable Trace and
// Evidence identities.
type ReviewSession struct {
	run             *ReviewRun
	trace           *Trace
	tools           *ToolRuntime
	messages        []review.ToolMessage
	history         map[string]bool
	historyEvidence map[string]evidenceRecovery
	claims          []review.CandidateClaim
	verdicts        []Verdict
	stopReason      string
	stats           TraceStats
	state           *agentState
	activeContext   *activeContext
	protocolRepairs int
}

type evidenceRecovery struct {
	StepID      string
	EvidenceIDs []string
}

func (s *ReviewSession) stop(reason string) {
	s.stopReason = reason
}

func newReviewSession(run *ReviewRun, trace *Trace, tools *ToolRuntime) *ReviewSession {
	session := &ReviewSession{
		run:             run,
		trace:           trace,
		tools:           tools,
		history:         make(map[string]bool),
		historyEvidence: make(map[string]evidenceRecovery),
		messages:        []review.ToolMessage{{Role: "user", Content: buildAgentPrompt(trace, tools.changes)}},
	}
	tools.setEvidenceReader(session.rehydrateEvidence)
	return session
}

// Run executes the existing single-session LLM→tool loop. Keeping this loop
// on ReviewSession makes its state boundary explicit without introducing a
// separate AgentLoop runtime.
func (s *ReviewSession) Run(ctx context.Context) ([]review.CandidateClaim, []Verdict, error) {
	defer func() {
		s.trace.Conversation = cloneToolMessages(s.messages)
		s.trace.Stats = s.stats
	}()
	s.state = newAgentState(s.trace)
	s.activeContext = newActiveContext(s.run.modelContextBudget, s.run.contextCompactionEnabled)
	s.activeContext.setCompactedKernel(review.ToolMessage{Role: "user", Content: buildCompactedAgentPrompt(s.trace, s.tools.changes)})
	s.syncReviewState()
	tools := append(s.tools.definitions(), submitClaimsDefinition())
	for s.stats.DecisionCount < s.run.maxTurns {
		s.activeContext.setInvestigationIndex(investigationIndexPrompt(s.trace))
		if s.run.maxTotalTokens > 0 && s.trace.Usage.TotalTokens >= s.run.maxTotalTokens {
			s.state.review.Phase = "partial"
			s.stop(StopPartial)
			s.syncReviewState()
			return s.claims, s.verdicts, nil
		}
		if s.activeContext.needsCompaction(s.messages) {
			if err := s.activeContext.compact(ctx, s); err != nil {
				s.trace.Errors = append(s.trace.Errors, err.Error())
				s.trace.Compaction = s.activeContext.stats
				s.state.review.Phase = "partial"
				s.stop(StopPartial)
				s.syncReviewState()
				return s.claims, s.verdicts, nil
			}
		}
		step := len(s.trace.Investigation) + 1
		s.stats.DecisionCount++
		modelMessages := s.activeContext.messages(s.messages)
		s.activeContext.stats.FullBytes = toolMessagesBytes(s.messages)
		s.activeContext.stats.ActiveBytes = toolMessagesBytes(modelMessages)
		s.trace.Compaction = s.activeContext.stats
		observedCalls := len(s.trace.LLMCalls)
		message, usage, err := s.run.llm.CompleteToolsWithUsage(ctx, agentSystemPrompt+s.state.prompt(), modelMessages, tools, s.run.model)
		// review.LLM reports usage through the run observer. A custom LLMClient
		// may only return usage, so account for it when no observer callback was
		// emitted; this keeps Trace token accounting complete without doubling
		// the native client's totals.
		if len(s.trace.LLMCalls) == observedCalls {
			s.recordUsage(usage)
		}
		if err != nil {
			if ctx.Err() != nil {
				s.stop(StopContextCanceled)
			} else {
				s.stop(StopStageError)
			}
			return s.claims, s.verdicts, err
		}
		if len(message.ToolCalls) == 0 && s.protocolRepairs < 1 {
			// Providers occasionally return a plain-text final answer despite the
			// function-calling contract. Preserve it in Trace and give the model one
			// explicit repair turn; this is protocol recovery, not a second planner.
			s.protocolRepairs++
			s.trace.Errors = append(s.trace.Errors, "协议修复：模型未调用 Function Calling")
			s.messages = append(s.messages, message, review.ToolMessage{
				Role: "user",
				Content: "Protocol reminder: do not answer in plain text. Call exactly one function now. " +
					"If your review is complete, call submit_claims with the complete claims array (possibly empty).",
			})
			continue
		}
		if len(message.ToolCalls) != 1 {
			s.stop(StopInvalidDecision)
			return s.claims, s.verdicts, fmt.Errorf("agent must return exactly one tool call, got %d", len(message.ToolCalls))
		}
		call := message.ToolCalls[0]
		s.messages = append(s.messages, message)
		if call.Function.Name == "submit_claims" {
			submission, err := decodeStrict[submitClaimsArgs](call.Function.Arguments)
			if err != nil {
				s.stop(StopInvalidDecision)
				return s.claims, s.verdicts, fmt.Errorf("submit_claims arguments: %w", err)
			}
			s.claims = submission.candidateClaims()
			var needsRevision bool
			s.verdicts, needsRevision = verifyClaims(s.run.repo, s.claims, s.run.evidenceSnapshot())
			if !needsRevision {
				s.state.finish()
				s.syncReviewState()
				s.stop(StopAgentDone)
				return s.claims, s.verdicts, nil
			}
			s.messages = append(s.messages, toolResult(call.ID, map[string]any{
				"accepted":    false,
				"verdicts":    s.verdicts,
				"instruction": "Use the verdict reasons to investigate or correct the claims, then submit the complete claim set again.",
			}))
			continue
		}
		prepared, err := s.tools.prepare(call.Function.Name, call.Function.Arguments)
		if err != nil {
			s.stop(StopInvalidDecision)
			return s.claims, s.verdicts, fmt.Errorf("%s arguments: %w", call.Function.Name, err)
		}
		executedToolCalls := s.stats.SuccessfulToolCalls + s.stats.FailedToolCalls + s.stats.NoNewEvidenceCalls
		if s.run.maxToolCalls > 0 && executedToolCalls >= s.run.maxToolCalls {
			msg := "工具调用安全上限已到；请基于现有证据提交 claims。若无法给出完整结论，本次将以 partial 结束。"
			s.trace.Investigation = append(s.trace.Investigation, InvestigationStep{ID: fmt.Sprintf("s%d", step), Tool: call.Function.Name, Args: prepared.Args, Error: msg})
			s.messages = append(s.messages, toolResult(call.ID, map[string]any{"error": msg}))
			s.state.observe(call.Function.Name, nil, msg)
			s.syncReviewState()
			continue
		}
		key := call.Function.Name + ":" + prepared.Canonical
		if s.history[key] {
			s.stats.DuplicateCalls++
			msg := fmt.Sprintf("工具 %s 使用相同参数重复调用，未产生新证据", call.Function.Name)
			s.trace.Errors = append(s.trace.Errors, fmt.Sprintf("重复工具决策: %s", key))
			s.trace.Investigation = append(s.trace.Investigation, InvestigationStep{ID: fmt.Sprintf("s%d", step), Tool: call.Function.Name, Args: prepared.Args, Error: msg})
			result := map[string]any{"error": msg}
			if recovery, ok := s.historyEvidence[key]; ok && len(recovery.EvidenceIDs) > 0 {
				result["original_step_id"] = recovery.StepID
				result["evidence_ids"] = recovery.EvidenceIDs
				result["instruction"] = "If the original content is no longer in context, call read_evidence with the relevant evidence_ids."
			}
			s.messages = append(s.messages, toolResult(call.ID, result))
			s.state.observe(call.Function.Name, nil, msg)
			s.syncReviewState()
			continue
		}
		s.history[key] = true
		investigationStep, ev := executeStep(call.Function.Name, prepared.Args, func() ([]*Evidence, error) {
			return s.tools.execute(ctx, prepared)
		})
		investigationStep.ID = fmt.Sprintf("s%d", step)
		_, novelEvidence := s.appendEvidence(investigationStep.ID, ev)
		s.historyEvidence[key] = evidenceRecovery{StepID: investigationStep.ID, EvidenceIDs: evidenceIDs(ev)}
		s.trace.Investigation = append(s.trace.Investigation, investigationStep)
		if investigationStep.Error == "" {
			if len(ev) == 0 || !novelEvidence {
				investigationStep.Error = "工具调用未产生新证据"
				s.trace.Investigation[len(s.trace.Investigation)-1].Error = investigationStep.Error
				s.stats.NoNewEvidenceCalls++
			} else {
				s.stats.SuccessfulToolCalls++
			}
		} else {
			s.stats.FailedToolCalls++
		}
		s.run.logger.Info("workflow tool", "trace_id", s.trace.ID, "step", step, "tool", call.Function.Name, "evidence", len(ev), "error", investigationStep.Error)
		s.messages = append(s.messages, toolResult(call.ID, map[string]any{"evidence": ev, "error": investigationStep.Error}))
		s.state.observe(call.Function.Name, ev, investigationStep.Error)
		s.syncReviewState()
	}
	s.state.review.Phase = "partial"
	s.stop(StopPartial)
	s.syncReviewState()
	return s.claims, s.verdicts, nil
}

func evidenceIDs(evidence []*Evidence) []string {
	ids := make([]string, 0, len(evidence))
	for _, item := range evidence {
		if item != nil && item.ID != "" {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

func (s *ReviewSession) rehydrateEvidence(ids []string) ([]*Evidence, error) {
	byID := make(map[string]*Evidence)
	for _, item := range s.run.evidenceSnapshot() {
		if item != nil {
			byID[item.ID] = item
		}
	}
	result := make([]*Evidence, 0, len(ids))
	for _, id := range ids {
		item, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("Evidence %s does not exist in this review", id)
		}
		result = append(result, cloneEvidence(item))
	}
	return result, nil
}

func (s *ReviewSession) recordUsage(usage review.LLMUsage) {
	s.trace.Usage.PromptTokens += usage.PromptTokens
	s.trace.Usage.CompletionTokens += usage.CompletionTokens
	s.trace.Usage.TotalTokens += usage.TotalTokens
}

func (s *ReviewSession) syncReviewState() {
	if s.state != nil {
		s.trace.ReviewState = s.state.snapshot()
	}
}

// appendEvidence preserves every run-owned Evidence object. Exact duplicate
// results may reuse the existing identity; covered-but-different read windows
// are appended with their own identity and reported as non-novel. A larger
// result is always appended, so an Evidence ID never changes its meaning.
func (s *ReviewSession) appendEvidence(stepID string, evidence []*Evidence) (observed, novel bool) {
	observedEvidence := false
	novelEvidence := false
	for _, item := range evidence {
		if item == nil {
			continue
		}
		item.StepID = stepID
		var reused, reusedStepID string
		covered := false
		for _, prior := range s.run.evidenceSnapshot() {
			if prior == nil || prior.Source != item.Source || prior.File != item.File {
				continue
			}
			if sameEvidenceMeaning(prior, item) {
				reused = prior.ID
				reusedStepID = prior.StepID
				break
			}
			if item.Source == "read_code" && item.Line >= prior.Line && maxEvidenceLine(item) <= maxEvidenceLine(prior) {
				covered = true
			}
		}
		if reused != "" {
			item.ID = reused
			item.StepID = reusedStepID
			observedEvidence = true
			continue
		}
		s.run.appendEvidence(item)
		observedEvidence = true
		if !covered {
			novelEvidence = true
		}
	}
	return observedEvidence, novelEvidence
}

func sameEvidenceMeaning(a, b *Evidence) bool {
	return a != nil && b != nil &&
		a.Source == b.Source &&
		a.Type == b.Type &&
		a.File == b.File &&
		a.Line == b.Line &&
		a.EndLine == b.EndLine &&
		a.Content == b.Content &&
		a.Symbol == b.Symbol
}
