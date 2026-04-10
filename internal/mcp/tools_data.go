package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerDataTools(s *Server, deps *ToolDeps) {
	// data.export
	s.RegisterTool(&Tool{
		Name:        "data.export",
		Description: "Export knowledge data (configurations, benchmarks, notes) to JSON. Filter by hardware, model, or engine.",
		InputSchema: schema(
			`"hardware":{"type":"string","description":"Filter by hardware profile ID"},` +
				`"model":{"type":"string","description":"Filter by model name"},` +
				`"engine":{"type":"string","description":"Filter by engine type"},` +
				`"output_path":{"type":"string","description":"File path to write JSON. If omitted, returns JSON in response."}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ExportKnowledge == nil {
				return ErrorResult("data.export not implemented"), nil
			}
			data, err := deps.ExportKnowledge(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("export knowledge: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// data.import
	s.RegisterTool(&Tool{
		Name:        "data.import",
		Description: "Import knowledge data from a JSON file. Conflict resolution: 'skip' (default) or 'overwrite'. Supports dry-run. Atomic transaction.",
		InputSchema: schema(
			`"input_path":{"type":"string","description":"Path to JSON file to import"},`+
				`"conflict":{"type":"string","enum":["skip","overwrite"],"description":"Conflict resolution (default: skip)"},`+
				`"dry_run":{"type":"boolean","description":"Preview import without writing (default: false)"}`,
			"input_path"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ImportKnowledge == nil {
				return ErrorResult("data.import not implemented"), nil
			}
			data, err := deps.ImportKnowledge(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("import knowledge: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})
}
