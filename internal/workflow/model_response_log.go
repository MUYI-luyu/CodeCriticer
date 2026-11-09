package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

const modelResponseLogPath = "logs/model-responses.jsonl"

var modelResponseLogMu sync.Mutex

// appendModelResponseLog records the provider's returned payload for later
// inspection. It is intentionally outside the Agent conversation and never
// changes the message sent to the next model call.
func appendModelResponseLog(path string, call review.LLMCall) error {
	record := struct {
		Timestamp time.Time       `json:"timestamp"`
		TraceID   string          `json:"trace_id,omitempty"`
		Stage     string          `json:"stage,omitempty"`
		Model     string          `json:"model"`
		Attempt   int             `json:"attempt"`
		Status    int             `json:"status"`
		Response  string          `json:"response,omitempty"`
		Usage     review.LLMUsage `json:"usage"`
		Error     string          `json:"error,omitempty"`
	}{
		Timestamp: time.Now().UTC(),
		TraceID:   call.TraceID,
		Stage:     call.Stage,
		Model:     call.Model,
		Attempt:   call.Attempt,
		Status:    call.Status,
		Response:  call.Response,
		Usage:     call.Usage,
		Error:     call.Error,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	modelResponseLogMu.Lock()
	defer modelResponseLogMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}
