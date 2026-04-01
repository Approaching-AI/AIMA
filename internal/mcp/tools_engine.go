package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerEngineTools(s *Server, deps *ToolDeps) {
	// engine.scan
	s.RegisterTool(&Tool{
		Name:        "engine.scan",
		Description: "Scan this device for locally available inference engines (container images and native binaries) and register newly found ones.",
		InputSchema: schema(`"runtime":{"type":"string","enum":["auto","container","native"],"description":"Runtime filter: 'auto' scans both container and native (default), 'container' scans only K3S/Docker images, 'native' scans only local binaries"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ScanEngines == nil {
				return ErrorResult("engine.scan not implemented"), nil
			}
			var p struct {
				Runtime string `json:"runtime"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return ErrorResult(fmt.Sprintf("invalid params: %v", err)), nil
				}
			}
			if p.Runtime == "" {
				p.Runtime = "auto"
			}
			data, err := deps.ScanEngines(ctx, p.Runtime, false)
			if err != nil {
				return nil, fmt.Errorf("scan engines: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// engine.info
	s.RegisterTool(&Tool{
		Name:        "engine.info",
		Description: "Get full information about a specific engine: availability, hardware requirements, startup config, supported features, and constraints.",
		InputSchema: schema(`"name":{"type":"string","description":"Engine type (e.g. 'llamacpp', 'vllm', 'sglang'), image name, or engine ID. Call engine.list to see available names."}`, "name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.GetEngineInfo == nil {
				return ErrorResult("engine.info not implemented"), nil
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
			data, err := deps.GetEngineInfo(ctx, p.Name)
			if err != nil {
				return nil, fmt.Errorf("engine info %s: %w", p.Name, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// engine.list
	s.RegisterTool(&Tool{
		Name:        "engine.list",
		Description: "List inference engines registered in the local database with names, types, runtime (container/native), and statuses.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ListEngines == nil {
				return ErrorResult("engine.list not implemented"), nil
			}
			data, err := deps.ListEngines(ctx)
			if err != nil {
				return nil, fmt.Errorf("list engines: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// engine.pull
	s.RegisterTool(&Tool{
		Name:        "engine.pull",
		Description: "Download an inference engine image or binary from its configured source. Downloads a container image or native binary depending on this device's platform. If name is omitted, pulls the default engine for this hardware. Automatically registers the engine after pulling.",
		InputSchema: schema(`"name":{"type":"string","description":"Engine type to pull, e.g. 'llamacpp', 'vllm', 'sglang'. Omit to pull the default engine for this hardware."}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.PullEngine == nil {
				return ErrorResult("engine.pull not implemented"), nil
			}
			var p struct {
				Name string `json:"name"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return ErrorResult(fmt.Sprintf("invalid params: %v", err)), nil
				}
			}
			name := p.Name
			// Empty name is handled by the PullEngine implementation (uses catalog.DefaultEngine).
			// Pass nil onProgress: MCP JSON-RPC has no streaming capability.
			if err := deps.PullEngine(ctx, name, nil); err != nil {
				return nil, fmt.Errorf("pull engine %s: %w", name, err)
			}
			return TextResult(fmt.Sprintf("engine %s pulled successfully", name)), nil
		},
	})

	// engine.import
	s.RegisterTool(&Tool{
		Name:        "engine.import",
		Description: "Import an engine container image from a local OCI tar file and register it (airgap use case).",
		InputSchema: schema(`"path":{"type":"string","description":"Absolute path to the OCI tar file, e.g. '/data/images/vllm-cuda.tar'"}`, "path"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ImportEngine == nil {
				return ErrorResult("engine.import not implemented"), nil
			}
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Path == "" {
				return ErrorResult("path is required"), nil
			}
			if err := deps.ImportEngine(ctx, p.Path); err != nil {
				return nil, fmt.Errorf("import engine from %s: %w", p.Path, err)
			}
			return TextResult(fmt.Sprintf("engine image imported from %s", p.Path)), nil
		},
	})

	// engine.remove
	s.RegisterTool(&Tool{
		Name:        "engine.remove",
		Description: "Remove an engine record from the local database. Optionally deletes the actual container image or native binary. A rollback snapshot is created automatically. Blocked for agent-initiated calls.",
		InputSchema: schema(
			`"name":{"type":"string","description":"Engine name or ID to remove. Call engine.list to see registered engines."},`+
				`"delete_files":{"type":"boolean","description":"Also delete the actual container image (docker rmi) or native binary file. Default false."}`,
			"name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.RemoveEngine == nil {
				return ErrorResult("engine.remove not implemented"), nil
			}
			var p struct {
				Name        string `json:"name"`
				DeleteFiles bool   `json:"delete_files"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			if err := deps.RemoveEngine(ctx, p.Name, p.DeleteFiles); err != nil {
				return nil, fmt.Errorf("remove engine %s: %w", p.Name, err)
			}
			msg := fmt.Sprintf("engine %s removed", p.Name)
			if p.DeleteFiles {
				msg += " (files cleaned up)"
			}
			return TextResult(msg), nil
		},
	})

	// engine.plan
	s.RegisterTool(&Tool{
		Name:        "engine.plan",
		Description: "Show all engines compatible with the current hardware, their install status, and a recommendation. Use before engine.pull to help the user choose.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.EnginePlan == nil {
				return ErrorResult("engine.plan not implemented"), nil
			}
			data, err := deps.EnginePlan(ctx)
			if err != nil {
				return nil, fmt.Errorf("engine plan: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})
}
