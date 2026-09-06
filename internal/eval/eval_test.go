package eval

import (
	"errors"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

func TestDetectFailureStage(t *testing.T) {
	tests := []struct {
		name  string
		trace *workflow.Trace
		err   error
		want  string
	}{
		{
			name:  "Review 请求失败",
			trace: &workflow.Trace{LLMCalls: []review.LLMCall{{Stage: "review", Error: "timeout"}}},
			err:   errors.New("review: timeout"),
			want:  "review",
		},
		{
			name:  "调查决策失败",
			trace: &workflow.Trace{LLMCalls: []review.LLMCall{{Stage: "investigate", Error: "EOF"}}},
			err:   errors.New("EOF"),
			want:  "investigate",
		},
		{
			name:  "Evaluate 未产生记录",
			trace: &workflow.Trace{Findings: []review.Finding{{File: "main.go", Line: 10}}},
			err:   errors.New("evaluate failed"),
			want:  "evaluate",
		},
	}
	for _, test := range tests {
		if got := detectFailureStage(test.trace, test.err); got != test.want {
			t.Errorf("%s: got=%s want=%s", test.name, got, test.want)
		}
	}
}
