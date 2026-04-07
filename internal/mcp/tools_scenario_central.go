package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerCentralScenarioTools(s *Server, deps *ToolDeps) {
	// scenario.generate — request AI-generated deployment scenario from central
	s.RegisterTool(&Tool{
		Name:        "scenario.generate",
		Description: "Request the central server to generate an AI-powered multi-model deployment scenario for given hardware and models.",
		InputSchema: schema(
			`"hardware":{"type":"string","description":"Hardware profile, e.g. 'nvidia-gb10-arm64'"},`+
				`"models":{"type":"array","items":{"type":"string"},"description":"Model names to include, e.g. ['qwen3-8b','glm-4.7-flash']"},`+
				`"goal":{"type":"string","description":"Optimization goal: 'balanced', 'low-latency', 'maximize-models'"}`,
			"hardware", "models"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.RequestScenario == nil {
				return ErrorResult("scenario.generate not configured — set central.endpoint first"), nil
			}
			var p struct {
				Hardware string   `json:"hardware"`
				Models   []string `json:"models"`
				Goal     string   `json:"goal"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Hardware == "" || len(p.Models) == 0 {
				return ErrorResult("hardware and models are required"), nil
			}
			data, err := deps.RequestScenario(ctx, p.Hardware, p.Models, p.Goal)
			if err != nil {
				return nil, fmt.Errorf("scenario.generate for %s: %w", p.Hardware, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// scenario.list_central — list scenarios from central server
	s.RegisterTool(&Tool{
		Name:        "scenario.list_central",
		Description: "List deployment scenarios from the central knowledge server, filtered by hardware or source.",
		InputSchema: schema(
			`"hardware":{"type":"string","description":"Filter by hardware profile"},`+
				`"source":{"type":"string","description":"Filter by source: 'advisor', 'user', 'analyzer'"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ListCentralScenarios == nil {
				return ErrorResult("scenario.list_central not configured — set central.endpoint first"), nil
			}
			var p struct {
				Hardware string `json:"hardware"`
				Source   string `json:"source"`
			}
			if len(params) > 0 {
				_ = json.Unmarshal(params, &p)
			}
			data, err := deps.ListCentralScenarios(ctx, p.Hardware, p.Source)
			if err != nil {
				return nil, fmt.Errorf("scenario.list_central: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})
}
