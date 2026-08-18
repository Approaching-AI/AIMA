package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerSystemTools(s *Server, deps *ToolDeps) {
	// system.status
	s.RegisterTool(&Tool{
		Name:        "system.status",
		Description: "Get a combined system overview: hardware summary, active deployments, and GPU metrics in one call.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SystemStatus == nil {
				return ErrorResult("system.status not implemented"), nil
			}
			data, err := deps.SystemStatus(ctx)
			if err != nil {
				return nil, fmt.Errorf("system status: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// system.config
	s.RegisterTool(&Tool{
		Name:        "system.config",
		Description: "Get or set a persistent system configuration value. Omit value to read, provide value to write. Sensitive keys are masked.",
		InputSchema: schema(
			`"key":{"type":"string","description":"Configuration key: `+SupportedConfigKeysString()+`"},`+
				`"value":{"type":"string","description":"Value to set. Omit this field to read the current value."}`,
			"key"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Key   string  `json:"key"`
				Value *string `json:"value"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Key == "" {
				return ErrorResult("key is required"), nil
			}
			if !IsValidConfigKey(p.Key) {
				return ErrorResult(fmt.Sprintf("unknown config key %q; supported keys: %s", p.Key, SupportedConfigKeysString())), nil
			}

			if p.Value != nil {
				// Set
				if deps.SetConfig == nil {
					return ErrorResult("system.config set not implemented"), nil
				}
				if err := deps.SetConfig(ctx, p.Key, *p.Value); err != nil {
					return nil, fmt.Errorf("set config %s: %w", p.Key, err)
				}
				display := *p.Value
				if IsSensitiveConfigKey(p.Key) {
					display = "***"
				}
				return TextResult(fmt.Sprintf("config %s set to %s", p.Key, display)), nil
			}

			// Get
			if deps.GetConfig == nil {
				return ErrorResult("system.config get not implemented"), nil
			}
			val, err := deps.GetConfig(ctx, p.Key)
			if err != nil {
				// Key not found → return empty string (not an error).
				// This prevents HTTP 500 storms from UI polling for unconfigured keys.
				return TextResult(""), nil
			}
			if IsSensitiveConfigKey(p.Key) {
				val = "***"
			}
			return TextResult(val), nil
		},
	})

	// system.diagnostics
	s.RegisterTool(&Tool{
		Name:        "system.diagnostics",
		Description: "Export a telemetry-free local diagnostics bundle for troubleshooting. Secrets are redacted. By default writes a JSON file under the local AIMA data directory; set inline=true to return the bundle directly.",
		InputSchema: schema(
			`"output_path":{"type":"string","description":"Optional output JSON path. If omitted and inline=false, AIMA writes to its local diagnostics directory."},` +
				`"inline":{"type":"boolean","description":"Return the diagnostics bundle directly instead of writing a file. Default: false."},` +
				`"include_logs":{"type":"boolean","description":"Include redacted deployment log tails. Default: true."},` +
				`"log_lines":{"type":"integer","description":"Deployment log tail lines per deployment. Default: 80."}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DiagnosticsExport == nil {
				return ErrorResult("system.diagnostics not implemented"), nil
			}
			data, err := deps.DiagnosticsExport(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("system diagnostics: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})
}
