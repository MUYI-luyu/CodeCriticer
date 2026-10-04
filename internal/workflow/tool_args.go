package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MUYI-luyu/codecritic/internal/review"
)

const (
	maxReadLines        = 200
	maxReadContextLines = 50
	maxValidationSecs   = 120
)

type readDiffArgs struct {
	File   string `json:"file"`
	HunkID string `json:"hunk_id"`
}

type readCodeArgs struct {
	File     string           `json:"file"`
	Selector readCodeSelector `json:"selector"`
}

type readCodeSelector interface{ readCodeSelector() }

type fileStartSelector struct {
	Kind string `json:"kind"`
}

func (fileStartSelector) readCodeSelector() {}

type rangeSelector struct {
	Kind     string `json:"kind"`
	Start    int    `json:"start"`
	MaxLines int    `json:"max_lines"`
}

func (rangeSelector) readCodeSelector() {}

type lineSelector struct {
	Kind         string `json:"kind"`
	Line         int    `json:"line"`
	ContextLines int    `json:"context_lines"`
}

func (lineSelector) readCodeSelector() {}

type symbolSelector struct {
	Kind   string `json:"kind"`
	Symbol string `json:"symbol"`
}

func (symbolSelector) readCodeSelector() {}

type readCodeEnvelope struct {
	File     string          `json:"file"`
	Selector json.RawMessage `json:"selector"`
}

type selectorKind struct {
	Kind string `json:"kind"`
}

type searchCodeArgs struct {
	Keyword string  `json:"keyword"`
	File    *string `json:"file"`
}

type inspectSymbolArgs struct {
	Symbol   string `json:"symbol"`
	File     string `json:"file"`
	Line     *int   `json:"line"`
	Relation string `json:"relation"`
}

type inspectConcurrencyArgs struct {
	Symbol string `json:"symbol"`
	File   string `json:"file"`
	Line   *int   `json:"line"`
}

type goValidationArgs struct {
	Mode           string  `json:"mode"`
	Package        string  `json:"package"`
	Test           *string `json:"test"`
	TimeoutSeconds int     `json:"timeout_seconds"`
}

type emptyToolArgs struct{}

type candidateClaimInput struct {
	File        string   `json:"file"`
	Line        int      `json:"line"`
	Severity    string   `json:"severity"`
	Msg         string   `json:"msg"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type submitClaimsArgs struct {
	Claims []candidateClaimInput `json:"claims"`
}

func (a submitClaimsArgs) candidateClaims() []review.CandidateClaim {
	claims := make([]review.CandidateClaim, len(a.Claims))
	for i, claim := range a.Claims {
		claims[i] = review.CandidateClaim{
			File: claim.File, Line: claim.Line, Severity: claim.Severity,
			Msg: claim.Msg, EvidenceIDs: claim.EvidenceIDs,
		}
	}
	return claims
}

type preparedToolCall struct {
	Args      any
	Canonical string
	execute   func(context.Context) ([]*Evidence, error)
}

func prepareReadDiff(t *ToolRuntime, arguments string) (preparedToolCall, error) {
	args, err := decodeStrict[readDiffArgs](arguments)
	if err != nil {
		return preparedToolCall{}, err
	}
	if err := requireNonBlank("file", args.File); err != nil {
		return preparedToolCall{}, err
	}
	if err := requireNonBlank("hunk_id", args.HunkID); err != nil {
		return preparedToolCall{}, err
	}
	return prepareTyped(args, func(context.Context) ([]*Evidence, error) { return t.readDiff(args) })
}

func prepareReadCode(t *ToolRuntime, arguments string) (preparedToolCall, error) {
	args, err := decodeReadCodeArgs(arguments)
	if err != nil {
		return preparedToolCall{}, err
	}
	return prepareTyped(args, func(context.Context) ([]*Evidence, error) { return t.readCode(args) })
}

func prepareSearchCode(t *ToolRuntime, arguments string) (preparedToolCall, error) {
	args, err := decodeStrict[searchCodeArgs](arguments)
	if err != nil {
		return preparedToolCall{}, err
	}
	if err := requireNonBlank("keyword", args.Keyword); err != nil {
		return preparedToolCall{}, err
	}
	if args.File != nil {
		if err := requireNonBlank("file", *args.File); err != nil {
			return preparedToolCall{}, err
		}
	}
	return prepareTyped(args, func(context.Context) ([]*Evidence, error) { return t.searchCode(args) })
}

func prepareInspectSymbol(t *ToolRuntime, arguments string) (preparedToolCall, error) {
	args, err := decodeStrict[inspectSymbolArgs](arguments)
	if err != nil {
		return preparedToolCall{}, err
	}
	if err := validateInspectSymbolArgs(args); err != nil {
		return preparedToolCall{}, err
	}
	return prepareTyped(args, func(context.Context) ([]*Evidence, error) { return t.inspectSymbol(args) })
}

func prepareInspectConcurrency(t *ToolRuntime, arguments string) (preparedToolCall, error) {
	args, err := decodeStrict[inspectConcurrencyArgs](arguments)
	if err != nil {
		return preparedToolCall{}, err
	}
	if err := validateInspectConcurrencyArgs(args); err != nil {
		return preparedToolCall{}, err
	}
	return prepareTyped(args, func(context.Context) ([]*Evidence, error) { return t.inspectConcurrency(args) })
}

func prepareStaticRules(t *ToolRuntime, arguments string) (preparedToolCall, error) {
	args, err := decodeStrict[emptyToolArgs](arguments)
	if err != nil {
		return preparedToolCall{}, err
	}
	return prepareTyped(args, func(context.Context) ([]*Evidence, error) { return t.staticRules() })
}

func prepareGoValidation(t *ToolRuntime, arguments string) (preparedToolCall, error) {
	args, err := decodeStrict[goValidationArgs](arguments)
	if err != nil {
		return preparedToolCall{}, err
	}
	if err := t.validateGoValidationArgs(args); err != nil {
		return preparedToolCall{}, err
	}
	return prepareTyped(args, func(ctx context.Context) ([]*Evidence, error) { return t.runGoValidation(ctx, args) })
}

func validateInspectConcurrencyArgs(args inspectConcurrencyArgs) error {
	if err := requireNonBlank("symbol", args.Symbol); err != nil {
		return err
	}
	if err := requireNonBlank("file", args.File); err != nil {
		return err
	}
	if args.Line != nil && *args.Line < 1 {
		return fmt.Errorf("line must be >= 1 when provided")
	}
	return nil
}

func prepareTyped[T any](args T, execute func(context.Context) ([]*Evidence, error)) (preparedToolCall, error) {
	canonical, err := json.Marshal(args)
	if err != nil {
		return preparedToolCall{}, fmt.Errorf("canonicalize tool arguments: %w", err)
	}
	return preparedToolCall{Args: args, Canonical: string(canonical), execute: execute}, nil
}

func decodeStrict[T any](arguments string) (T, error) {
	var args T
	if err := decodeToolArgs(arguments, &args); err != nil {
		return args, err
	}
	return args, nil
}

func decodeReadCodeArgs(arguments string) (readCodeArgs, error) {
	envelope, err := decodeStrict[readCodeEnvelope](arguments)
	if err != nil {
		return readCodeArgs{}, err
	}
	if err := requireNonBlank("file", envelope.File); err != nil {
		return readCodeArgs{}, err
	}
	var kind selectorKind
	if err := json.Unmarshal(envelope.Selector, &kind); err != nil {
		return readCodeArgs{}, fmt.Errorf("selector: %w", err)
	}
	args := readCodeArgs{File: envelope.File}
	switch kind.Kind {
	case "file_start":
		selector, err := decodeStrict[fileStartSelector](string(envelope.Selector))
		if err != nil {
			return readCodeArgs{}, fmt.Errorf("selector: %w", err)
		}
		args.Selector = selector
	case "range":
		selector, err := decodeStrict[rangeSelector](string(envelope.Selector))
		if err != nil {
			return readCodeArgs{}, fmt.Errorf("selector: %w", err)
		}
		if selector.Start < 1 || selector.MaxLines < 1 || selector.MaxLines > maxReadLines {
			return readCodeArgs{}, fmt.Errorf("range selector requires start >= 1 and max_lines between 1 and %d", maxReadLines)
		}
		args.Selector = selector
	case "line":
		selector, err := decodeStrict[lineSelector](string(envelope.Selector))
		if err != nil {
			return readCodeArgs{}, fmt.Errorf("selector: %w", err)
		}
		if selector.Line < 1 || selector.ContextLines < 0 || selector.ContextLines > maxReadContextLines {
			return readCodeArgs{}, fmt.Errorf("line selector requires line >= 1 and context_lines between 0 and %d", maxReadContextLines)
		}
		args.Selector = selector
	case "symbol":
		selector, err := decodeStrict[symbolSelector](string(envelope.Selector))
		if err != nil {
			return readCodeArgs{}, fmt.Errorf("selector: %w", err)
		}
		if err := requireNonBlank("symbol", selector.Symbol); err != nil {
			return readCodeArgs{}, err
		}
		args.Selector = selector
	default:
		return readCodeArgs{}, fmt.Errorf("unknown read_code selector kind %q", kind.Kind)
	}
	return args, nil
}

func validateInspectSymbolArgs(args inspectSymbolArgs) error {
	if err := requireNonBlank("symbol", args.Symbol); err != nil {
		return err
	}
	if err := requireNonBlank("file", args.File); err != nil {
		return err
	}
	if args.Line != nil && *args.Line < 1 {
		return fmt.Errorf("line must be >= 1 when provided")
	}
	switch args.Relation {
	case "definition", "references", "callers", "callees":
		return nil
	default:
		return fmt.Errorf("unknown inspect_symbol relation %q", args.Relation)
	}
}

func (t *ToolRuntime) validateGoValidationArgs(args goValidationArgs) error {
	if args.TimeoutSeconds < 1 || args.TimeoutSeconds > maxValidationSecs {
		return fmt.Errorf("timeout_seconds must be between 1 and %d", maxValidationSecs)
	}
	if err := validatePackagePattern(args.Package); err != nil {
		return err
	}
	if args.Test != nil {
		if err := requireNonBlank("test", *args.Test); err != nil {
			return err
		}
	}
	switch args.Mode {
	case "compile":
		return nil
	case "test", "race":
		if !t.allowExecution {
			return fmt.Errorf("%s execution is disabled; rerun CodeCritic with --allow-exec", args.Mode)
		}
		return nil
	default:
		return fmt.Errorf("unknown validation mode %q", args.Mode)
	}
}

func requireNonBlank(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be blank", name)
	}
	return nil
}
