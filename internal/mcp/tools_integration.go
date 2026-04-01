package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerIntegrationTools(s *Server, deps *ToolDeps) {
	// fleet.list_devices
	s.RegisterTool(&Tool{
		Name:        "fleet.list_devices",
		Description: "List all AIMA devices discovered on the LAN via mDNS with device IDs, hostnames, addresses, and ports.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.FleetListDevices == nil {
				return ErrorResult("fleet.list_devices not implemented"), nil
			}
			data, err := deps.FleetListDevices(ctx)
			if err != nil {
				return nil, fmt.Errorf("fleet list devices: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// fleet.device_info
	s.RegisterTool(&Tool{
		Name:        "fleet.device_info",
		Description: "Get detailed information about a remote device: hardware, installed models, and running deployments.",
		InputSchema: schema(`"device_id":{"type":"string","description":"Device ID from fleet.list_devices, e.g. 'gb10', 'mac-m4'"}`, "device_id"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.FleetDeviceInfo == nil {
				return ErrorResult("fleet.device_info not implemented"), nil
			}
			var p struct {
				DeviceID string `json:"device_id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.DeviceID == "" {
				return ErrorResult("device_id is required"), nil
			}
			data, err := deps.FleetDeviceInfo(ctx, p.DeviceID)
			if err != nil {
				return nil, fmt.Errorf("fleet device info %s: %w", p.DeviceID, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// fleet.device_tools
	s.RegisterTool(&Tool{
		Name:        "fleet.device_tools",
		Description: "List the MCP tools available on a specific remote device.",
		InputSchema: schema(`"device_id":{"type":"string","description":"Device ID from fleet.list_devices, e.g. 'gb10'"}`, "device_id"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.FleetDeviceTools == nil {
				return ErrorResult("fleet.device_tools not implemented"), nil
			}
			var p struct {
				DeviceID string `json:"device_id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.DeviceID == "" {
				return ErrorResult("device_id is required"), nil
			}
			data, err := deps.FleetDeviceTools(ctx, p.DeviceID)
			if err != nil {
				return nil, fmt.Errorf("fleet device tools %s: %w", p.DeviceID, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// fleet.exec_tool
	s.RegisterTool(&Tool{
		Name:        "fleet.exec_tool",
		Description: "Execute any MCP tool on a remote fleet device. Agent guardrails apply to the inner tool_name.",
		InputSchema: schema(
			`"device_id":{"type":"string","description":"Device ID from fleet.list_devices, e.g. 'gb10', 'linux-1'. Call fleet.list_devices first if unsure."},`+
				`"tool_name":{"type":"string","description":"MCP tool name to execute remotely, e.g. 'hardware.detect', 'model.list', 'deploy.status'. Call fleet.device_tools first to see available tools."},`+
				`"params":{"type":"object","description":"Tool parameters as a JSON object. Omit or pass {} if the tool takes no parameters. Example: {\"name\": \"aima-vllm-qwen3-0-6b\"} for deploy.status."}`,
			"device_id", "tool_name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.FleetExecTool == nil {
				return ErrorResult("fleet.exec_tool not implemented"), nil
			}
			var p struct {
				DeviceID string          `json:"device_id"`
				ToolName string          `json:"tool_name"`
				Params   json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.DeviceID == "" || p.ToolName == "" {
				return ErrorResult("device_id and tool_name are required"), nil
			}
			data, err := deps.FleetExecTool(ctx, p.DeviceID, p.ToolName, p.Params)
			if err != nil {
				return nil, fmt.Errorf("fleet exec %s on %s: %w", p.ToolName, p.DeviceID, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// --- Power History (F4) ---

	s.RegisterTool(&Tool{
		Name:        "device.power_history",
		Description: "Query historical GPU power, temperature, and utilization samples over time.",
		InputSchema: schema(
			`"from":{"type":"string","description":"Start time (ISO 8601 or 'now-1h', 'now-6h', 'now-24h')"},`+
				`"to":{"type":"string","description":"End time (ISO 8601 or 'now')"}`,
		),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.PowerHistory == nil {
				return ErrorResult("power history not available"), nil
			}
			data, err := deps.PowerHistory(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("device.power_history: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// S3: Power mode
	s.RegisterTool(&Tool{
		Name:        "device.power_mode",
		Description: "List available power modes and current mode. Switch between performance/balanced/powersave.",
		InputSchema: schema(
			`"action":{"type":"string","description":"Action: get (default) or set","enum":["get","set"]},`+
				`"mode":{"type":"string","description":"Power mode to set (for set action)"}`,
		),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.PowerMode == nil {
				return ErrorResult("power mode not available"), nil
			}
			data, err := deps.PowerMode(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("device.power_mode: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// openclaw.sync
	s.RegisterTool(&Tool{
		Name:        "openclaw.sync",
		Description: "Sync AIMA deployed models to OpenClaw config. Categorizes by modality, writes OpenClaw providers, and manages the local AIMA MCP server entry.",
		InputSchema: schema(`"dry_run":{"type":"boolean","description":"If true, preview changes without writing to openclaw.json (default false)"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.OpenClawSync == nil {
				return ErrorResult("openclaw.sync not available"), nil
			}
			var p struct {
				DryRun bool `json:"dry_run"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return ErrorResult("invalid params: " + err.Error()), nil
				}
			}
			data, err := deps.OpenClawSync(ctx, p.DryRun)
			if err != nil {
				return nil, fmt.Errorf("openclaw sync: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// openclaw.status
	s.RegisterTool(&Tool{
		Name:        "openclaw.status",
		Description: "Inspect the current OpenClaw integration state, including local gateway reachability, config presence, MCP server registration, and AIMA sync drift.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.OpenClawStatus == nil {
				return ErrorResult("openclaw.status not available"), nil
			}
			data, err := deps.OpenClawStatus(ctx)
			if err != nil {
				return nil, fmt.Errorf("openclaw status: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// openclaw.claim
	s.RegisterTool(&Tool{
		Name:        "openclaw.claim",
		Description: "Explicitly claim legacy OpenClaw config that already points at the local AIMA proxy, migrating it into AIMA-managed ownership state.",
		InputSchema: schema(`"dry_run":{"type":"boolean","description":"If true, preview the claim result without writing managed state (default false)"},"sections":{"type":"array","items":{"type":"string"},"description":"Optional claim sections: llm, asr, vision, tts, image_gen. Default claims all detectable sections."}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.OpenClawClaim == nil {
				return ErrorResult("openclaw.claim not available"), nil
			}
			var p struct {
				DryRun   bool     `json:"dry_run"`
				Sections []string `json:"sections"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return ErrorResult("invalid params: " + err.Error()), nil
				}
			}
			data, err := deps.OpenClawClaim(ctx, p.Sections, p.DryRun)
			if err != nil {
				return nil, fmt.Errorf("openclaw claim: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// scenario.list
	s.RegisterTool(&Tool{
		Name:        "scenario.list",
		Description: "List available deployment scenarios from the catalog. Each scenario is a pre-defined multi-model deployment recipe.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ScenarioList == nil {
				return ErrorResult("scenario.list not available"), nil
			}
			data, err := deps.ScenarioList(ctx)
			if err != nil {
				return nil, fmt.Errorf("scenario list: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// scenario.show
	s.RegisterTool(&Tool{
		Name:        "scenario.show",
		Description: "Show full details of a deployment scenario including deployments, memory budget, startup order, alternative configs, integrations, verification results, and open questions.",
		InputSchema: schema(
			`"name":{"type":"string","description":"Scenario name, e.g. 'openclaw-multi'. Call scenario.list to see available scenarios."}`,
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
			`"name":{"type":"string","description":"Scenario name, e.g. 'openclaw-multi'. Call scenario.list to see available scenarios."},`+
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

	// D4: App management
	s.RegisterTool(&Tool{
		Name:        "app.register",
		Description: "Register an application with its inference dependency declarations (LLM, embedding, rerank, etc.).",
		InputSchema: schema(
			`"name":{"type":"string","description":"App name"},`+
				`"inference_needs":{"type":"array","description":"Array of inference needs","items":{"type":"object","properties":{"type":{"type":"string"},"model":{"type":"string"},"required":{"type":"boolean"},"performance":{"type":"string"}}}},`+
				`"resource_budget":{"type":"object","description":"Resource budget","properties":{"cpu_cores":{"type":"integer"},"memory_mb":{"type":"integer"}}},`+
				`"time_constraints":{"type":"object","description":"Time constraints","properties":{"max_cold_start_s":{"type":"number"}}}`,
			"name", "inference_needs"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.AppRegister == nil {
				return ErrorResult("app register not available"), nil
			}
			data, err := deps.AppRegister(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("app.register: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name:        "app.provision",
		Description: "Auto-deploy all required inference services for a registered app.",
		InputSchema: schema(
			`"name":{"type":"string","description":"App name to provision"}`,
			"name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.AppProvision == nil {
				return ErrorResult("app provision not available"), nil
			}
			data, err := deps.AppProvision(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("app.provision: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name:        "app.list",
		Description: "List all registered apps with their dependency satisfaction status.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.AppList == nil {
				return ErrorResult("app list not available"), nil
			}
			data, err := deps.AppList(ctx)
			if err != nil {
				return nil, fmt.Errorf("app.list: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})
}
