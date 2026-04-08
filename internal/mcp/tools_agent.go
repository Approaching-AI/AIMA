package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerAgentTools(s *Server, deps *ToolDeps) {
	// support — renamed from support.askforhelp
	s.RegisterTool(&Tool{
		Name:        "support",
		Description: "Connect this AIMA instance to the support service (https://aimaserver.com/platform) as a device, and optionally create a remote help task from a natural-language description.",
		InputSchema: schema(
			`"description":{"type":"string","description":"Optional natural-language request to create a support task immediately"},` +
				`"endpoint":{"type":"string","description":"Optional override for support.endpoint; persisted when provided"},` +
				`"invite_code":{"type":"string","description":"Optional invite code for first-time registration; persisted when provided"},` +
				`"worker_code":{"type":"string","description":"Optional worker enrollment code for first-time registration; persisted when provided"},` +
				`"recovery_code":{"type":"string","description":"Optional saved recovery code used when refreshing an older registration"},` +
				`"referral_code":{"type":"string","description":"Optional referral code for self-service registration"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SupportAskForHelp == nil {
				return ErrorResult("support not implemented"), nil
			}
			var p struct {
				Description  string `json:"description"`
				Endpoint     string `json:"endpoint"`
				InviteCode   string `json:"invite_code"`
				WorkerCode   string `json:"worker_code"`
				RecoveryCode string `json:"recovery_code"`
				ReferralCode string `json:"referral_code"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return nil, fmt.Errorf("parse params: %w", err)
				}
			}
			data, err := deps.SupportAskForHelp(ctx, p.Description, p.Endpoint, p.InviteCode, p.WorkerCode, p.RecoveryCode, p.ReferralCode)
			if err != nil {
				return nil, fmt.Errorf("support askforhelp: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// agent.ask
	s.RegisterTool(&Tool{
		Name:        "agent.ask",
		Description: "Route a natural language query through the Go Agent (L3a). Returns the agent's response and a session_id for multi-turn conversations. Blocked for agent-initiated calls (prevents recursive invocation).",
		InputSchema: schema(
			`"query":{"type":"string","description":"The question to ask"},`+
				`"dangerously_skip_permissions":{"type":"boolean","description":"Skip deploy approval gate (use with caution)"},`+
				`"session_id":{"type":"string","description":"Session ID to continue a conversation"}`,
			"query"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.DispatchAsk == nil {
				return ErrorResult("agent.ask not implemented"), nil
			}
			var p struct {
				Query     string `json:"query"`
				SkipPerms bool   `json:"dangerously_skip_permissions"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Query == "" {
				return ErrorResult("query is required"), nil
			}
			data, sid, err := deps.DispatchAsk(ctx, p.Query, p.SkipPerms, p.SessionID)
			if err != nil {
				return nil, fmt.Errorf("agent ask: %w", err)
			}
			// Merge session_id into the response
			var resp map[string]any
			if err := json.Unmarshal(data, &resp); err != nil {
				resp = map[string]any{"result": string(data)}
			}
			if sid != "" {
				resp["session_id"] = sid
			}
			merged, _ := json.Marshal(resp)
			return TextResult(string(merged)), nil
		},
	})

	// agent.status
	s.RegisterTool(&Tool{
		Name:        "agent.status",
		Description: "Check agent subsystem availability and routing: whether L3a (Go Agent) is healthy, which endpoint/model is selected, and what fallback candidates exist.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.AgentStatus == nil {
				return ErrorResult("agent.status not implemented"), nil
			}
			data, err := deps.AgentStatus(ctx)
			if err != nil {
				return nil, fmt.Errorf("agent status: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// agent.rollback — merge rollback_list + rollback via action param (list/restore)
	s.RegisterTool(&Tool{
		Name:        "agent.rollback",
		Description: "Rollback snapshot management. action=list: list available rollback snapshots created before destructive operations. action=restore: restore a resource from a rollback snapshot (blocked for agent-initiated calls).",
		InputSchema: schema(
			`"action":{"type":"string","enum":["list","restore"],"description":"Rollback action"},`+
				`"id":{"type":"integer","description":"Snapshot ID from action=list (required for action=restore)"}`,
			"action"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Action string `json:"action"`
				ID     int64  `json:"id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			switch p.Action {
			case "list":
				if deps.RollbackList == nil {
					return ErrorResult("agent.rollback list not implemented"), nil
				}
				data, err := deps.RollbackList(ctx)
				if err != nil {
					return nil, fmt.Errorf("list rollback snapshots: %w", err)
				}
				return TextResult(string(data)), nil
			case "restore":
				if deps.RollbackRestore == nil {
					return ErrorResult("agent.rollback restore not implemented"), nil
				}
				if p.ID <= 0 {
					return ErrorResult("id is required (positive integer) for action=restore"), nil
				}
				data, err := deps.RollbackRestore(ctx, p.ID)
				if err != nil {
					return nil, fmt.Errorf("rollback snapshot %d: %w", p.ID, err)
				}
				return TextResult(string(data)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown action %q; supported: list, restore", p.Action)), nil
			}
		},
	})
}
