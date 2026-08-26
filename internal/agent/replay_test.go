package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

func TestTraceSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	tracePath := filepath.Join(tmpDir, "test_trace.json")

	// 创建测试 trace
	trace := &Trace{
		ID:   "test-trace-001",
		Diff: "diff --git a/test.go b/test.go\n...",
		Repo: "/tmp/test-repo",
		Result: &Result{
			Converged: true,
			Reason:    "all_high_confidence",
			Attempts: []Attempt{
				{
					Round:     1,
					StartedAt: time.Now(),
					Duration:  2 * time.Second,
					Findings: []review.Finding{
						{File: "test.go", Line: 10, Msg: "test finding"},
					},
					Validations: []Validation{
						{FindingID: 0, Confidence: 0.9},
					},
				},
			},
			FinalFindings: []review.Finding{
				{File: "test.go", Line: 10, Msg: "test finding"},
			},
			TotalDuration: 2 * time.Second,
		},
		CreatedAt: time.Now(),
	}

	// 保存
	if err := trace.Save(tracePath); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// 检查文件存在
	if _, err := os.Stat(tracePath); os.IsNotExist(err) {
		t.Fatal("Trace file not created")
	}

	// 加载
	loaded, err := Load(tracePath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// 验证
	if loaded.ID != trace.ID {
		t.Errorf("ID = %q, want %q", loaded.ID, trace.ID)
	}

	if loaded.Result == nil {
		t.Fatal("Result is nil")
	}

	if len(loaded.Result.Attempts) != 1 {
		t.Errorf("Attempts count = %d, want 1", len(loaded.Result.Attempts))
	}
}

func TestTraceReplay(t *testing.T) {
	trace := &Trace{
		ID: "replay-test",
		Result: &Result{
			Attempts: []Attempt{
				{Round: 1, Findings: []review.Finding{{File: "a.go", Line: 1}}},
				{Round: 2, Findings: []review.Finding{{File: "b.go", Line: 2}}},
				{Round: 3, Findings: []review.Finding{{File: "c.go", Line: 3}}},
			},
		},
	}

	// 回放第 2 轮
	attempts := trace.Replay(2)
	if len(attempts) != 2 {
		t.Errorf("Replay(2) returned %d attempts, want 2", len(attempts))
	}

	// 回放所有轮
	attempts = trace.Replay(3)
	if len(attempts) != 3 {
		t.Errorf("Replay(3) returned %d attempts, want 3", len(attempts))
	}

	// 越界
	attempts = trace.Replay(4)
	if attempts != nil {
		t.Error("Replay(4) should return nil for out of range")
	}
}

func TestTraceLastAttempt(t *testing.T) {
	trace := &Trace{
		Result: &Result{
			Attempts: []Attempt{
				{Round: 1},
				{Round: 2},
				{Round: 3},
			},
		},
	}

	last := trace.LastAttempt()
	if last == nil {
		t.Fatal("LastAttempt() returned nil")
	}

	if last.Round != 3 {
		t.Errorf("LastAttempt().Round = %d, want 3", last.Round)
	}
}

func TestTraceStats(t *testing.T) {
	trace := &Trace{
		Result: &Result{
			Converged: true,
			Reason:    "stable",
			Attempts: []Attempt{
				{
					Round:    1,
					Duration: 2 * time.Second,
					Findings: []review.Finding{{}, {}},
					Validations: []Validation{
						{Confidence: 0.8},
						{Confidence: 0.9},
					},
				},
				{
					Round:    2,
					Duration: 3 * time.Second,
					Findings: []review.Finding{{}},
					Validations: []Validation{
						{Confidence: 0.95},
					},
				},
			},
			FinalFindings: []review.Finding{{}},
			TotalDuration: 5 * time.Second,
		},
	}

	stats := trace.Stats()

	if stats.TotalAttempts != 2 {
		t.Errorf("TotalAttempts = %d, want 2", stats.TotalAttempts)
	}

	if !stats.Converged {
		t.Error("Converged should be true")
	}

	if len(stats.FindingsPerRound) != 2 {
		t.Errorf("FindingsPerRound length = %d, want 2", len(stats.FindingsPerRound))
	}

	if stats.FindingsPerRound[0] != 2 {
		t.Errorf("FindingsPerRound[0] = %d, want 2", stats.FindingsPerRound[0])
	}

	if stats.FinalFindings != 1 {
		t.Errorf("FinalFindings = %d, want 1", stats.FinalFindings)
	}
}

func TestAnalyzeTrace(t *testing.T) {
	tmpDir := t.TempDir()
	tracePath := filepath.Join(tmpDir, "analysis_test.json")

	// 创建包含工具调用的 trace
	trace := &Trace{
		ID:   "analysis-test",
		Diff: "test diff",
		Repo: "/tmp/repo",
		Result: &Result{
			Converged: true,
			Reason:    "done",
			Attempts: []Attempt{
				{
					Round:     1,
					StartedAt: time.Now(),
					Duration:  3 * time.Second,
					Findings:  []review.Finding{{File: "a.go", Line: 1}},
					Validations: []Validation{
						{FindingID: 0, Confidence: 0.85},
					},
					ToolCalls: []ToolCall{
						{Tool: "locate_symbols"},
						{Tool: "static_rules"},
						{Tool: "review_point"},
					},
				},
			},
			FinalFindings: []review.Finding{{File: "a.go", Line: 1}},
			TotalDuration: 3 * time.Second,
		},
		CreatedAt: time.Now(),
	}

	if err := trace.Save(tracePath); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// 分析
	analysis, err := AnalyzeTrace(tracePath)
	if err != nil {
		t.Fatalf("AnalyzeTrace() error: %v", err)
	}

	if analysis.TraceID != "analysis-test" {
		t.Errorf("TraceID = %q, want %q", analysis.TraceID, "analysis-test")
	}

	if analysis.TotalRounds != 1 {
		t.Errorf("TotalRounds = %d, want 1", analysis.TotalRounds)
	}

	if !analysis.Convergence.Converged {
		t.Error("Converged should be true")
	}

	if analysis.ToolUsage.TotalCalls != 3 {
		t.Errorf("TotalCalls = %d, want 3", analysis.ToolUsage.TotalCalls)
	}

	if analysis.ToolUsage.ToolCallCounts["locate_symbols"] != 1 {
		t.Error("locate_symbols should be called once")
	}
}

func TestFormatAnalysis(t *testing.T) {
	analysis := &ReplayAnalysis{
		TraceID:     "format-test",
		TotalRounds: 2,
		Convergence: ConvergenceInfo{
			Converged:        true,
			Reason:           "stable",
			RoundsToConverge: 2,
		},
		Performance: PerformanceInfo{
			TotalDuration: 5 * time.Second,
			AvgRoundTime:  2500 * time.Millisecond,
			FastestRound:  2 * time.Second,
			SlowestRound:  3 * time.Second,
		},
		Quality: QualityInfo{
			FindingsPerRound:      []int{3, 2},
			AvgConfidencePerRound: []float64{0.8, 0.9},
			FinalFindingsCount:    2,
			HighConfidenceCount:   4,
			LowConfidenceCount:    1,
		},
		ToolUsage: ToolUsageInfo{
			TotalCalls: 6,
			ToolCallCounts: map[string]int{
				"locate_symbols": 2,
				"static_rules":   2,
				"review_point":   2,
			},
			AvgCallsPerRound: 3.0,
		},
	}

	formatted := FormatAnalysis(analysis)

	if formatted == "" {
		t.Error("FormatAnalysis() returned empty string")
	}

	// 验证包含关键信息
	if !containsAll(formatted, "format-test", "收敛", "性能信息", "质量信息", "工具使用") {
		t.Error("Formatted output missing expected sections")
	}
}

func containsAll(s string, substrs ...string) bool {
	for _, substr := range substrs {
		if !contains(s, substr) {
			return false
		}
	}
	return true
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && anyContains(s, substr))
}

func anyContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
