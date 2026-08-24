package agent

import (
	"context"
	"testing"
)

type fakeLLM struct {
	text string
	err  error
}

func (f fakeLLM) CompleteJSON(context.Context, string, string) (string, error) {
	return f.text, f.err
}

func TestParseAction(t *testing.T) {
	a, err := parseAction("```json\n{\"tool\":\"done\",\"reason\":\"ok\"}\n```")
	if err != nil {
		t.Fatalf("parseAction: %v", err)
	}
	if a.Tool != "done" || a.Reason != "ok" {
		t.Fatalf("unexpected action: %+v", a)
	}
}

func TestToolRegistry(t *testing.T) {
	r := NewToolRegistry()
	r.Register(LocateSymbolsTool{})
	if _, ok := r.Get("locate_symbols"); !ok {
		t.Fatalf("tool not registered")
	}
	if len(r.List()) == 0 {
		t.Fatalf("expected list entries")
	}
}

func TestOrchestratorDone(t *testing.T) {
	reg := NewToolRegistry()
	llm := fakeLLM{text: `{"tool":"done","reason":"complete"}`}
	o := NewOrchestrator(llm, reg)
	res, err := o.Execute(context.Background(), NewState("", ""))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Converged {
		t.Fatalf("expected converged result: %+v", res)
	}
}
