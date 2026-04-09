package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerAgentTools(s *Server, deps *ToolDeps) {
	// support.askforhelp
	s.RegisterTool(&Tool{
		Name:        "support.askforhelp",
		Description: "Connect this AIMA instance to the support service (https://aimaserver.com) as a device, and optionally create a remote help task from a natural-language description.",
		InputSchema: schema(
			`"description":{"type":"string","description":"Optional natural-language request to create a support task immediately"},` +
				`"endpoint":{"type":"string","description":"Optional override for support.endpoint; persisted when provided"},` +
				`"invite_code":{"type":"string","description":"Optional invite code for first-time registration; persisted when provided"},` +
				`"worker_code":{"type":"string","description":"Optional worker enrollment code for first-time registration; persisted when provided"},` +
				`"recovery_code":{"type":"string","description":"Optional saved recovery code used when refreshing an older registration"},` +
				`"referral_code":{"type":"string","description":"Optional referral code for self-service registration"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SupportAskForHelp == nil {
				return ErrorResult("support.askforhelp not implemented"), nil
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

	// agent.guide
	s.RegisterTool(&Tool{
		Name:        "agent.guide",
		Description: "Return the full AIMA Agent Usage Guide with tool parameters, workflow examples, and API reference.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.AgentGuide == nil {
				return ErrorResult("agent.guide not implemented"), nil
			}
			data, err := deps.AgentGuide(ctx)
			if err != nil {
				return nil, fmt.Errorf("agent guide: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// agent.rollback_list
	s.RegisterTool(&Tool{
		Name:        "agent.rollback_list",
		Description: "List available rollback snapshots created before destructive operations.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.RollbackList == nil {
				return ErrorResult("agent.rollback_list not implemented"), nil
			}
			data, err := deps.RollbackList(ctx)
			if err != nil {
				return nil, fmt.Errorf("list rollback snapshots: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// agent.rollback
	s.RegisterTool(&Tool{
		Name:        "agent.rollback",
		Description: "Restore a resource from a rollback snapshot. Blocked for agent-initiated calls.",
		InputSchema: schema(`"id":{"type":"integer","description":"Snapshot ID from agent.rollback_list"}`, "id"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.RollbackRestore == nil {
				return ErrorResult("agent.rollback not implemented"), nil
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
			data, err := deps.RollbackRestore(ctx, p.ID)
			if err != nil {
				return nil, fmt.Errorf("rollback snapshot %d: %w", p.ID, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// --- Patrol & Alerts (A2) ---

	s.RegisterTool(&Tool{
		Name:        "agent.patrol_status",
		Description: "Get patrol loop state: running status, last run time, next run, and alert count.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.PatrolStatus == nil {
				return ErrorResult("patrol not available"), nil
			}
			data, err := deps.PatrolStatus(ctx)
			if err != nil {
				return nil, fmt.Errorf("agent.patrol_status: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name:        "agent.alerts",
		Description: "List active patrol alerts with severity, type, and message.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.PatrolAlerts == nil {
				return ErrorResult("patrol not available"), nil
			}
			data, err := deps.PatrolAlerts(ctx)
			if err != nil {
				return nil, fmt.Errorf("agent.alerts: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name:        "agent.patrol_config",
		Description: "Get or set patrol configuration. Set interval to '0s' to disable patrol. Keys: interval (duration, e.g. '5m'), gpu_temp_warn (int, Celsius), gpu_idle_pct (int, %), gpu_idle_minutes (int), vram_opportunity_pct (int, %), self_heal ('true'/'false').",
		InputSchema: schema(
			`"action":{"type":"string","enum":["get","set"],"description":"'get' to read all config, 'set' to update a key"},`+
				`"key":{"type":"string","enum":["interval","gpu_temp_warn","gpu_idle_pct","gpu_idle_minutes","vram_opportunity_pct","self_heal"],"description":"Config key to set"},`+
				`"value":{"type":"string","description":"New value (only for 'set')"}`,
			"action"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.PatrolConfig == nil {
				return ErrorResult("patrol not available"), nil
			}
			data, err := deps.PatrolConfig(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("agent.patrol_config: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name:        "agent.patrol_actions",
		Description: "List automated actions taken by the patrol loop (self-healing, notifications).",
		InputSchema: schema(
			`"limit":{"type":"integer","description":"Maximum number of actions to return (default 50)"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.PatrolActions == nil {
				return ErrorResult("patrol actions not available"), nil
			}
			var p struct {
				Limit int `json:"limit"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return ErrorResult("invalid params: " + err.Error()), nil
				}
			}
			if p.Limit <= 0 {
				p.Limit = 50
			}
			data, err := deps.PatrolActions(ctx, p.Limit)
			if err != nil {
				return nil, fmt.Errorf("agent.patrol_actions: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// --- Exploration Runner ---

	s.RegisterTool(&Tool{
		Name:        "explore.start",
		Description: "Start a persisted exploration run. Supports tuning, validation, and open-question validation runs.",
		InputSchema: schema(
			`"kind":{"type":"string","enum":["tune","validate","open_question"],"description":"Exploration kind."},`+
				`"goal":{"type":"string","description":"Human-readable objective for the run."},`+
				`"requested_by":{"type":"string","description":"Who requested the run."},`+
				`"executor":{"type":"string","description":"Executor identity. Currently only local_go is supported."},`+
				`"approval_mode":{"type":"string","description":"Approval mode metadata for the run."},`+
				`"source_ref":{"type":"string","description":"Optional source reference such as gap_id, open_question_id, or alert_id."},`+
				`"target":{"type":"object","description":"Exploration target","properties":{"hardware":{"type":"string"},"model":{"type":"string"},"engine":{"type":"string"}}},`+
				`"search_space":{"type":"object","description":"Parameter grid as key -> candidate array."},`+
				`"constraints":{"type":"object","description":"Execution constraints","properties":{"max_candidates":{"type":"integer"}}},`+
				`"benchmark_profile":{"type":"object","description":"Benchmark profile","properties":{"endpoint":{"type":"string"},"concurrency":{"type":"integer"},"rounds":{"type":"integer"}}}`,
			"target"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ExploreStart == nil {
				return ErrorResult("explore.start not implemented"), nil
			}
			data, err := deps.ExploreStart(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("explore.start: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name:        "explore.status",
		Description: "Get current status of an exploration run with events and tuning progress.",
		InputSchema: schema(`"id":{"type":"string","description":"Exploration run ID."}`, "id"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ExploreStatus == nil {
				return ErrorResult("explore.status not implemented"), nil
			}
			var p struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.ID == "" {
				return ErrorResult("id is required"), nil
			}
			data, err := deps.ExploreStatus(ctx, p.ID)
			if err != nil {
				return nil, fmt.Errorf("explore.status: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name:        "explore.stop",
		Description: "Stop a running exploration run.",
		InputSchema: schema(`"id":{"type":"string","description":"Exploration run ID."}`, "id"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ExploreStop == nil {
				return ErrorResult("explore.stop not implemented"), nil
			}
			var p struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.ID == "" {
				return ErrorResult("id is required"), nil
			}
			data, err := deps.ExploreStop(ctx, p.ID)
			if err != nil {
				return nil, fmt.Errorf("explore.stop: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name:        "explore.result",
		Description: "Get the final or current result of an exploration run with events and summaries.",
		InputSchema: schema(`"id":{"type":"string","description":"Exploration run ID."}`, "id"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ExploreResult == nil {
				return ErrorResult("explore.result not implemented"), nil
			}
			var p struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.ID == "" {
				return ErrorResult("id is required"), nil
			}
			data, err := deps.ExploreResult(ctx, p.ID)
			if err != nil {
				return nil, fmt.Errorf("explore.result: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// --- Auto-tuning (A3) ---

	s.RegisterTool(&Tool{
		Name:        "tuning.start",
		Description: "Start an auto-tuning session. Iterates config parameter combinations, benchmarks each, promotes the best.",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model name to tune"},`+
				`"hardware":{"type":"string","description":"Hardware profile used to persist benchmark results."},`+
				`"engine":{"type":"string","description":"Engine type (auto-detect if empty)"},`+
				`"endpoint":{"type":"string","description":"Inference endpoint override for benchmarking."},`+
				`"parameters":{"type":"array","items":{"type":"object"},"description":"Tunable parameter definitions"},`+
				`"max_configs":{"type":"integer","description":"Maximum configs to test (default: 20)"}`,
			"model"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.TuningStart == nil {
				return ErrorResult("tuning not available"), nil
			}
			data, err := deps.TuningStart(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("tuning.start: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name:        "tuning.status",
		Description: "Get current auto-tuning session progress: configs tested, current best throughput, ETA.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.TuningStatus == nil {
				return ErrorResult("tuning not available"), nil
			}
			data, err := deps.TuningStatus(ctx)
			if err != nil {
				return nil, fmt.Errorf("tuning.status: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name:        "tuning.stop",
		Description: "Cancel an ongoing auto-tuning session. The best config found so far is deployed.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.TuningStop == nil {
				return ErrorResult("tuning not available"), nil
			}
			data, err := deps.TuningStop(ctx)
			if err != nil {
				return nil, fmt.Errorf("tuning.stop: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	s.RegisterTool(&Tool{
		Name:        "tuning.results",
		Description: "Get the results of the last completed tuning session: ranked configs with benchmark data.",
		InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.TuningResults == nil {
				return ErrorResult("tuning not available"), nil
			}
			data, err := deps.TuningResults(ctx)
			if err != nil {
				return nil, fmt.Errorf("tuning.results: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})
}
