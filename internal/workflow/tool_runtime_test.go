package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestToolRuntimeRegistryControlsSurfacePermissions(t *testing.T) {
	root := t.TempDir()
	agent := newToolRuntime(root, "", nil, false, toolSurfaceAgent)
	mcpRuntime := newToolRuntime(root, "", nil, false, toolSurfaceMCP)

	names := func(runtime *ToolRuntime) []string {
		definitions := runtime.definitions()
		out := make([]string, len(definitions))
		for i, definition := range definitions {
			out[i] = definition.Function.Name
		}
		return out
	}
	if got, want := names(agent), []string{"read_diff", "read_code", "search_code", "inspect_symbol", "inspect_concurrency", "run_static_rules", "run_go_validation"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("agent tools=%v want=%v", got, want)
	}
	if got, want := names(mcpRuntime), []string{"read_diff", "read_code", "search_code", "inspect_symbol", "inspect_concurrency"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("MCP tools=%v want=%v", got, want)
	}
	for _, name := range []string{"run_static_rules", "run_go_validation", "submit_claims"} {
		if _, err := mcpRuntime.prepare(name, `{}`); err == nil || !strings.Contains(err.Error(), "not available") {
			t.Fatalf("MCP prepare(%q) err=%v", name, err)
		}
	}
}

func TestToolRuntimeBuildsGraphOnlyForGraphTools(t *testing.T) {
	root := writeToolRuntimeRepo(t, "package sample\n\nfunc Value() int { return 1 }\n")
	runtime := newToolRuntime(root, "", nil, false, toolSurfaceAgent)

	read, err := runtime.prepare("read_code", `{"file":"main.go","selector":{"kind":"symbol","symbol":"Value"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.execute(context.Background(), read); err != nil {
		t.Fatal(err)
	}
	if runtime.index != nil || runtime.graphErr != nil {
		t.Fatalf("read_code eagerly initialized graph: index=%v err=%v", runtime.index != nil, runtime.graphErr)
	}

	inspect, err := runtime.prepare("inspect_symbol", `{"symbol":"Value","file":"main.go","line":null,"relation":"definition"}`)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := runtime.execute(context.Background(), inspect)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.index == nil || len(evidence) == 0 {
		t.Fatalf("inspect_symbol did not initialize and use graph: index=%v evidence=%+v", runtime.index != nil, evidence)
	}
}

func TestToolRuntimeGraphFailureDoesNotDisableReadTools(t *testing.T) {
	root := writeToolRuntimeRepo(t, "package sample\n\nfunc Broken( {\n")
	runtime := newToolRuntime(root, "", nil, false, toolSurfaceAgent)

	read, err := runtime.prepare("read_code", `{"file":"main.go","selector":{"kind":"file_start"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if evidence, err := runtime.execute(context.Background(), read); err != nil || len(evidence) != 1 {
		t.Fatalf("read_code evidence=%+v err=%v", evidence, err)
	}
	inspect, err := runtime.prepare("inspect_symbol", `{"symbol":"Broken","file":"main.go","line":null,"relation":"definition"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.execute(context.Background(), inspect); err == nil || !strings.Contains(err.Error(), "build Go analysis index") {
		t.Fatalf("inspect_symbol err=%v", err)
	}
}

func TestToolRuntimeCancellationStopsBeforeToolExecution(t *testing.T) {
	root := writeToolRuntimeRepo(t, "package sample\n")
	runtime := newToolRuntime(root, "", nil, false, toolSurfaceAgent)
	prepared, err := runtime.prepare("read_code", `{"file":"main.go","selector":{"kind":"file_start"}}`)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runtime.execute(ctx, prepared); !errors.Is(err, context.Canceled) {
		t.Fatalf("execute err=%v", err)
	}
}

func writeToolRuntimeRepo(t *testing.T, source string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/runtime\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
