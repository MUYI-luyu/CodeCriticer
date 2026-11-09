package workflow

import "github.com/MUYI-luyu/codecritic/internal/review"

type runtimeToolSpec struct {
	definition review.ToolDefinition
	surfaces   toolSurface
	prepare    func(*ToolRuntime, string) (preparedToolCall, error)
}

func runtimeToolSpecs(allowExecution bool) []runtimeToolSpec {
	definitions := make(map[string]review.ToolDefinition)
	for _, definition := range toolDefinitions(allowExecution) {
		definitions[definition.Function.Name] = definition
	}
	// The built-in reviewer deliberately keeps its default tool surface small.
	// Reading the diff/source, searching, and static diagnostics are enough for
	// the ordinary ReAct loop. Graph analysis and process execution remain
	// available to an MCP host, where a human or a richer coding agent can opt
	// into them for a specific question instead of paying their context and
	// failure cost on every review.
	shared := toolSurfaceAgent | toolSurfaceMCP
	return []runtimeToolSpec{
		{definition: definitions["read_diff"], surfaces: shared, prepare: prepareReadDiff},
		{definition: definitions["read_code"], surfaces: shared, prepare: prepareReadCode},
		{definition: definitions["search_code"], surfaces: shared, prepare: prepareSearchCode},
		{definition: definitions["read_evidence"], surfaces: toolSurfaceAgent, prepare: prepareReadEvidence},
		{definition: definitions["inspect_symbol"], surfaces: toolSurfaceMCP, prepare: prepareInspectSymbol},
		{definition: definitions["inspect_concurrency"], surfaces: toolSurfaceMCP, prepare: prepareInspectConcurrency},
		{definition: definitions["run_static_rules"], surfaces: toolSurfaceAgent, prepare: prepareStaticRules},
		// Validation is intentionally not a model-callable default. It remains a
		// package capability for the CLI/eval harness and direct operator use.
	}
}

func submitClaimsDefinition() review.ToolDefinition {
	definitions := toolDefinitions(false)
	return definitions[len(definitions)-1]
}

// agentTools remains a schema characterization helper. Production sessions
// obtain code-tool definitions from their ToolRuntime and append submit_claims.
func agentTools(allowExecution bool) []review.ToolDefinition {
	specs := runtimeToolSpecs(allowExecution)
	definitions := make([]review.ToolDefinition, 0, len(specs)+1)
	for _, spec := range specs {
		if spec.surfaces&toolSurfaceAgent == 0 {
			continue
		}
		definitions = append(definitions, spec.definition)
	}
	return append(definitions, submitClaimsDefinition())
}

func toolDefinitions(allowExecution bool) []review.ToolDefinition {
	stringValue := map[string]any{"type": "string", "pattern": `\S`}
	nullableString := map[string]any{"type": []string{"string", "null"}, "pattern": `\S`}
	nullableInteger := map[string]any{"type": []string{"integer", "null"}, "minimum": 1}
	validationModes := []string{"compile"}
	if allowExecution {
		validationModes = append(validationModes, "test", "race")
	}
	return []review.ToolDefinition{
		functionTool("read_diff", "Read one complete diff hunk listed in the initial manifest. Use this for every omitted hunk relevant to the review.", map[string]any{
			"file": stringValue, "hunk_id": stringValue,
		}, []string{"file", "hunk_id"}),
		functionTool("read_code", "Read repository code using exactly one selector. Choose file_start for the first 200 lines, range for a bounded line span, line for focused context, or symbol for an exact declaration such as Server.Close.", map[string]any{
			"file": stringValue,
			"selector": map[string]any{"anyOf": []any{
				strictObject(map[string]any{
					"kind": map[string]any{"type": "string", "enum": []string{"file_start"}},
				}, []string{"kind"}),
				strictObject(map[string]any{
					"kind":      map[string]any{"type": "string", "enum": []string{"range"}},
					"start":     map[string]any{"type": "integer", "minimum": 1},
					"max_lines": map[string]any{"type": "integer", "minimum": 1, "maximum": maxReadLines},
				}, []string{"kind", "start", "max_lines"}),
				strictObject(map[string]any{
					"kind":          map[string]any{"type": "string", "enum": []string{"line"}},
					"line":          map[string]any{"type": "integer", "minimum": 1},
					"context_lines": map[string]any{"type": "integer", "minimum": 0, "maximum": maxReadContextLines},
				}, []string{"kind", "line", "context_lines"}),
				strictObject(map[string]any{
					"kind":   map[string]any{"type": "string", "enum": []string{"symbol"}},
					"symbol": stringValue,
				}, []string{"kind", "symbol"}),
			}},
		}, []string{"file", "selector"}),
		functionTool("search_code", "Literal search over Go source. Results include nearby code. An empty scoped result is explicit absence; operational failure is returned as an error.", map[string]any{
			"keyword": stringValue, "file": nullableString,
		}, []string{"keyword", "file"}),
		functionTool("read_evidence", "Re-read canonical Evidence from an earlier tool result by ID when it is no longer in the active context. This is read-only and does not create new Evidence.", map[string]any{
			"evidence_ids": map[string]any{"type": "array", "minItems": 1, "maxItems": 8, "items": stringValue},
		}, []string{"evidence_ids"}),
		functionTool("inspect_symbol", "Use Go compiler identity to inspect a definition or references, or a CHA call hierarchy. Call results state whether an edge is exact or an over-approximation; this is navigation evidence, not data-flow proof.", map[string]any{
			"symbol": stringValue, "file": stringValue, "line": nullableInteger,
			"relation": map[string]any{"type": "string", "enum": []string{"definition", "references", "callers", "callees"}},
		}, []string{"symbol", "file", "line", "relation"}),
		functionTool("inspect_concurrency", "Inspect a bounded Go concurrency slice rooted at one function. Returns compiler-derived lock/channel/go/defer operations, call paths, path-held may/must locksets, resource provenance, and explicit uncertainty. It returns facts, never a bug verdict.", map[string]any{
			"symbol": stringValue, "file": stringValue, "line": nullableInteger,
		}, []string{"symbol", "file", "line"}),
		functionTool("run_static_rules", "Run the available static analyzers and return diagnostics as Evidence. Diagnostics are leads, not proof.", map[string]any{}, []string{}),
		functionTool("run_go_validation", "Run a bounded Go compile/test/race validation. Compile never runs target tests; test and race modes are exposed only with explicit --allow-exec permission. A failing command is Evidence, not automatic proof of a Claim.", map[string]any{
			"mode":    map[string]any{"type": "string", "enum": validationModes},
			"package": map[string]any{"type": "string", "pattern": goPackageSchemaPattern}, "test": nullableString,
			"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": maxValidationSecs},
		}, []string{"mode", "package", "test", "timeout_seconds"}),
		functionTool("submit_claims", "Finish the review by submitting the complete set of candidate issues. Use an empty array when no issue is supported.", map[string]any{
			"claims": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file":         stringValue,
						"line":         map[string]any{"type": "integer", "minimum": 1},
						"severity":     map[string]any{"type": "string", "enum": []string{"error", "warning", "info"}},
						"certainty":    map[string]any{"type": "string", "enum": []string{"certain", "schedule_dependent", "contract_dependent"}},
						"msg":          stringValue,
						"evidence_ids": map[string]any{"type": "array", "items": stringValue, "minItems": 1},
					},
					"required":             []string{"file", "line", "severity", "certainty", "msg", "evidence_ids"},
					"additionalProperties": false,
				},
			},
		}, []string{"claims"}),
	}
}

func functionTool(name, description string, properties map[string]any, required []string) review.ToolDefinition {
	return review.ToolDefinition{
		Type: "function",
		Function: review.ToolFunction{
			Name: name, Description: description, Strict: true,
			Parameters: map[string]any{
				"type":                 "object",
				"properties":           properties,
				"required":             required,
				"additionalProperties": false,
			},
		},
	}
}

func strictObject(properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}
