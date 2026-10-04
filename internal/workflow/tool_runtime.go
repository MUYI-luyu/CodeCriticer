package workflow

import (
	"context"
	"fmt"
	"sync"

	changediff "github.com/MUYI-luyu/codecritic/internal/diff"
	"github.com/MUYI-luyu/codecritic/internal/graph"
	"github.com/MUYI-luyu/codecritic/internal/recall"
	"github.com/MUYI-luyu/codecritic/internal/review"
)

type toolSurface uint8

const (
	toolSurfaceAgent toolSurface = 1 << iota
	toolSurfaceMCP
)

// ToolRuntime owns the immutable roots, permissions and analysis resources
// shared by every code-analysis tool in one caller surface. Individual tools
// retain their typed arguments and specialized execution semantics.
type ToolRuntime struct {
	repo           string
	validationRepo string
	changes        []changediff.Change
	allowExecution bool
	surface        toolSurface

	store *recall.Store

	graphOnce sync.Once
	index     *graph.Index
	graphErr  error

	registryOnce sync.Once
	ordered      []runtimeToolSpec
	registry     map[string]runtimeToolSpec
}

func newToolRuntime(repo, validationRepo string, changes []changediff.Change, allowExecution bool, surface toolSurface) *ToolRuntime {
	return &ToolRuntime{
		repo:           repo,
		validationRepo: validationRepo,
		changes:        append([]changediff.Change(nil), changes...),
		allowExecution: allowExecution,
		surface:        surface,
		store:          recall.New(repo, nil),
	}
}

func (t *ToolRuntime) initRegistry() {
	t.registryOnce.Do(func() {
		t.registry = make(map[string]runtimeToolSpec)
		for _, spec := range runtimeToolSpecs(t.allowExecution) {
			if spec.surfaces&t.surface == 0 {
				continue
			}
			name := spec.definition.Function.Name
			if name == "" || spec.prepare == nil {
				panic("workflow: invalid tool registry entry")
			}
			if _, exists := t.registry[name]; exists {
				panic("workflow: duplicate tool registry entry: " + name)
			}
			t.ordered = append(t.ordered, spec)
			t.registry[name] = spec
		}
	})
}

func (t *ToolRuntime) definitions() []review.ToolDefinition {
	t.initRegistry()
	out := make([]review.ToolDefinition, len(t.ordered))
	for i, spec := range t.ordered {
		out[i] = spec.definition
	}
	return out
}

func (t *ToolRuntime) prepare(name, arguments string) (preparedToolCall, error) {
	t.initRegistry()
	spec, ok := t.registry[name]
	if !ok {
		return preparedToolCall{}, fmt.Errorf("tool %q is not available on this runtime", name)
	}
	return spec.prepare(t, arguments)
}

// execute is the single shared boundary for cancellation and Evidence path
// normalization. The prepared closure still invokes the concrete tool's own
// typed implementation (file query, lazy graph analysis, analyzer, or process).
func (t *ToolRuntime) execute(ctx context.Context, prepared preparedToolCall) ([]*Evidence, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	evidence, err := prepared.execute(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range evidence {
		if err := normalizeEvidencePath(t.repo, item); err != nil {
			return nil, err
		}
	}
	return evidence, nil
}

func (t *ToolRuntime) analysisIndex() (*graph.Index, error) {
	t.graphOnce.Do(func() {
		if t.index != nil {
			return
		}
		t.index, t.graphErr = graph.Build(t.repo)
	})
	if t.graphErr != nil {
		return nil, fmt.Errorf("build Go analysis index: %w", t.graphErr)
	}
	if t.index == nil {
		return nil, fmt.Errorf("Go analysis index unavailable")
	}
	return t.index, nil
}
