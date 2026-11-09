package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

func TestAppendModelResponseLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "responses.jsonl")
	call := review.LLMCall{
		TraceID:  "review-test",
		Stage:    "agent",
		Model:    "test-model",
		Attempt:  1,
		Status:   200,
		Response: `{"role":"assistant","content":"inspect","tool_calls":[]}`,
		Usage:    review.LLMUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	}
	if err := appendModelResponseLog(path, call); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		TraceID  string `json:"trace_id"`
		Response string `json:"response"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid JSONL: %v", err)
	}
	if got.TraceID != call.TraceID || got.Response != call.Response {
		t.Fatalf("record=%+v", got)
	}
}
