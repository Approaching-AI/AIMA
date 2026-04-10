package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerScenarioTools(s *Server, deps *ToolDeps) {
	// scenario.show
	s.RegisterTool(&Tool{
		Name:        "scenario.show",
		Description: "Show full details of a deployment scenario including deployments, memory budget, startup order, alternative configs, integrations, verification results, and open questions.",
		InputSchema: schema(
			`"name":{"type":"string","description":"Scenario name, e.g. 'openclaw-multi'. Call catalog.list with kind=scenarios to see available scenarios."}`,
			"name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ScenarioShow == nil {
				return ErrorResult("scenario.show not available"), nil
			}
			var p struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			data, err := deps.ScenarioShow(ctx, p.Name)
			if err != nil {
				return nil, fmt.Errorf("scenario show: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// scenario.apply
	s.RegisterTool(&Tool{
		Name:        "scenario.apply",
		Description: "Deploy all models defined in a deployment scenario. Supports dry_run to preview without executing.",
		InputSchema: schema(
			`"name":{"type":"string","description":"Scenario name, e.g. 'openclaw-multi'. Call catalog.list with kind=scenarios to see available scenarios."},`+
				`"dry_run":{"type":"boolean","description":"If true, preview deployment plans without executing (default false)"}`,
			"name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ScenarioApply == nil {
				return ErrorResult("scenario.apply not available"), nil
			}
			var p struct {
				Name   string `json:"name"`
				DryRun bool   `json:"dry_run"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			data, err := deps.ScenarioApply(ctx, p.Name, p.DryRun)
			if err != nil {
				return nil, fmt.Errorf("scenario apply: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})
}
