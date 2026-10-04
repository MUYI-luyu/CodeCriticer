package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPServerExposesOnlySnapshotBoundReadTools(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "mcp@example.com")
	gitTest(t, repo, "config", "user.name", "MCP Test")
	writeSnapshotFile(t, repo, "go.mod", "module example.com/mcp\n\ngo 1.23\n")
	writeSnapshotFile(t, repo, "main.go", "package sample\n\nfunc Value() int { return 1 }\n")
	gitTest(t, repo, "add", "go.mod", "main.go")
	gitTest(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))

	writeSnapshotFile(t, repo, "main.go", "package sample\n\nimport \"sync\"\n\nvar mu sync.Mutex\n\nfunc Value() int { mu.Lock(); defer mu.Unlock(); return 2 }\n")
	gitTest(t, repo, "add", "main.go")
	gitTest(t, repo, "commit", "-m", "head")
	head := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))

	snapshot, diffData, err := CreateGitReviewSnapshot(context.Background(), repo, base, head, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })

	// The MCP service must keep reading the committed snapshot, not the mutable source checkout.
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package sample\n\nfunc Value() int { return 99 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	service, err := newMCPToolService(snapshot, diffData)
	if err != nil {
		t.Fatal(err)
	}
	server := newMCPServer(service)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run(context.Background(), serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "codecritic-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if initialized := session.InitializeResult(); initialized == nil || !strings.Contains(initialized.Instructions, `"hunk_id":"h1"`) || !strings.Contains(initialized.Instructions, head) {
		t.Fatalf("MCP instructions do not expose the pinned diff manifest: %+v", initialized)
	}

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
			t.Fatalf("tool %s is not marked read-only and idempotent: %+v", tool.Name, tool.Annotations)
		}
	}
	sort.Strings(names)
	wantNames := []string{"inspect_concurrency", "inspect_symbol", "read_code", "read_diff", "search_code"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("MCP tools=%v want=%v", names, wantNames)
	}

	calls := []struct {
		name string
		args map[string]any
	}{
		{name: "read_diff", args: map[string]any{"file": "main.go", "hunk_id": "h1"}},
		{name: "read_code", args: map[string]any{"file": "main.go", "selector": map[string]any{"kind": "symbol", "symbol": "Value"}}},
		{name: "search_code", args: map[string]any{"keyword": "Value", "file": "main.go"}},
		{name: "inspect_symbol", args: map[string]any{"symbol": "Value", "file": "main.go", "line": nil, "relation": "definition"}},
		{name: "inspect_concurrency", args: map[string]any{"symbol": "Value", "file": "main.go", "line": nil}},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			payload := callMCPAndDecode(t, session, call.name, call.args)
			if payload.Snapshot["base_sha"] != base || payload.Snapshot["head_sha"] != head || payload.Snapshot["diff_hash"] != snapshot.DiffHash {
				t.Fatalf("snapshot metadata=%v", payload.Snapshot)
			}
			if len(payload.Evidence) == 0 {
				t.Fatalf("%s returned no evidence", call.name)
			}
			for _, item := range payload.Evidence {
				if item.ID == "" || item.StepID == "" {
					t.Fatalf("MCP evidence lacks provenance IDs: %+v", item)
				}
			}
			assertMCPMatchesInternalTool(t, service, call.name, call.args, payload.Evidence)
		})
	}
	readPayload := callMCPAndDecode(t, session, "read_code", calls[1].args)
	if !strings.Contains(readPayload.Evidence[0].Content, "return 2") || strings.Contains(readPayload.Evidence[0].Content, "return 99") {
		t.Fatalf("MCP read_code escaped snapshot: %+v", readPayload.Evidence)
	}

	bad, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "read_code",
		Arguments: map[string]any{
			"file": "main.go", "selector": map[string]any{"kind": "file_start"}, "unknown": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bad.IsError {
		t.Fatalf("unknown MCP argument was accepted: %+v", bad)
	}

	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("MCP server stopped with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MCP server did not stop after client close")
	}
}

type mcpEvidencePayload struct {
	Evidence []*Evidence       `json:"evidence"`
	Snapshot map[string]string `json:"snapshot"`
}

func callMCPAndDecode(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) mcpEvidencePayload {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("MCP %s result=%+v", name, result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("MCP content type=%T", result.Content[0])
	}
	var payload mcpEvidencePayload
	if err := json.Unmarshal([]byte(text.Text), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func assertMCPMatchesInternalTool(t *testing.T, service *mcpToolService, name string, arguments map[string]any, actual []*Evidence) {
	t.Helper()
	encodedArgs, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.tools.prepare(name, string(encodedArgs))
	if err != nil {
		t.Fatal(err)
	}
	direct, err := service.tools.execute(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	stripProvenance := func(items []*Evidence) []*Evidence {
		cloned := make([]*Evidence, len(items))
		for i, item := range items {
			copy := *item
			copy.ID = ""
			copy.StepID = ""
			cloned[i] = &copy
		}
		return cloned
	}
	if !reflect.DeepEqual(stripProvenance(actual), stripProvenance(direct)) {
		t.Fatalf("MCP evidence diverges from internal %s:\nMCP=%+v\ndirect=%+v", name, actual, direct)
	}
}

func TestMCPServiceRejectsDiffOutsideSnapshot(t *testing.T) {
	snapshot := &ReviewSnapshot{
		Root: "repo", BaseSHA: "base", HeadSHA: "head", DiffHash: hashBytes([]byte("expected")),
		sourceRoot: "source", tempRoot: "temp",
	}
	if _, err := newMCPToolService(snapshot, []byte("different")); err == nil {
		t.Fatal("expected MCP snapshot diff mismatch")
	}
}
