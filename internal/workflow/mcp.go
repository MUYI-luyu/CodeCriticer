package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	changediff "github.com/MUYI-luyu/codecritic/internal/diff"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpServerVersion = "0.1.0"

// mcpToolService adapts the shared ToolRuntime to MCP. It owns no
// snapshot lifecycle: the caller must keep snapshot open while the server runs.
type mcpToolService struct {
	snapshot *ReviewSnapshot
	tools    *ToolRuntime

	mu           sync.Mutex
	nextStep     int
	nextEvidence int
}

func newMCPToolService(snapshot *ReviewSnapshot, diffData []byte) (*mcpToolService, error) {
	if snapshot == nil || snapshot.Root == "" || snapshot.BaseSHA == "" || snapshot.HeadSHA == "" || snapshot.DiffHash == "" || snapshot.sourceRoot == "" || snapshot.tempRoot == "" {
		return nil, fmt.Errorf("invalid review snapshot")
	}
	if hashBytes(diffData) != snapshot.DiffHash {
		return nil, fmt.Errorf("MCP diff differs from review snapshot")
	}
	changes, err := changediff.Parse(diffData)
	if err != nil {
		return nil, fmt.Errorf("parse MCP diff: %w", err)
	}
	for i := range changes {
		change := &changes[i]
		if change.File != "" && change.File != "/dev/null" {
			change.File, err = repoRelativePath(snapshot.Root, change.File)
			if err != nil {
				return nil, fmt.Errorf("MCP diff path: %w", err)
			}
			if source, readErr := readRepoFile(snapshot.Root, change.File); readErr == nil {
				change.Annotate(source)
			}
		}
		if change.Old != "" && change.Old != "/dev/null" {
			change.Old, err = repoRelativePath(snapshot.Root, change.Old)
			if err != nil {
				return nil, fmt.Errorf("MCP old diff path: %w", err)
			}
		}
	}
	return &mcpToolService{
		snapshot: snapshot,
		tools:    newToolRuntime(snapshot.Root, "", changes, false, toolSurfaceMCP),
	}, nil
}

func (s *mcpToolService) call(ctx context.Context, name string, arguments json.RawMessage) *mcp.CallToolResult {
	prepared, err := s.tools.prepare(name, string(arguments))
	if err != nil {
		return mcpToolError(fmt.Errorf("%s arguments: %w", name, err))
	}
	evidence, err := s.tools.execute(ctx, prepared)
	if err != nil {
		return mcpToolError(err)
	}

	s.mu.Lock()
	s.nextStep++
	stepID := fmt.Sprintf("mcp-s%d", s.nextStep)
	for _, item := range evidence {
		s.nextEvidence++
		item.ID = fmt.Sprintf("mcp-e%d", s.nextEvidence)
		item.StepID = stepID
	}
	s.mu.Unlock()

	payload := map[string]any{
		"evidence": evidence,
		"snapshot": map[string]string{
			"base_sha":  s.snapshot.BaseSHA,
			"head_sha":  s.snapshot.HeadSHA,
			"diff_hash": s.snapshot.DiffHash,
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return mcpToolError(fmt.Errorf("encode MCP evidence: %w", err))
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		StructuredContent: payload,
	}
}

func mcpToolError(err error) *mcp.CallToolResult {
	result := &mcp.CallToolResult{}
	result.SetError(err)
	return result
}

func newMCPServer(service *mcpToolService) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "codecritic", Version: mcpServerVersion},
		&mcp.ServerOptions{Instructions: mcpInstructions(service)},
	)
	closedWorld := false
	for _, definition := range service.tools.definitions() {
		definition := definition
		server.AddTool(&mcp.Tool{
			Name:        definition.Function.Name,
			Description: definition.Function.Description,
			InputSchema: definition.Function.Parameters,
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    true,
				IdempotentHint:  true,
				DestructiveHint: &closedWorld,
				OpenWorldHint:   &closedWorld,
			},
		}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return service.call(ctx, definition.Function.Name, request.Params.Arguments), nil
		})
	}
	return server
}

func mcpInstructions(service *mcpToolService) string {
	type manifestEntry struct {
		File     string `json:"file"`
		HunkID   string `json:"hunk_id"`
		OldRange string `json:"old_range"`
		NewRange string `json:"new_range"`
		Section  string `json:"section,omitempty"`
	}
	manifest := make([]manifestEntry, 0)
	for _, change := range service.tools.changes {
		file := changeReviewFile(change)
		for _, hunk := range change.Hunks {
			manifest = append(manifest, manifestEntry{
				File: file, HunkID: hunk.ID,
				OldRange: fmt.Sprintf("%d,%d", hunk.OldStart, hunk.OldLines),
				NewRange: fmt.Sprintf("%d,%d", hunk.NewStart, hunk.NewLines),
				Section:  hunk.Section,
			})
		}
	}
	payload := map[string]any{
		"snapshot": map[string]string{
			"base_sha":  service.snapshot.BaseSHA,
			"head_sha":  service.snapshot.HeadSHA,
			"diff_hash": service.snapshot.DiffHash,
		},
		"diff_manifest": manifest,
	}
	encoded, _ := json.Marshal(payload)
	return "Review the pinned Go change using the read-only tools. read_diff requires a file and hunk_id from this manifest. Tools return Evidence facts, not bug verdicts. Snapshot and manifest: " + string(encoded)
}

// ServeMCPStdio exposes CodeCritic's read-only analysis tools over the official
// MCP stdio transport. The caller owns and closes snapshot after this returns.
func ServeMCPStdio(ctx context.Context, snapshot *ReviewSnapshot, diffData []byte) error {
	service, err := newMCPToolService(snapshot, diffData)
	if err != nil {
		return err
	}
	return newMCPServer(service).Run(ctx, &mcp.StdioTransport{})
}
