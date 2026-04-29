package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerExternalTools(s *Server, deps *ToolDeps) {
	s.RegisterTool(&Tool{
		Name:        "external.scan",
		Description: "Scan localhost for externally started inference or model API services and record them as discovered, unmanaged services.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ScanExternalServices == nil {
				return ErrorResult("external.scan not implemented"), nil
			}
			data, err := deps.ScanExternalServices(ctx)
			if err != nil {
				return nil, fmt.Errorf("scan external services: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name:        "external.list",
		Description: "List externally discovered, unmanaged local services with base URLs and advertised models.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ListExternalServices == nil {
				return ErrorResult("external.list not implemented"), nil
			}
			data, err := deps.ListExternalServices(ctx)
			if err != nil {
				return nil, fmt.Errorf("list external services: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name:        "external.import",
		Description: "Import a discovered OpenAI-compatible external service into the AIMA proxy route table. This does not manage the external process lifecycle.",
		InputSchema: schema(
			`"id":{"type":"string","description":"Discovered external service id from external.list"},` +
				`"base_url":{"type":"string","description":"Discovered external service base_url from external.list"},` +
				`"models":{"type":"array","items":{"type":"string"},"description":"Optional model ids to import. Defaults to all discovered models for the service."}`,
		),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ImportExternalService == nil {
				return ErrorResult("external.import not implemented"), nil
			}
			var p struct {
				ID      string   `json:"id"`
				BaseURL string   `json:"base_url"`
				Models  []string `json:"models"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return ErrorResult(fmt.Sprintf("invalid params: %v", err)), nil
				}
			}
			idOrBaseURL := p.ID
			if idOrBaseURL == "" {
				idOrBaseURL = p.BaseURL
			}
			if idOrBaseURL == "" {
				return ErrorResult("id or base_url is required"), nil
			}
			data, err := deps.ImportExternalService(ctx, idOrBaseURL, p.Models)
			if err != nil {
				return nil, fmt.Errorf("import external service: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})
}
