package workflow

import (
	"errors"
	"strings"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

func TestActiveContextDropsCheckpointBeforeExceedingBudget(t *testing.T) {
	history := []review.ToolMessage{
		{Role: "user", Content: strings.Repeat("kernel ", 80)},
		{Role: "assistant", ToolCalls: []review.ToolCall{{ID: "call-1", Function: review.FunctionCall{Name: "read_code", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "call-1", Content: strings.Repeat("recent tool output ", 40)},
	}
	budget := toolMessagesBytes(history) + 16
	context := newActiveContext(budget, true)
	context.projected = true
	context.checkpointAt = 1
	context.checkpoint = &ContextCheckpoint{
		Task:     strings.Repeat("checkpoint ", 80),
		Reviewed: []CheckpointFact{{File: "main.go", Range: "1-10", Fact: "source was read"}},
	}

	messages, err := context.fit(history)
	if err != nil {
		t.Fatal(err)
	}
	if context.checkpoint != nil {
		t.Fatalf("checkpoint should be discarded when it exceeds budget: %+v", context.checkpoint)
	}
	if got := toolMessagesBytes(messages); got > budget {
		t.Fatalf("active context is %d bytes, budget is %d", got, budget)
	}
	if len(messages) != len(history) || messages[1].Role != "assistant" || messages[2].Role != "tool" {
		t.Fatalf("assistant/tool pair was not retained: %+v", messages)
	}
}

func TestActiveContextDoesNotCheckpointWithoutAToolExchange(t *testing.T) {
	history := []review.ToolMessage{
		{Role: "user", Content: strings.Repeat("kernel ", 80)},
		{Role: "assistant", ToolCalls: []review.ToolCall{{ID: "call-1", Function: review.FunctionCall{Name: "read_code", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "call-1", Content: strings.Repeat("tool output ", 200)},
	}
	context := newActiveContext(toolMessagesBytes(history[:1])+32, true)
	_, err := context.messagesForCheckpoint(history)
	if !errors.Is(err, errCheckpointNeedsToolExchange) {
		t.Fatalf("expected missing checkpoint source error, got %v", err)
	}
}

func TestActiveContextRejectsKernelThatExceedsBudget(t *testing.T) {
	history := []review.ToolMessage{{Role: "user", Content: strings.Repeat("kernel ", 80)}}
	context := newActiveContext(toolMessagesBytes(history)-1, true)
	if _, err := context.fit(history); err == nil || !strings.Contains(err.Error(), "context_budget_exhausted") {
		t.Fatalf("expected context_budget_exhausted, got %v", err)
	}
}
