package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	changediff "github.com/MUYI-luyu/codecritic/internal/diff"
	"github.com/MUYI-luyu/codecritic/internal/graph"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

func (t *ToolRuntime) readDiff(args readDiffArgs) ([]*Evidence, error) {
	file, err := repoRelativePath(t.repo, args.File)
	if err != nil {
		return nil, err
	}
	for _, change := range t.changes {
		if changeReviewFile(change) != file {
			continue
		}
		for _, hunk := range change.Hunks {
			if hunk.ID != args.HunkID {
				continue
			}
			line := hunk.NewStart
			if line <= 0 {
				line = hunk.OldStart
			}
			if line <= 0 {
				line = 1
			}
			end := line + hunk.NewLines - 1
			if hunk.NewLines <= 0 {
				end = line
			}
			return []*Evidence{{Source: "read_diff", Type: "diff_hunk", File: file, Line: line, EndLine: end, Content: formatHunk(hunk)}}, nil
		}
	}
	return nil, fmt.Errorf("diff hunk not found: %s %s", file, args.HunkID)
}

func (t *ToolRuntime) readCode(args readCodeArgs) ([]*Evidence, error) {
	file, err := repoRelativePath(t.repo, args.File)
	if err != nil {
		return nil, err
	}
	path, err := secureRepoFile(t.repo, file)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	totalLines := bytes.Count(b, []byte("\n")) + 1
	start, end := 1, min(maxReadLines, totalLines)
	symbol := ""
	switch selector := args.Selector.(type) {
	case fileStartSelector:
	case rangeSelector:
		start = selector.Start
		end = start + selector.MaxLines - 1
	case lineSelector:
		start, end = selector.Line-selector.ContextLines, selector.Line+selector.ContextLines
		if start < 1 {
			start = 1
		}
	case symbolSelector:
		symbol = selector.Symbol
		decl, err := changediff.Find(b, symbol)
		if err != nil {
			return nil, err
		}
		start, end = decl.Line, decl.EndLine
		if end-start >= maxReadLines {
			end = start + maxReadLines - 1
		}
	default:
		return nil, fmt.Errorf("unsupported read_code selector %T", args.Selector)
	}
	lines := bytes.Split(b, []byte("\n"))
	if start > len(lines) {
		return nil, fmt.Errorf("read_code 起始行超出文件范围")
	}
	if end > len(lines) {
		end = len(lines)
	}
	content := numberedContent(start, lines[start-1:end])
	return []*Evidence{{Source: "read_code", Type: "code", File: file, Line: start, EndLine: end, Content: content, Symbol: symbol}}, nil
}

func (t *ToolRuntime) searchCode(args searchCodeArgs) ([]*Evidence, error) {
	word := args.Keyword
	if t.store == nil {
		return nil, fmt.Errorf("search_code unavailable: recall store is nil")
	}
	docs, err := t.store.Search(word)
	if err != nil {
		return nil, err
	}
	limitedFile := ""
	if args.File != nil {
		file, err := repoRelativePath(t.repo, *args.File)
		if err != nil {
			return nil, err
		}
		filtered := docs[:0]
		for _, d := range docs {
			rel, err := repoRelativePath(t.repo, d.File)
			if err != nil {
				continue
			}
			if rel == file {
				filtered = append(filtered, d)
			}
		}
		docs = filtered
		limitedFile = file
	}
	if len(docs) == 0 && limitedFile != "" {
		return []*Evidence{{Source: "search_code", Type: "search_absence", File: limitedFile, Line: 1, Content: fmt.Sprintf("文件 %s 中未找到 %q", limitedFile, word)}}, nil
	}
	out := make([]*Evidence, 0, len(docs))
	for _, d := range docs {
		if len(out) >= 20 {
			break
		}
		file, err := repoRelativePath(t.repo, d.File)
		if err != nil {
			continue
		}
		start := d.Line - 3
		if start < 1 {
			start = 1
		}
		contextLines := bytes.Split([]byte(d.Text), []byte("\n"))
		out = append(out, &Evidence{Source: "search_code", Type: "search_result", File: file, Line: start, EndLine: start + len(contextLines) - 1, Content: numberedContent(start, contextLines)})
	}
	return out, nil
}

func numberedContent(start int, lines [][]byte) string {
	var out strings.Builder
	for i, line := range lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "%d | %s", start+i, line)
	}
	return out.String()
}

func (t *ToolRuntime) inspectSymbol(args inspectSymbolArgs) ([]*Evidence, error) {
	name, file, relation := args.Symbol, args.File, args.Relation
	line := 0
	if args.Line != nil {
		line = *args.Line
	}
	index, err := t.analysisIndex()
	if err != nil {
		return nil, fmt.Errorf("inspect_symbol unavailable: %w", err)
	}
	file, err = repoRelativePath(t.repo, file)
	if err != nil {
		return nil, err
	}
	facts, err := index.InspectSymbol(graph.SymbolRef{Name: name, File: file, Line: line}, relation)
	if err != nil {
		return nil, err
	}
	out := make([]*Evidence, 0, len(facts))
	for _, fact := range facts {
		path, err := repoRelativePath(t.repo, fact.File)
		if err != nil {
			continue
		}
		content := fmt.Sprintf("precision=%s; %s", fact.Precision, fact.Detail)
		out = append(out, &Evidence{Source: "inspect_symbol", Type: fact.Kind, File: path, Line: fact.Line, Content: content, Symbol: fact.Symbol})
	}
	return out, nil
}

func (t *ToolRuntime) inspectConcurrency(args inspectConcurrencyArgs) ([]*Evidence, error) {
	index, err := t.analysisIndex()
	if err != nil {
		return nil, fmt.Errorf("inspect_concurrency unavailable: %w", err)
	}
	file, err := repoRelativePath(t.repo, args.File)
	if err != nil {
		return nil, err
	}
	line := 0
	if args.Line != nil {
		line = *args.Line
	}
	facts, err := index.InspectConcurrency(graph.SymbolRef{Name: args.Symbol, File: file, Line: line})
	if err != nil {
		return nil, err
	}
	out := make([]*Evidence, 0, len(facts))
	for _, fact := range facts {
		path, err := repoRelativePath(t.repo, fact.File)
		if err != nil {
			continue
		}
		out = append(out, &Evidence{
			Source: "inspect_concurrency", Type: fact.Kind, File: path, Line: fact.Line,
			Content: fmt.Sprintf("precision=%s; %s", fact.Precision, fact.Detail), Symbol: fact.Symbol,
		})
	}
	return out, nil
}

func (t *ToolRuntime) staticRules() ([]*Evidence, error) {
	diagnostics, err := review.Rules(t.repo)
	if err != nil {
		return nil, err
	}
	out := make([]*Evidence, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		out = append(out, &Evidence{Source: "run_static_rules", Type: "static_diagnostic", File: diagnostic.File, Line: diagnostic.Line, Content: diagnostic.Message, Symbol: diagnostic.Analyzer})
	}
	return out, nil
}

var goLocationPattern = regexp.MustCompile(`(?m)([A-Za-z0-9_./-]+\.go):(\d+)`)

func (t *ToolRuntime) runGoValidation(ctx context.Context, args goValidationArgs) ([]*Evidence, error) {
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(args.TimeoutSeconds)*time.Second)
	defer cancel()
	var result commandResult
	var err error
	if args.Mode == "compile" {
		result, err = t.compilePackages(runCtx, args.Package)
	} else {
		commandArgs := []string{"test", "-buildvcs=false", "-count=1"}
		if args.Mode == "race" {
			commandArgs = append(commandArgs, "-race")
		}
		if args.Test != nil {
			commandArgs = append(commandArgs, "-run", *args.Test)
		}
		commandArgs = append(commandArgs, args.Package)
		runRoot := t.repo
		if t.validationRepo != "" {
			runRoot = t.validationRepo
		}
		result, err = runGoCommand(runCtx, runRoot, commandArgs)
	}
	if err != nil {
		if runCtx.Err() != nil {
			return nil, fmt.Errorf("go validation timed out after %ds", args.TimeoutSeconds)
		}
		return nil, err
	}
	file, line := validationLocation(t.changes, result.Output)
	return []*Evidence{{Source: "run_go_validation", Type: "validation_" + result.Status, File: file, Line: line, Content: result.Content}}, nil
}

type commandResult struct {
	Status   string
	ExitCode int
	Output   string
	Content  string
}

type listedPackage struct {
	ImportPath string
	Dir        string
}

func (t *ToolRuntime) compilePackages(ctx context.Context, pattern string) (commandResult, error) {
	packages, listResult, err := listGoPackages(ctx, t.repo, pattern)
	if err != nil || listResult.Status == "failed" {
		return listResult, err
	}
	if len(packages) == 0 {
		return commandResult{}, fmt.Errorf("go list returned no packages for %q", pattern)
	}
	buildDir, err := os.MkdirTemp("", "codecritic-build-")
	if err != nil {
		return commandResult{}, fmt.Errorf("create build output directory: %w", err)
	}
	defer os.RemoveAll(buildDir)
	var transcript strings.Builder
	fmt.Fprintf(&transcript, "%s\nresolved_packages=%d\n", listResult.Content, len(packages))
	for i, pkg := range packages {
		if err := ensureDirectoryWithinRepo(t.repo, pkg.Dir); err != nil {
			return commandResult{}, fmt.Errorf("go list package %q: %w", pkg.ImportPath, err)
		}
		outputPath := filepath.Join(buildDir, fmt.Sprintf("package-%d", i))
		result, err := runGoCommand(ctx, pkg.Dir, []string{"build", "-buildvcs=false", "-o", outputPath, "."})
		if err != nil {
			return commandResult{}, err
		}
		fmt.Fprintf(&transcript, "package=%s dir=%s\n%s\n", pkg.ImportPath, pkg.Dir, result.Content)
		if result.Status == "failed" {
			result.Content = transcript.String()
			return result, nil
		}
	}
	content := transcript.String() + "status=passed exit_code=0\n"
	return commandResult{Status: "passed", Output: content, Content: content}, nil
}

func listGoPackages(ctx context.Context, repo, pattern string) ([]listedPackage, commandResult, error) {
	commandArgs := []string{"list", "-json=ImportPath,Dir", pattern}
	cmd := exec.CommandContext(ctx, "go", commandArgs...)
	cmd.Dir = repo
	var stdout bytes.Buffer
	var stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, commandResult{}, ctx.Err()
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return nil, commandResult{}, fmt.Errorf("start go list: %w", err)
		}
		content := fmt.Sprintf("go %s\nstatus=failed exit_code=%d\n%s", strings.Join(commandArgs, " "), exitErr.ExitCode(), stderr.String())
		return nil, commandResult{Status: "failed", ExitCode: exitErr.ExitCode(), Output: stderr.String(), Content: content}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	packages := make([]listedPackage, 0)
	for {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			return nil, commandResult{}, fmt.Errorf("decode go list output: %w", err)
		}
		if pkg.ImportPath == "" || pkg.Dir == "" {
			return nil, commandResult{}, fmt.Errorf("go list returned package without ImportPath or Dir")
		}
		packages = append(packages, pkg)
	}
	content := fmt.Sprintf("go %s\nstatus=passed exit_code=0\n%s", strings.Join(commandArgs, " "), stderr.String())
	return packages, commandResult{Status: "passed", Output: stderr.String(), Content: content}, nil
}

func runGoCommand(ctx context.Context, dir string, args []string) (commandResult, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	var output limitedBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if ctx.Err() != nil {
		return commandResult{}, ctx.Err()
	}
	status, exitCode := "passed", 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return commandResult{}, fmt.Errorf("start go %s: %w", args[0], err)
		}
		status, exitCode = "failed", exitErr.ExitCode()
	}
	content := fmt.Sprintf("go %s\nstatus=%s exit_code=%d\n%s", strings.Join(args, " "), status, exitCode, output.String())
	return commandResult{Status: status, ExitCode: exitCode, Output: output.String(), Content: content}, nil
}

func ensureDirectoryWithinRepo(repo, dir string) error {
	realRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return err
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(realRoot, realDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("package directory escapes repository: %s", dir)
	}
	return nil
}

const goPackageSchemaPattern = `^(\.|\./.*)$`

func validatePackagePattern(pkg string) error {
	if pkg == "." || pkg == "./" {
		return nil
	}
	if !strings.HasPrefix(pkg, "./") || strings.Contains(pkg, `\`) {
		return fmt.Errorf("package must be . or a repository-relative ./ pattern")
	}
	rest := strings.TrimPrefix(pkg, "./")
	if rest == "" {
		return nil
	}
	for _, r := range rest {
		if unicode.IsControl(r) {
			return fmt.Errorf("package pattern contains a control character")
		}
	}
	for _, segment := range strings.Split(rest, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("package pattern escapes or ambiguously addresses the repository: %q", pkg)
		}
	}
	return nil
}

func validationLocation(changes []changediff.Change, output string) (string, int) {
	if match := goLocationPattern.FindStringSubmatch(output); len(match) == 3 {
		line := 1
		fmt.Sscanf(match[2], "%d", &line)
		return filepath.ToSlash(filepath.Clean(match[1])), line
	}
	for _, change := range changes {
		if file := changeReviewFile(change); file != "" && file != "/dev/null" {
			return file, 1
		}
	}
	return "go.mod", 1
}

type limitedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func changeReviewFile(change changediff.Change) string {
	if change.File != "" && change.File != "/dev/null" {
		return filepath.ToSlash(change.File)
	}
	return filepath.ToSlash(change.Old)
}

func formatHunk(hunk changediff.Hunk) string {
	var out strings.Builder
	fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@ %s\n", hunk.OldStart, hunk.OldLines, hunk.NewStart, hunk.NewLines, hunk.Section)
	for _, line := range hunk.Lines {
		prefix := " "
		switch line.Kind {
		case "add":
			prefix = "+"
		case "delete":
			prefix = "-"
		case "metadata":
			prefix = "\\"
		}
		symbol := line.Symbol
		if symbol == "" {
			symbol = "?"
		}
		fmt.Fprintf(&out, "%s old=%d new=%d symbol=%s | %s\n", prefix, line.OldLine, line.NewLine, symbol, line.Text)
	}
	return out.String()
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	const limit = 32 << 10
	original := len(p)
	if b.buf.Len() < limit {
		remaining := limit - b.buf.Len()
		if len(p) > remaining {
			p = p[:remaining]
			b.truncated = true
		}
		_, _ = b.buf.Write(p)
	} else {
		b.truncated = true
	}
	return original, nil
}

func (b *limitedBuffer) String() string {
	if b.truncated {
		return b.buf.String() + "\n[output truncated at 32768 bytes]"
	}
	return b.buf.String()
}

func encodeEvidence(evs []*Evidence) string { b, _ := json.Marshal(evs); return string(b) }
func executeStep(name string, args any, fn func() ([]*Evidence, error)) (InvestigationStep, []*Evidence) {
	started := time.Now()
	ev, err := fn()
	step := InvestigationStep{Tool: name, Args: args, Duration: time.Since(started)}
	if err != nil {
		step.Error = err.Error()
	}
	return step, ev
}
