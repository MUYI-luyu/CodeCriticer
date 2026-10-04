package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNativeToolCallingRequestAndResponse(t *testing.T) {
	var request chatReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"read_code","arguments":"{\"file\":\"main.go\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer srv.Close()

	l := NewLLMWithConfig(WithAPIKey("test"), WithBaseURL(srv.URL))
	tools := []ToolDefinition{{Type: "function", Function: ToolFunction{Name: "read_code", Strict: true, Parameters: map[string]any{"type": "object"}}}}
	message, _, err := l.CompleteToolsWithUsage(context.Background(), "system", []ToolMessage{{Role: "user", Content: "inspect"}}, tools, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if request.ToolChoice != "required" || request.ParallelToolCalls == nil || *request.ParallelToolCalls {
		t.Fatalf("request does not enforce one native tool call: %+v", request)
	}
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Name != "read_code" {
		t.Fatalf("message=%+v", message)
	}
}

func TestDefaultRequestTimeoutSupportsReasoningModels(t *testing.T) {
	l := NewLLMWithConfig(WithAPIKey("test"), WithBaseURL("https://example.com/v1"))
	if l.client.Timeout != 120*time.Second {
		t.Fatalf("timeout=%v", l.client.Timeout)
	}
}

func TestToolCompletionRetriesOn429(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"submit_claims","arguments":"{\"claims\":[]}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer srv.Close()

	l := NewLLMWithConfig(WithAPIKey("test"), WithBaseURL(srv.URL), WithModel("test-model"))
	if _, _, err := l.CompleteToolsWithUsage(context.Background(), "system", []ToolMessage{{Role: "user", Content: "prompt"}}, []ToolDefinition{{Type: "function", Function: ToolFunction{Name: "submit_claims"}}}, "test-model"); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts=%d", attempts)
	}
	stat := l.Metrics()["test-model"]
	if stat.Calls != 1 || stat.Success != 1 || stat.Retries != 1 || stat.InputTokens != 10 || stat.OutputTokens != 5 {
		t.Fatalf("metrics=%+v", stat)
	}
}

func TestToolCompletionDoesNotRetry400(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer srv.Close()

	l := NewLLMWithConfig(WithAPIKey("test"), WithBaseURL(srv.URL), WithModel("test-model"))
	if _, _, err := l.CompleteToolsWithUsage(context.Background(), "system", []ToolMessage{{Role: "user", Content: "prompt"}}, []ToolDefinition{{Type: "function", Function: ToolFunction{Name: "submit_claims"}}}, "test-model"); err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d", attempts)
	}
}
