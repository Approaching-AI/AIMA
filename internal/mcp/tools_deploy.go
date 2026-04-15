package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerDeployTools(s *Server, deps *ToolDeps) {
	// deploy.apply
	s.RegisterTool(&Tool{
		Name:        "deploy.apply",
		Description: "Deploy a model as an inference service. Auto-detects hardware, resolves optimal config, creates K3S Pod or native process. Returns NEEDS_APPROVAL — present the plan to the user, then call deploy.approve with the approval ID.",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model to deploy, e.g. 'qwen3-0.6b'. Call model.list to verify it is available locally."},`+
				`"engine":{"type":"string","description":"Engine type, e.g. 'vllm', 'llamacpp'. Omit to auto-select the best engine for this hardware."},`+
				`"slot":{"type":"string","description":"Partition slot for multi-model deployment, e.g. 'slot-0'. Omit for default full-device allocation."},`+
				`"config":{"type":"object","description":"Engine config overrides, e.g. {\"gpu_memory_utilization\": 0.9, \"max_model_len\": 131072, \"tensor_parallel_size\": 2}"},`+
				`"max_cold_start_s":{"type":"integer","description":"Maximum acceptable cold start time in seconds. Engines exceeding this are excluded from auto-selection. 0 or omitted means no constraint."},`+
				`"auto_pull":{"type":"boolean","description":"Whether to auto-download missing models/engine images. Defaults to true. Set false to fail fast if resources are not locally available."},`+
				`"no_pull":{"type":"boolean","description":"Alias for auto_pull=false. Require all model/engine assets to already exist locally."}`,
			"model"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeployApply == nil {
				return ErrorResult("deploy.apply not implemented"), nil
			}
			var p struct {
				Model         string         `json:"model"`
				Engine        string         `json:"engine"`
				Slot          string         `json:"slot"`
				Config        map[string]any `json:"config"`
				MaxColdStartS int            `json:"max_cold_start_s"`
				AutoPull      *bool          `json:"auto_pull"`
				NoPull        bool           `json:"no_pull"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Model == "" {
				return ErrorResult("model is required"), nil
			}
			if p.MaxColdStartS > 0 {
				if p.Config == nil {
					p.Config = map[string]any{}
				}
				p.Config["max_cold_start_s"] = p.MaxColdStartS
			}
			noPull := p.NoPull || (p.AutoPull != nil && !*p.AutoPull)
			data, err := deps.DeployApply(ctx, p.Engine, p.Model, p.Slot, p.Config, noPull)
			if err != nil {
				return nil, fmt.Errorf("deploy apply %s: %w", p.Model, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// deploy.run
	s.RegisterTool(&Tool{
		Name:        "deploy.run",
		Description: "One-step: resolve config, pull engine and model if needed, deploy, and wait for the service to be ready. Combines deploy.dry_run + engine.pull + model.pull + deploy.apply + deploy.status polling. Returns when the service is ready or after timeout.",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model to deploy, e.g. 'qwen3-8b'."},`+
				`"engine":{"type":"string","description":"Engine type override. Omit to auto-select."},`+
				`"slot":{"type":"string","description":"Partition slot name. Omit for default."},`+
				`"config":{"type":"object","description":"Engine config overrides, e.g. {\"gpu_memory_utilization\": 0.9, \"max_model_len\": 131072}"},`+
				`"max_cold_start_s":{"type":"integer","description":"Maximum acceptable cold start time in seconds. Engines exceeding this are excluded from auto-selection."},`+
				`"no_pull":{"type":"boolean","description":"Skip auto-downloading missing engine/model. Default false."}`,
			"model"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeployRun == nil {
				return ErrorResult("deploy.run not implemented"), nil
			}
			var p struct {
				Model         string         `json:"model"`
				Engine        string         `json:"engine"`
				Slot          string         `json:"slot"`
				Config        map[string]any `json:"config"`
				MaxColdStartS int            `json:"max_cold_start_s"`
				NoPull        bool           `json:"no_pull"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Model == "" {
				return ErrorResult("model is required"), nil
			}
			if p.MaxColdStartS > 0 {
				if p.Config == nil {
					p.Config = map[string]any{}
				}
				p.Config["max_cold_start_s"] = p.MaxColdStartS
			}
			// MCP has no streaming; pass nil for progress callbacks.
			data, err := deps.DeployRun(ctx, p.Model, p.Engine, p.Slot, p.Config, p.NoPull, nil, nil, nil)
			if err != nil {
				return nil, fmt.Errorf("deploy run %s: %w", p.Model, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// deploy.dry_run
	s.RegisterTool(&Tool{
		Name:        "deploy.dry_run",
		Description: "Preview a deployment without executing it. Returns resolved config, hardware fitness report, generated Pod YAML, and warnings. No side effects. Set output=pod_yaml to get only the generated Pod YAML manifest.",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model to deploy, e.g. 'qwen3-0.6b'"},`+
				`"engine":{"type":"string","description":"Engine type, e.g. 'vllm', 'llamacpp'. Omit to auto-select."},`+
				`"slot":{"type":"string","description":"Partition slot for multi-model, e.g. 'slot-0'. Omit for default."},`+
				`"config":{"type":"object","description":"Engine config overrides, e.g. {\"gpu_memory_utilization\": 0.9}"},`+
				`"max_cold_start_s":{"type":"integer","description":"Maximum acceptable cold start time in seconds. Engines exceeding this are excluded from auto-selection."},`+
				`"output":{"type":"string","enum":["","pod_yaml"],"description":"Output format: omit for full dry-run report, 'pod_yaml' for K3S Pod YAML manifest only"}`,
			"model"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Model         string         `json:"model"`
				Engine        string         `json:"engine"`
				Slot          string         `json:"slot"`
				Config        map[string]any `json:"config"`
				MaxColdStartS int            `json:"max_cold_start_s"`
				Output        string         `json:"output"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Model == "" {
				return ErrorResult("model is required"), nil
			}
			if p.MaxColdStartS > 0 {
				if p.Config == nil {
					p.Config = map[string]any{}
				}
				p.Config["max_cold_start_s"] = p.MaxColdStartS
			}

			// output=pod_yaml: call the pod generator with the same effective overrides.
			if p.Output == "pod_yaml" {
				if deps.GeneratePod == nil {
					return ErrorResult("deploy.dry_run pod_yaml not implemented"), nil
				}
				data, err := deps.GeneratePod(ctx, p.Model, p.Engine, p.Slot, p.Config)
				if err != nil {
					return nil, fmt.Errorf("generate pod for %s/%s: %w", p.Model, p.Engine, err)
				}
				return TextResult(string(data)), nil
			}

			if deps.DeployDryRun == nil {
				return ErrorResult("deploy.dry_run not implemented"), nil
			}
			data, err := deps.DeployDryRun(ctx, p.Engine, p.Model, p.Slot, p.Config)
			if err != nil {
				return nil, fmt.Errorf("deploy dry run %s: %w", p.Model, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// deploy.approve
	s.RegisterTool(&Tool{
		Name:        "deploy.approve",
		Description: "Approve and execute a pending deployment. Call only after presenting the plan from deploy.apply to the user and receiving confirmation.",
		InputSchema: schema(`"id":{"type":"integer","description":"Approval ID from the deploy.apply NEEDS_APPROVAL response, e.g. 1"}`, "id"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeployApprove == nil {
				return ErrorResult("deploy.approve not implemented"), nil
			}
			var p struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.ID <= 0 {
				return ErrorResult("id is required (positive integer)"), nil
			}
			data, err := deps.DeployApprove(ctx, p.ID)
			if err != nil {
				return nil, fmt.Errorf("deploy approve %d: %w", p.ID, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// deploy.delete
	s.RegisterTool(&Tool{
		Name:        "deploy.delete",
		Description: "Delete a running deployment (stops the inference service and removes the K3S Pod or native process). This is a destructive operation (a rollback snapshot is created automatically). Blocked for agent-initiated calls.",
		InputSchema: schema(`"name":{"type":"string","description":"Deployment name to delete, e.g. 'aima-vllm-qwen3-0-6b'. Call deploy.list to see active deployments."}`, "name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeployDelete == nil {
				return ErrorResult("deploy.delete not implemented"), nil
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
			if err := deps.DeployDelete(ctx, p.Name); err != nil {
				return nil, fmt.Errorf("deploy delete %s: %w", p.Name, err)
			}
			return TextResult(fmt.Sprintf("deployment %s deleted", p.Name)), nil
		},
	})

	// deploy.status
	s.RegisterTool(&Tool{
		Name:        "deploy.status",
		Description: "Get detailed deployment state for one deployment: model, engine, slot, runtime, ready address, config, startup progress, and failure detail. Accepts deployment name or model name.",
		InputSchema: schema(`"name":{"type":"string","description":"Deployment name (e.g. 'aima-vllm-qwen3-0-6b') or model name (e.g. 'qwen3-0.6b'). Call deploy.list if unsure."}`, "name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeployStatus == nil {
				return ErrorResult("deploy.status not implemented"), nil
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
			data, err := deps.DeployStatus(ctx, p.Name)
			if err != nil {
				return nil, fmt.Errorf("deploy status %s: %w", p.Name, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// deploy.list
	s.RegisterTool(&Tool{
		Name:        "deploy.list",
		Description: "List active deployment overviews on this device: name, model, engine, slot, runtime, phase/status, ready address, and startup summary. Use deploy.status for full per-deployment details.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeployList == nil {
				return ErrorResult("deploy.list not implemented"), nil
			}
			data, err := deps.DeployList(ctx)
			if err != nil {
				return nil, fmt.Errorf("deploy list: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// deploy.logs
	s.RegisterTool(&Tool{
		Name:        "deploy.logs",
		Description: "Get recent log output from a deployment. Accepts deployment name or model name.",
		InputSchema: schema(
			`"name":{"type":"string","description":"Deployment name (e.g. 'aima-vllm-qwen3-0-6b') or model name. Call deploy.list if unsure."},`+
				`"tail":{"type":"integer","description":"Number of log lines to return, e.g. 50. Default: 100."}`,
			"name"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DeployLogs == nil {
				return ErrorResult("deploy.logs not implemented"), nil
			}
			var p struct {
				Name string `json:"name"`
				Tail int    `json:"tail"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Name == "" {
				return ErrorResult("name is required"), nil
			}
			if p.Tail <= 0 {
				p.Tail = 100
			}
			logs, err := deps.DeployLogs(ctx, p.Name, p.Tail)
			if err != nil {
				return nil, fmt.Errorf("deploy logs %s: %w", p.Name, err)
			}
			return TextResult(logs), nil
		},
	})
}
