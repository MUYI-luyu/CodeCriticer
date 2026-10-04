package workflow

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

func TestGitReviewSnapshotPinsDiffAndToolRoot(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "snapshot@example.com")
	gitTest(t, repo, "config", "user.name", "Snapshot Test")
	writeSnapshotFile(t, repo, "go.mod", "module example.com/snapshot\n\ngo 1.22\n")
	writeSnapshotFile(t, repo, "main.go", "package snapshot\n\nfunc Value() int { return 1 }\n")
	writeSnapshotFile(t, repo, "main_test.go", "package snapshot\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != 1 { t.Fatal(Value()) } }\n")
	gitTest(t, repo, "add", "go.mod", "main.go", "main_test.go")
	gitTest(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))

	writeSnapshotFile(t, repo, "main.go", "package snapshot\n\nfunc Value() int { return 2 }\n")
	writeSnapshotFile(t, repo, "main_test.go", "package snapshot\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != 2 { t.Fatal(Value()) } }\n")
	gitTest(t, repo, "add", "main.go", "main_test.go")
	gitTest(t, repo, "commit", "-m", "head")
	head := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))

	snapshot, diffData, err := CreateGitReviewSnapshot(context.Background(), repo, base, head, true)
	if err != nil {
		t.Fatal(err)
	}
	analysisRoot, executionRoot := snapshot.Root, snapshot.executionRepo()
	t.Cleanup(func() { _ = snapshot.Close() })

	if snapshot.BaseSHA != base || snapshot.HeadSHA != head || snapshot.DiffHash != hashBytes(diffData) {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if executionRoot == "" || executionRoot == analysisRoot {
		t.Fatalf("analysis=%q execution=%q", analysisRoot, executionRoot)
	}
	if !strings.Contains(string(diffData), "-func Value() int { return 1 }") || !strings.Contains(string(diffData), "+func Value() int { return 2 }") {
		t.Fatalf("diff=%s", diffData)
	}

	// A later source-worktree mutation must not affect either committed snapshot.
	writeSnapshotFile(t, repo, "main.go", "package snapshot\n\nfunc Value() int { return 99 }\n")
	originalTest := exec.Command("go", "test", "-buildvcs=false", "./...")
	originalTest.Dir = repo
	if output, err := originalTest.CombinedOutput(); err == nil {
		t.Fatalf("mutated source worktree unexpectedly passes tests: %s", output)
	}
	for _, root := range []string{analysisRoot, executionRoot} {
		data, err := os.ReadFile(filepath.Join(root, "main.go"))
		if err != nil || !strings.Contains(string(data), "return 2") {
			t.Fatalf("snapshot root %q changed: %v %s", root, err, data)
		}
	}

	llm := &scriptedLLM{calls: []review.ToolCall{
		toolCall("read_code", `{"file":"main.go","selector":{"kind":"file_start"}}`),
		toolCall("inspect_symbol", `{"symbol":"Value","file":"main.go","line":null,"relation":"definition"}`),
		toolCall("run_go_validation", `{"mode":"test","package":".","test":null,"timeout_seconds":30}`),
		toolCall("submit_claims", `{"claims":[]}`),
	}}
	wf, err := NewWithSnapshot(llm, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Root != "" || snapshot.BaseSHA != "" || snapshot.HeadSHA != "" || snapshot.DiffHash != "" {
		t.Fatalf("snapshot ownership was not transferred: %+v", snapshot)
	}
	// The caller no longer aliases the run-owned identity after the transfer.
	snapshot.Root = "tampered"
	snapshot.BaseSHA = "tampered"
	snapshot.HeadSHA = "tampered"
	snapshot.DiffHash = "tampered"
	result, err := wf.Run(context.Background(), Request{Diff: diffData, AllowExecution: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Trace.Snapshot == nil || result.Trace.Snapshot.HeadSHA != head || result.Trace.Snapshot.DiffHash != hashBytes(diffData) {
		t.Fatalf("trace snapshot=%+v", result.Trace.Snapshot)
	}
	foundPinnedCode, foundPinnedSymbol, validationPassed := false, false, false
	for _, evidence := range result.Trace.Evidence {
		if evidence.Source == "read_code" && strings.Contains(evidence.Content, "return 2") && !strings.Contains(evidence.Content, "return 99") {
			foundPinnedCode = true
		}
		if evidence.Source == "inspect_symbol" && evidence.Symbol == "Value" && evidence.File == "main.go" {
			foundPinnedSymbol = true
		}
		if evidence.Source == "run_go_validation" && evidence.Type == "validation_passed" {
			validationPassed = true
		}
	}
	if !foundPinnedCode || !foundPinnedSymbol || !validationPassed {
		t.Fatalf("evidence=%+v", result.Trace.Evidence)
	}
	if _, err := os.Stat(analysisRoot); !os.IsNotExist(err) {
		t.Fatalf("ReviewRun did not close its owned analysis snapshot: %v", err)
	}
	if _, err := os.Stat(executionRoot); !os.IsNotExist(err) {
		t.Fatalf("ReviewRun did not close its owned execution snapshot: %v", err)
	}

	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatalf("second close must be idempotent: %v", err)
	}
	if _, err := os.Stat(analysisRoot); !os.IsNotExist(err) {
		t.Fatalf("analysis worktree still exists: %v", err)
	}
	if _, err := os.Stat(executionRoot); !os.IsNotExist(err) {
		t.Fatalf("execution worktree still exists: %v", err)
	}
	registered := gitTest(t, repo, "worktree", "list", "--porcelain")
	if strings.Contains(registered, analysisRoot) || strings.Contains(registered, executionRoot) {
		t.Fatalf("snapshot worktree registration leaked:\n%s", registered)
	}
}

func TestGitReviewSnapshotRejectsUnknownCommit(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	if _, _, err := CreateGitReviewSnapshot(context.Background(), repo, "missing", "HEAD", false); err == nil {
		t.Fatal("expected an invalid commit error")
	}
}

func TestReviewRunCloseBeforeStartReleasesOwnedSnapshot(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "snapshot@example.com")
	gitTest(t, repo, "config", "user.name", "Snapshot Test")
	writeSnapshotFile(t, repo, "main.go", "package snapshot\n\nvar Value = 1\n")
	gitTest(t, repo, "add", "main.go")
	gitTest(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))
	writeSnapshotFile(t, repo, "main.go", "package snapshot\n\nvar Value = 2\n")
	gitTest(t, repo, "add", "main.go")
	gitTest(t, repo, "commit", "-m", "head")
	head := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))

	snapshot, diffData, err := CreateGitReviewSnapshot(context.Background(), repo, base, head, false)
	if err != nil {
		t.Fatal(err)
	}
	analysisRoot := snapshot.Root
	run, err := NewWithSnapshot(&scriptedLLM{}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := os.Stat(analysisRoot); !os.IsNotExist(err) {
		t.Fatalf("abandoned run left analysis snapshot behind: %v", err)
	}
	if result, err := run.Run(context.Background(), Request{Diff: diffData}); !errors.Is(err, ErrReviewRunClosed) || result != nil {
		t.Fatalf("closed run result=%+v err=%v", result, err)
	}
}

func TestGitReviewSnapshotCloseCanRetryAfterGitFailure(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "snapshot@example.com")
	gitTest(t, repo, "config", "user.name", "Snapshot Test")
	writeSnapshotFile(t, repo, "main.go", "package snapshot\n\nvar Value = 1\n")
	gitTest(t, repo, "add", "main.go")
	gitTest(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))
	writeSnapshotFile(t, repo, "main.go", "package snapshot\n\nvar Value = 2\n")
	gitTest(t, repo, "add", "main.go")
	gitTest(t, repo, "commit", "-m", "head")
	head := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))

	snapshot, _, err := CreateGitReviewSnapshot(context.Background(), repo, base, head, false)
	if err != nil {
		t.Fatal(err)
	}
	analysisRoot, tempRoot := snapshot.Root, snapshot.tempRoot
	gitDir, hiddenGitDir := filepath.Join(repo, ".git"), filepath.Join(repo, ".git-hidden")
	hidden := false
	t.Cleanup(func() {
		if hidden {
			_ = os.Rename(hiddenGitDir, gitDir)
		}
		_ = snapshot.Close()
	})
	if err := os.Rename(gitDir, hiddenGitDir); err != nil {
		t.Fatal(err)
	}
	hidden = true
	if err := snapshot.Close(); err == nil {
		t.Fatal("expected cleanup failure while source Git metadata is unavailable")
	}
	if _, err := os.Stat(analysisRoot); err != nil {
		t.Fatalf("failed cleanup must preserve worktree for retry: %v", err)
	}
	if _, err := os.Stat(tempRoot); err != nil {
		t.Fatalf("failed cleanup must preserve temporary root for retry: %v", err)
	}
	if err := os.Rename(hiddenGitDir, gitDir); err != nil {
		t.Fatal(err)
	}
	hidden = false
	if err := snapshot.Close(); err != nil {
		t.Fatalf("retry cleanup failed: %v", err)
	}
	if _, err := os.Stat(analysisRoot); !os.IsNotExist(err) {
		t.Fatalf("analysis worktree remains after retry: %v", err)
	}
}

func TestSnapshotWorkflowRejectsForgedMetadata(t *testing.T) {
	_, err := NewWithSnapshot(&scriptedLLM{}, &ReviewSnapshot{
		Root: t.TempDir(), BaseSHA: "base", HeadSHA: "head", DiffHash: hashBytes([]byte("diff")),
	})
	if err == nil {
		t.Fatal("expected snapshot not created by CreateGitReviewSnapshot to be rejected")
	}
}

func TestSnapshotWorkflowRejectsDifferentDiff(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "snapshot@example.com")
	gitTest(t, repo, "config", "user.name", "Snapshot Test")
	writeSnapshotFile(t, repo, "go.mod", "module example.com/snapshot\n\ngo 1.22\n")
	writeSnapshotFile(t, repo, "main.go", "package snapshot\n")
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))
	writeSnapshotFile(t, repo, "main.go", "package snapshot\n\nvar Changed = true\n")
	gitTest(t, repo, "add", "main.go")
	gitTest(t, repo, "commit", "-m", "head")
	head := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))
	snapshot, _, err := CreateGitReviewSnapshot(context.Background(), repo, base, head, false)
	if err != nil {
		t.Fatal(err)
	}
	analysisRoot := snapshot.Root
	defer snapshot.Close()
	wf, err := NewWithSnapshot(&scriptedLLM{}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wf.Run(context.Background(), Request{Diff: []byte("different")})
	if err == nil || result.Trace.StopReason != StopStageError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, statErr := os.Stat(analysisRoot); !os.IsNotExist(statErr) {
		t.Fatalf("early-return cleanup left analysis snapshot behind: %v", statErr)
	}
}

func TestSnapshotWorkflowRejectsExecutionWithoutExecutionWorktree(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "snapshot@example.com")
	gitTest(t, repo, "config", "user.name", "Snapshot Test")
	writeSnapshotFile(t, repo, "main.go", "package snapshot\n\nvar Value = 1\n")
	gitTest(t, repo, "add", "main.go")
	gitTest(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))
	writeSnapshotFile(t, repo, "main.go", "package snapshot\n\nvar Value = 2\n")
	gitTest(t, repo, "add", "main.go")
	gitTest(t, repo, "commit", "-m", "head")
	head := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))

	snapshot, diffData, err := CreateGitReviewSnapshot(context.Background(), repo, base, head, false)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	wf, err := NewWithSnapshot(&scriptedLLM{}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wf.Run(context.Background(), Request{Diff: diffData, AllowExecution: true})
	if err == nil || result.Trace.StopReason != StopStageError || !strings.Contains(err.Error(), "without an execution worktree") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGitReviewSnapshotDiffUsesExactBaseNotMergeBase(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "snapshot@example.com")
	gitTest(t, repo, "config", "user.name", "Snapshot Test")
	writeSnapshotFile(t, repo, "main.go", "package snapshot\n\nvar Value = 1\n")
	gitTest(t, repo, "add", "main.go")
	gitTest(t, repo, "commit", "-m", "common")
	common := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))

	writeSnapshotFile(t, repo, "main.go", "package snapshot\n\nvar Value = 2\n")
	gitTest(t, repo, "add", "main.go")
	gitTest(t, repo, "commit", "-m", "base branch")
	base := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))

	gitTest(t, repo, "checkout", "--detach", common)
	writeSnapshotFile(t, repo, "main.go", "package snapshot\n\nvar Value = 3\n")
	gitTest(t, repo, "add", "main.go")
	gitTest(t, repo, "commit", "-m", "head branch")
	head := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))

	snapshot, diffData, err := CreateGitReviewSnapshot(context.Background(), repo, base, head, false)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	text := string(diffData)
	if !strings.Contains(text, "-var Value = 2") || !strings.Contains(text, "+var Value = 3") || strings.Contains(text, "-var Value = 1") {
		t.Fatalf("diff did not use exact base/head commits:\n%s", text)
	}
}

func gitTest(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeSnapshotFile(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
