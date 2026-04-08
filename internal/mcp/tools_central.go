package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerCentralTools(s *Server, deps *ToolDeps) {
	// central.sync — push/pull/status via action param
	s.RegisterTool(&Tool{
		Name:        "central.sync",
		Description: "Sync knowledge with the central server. action=push: push local knowledge to central. action=pull: pull new knowledge from central (includes advisories and scenarios). action=status: show sync status, connectivity, and last push/pull timestamps.",
		InputSchema: schema(
			`"action":{"type":"string","enum":["push","pull","status"],"description":"Sync action to perform"}`,
			"action"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Action string `json:"action"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			switch p.Action {
			case "push":
				if deps.SyncPush == nil {
					return ErrorResult("sync not configured — set central.endpoint and central.api_key first"), nil
				}
				data, err := deps.SyncPush(ctx)
				if err != nil {
					return nil, fmt.Errorf("central.sync push: %w", err)
				}
				return TextResult(string(data)), nil
			case "pull":
				if deps.SyncPull == nil {
					return ErrorResult("sync not configured — set central.endpoint and central.api_key first"), nil
				}
				data, err := deps.SyncPull(ctx)
				if err != nil {
					return nil, fmt.Errorf("central.sync pull: %w", err)
				}
				return TextResult(string(data)), nil
			case "status":
				if deps.SyncStatus == nil {
					return ErrorResult("sync not configured — set central.endpoint and central.api_key first"), nil
				}
				data, err := deps.SyncStatus(ctx)
				if err != nil {
					return nil, fmt.Errorf("central.sync status: %w", err)
				}
				return TextResult(string(data)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown action %q; supported: push, pull, status", p.Action)), nil
			}
		},
	})

	// central.advise — request/feedback via action param
	s.RegisterTool(&Tool{
		Name:        "central.advise",
		Description: "Central server advisory. action=request: request an AI-powered engine/config recommendation for a model. action=feedback: send feedback on a received advisory after local validation.",
		InputSchema: schema(
			`"action":{"type":"string","enum":["request","feedback"],"description":"Advisory action"},`+
				`"model":{"type":"string","description":"Model name to get advice for, e.g. 'qwen3-8b' (for request)"},`+
				`"engine":{"type":"string","description":"Engine type, e.g. 'vllm'. Omit to get a recommendation (for request)."},`+
				`"intent":{"type":"string","description":"Optimization intent: 'low-latency', 'high-throughput', 'balanced' (for request)"},`+
				`"advisory_id":{"type":"string","description":"Advisory ID from central server (for feedback)"},`+
				`"status":{"type":"string","enum":["validated","rejected","accepted"],"description":"Feedback status; validated is preferred (for feedback)"},`+
				`"reason":{"type":"string","description":"Explanation of why advisory was validated or rejected (for feedback)"}`,
			"action"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Action     string `json:"action"`
				Model      string `json:"model"`
				Engine     string `json:"engine"`
				Intent     string `json:"intent"`
				AdvisoryID string `json:"advisory_id"`
				Status     string `json:"status"`
				Reason     string `json:"reason"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			switch p.Action {
			case "request":
				if deps.RequestAdvise == nil {
					return ErrorResult("central advise not configured — set central.endpoint first"), nil
				}
				if p.Model == "" {
					return ErrorResult("model is required for action=request"), nil
				}
				data, err := deps.RequestAdvise(ctx, p.Model, p.Engine, p.Intent)
				if err != nil {
					return nil, fmt.Errorf("central.advise request for %s: %w", p.Model, err)
				}
				return TextResult(string(data)), nil
			case "feedback":
				if deps.AdvisoryFeedback == nil {
					return ErrorResult("advisory feedback not configured — set central.endpoint first"), nil
				}
				if p.AdvisoryID == "" || p.Status == "" {
					return ErrorResult("advisory_id and status are required for action=feedback"), nil
				}
				data, err := deps.AdvisoryFeedback(ctx, p.AdvisoryID, p.Status, p.Reason)
				if err != nil {
					return nil, fmt.Errorf("central.advise feedback for %s: %w", p.AdvisoryID, err)
				}
				return TextResult(string(data)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown action %q; supported: request, feedback", p.Action)), nil
			}
		},
	})

	// central.scenario — generate/list via action param
	s.RegisterTool(&Tool{
		Name:        "central.scenario",
		Description: "Central server scenarios. action=generate: request the central server to generate an AI-powered multi-model deployment scenario. action=list: list deployment scenarios from the central server filtered by hardware or source.",
		InputSchema: schema(
			`"action":{"type":"string","enum":["generate","list"],"description":"Scenario action"},`+
				`"hardware":{"type":"string","description":"Hardware profile, e.g. 'nvidia-gb10-arm64' (required for generate, optional filter for list)"},`+
				`"models":{"type":"array","items":{"type":"string"},"description":"Model names to include, e.g. ['qwen3-8b','glm-4.7-flash'] (required for generate)"},`+
				`"goal":{"type":"string","description":"Optimization goal: 'balanced', 'low-latency', 'maximize-models' (for generate)"},`+
				`"source":{"type":"string","description":"Filter by source: 'advisor', 'user', 'analyzer' (for list)"}`,
			"action"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Action   string   `json:"action"`
				Hardware string   `json:"hardware"`
				Models   []string `json:"models"`
				Goal     string   `json:"goal"`
				Source   string   `json:"source"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			switch p.Action {
			case "generate":
				if deps.RequestScenario == nil {
					return ErrorResult("scenario.generate not configured — set central.endpoint first"), nil
				}
				if p.Hardware == "" || len(p.Models) == 0 {
					return ErrorResult("hardware and models are required for action=generate"), nil
				}
				data, err := deps.RequestScenario(ctx, p.Hardware, p.Models, p.Goal)
				if err != nil {
					return nil, fmt.Errorf("central.scenario generate for %s: %w", p.Hardware, err)
				}
				return TextResult(string(data)), nil
			case "list":
				if deps.ListCentralScenarios == nil {
					return ErrorResult("scenario.list_central not configured — set central.endpoint first"), nil
				}
				data, err := deps.ListCentralScenarios(ctx, p.Hardware, p.Source)
				if err != nil {
					return nil, fmt.Errorf("central.scenario list: %w", err)
				}
				return TextResult(string(data)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown action %q; supported: generate, list", p.Action)), nil
			}
		},
	})
}
