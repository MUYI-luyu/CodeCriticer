package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ReviewSnapshot pins every review input to one committed Git revision.
// Root is the only repository root used by code-reading and analysis tools.
type ReviewSnapshot struct {
	Root     string `json:"root"`
	BaseSHA  string `json:"base_sha"`
	HeadSHA  string `json:"head_sha"`
	DiffHash string `json:"diff_hash"`

	sourceRoot     string
	executionRoot  string
	tempRoot       string
	analysisAdded  bool
	executionAdded bool
}

// CreateGitReviewSnapshot resolves base/head commits, generates their diff and
// checks out head into a detached worktree. When execution is enabled, tests run
// in a second disposable worktree so target code cannot mutate the analysis root.
func CreateGitReviewSnapshot(ctx context.Context, repo, base, head string, allowExecution bool) (*ReviewSnapshot, []byte, error) {
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(base) == "" || strings.TrimSpace(head) == "" {
		return nil, nil, fmt.Errorf("repository, base SHA and head SHA are required")
	}
	rootOutput, err := gitOutput(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, nil, fmt.Errorf("resolve repository root: %w", err)
	}
	sourceRoot, err := filepath.EvalSymlinks(strings.TrimSpace(string(rootOutput)))
	if err != nil {
		return nil, nil, fmt.Errorf("resolve repository path: %w", err)
	}
	baseSHA, err := resolveCommit(ctx, sourceRoot, base)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve base %q: %w", base, err)
	}
	headSHA, err := resolveCommit(ctx, sourceRoot, head)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve head %q: %w", head, err)
	}
	diffData, err := gitOutput(ctx, sourceRoot, "-c", "core.quotepath=false", "diff", "--binary", "--no-ext-diff", baseSHA, headSHA, "--")
	if err != nil {
		return nil, nil, fmt.Errorf("generate review diff: %w", err)
	}
	if len(diffData) == 0 {
		return nil, nil, fmt.Errorf("base %s and head %s have no diff", baseSHA, headSHA)
	}
	tempRoot, err := os.MkdirTemp("", "codecritic-snapshot-")
	if err != nil {
		return nil, nil, fmt.Errorf("create snapshot directory: %w", err)
	}
	snapshot := &ReviewSnapshot{
		BaseSHA: baseSHA, HeadSHA: headSHA, DiffHash: hashBytes(diffData),
		sourceRoot: sourceRoot, tempRoot: tempRoot,
	}
	cleanupOnError := func(cause error) (*ReviewSnapshot, []byte, error) {
		if cleanupErr := snapshot.Close(); cleanupErr != nil {
			cause = fmt.Errorf("%w; cleanup failed: %v", cause, cleanupErr)
		}
		return nil, nil, cause
	}
	snapshot.Root = filepath.Join(tempRoot, "analysis")
	if _, err := gitOutput(ctx, sourceRoot, "worktree", "add", "--detach", snapshot.Root, headSHA); err != nil {
		return cleanupOnError(fmt.Errorf("create analysis worktree: %w", err))
	}
	snapshot.analysisAdded = true
	if allowExecution {
		snapshot.executionRoot = filepath.Join(tempRoot, "execution")
		if _, err := gitOutput(ctx, sourceRoot, "worktree", "add", "--detach", snapshot.executionRoot, headSHA); err != nil {
			return cleanupOnError(fmt.Errorf("create execution worktree: %w", err))
		}
		snapshot.executionAdded = true
	}
	return snapshot, diffData, nil
}

func resolveCommit(ctx context.Context, repo, ref string) (string, error) {
	out, err := gitOutput(ctx, repo, "rev-parse", "--verify", strings.TrimSpace(ref)+"^{commit}")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("empty commit id")
	}
	return sha, nil
}

func gitOutput(ctx context.Context, repo string, args ...string) ([]byte, error) {
	cmdArgs := append([]string{"-C", repo}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// executionRepo returns the isolated repository used by test/race validation.
func (s *ReviewSnapshot) executionRepo() string {
	if s == nil {
		return ""
	}
	return s.executionRoot
}

func (s *ReviewSnapshot) traceCopy() *ReviewSnapshot {
	if s == nil {
		return nil
	}
	return &ReviewSnapshot{Root: s.Root, BaseSHA: s.BaseSHA, HeadSHA: s.HeadSHA, DiffHash: s.DiffHash}
}

// Close removes both detached worktrees and their temporary parent.
func (s *ReviewSnapshot) Close() error {
	if s == nil {
		return nil
	}
	var errs []string
	targets := []struct {
		root  string
		added *bool
	}{
		{root: s.executionRoot, added: &s.executionAdded},
		{root: s.Root, added: &s.analysisAdded},
	}
	for _, target := range targets {
		if target.root == "" || !*target.added {
			continue
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, err := gitOutput(cleanupCtx, s.sourceRoot, "worktree", "remove", "--force", target.root)
		cancel()
		if err != nil {
			errs = append(errs, err.Error())
		} else {
			*target.added = false
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("remove review snapshot: %s", strings.Join(errs, "; "))
	}
	if s.tempRoot != "" {
		if err := os.RemoveAll(s.tempRoot); err != nil {
			return fmt.Errorf("remove review snapshot directory: %w", err)
		}
	}
	s.Root = ""
	s.executionRoot = ""
	s.tempRoot = ""
	return nil
}
