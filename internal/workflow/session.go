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
	run        *ReviewRun
	trace      *Trace
	tools      *ToolRuntime
	messages   []review.ToolMessage
	history    map[string]bool
	claims     []review.CandidateClaim
	verdicts   []Verdict
	stopReason string
	stats      TraceStats
	maxSteps   int
}

func (s *ReviewSession) stop(reason string) {
	s.stopReason = reason
}

func newReviewSession(run *ReviewRun, trace *Trace, tools *ToolRuntime) *ReviewSession {
	return &ReviewSession{
		run:      run,
		trace:    trace,
		tools:    tools,
		maxSteps: run.maxSteps,
		history:  make(map[string]bool),
		messages: []review.ToolMessage{{Role: "user", Content: buildAgentPrompt(trace, tools.changes, tools.allowExecution)}},
	}
}

// Run executes the existing single-session LLM→tool loop. Keeping this loop
// on ReviewSession makes its state boundary explicit without introducing a
// separate AgentLoop runtime.
func (s *ReviewSession) Run(ctx context.Context) ([]review.CandidateClaim, []Verdict, error) {
	defer func() {
		s.trace.Conversation = cloneToolMessages(s.messages)
		s.trace.Stats = s.stats
	}()
	tools := append(s.tools.definitions(), submitClaimsDefinition())
	maxDecisions := s.maxSteps*2 + 2
	for s.stats.DecisionCount < maxDecisions {
		step := len(s.trace.Investigation) + 1
		s.stats.DecisionCount++
		modelMessages := s.messages
		if s.run.projectContext {
			var stats ContextProjectionStats
			modelMessages, stats = projectContext(s.messages)
			stats.Enabled = true
			stats.Applications = s.trace.Projection.Applications + 1
			s.trace.Projection = stats
		}
		observedCalls := len(s.trace.LLMCalls)
		message, usage, err := s.run.llm.CompleteToolsWithUsage(ctx, agentSystemPrompt, modelMessages, tools, s.run.model)
		// review.LLM reports usage through the run observer. A custom LLMClient
		// may only return usage, so account for it when no observer callback was
		// emitted; this keeps Trace token accounting complete without doubling
		// the native client's totals.
		if len(s.trace.LLMCalls) == observedCalls {
			s.trace.Usage.PromptTokens += usage.PromptTokens
			s.trace.Usage.CompletionTokens += usage.CompletionTokens
			s.trace.Usage.TotalTokens += usage.TotalTokens
		}
		if err != nil {
			if ctx.Err() != nil {
				s.stop(StopContextCanceled)
			} else {
				s.stop(StopStageError)
			}
			return s.claims, s.verdicts, err
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
		if executedToolCalls >= s.maxSteps {
			msg := "工具预算已耗尽；请基于现有证据提交 claims"
			s.trace.Investigation = append(s.trace.Investigation, InvestigationStep{ID: fmt.Sprintf("s%d", step), Tool: call.Function.Name, Args: prepared.Args, Error: msg})
			s.messages = append(s.messages, toolResult(call.ID, map[string]any{"error": msg}))
			continue
		}
		key := call.Function.Name + ":" + prepared.Canonical
		if s.history[key] {
			s.stats.DuplicateCalls++
			msg := fmt.Sprintf("工具 %s 使用相同参数重复调用，未产生新证据", call.Function.Name)
			s.trace.Errors = append(s.trace.Errors, fmt.Sprintf("重复工具决策: %s", key))
			s.trace.Investigation = append(s.trace.Investigation, InvestigationStep{ID: fmt.Sprintf("s%d", step), Tool: call.Function.Name, Args: prepared.Args, Error: msg})
			s.messages = append(s.messages, toolResult(call.ID, map[string]any{"error": msg}))
			continue
		}
		s.history[key] = true
		investigationStep, ev := executeStep(call.Function.Name, prepared.Args, func() ([]*Evidence, error) {
			return s.tools.execute(ctx, prepared)
		})
		investigationStep.ID = fmt.Sprintf("s%d", step)
		_, novelEvidence := s.appendEvidence(investigationStep.ID, ev)
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
	}
	s.stop(StopMaxSteps)
	return s.claims, s.verdicts, fmt.Errorf("agent decision budget exhausted")
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
