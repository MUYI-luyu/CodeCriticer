package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/review"
	"github.com/MUYI-luyu/codecritic/internal/workflow"
)

// reviewBenchMetadata is the subset of ReviewBench's invocation environment
// that must be echoed in findings.json.
type reviewBenchMetadata struct {
	NWO      string
	PRNumber int
	Base     string
	Head     string
	Agent    string
}

type reviewBenchOutput struct {
	PR struct {
		Repo   string `json:"repo"`
		Number int    `json:"pr_number"`
		Base   string `json:"base"`
		Head   string `json:"head"`
	} `json:"pr"`
	Agent    string               `json:"agent"`
	Findings []reviewBenchFinding `json:"findings"`
	Usage    *reviewBenchUsage    `json:"usage,omitempty"`
}

type reviewBenchFinding struct {
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Message   string `json:"message"`
	Producer  string `json:"producer"`
}

// reviewBenchUsage follows ReviewBench's optional usage contract. It makes
// cost and wall-clock data available to the official judge without changing
// what the review agent sees or how it investigates.
type reviewBenchUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
	TimeInMS         int `json:"time_in_ms,omitempty"`
}

// reviewBenchBudget only configures ReviewRun's existing safety valves. It is
// intentionally adapter-local: regular `codecritic review` behavior and the
// Agent's prompts, tools, and verdict logic remain unchanged.
type reviewBenchBudget struct {
	MaxToolCalls   int
	MaxTurns       int
	MaxTotalTokens int
}

func cmdReviewBench(args []string) {
	if err := runReviewBench(context.Background(), args); err != nil {
		fmt.Fprintln(os.Stderr, "ReviewBench adapter failed:", err)
		os.Exit(1)
	}
}

func runReviewBench(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("reviewbench does not accept CLI arguments; use ReviewBench RB_* environment variables")
	}
	metadata, err := reviewBenchMetadataFromEnv()
	if err != nil {
		return fmt.Errorf("read ReviewBench environment: %w", err)
	}
	key := getEnv("CodeCritic_API_KEY", "DEEPSEEK_API_KEY")
	if key == "" {
		return fmt.Errorf("CodeCritic_API_KEY or DEEPSEEK_API_KEY is required")
	}

	snapshot, diffData, err := workflow.CreateGitReviewSnapshot(ctx, reviewBenchRepoPath(), metadata.Base, metadata.Head, false)
	if err != nil {
		return fmt.Errorf("create ReviewBench snapshot: %w", err)
	}
	defer func() {
		if closeErr := snapshot.Close(); closeErr != nil {
			fmt.Fprintln(os.Stderr, "清理 ReviewBench snapshot 失败:", closeErr)
		}
	}()

	run, err := workflow.NewWithSnapshot(buildReviewBenchLLM(key), snapshot)
	if err != nil {
		return fmt.Errorf("create ReviewBench review run: %w", err)
	}
	defer func() {
		if closeErr := run.Close(); closeErr != nil {
			fmt.Fprintln(os.Stderr, "清理 ReviewBench review run 失败:", closeErr)
		}
	}()
	budget, err := reviewBenchBudgetFromEnv()
	if err != nil {
		return fmt.Errorf("read ReviewBench budget: %w", err)
	}
	if budget.MaxToolCalls > 0 {
		run.SetMaxToolCalls(budget.MaxToolCalls)
	}
	if budget.MaxTurns > 0 {
		run.SetMaxTurns(budget.MaxTurns)
	}
	if budget.MaxTotalTokens > 0 {
		run.SetMaxTotalTokens(budget.MaxTotalTokens)
	}
	result, err := run.Run(ctx, workflow.Request{Diff: diffData})
	if err != nil {
		return fmt.Errorf("run ReviewBench review: %w", err)
	}
	if tracePath := strings.TrimSpace(os.Getenv("RB_TRACE")); tracePath != "" {
		if err := result.Trace.Save(tracePath); err != nil {
			return fmt.Errorf("save ReviewBench trace: %w", err)
		}
	}
	if result.Trace.StopReason == workflow.StopPartial {
		return fmt.Errorf("review stopped partial; refusing to write findings")
	}
	accepted := make(map[string]bool)
	for _, claim := range result.Trace.FinalReport.ClaimsWithStatus(workflow.VerdictAccepted) {
		accepted[claim.ID] = true
	}
	usage := reviewBenchUsage{
		PromptTokens:     result.Trace.Usage.PromptTokens,
		CompletionTokens: result.Trace.Usage.CompletionTokens,
		TotalTokens:      result.Trace.Usage.TotalTokens,
		TimeInMS:         int(result.Trace.Duration.Milliseconds()),
	}
	if err := writeReviewBenchFindings(os.Getenv("RB_OUT"), metadata, result.Trace.FinalReport.Claims, accepted, usage); err != nil {
		return fmt.Errorf("write ReviewBench findings: %w", err)
	}
	return nil
}

func reviewBenchBudgetFromEnv() (reviewBenchBudget, error) {
	parse := func(name string) (int, error) {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			return 0, nil
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%s must be a non-negative integer", name)
		}
		return n, nil
	}
	tools, err := parse("RB_MAX_TOOL_CALLS")
	if err != nil {
		return reviewBenchBudget{}, err
	}
	turns, err := parse("RB_MAX_TURNS")
	if err != nil {
		return reviewBenchBudget{}, err
	}
	tokens, err := parse("RB_MAX_TOTAL_TOKENS")
	if err != nil {
		return reviewBenchBudget{}, err
	}
	return reviewBenchBudget{MaxToolCalls: tools, MaxTurns: turns, MaxTotalTokens: tokens}, nil
}

// reviewBenchRepoPath keeps the official runner contract while allowing the
// same adapter to be exercised locally without constructing /work/repo.
func reviewBenchRepoPath() string {
	if repo := strings.TrimSpace(os.Getenv("RB_REPO")); repo != "" {
		return repo
	}
	return "/work/repo"
}

func reviewBenchMetadataFromEnv() (reviewBenchMetadata, error) {
	metadata := reviewBenchMetadata{
		NWO:   strings.TrimSpace(os.Getenv("RB_NWO")),
		Base:  strings.TrimSpace(os.Getenv("RB_BASE")),
		Head:  strings.TrimSpace(os.Getenv("RB_HEAD")),
		Agent: strings.TrimSpace(os.Getenv("RB_AGENT")),
	}
	prNumber := strings.TrimSpace(os.Getenv("RB_PR_NUMBER"))
	output := strings.TrimSpace(os.Getenv("RB_OUT"))
	if metadata.NWO == "" || metadata.Base == "" || metadata.Head == "" || metadata.Agent == "" || prNumber == "" || output == "" {
		return reviewBenchMetadata{}, fmt.Errorf("RB_NWO, RB_PR_NUMBER, RB_BASE, RB_HEAD, RB_AGENT and RB_OUT are required")
	}
	if strings.Contains(metadata.NWO, "://") || strings.HasPrefix(metadata.NWO, "/") || strings.Count(metadata.NWO, "/") != 1 {
		return reviewBenchMetadata{}, fmt.Errorf("RB_NWO must be owner/repo")
	}
	n, err := strconv.Atoi(prNumber)
	if err != nil || n <= 0 {
		return reviewBenchMetadata{}, fmt.Errorf("RB_PR_NUMBER must be a positive integer")
	}
	metadata.PRNumber = n
	return metadata, nil
}

func buildReviewBenchLLM(key string) *review.LLM {
	baseURL := strings.TrimSpace(os.Getenv("RB_MODEL_BASE_URL"))
	if baseURL == "" {
		baseURL = getEnv("CodeCritic_URL", "DEEPSEEK_BASE_URL")
	}
	if baseURL == "" {
		baseURL = review.DefaultConfig().BaseURL
	}
	opts := []review.Option{review.WithAPIKey(key), review.WithBaseURL(baseURL)}
	if model := strings.TrimSpace(os.Getenv("RB_CONFIG_MODEL")); model != "" {
		opts = append(opts, review.WithModel(model))
	}
	return review.NewLLMWithConfig(opts...)
}

func writeReviewBenchFindings(path string, metadata reviewBenchMetadata, claims []review.CandidateClaim, accepted map[string]bool, usage reviewBenchUsage) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty RB_OUT")
	}
	var output reviewBenchOutput
	output.PR.Repo = "https://github.com/" + metadata.NWO
	output.PR.Number = metadata.PRNumber
	output.PR.Base = metadata.Base
	output.PR.Head = metadata.Head
	output.Agent = metadata.Agent
	// ReviewBench requires an array even for a completed review with no
	// findings. A nil Go slice would marshal as JSON null and be rejected.
	output.Findings = make([]reviewBenchFinding, 0)
	output.Usage = &usage
	for _, claim := range claims {
		if !accepted[claim.ID] || strings.EqualFold(strings.TrimSpace(claim.Severity), "info") {
			continue
		}
		output.Findings = append(output.Findings, reviewBenchFinding{
			File: claim.File, StartLine: claim.Line, EndLine: claim.Line,
			Message: claim.Msg, Producer: metadata.Agent,
		})
	}
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("encode findings: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	// ReviewBench's host-side runner validates and copies RB_OUT after the
	// container exits. Docker bind mounts can leave the file owned by root on
	// the host, so it must be world-readable even though it contains no secret.
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("write findings: %w", err)
	}
	return nil
}
