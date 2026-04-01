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

	// shell.exec
	s.RegisterTool(&Tool{
		Name:        "shell.exec",
		Description: "Execute a whitelisted shell command (nvidia-smi, df, free, uname, cat /proc/cpuinfo, read-only kubectl).",
		InputSchema: schema(`"command":{"type":"string","description":"Shell command to run. Must be one of: 'nvidia-smi', 'df -h', 'free -h', 'uname -a', 'cat /proc/cpuinfo', 'kubectl get pods', etc."}`, "command"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ExecShell == nil {
				return ErrorResult("shell.exec not implemented"), nil
			}
			var p struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Command == "" {
				return ErrorResult("command is required"), nil
			}
			if !isCommandAllowed(p.Command) {
				return ErrorResult(fmt.Sprintf("command not allowed: %s (allowed: nvidia-smi, df, free, uname, cat /proc/cpuinfo, kubectl get/describe/logs/top/version)", p.Command)), nil
			}
			data, err := deps.ExecShell(ctx, p.Command)
			if err != nil {
				return nil, fmt.Errorf("exec shell %q: %w", p.Command, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// discover.lan
	s.RegisterTool(&Tool{
		Name:        "discover.lan",
		Description: "Low-level mDNS scan for AIMA instances on the local network. Returns raw service entries.",
		InputSchema: schema(`"timeout_s":{"type":"integer","description":"Scan timeout in seconds (default 3)"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DiscoverLAN == nil {
				return ErrorResult("discover.lan not implemented"), nil
			}
			var p struct {
				TimeoutS int `json:"timeout_s"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return ErrorResult("invalid params: " + err.Error()), nil
				}
			}
			if p.TimeoutS <= 0 {
				p.TimeoutS = 3
			}
			data, err := deps.DiscoverLAN(ctx, p.TimeoutS)
			if err != nil {
				return nil, fmt.Errorf("discover lan: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// catalog.override
	s.RegisterTool(&Tool{
		Name:        "catalog.override",
		Description: "Write a YAML asset to the runtime overlay catalog (~/.aima/catalog/). Overrides factory-embedded asset or adds new one.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["engine_asset","model_asset","hardware_profile","partition_strategy","stack_component"],"description":"Asset kind"},"name":{"type":"string","description":"metadata.name of the asset"},"content":{"type":"string","description":"Full YAML content of the asset"}},"required":["kind","name","content"]}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.CatalogOverride == nil {
				return ErrorResult("catalog.override not implemented"), nil
			}
			var p struct {
				Kind    string `json:"kind"`
				Name    string `json:"name"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Kind == "" || p.Name == "" || p.Content == "" {
				return ErrorResult("kind, name, and content are required"), nil
			}
			data, err := deps.CatalogOverride(ctx, p.Kind, p.Name, p.Content)
			if err != nil {
				return nil, fmt.Errorf("catalog override: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// catalog.status
	s.RegisterTool(&Tool{
		Name:        "catalog.status",
		Description: "Show catalog asset counts: factory (compiled-in) vs overlay (runtime) for each asset type.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.CatalogStatus == nil {
				return ErrorResult("catalog.status not implemented"), nil
			}
			data, err := deps.CatalogStatus(ctx)
			if err != nil {
				return nil, fmt.Errorf("catalog status: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// catalog.validate
	s.RegisterTool(&Tool{
		Name:        "catalog.validate",
		Description: "Validate engine YAML catalog for schema issues: missing registries, baked-in proxy URLs, single-point-of-failure registries, and local-only distribution markers.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.CatalogValidate == nil {
				return ErrorResult("catalog.validate not implemented"), nil
			}
			data, err := deps.CatalogValidate(ctx)
			if err != nil {
				return nil, fmt.Errorf("catalog validate: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// stack.preflight
	s.RegisterTool(&Tool{
		Name:        "stack.preflight",
		Description: "Check which infrastructure stack components need downloads. Tier: 'docker' (default) or 'k3s' (full stack).",
		InputSchema: schema(`"tier":{"type":"string","description":"Init tier: 'docker' (default) or 'k3s' (includes K3S + HAMi)","enum":["docker","k3s"]}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.StackPreflight == nil {
				return ErrorResult("stack.preflight not implemented"), nil
			}
			var p struct {
				Tier string `json:"tier"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return ErrorResult("invalid params: " + err.Error()), nil
				}
			}
			if p.Tier == "" {
				p.Tier = "docker"
			}
			data, err := deps.StackPreflight(ctx, p.Tier)
			if err != nil {
				return nil, fmt.Errorf("stack preflight: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// stack.init
	s.RegisterTool(&Tool{
		Name:        "stack.init",
		Description: "Install and configure the infrastructure stack. Tier: 'docker' (default) or 'k3s' (full). Blocked for agent-initiated calls.",
		InputSchema: schema(`"tier":{"type":"string","description":"Init tier: 'docker' (default) or 'k3s' (includes K3S + HAMi)","enum":["docker","k3s"]},"allow_download":{"type":"boolean","description":"Auto-download missing component files (default false)"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.StackInit == nil {
				return ErrorResult("stack.init not implemented"), nil
			}
			var p struct {
				Tier          string `json:"tier"`
				AllowDownload bool   `json:"allow_download"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return ErrorResult(fmt.Sprintf("invalid params: %v", err)), nil
				}
			}
			if p.Tier == "" {
				p.Tier = "docker"
			}
			data, err := deps.StackInit(ctx, p.Tier, p.AllowDownload)
			if err != nil {
				return nil, fmt.Errorf("stack init: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// stack.status
	s.RegisterTool(&Tool{
		Name:        "stack.status",
		Description: "Check installation status of infrastructure stack components (K3S, HAMi) with versions and health.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.StackStatus == nil {
				return ErrorResult("stack.status not implemented"), nil
			}
			data, err := deps.StackStatus(ctx)
			if err != nil {
				return nil, fmt.Errorf("stack status: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})
}
