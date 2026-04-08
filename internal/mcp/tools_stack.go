package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerStackTools(s *Server, deps *ToolDeps) {
	// stack — status/preflight/init via action param
	s.RegisterTool(&Tool{
		Name:        "stack",
		Description: "Infrastructure stack management. action=status: check installation status of stack components (K3S, HAMi) with versions and health. action=preflight: check which components need downloads before installation. action=init: install and configure the infrastructure stack. Blocked for agent-initiated calls (init only).",
		InputSchema: schema(
			`"action":{"type":"string","enum":["status","preflight","init"],"description":"Stack action"},`+
				`"tier":{"type":"string","enum":["docker","k3s"],"description":"Init tier: 'docker' (default) or 'k3s' (includes K3S + HAMi)"},`+
				`"allow_download":{"type":"boolean","description":"Auto-download missing component files (for action=init, default false)"}`,
			"action"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Action        string `json:"action"`
				Tier          string `json:"tier"`
				AllowDownload bool   `json:"allow_download"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return nil, fmt.Errorf("parse params: %w", err)
				}
			}
			if p.Tier == "" {
				p.Tier = "docker"
			}
			switch p.Action {
			case "status":
				if deps.StackStatus == nil {
					return ErrorResult("stack.status not implemented"), nil
				}
				data, err := deps.StackStatus(ctx)
				if err != nil {
					return nil, fmt.Errorf("stack status: %w", err)
				}
				return TextResult(string(data)), nil
			case "preflight":
				if deps.StackPreflight == nil {
					return ErrorResult("stack.preflight not implemented"), nil
				}
				data, err := deps.StackPreflight(ctx, p.Tier)
				if err != nil {
					return nil, fmt.Errorf("stack preflight: %w", err)
				}
				return TextResult(string(data)), nil
			case "init":
				if deps.StackInit == nil {
					return ErrorResult("stack.init not implemented"), nil
				}
				data, err := deps.StackInit(ctx, p.Tier, p.AllowDownload)
				if err != nil {
					return nil, fmt.Errorf("stack init: %w", err)
				}
				return TextResult(string(data)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown action %q; supported: status, preflight, init", p.Action)), nil
			}
		},
	})
}
