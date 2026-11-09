package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

func TestReviewBenchRepoPathDefaultsToContractMount(t *testing.T) {
	t.Setenv("RB_REPO", "")
	if got := reviewBenchRepoPath(); got != "/work/repo" {
		t.Fatalf("default repo path = %q, want /work/repo", got)
	}
	t.Setenv("RB_REPO", "/tmp/local-review")
	if got := reviewBenchRepoPath(); got != "/tmp/local-review" {
		t.Fatalf("override repo path = %q", got)
	}
}

func TestWriteReviewBenchFindingsWritesAcceptedActionableClaimsOnly(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "findings.json")
	claims := []review.CandidateClaim{
		{ID: "accepted", File: "pkg/server.go", Line: 42, Severity: "warning", Msg: "the retry loop can spin forever"},
		{ID: "informational", File: "pkg/server.go", Line: 80, Severity: "info", Msg: "not emitted"},
		{ID: "rejected", File: "pkg/server.go", Line: 90, Severity: "warning", Msg: "not emitted"},
	}

	if err := writeReviewBenchFindings(output, reviewBenchMetadata{
		NWO:      "acme/example",
		PRNumber: 123,
		Base:     "base-sha",
		Head:     "head-sha",
		Agent:    "codecritic",
	}, claims, map[string]bool{"accepted": true, "informational": true}, reviewBenchUsage{PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15, TimeInMS: 42}); err != nil {
		t.Fatalf("writeReviewBenchFindings: %v", err)
	}

	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	var got reviewBenchOutput
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode findings: %v\n%s", err, data)
	}
	if got.PR.Repo != "https://github.com/acme/example" || got.PR.Number != 123 || got.PR.Base != "base-sha" || got.PR.Head != "head-sha" {
		t.Fatalf("unexpected PR metadata: %+v", got.PR)
	}
	if got.Agent != "codecritic" {
		t.Fatalf("agent = %q", got.Agent)
	}
	if len(got.Findings) != 1 {
		t.Fatalf("findings = %+v, want one accepted finding", got.Findings)
	}
	if finding := got.Findings[0]; finding.File != "pkg/server.go" || finding.StartLine != 42 || finding.EndLine != 42 || finding.Message != "the retry loop can spin forever" || finding.Producer != "codecritic" {
		t.Fatalf("unexpected finding: %+v", finding)
	}
	if got.Usage == nil || got.Usage.TotalTokens != 15 || got.Usage.TimeInMS != 42 {
		t.Fatalf("usage = %+v, want exported review usage", got.Usage)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatalf("stat findings: %v", err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("findings permissions = %o, want 644", info.Mode().Perm())
	}
}

func TestReviewBenchBudgetFromEnv(t *testing.T) {
	t.Setenv("RB_MAX_TOOL_CALLS", "12")
	t.Setenv("RB_MAX_TURNS", "24")
	t.Setenv("RB_MAX_TOTAL_TOKENS", "160000")
	got, err := reviewBenchBudgetFromEnv()
	if err != nil {
		t.Fatalf("reviewBenchBudgetFromEnv: %v", err)
	}
	if got != (reviewBenchBudget{MaxToolCalls: 12, MaxTurns: 24, MaxTotalTokens: 160000}) {
		t.Fatalf("budget = %+v", got)
	}
}

func TestReviewBenchBudgetRejectsInvalidValue(t *testing.T) {
	t.Setenv("RB_MAX_TURNS", "nope")
	if _, err := reviewBenchBudgetFromEnv(); err == nil {
		t.Fatal("reviewBenchBudgetFromEnv accepted invalid RB_MAX_TURNS")
	}
}

func TestWriteReviewBenchFindingsEncodesEmptyFindingsAsArray(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "findings.json")
	if err := writeReviewBenchFindings(output, reviewBenchMetadata{
		NWO: "acme/example", PRNumber: 123, Base: "base-sha", Head: "head-sha", Agent: "codecritic",
	}, nil, nil, reviewBenchUsage{}); err != nil {
		t.Fatalf("writeReviewBenchFindings: %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode findings: %v", err)
	}
	if string(raw["findings"]) != "[]" {
		t.Fatalf("findings JSON = %s, want []", raw["findings"])
	}
}
