package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerAutomationTools(s *Server, deps *ToolDeps) {
	// patrol — status/alerts/config/actions via action param
	s.RegisterTool(&Tool{
		Name:        "patrol",
		Description: "Patrol loop management. action=status: get patrol loop state (running status, last run, next run, alert count). action=alerts: list active patrol alerts with severity and message. action=config: get or set patrol configuration (interval, thresholds, self_heal). action=actions: list automated actions taken by the patrol loop.",
		InputSchema: schema(
			`"action":{"type":"string","enum":["status","alerts","config","actions"],"description":"Patrol action"},`+
				`"config_action":{"type":"string","enum":["get","set"],"description":"Config sub-action (for action=config)"},`+
				`"key":{"type":"string","enum":["interval","gpu_temp_warn","gpu_idle_pct","gpu_idle_minutes","vram_opportunity_pct","self_heal"],"description":"Config key (for action=config, config_action=set)"},`+
				`"value":{"type":"string","description":"New config value (for action=config, config_action=set)"},`+
				`"limit":{"type":"integer","description":"Max actions to return (for action=actions, default 50)"}`,
			"action"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Action string `json:"action"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			switch p.Action {
			case "status":
				if deps.PatrolStatus == nil {
					return ErrorResult("patrol not available"), nil
				}
				data, err := deps.PatrolStatus(ctx)
				if err != nil {
					return nil, fmt.Errorf("patrol status: %w", err)
				}
				return TextResult(string(data)), nil
			case "alerts":
				if deps.PatrolAlerts == nil {
					return ErrorResult("patrol not available"), nil
				}
				data, err := deps.PatrolAlerts(ctx)
				if err != nil {
					return nil, fmt.Errorf("patrol alerts: %w", err)
				}
				return TextResult(string(data)), nil
			case "config":
				if deps.PatrolConfig == nil {
					return ErrorResult("patrol not available"), nil
				}
				// Build config params: remap config_action → action for the underlying dep
				var rawP map[string]json.RawMessage
				if err := json.Unmarshal(params, &rawP); err != nil {
					return nil, fmt.Errorf("parse params: %w", err)
				}
				// The underlying PatrolConfig expects action=get|set
				configParams := map[string]json.RawMessage{}
				if ca, ok := rawP["config_action"]; ok {
					configParams["action"] = ca
				} else {
					configParams["action"] = json.RawMessage(`"get"`)
				}
				if k, ok := rawP["key"]; ok {
					configParams["key"] = k
				}
				if v, ok := rawP["value"]; ok {
					configParams["value"] = v
				}
				innerParams, _ := json.Marshal(configParams)
				data, err := deps.PatrolConfig(ctx, innerParams)
				if err != nil {
					return nil, fmt.Errorf("patrol config: %w", err)
				}
				return TextResult(string(data)), nil
			case "actions":
				if deps.PatrolActions == nil {
					return ErrorResult("patrol actions not available"), nil
				}
				if p.Limit <= 0 {
					p.Limit = 50
				}
				data, err := deps.PatrolActions(ctx, p.Limit)
				if err != nil {
					return nil, fmt.Errorf("patrol actions: %w", err)
				}
				return TextResult(string(data)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown action %q; supported: status, alerts, config, actions", p.Action)), nil
			}
		},
	})

	// explore — start/status/stop/result via action param
	s.RegisterTool(&Tool{
		Name:        "explore",
		Description: "Exploration run management. action=start: start a persisted exploration run (tune, validate, open_question). action=status: get current status with events and progress. action=stop: stop a running exploration run. action=result: get final or current result with events and summaries.",
		InputSchema: schema(
			`"action":{"type":"string","enum":["start","status","stop","result"],"description":"Explore action"},`+
				`"id":{"type":"string","description":"Exploration run ID (required for status, stop, result)"},`+
				`"kind":{"type":"string","enum":["tune","validate","open_question"],"description":"Exploration kind (for start)"},`+
				`"goal":{"type":"string","description":"Human-readable objective (for start)"},`+
				`"requested_by":{"type":"string","description":"Who requested the run (for start)"},`+
				`"executor":{"type":"string","description":"Executor identity, default local_go (for start)"},`+
				`"approval_mode":{"type":"string","description":"Approval mode metadata (for start)"},`+
				`"source_ref":{"type":"string","description":"Optional source reference such as gap_id or alert_id (for start)"},`+
				`"target":{"type":"object","description":"Exploration target with hardware, model, engine (for start)"},`+
				`"search_space":{"type":"object","description":"Parameter grid as key -> candidate array (for start)"},`+
				`"constraints":{"type":"object","description":"Execution constraints with max_candidates (for start)"},`+
				`"benchmark_profile":{"type":"object","description":"Benchmark profile with endpoint, concurrency, rounds (for start)"}`,
			"action"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Action string `json:"action"`
				ID     string `json:"id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
				switch p.Action {
				case "start":
					if deps.ExploreStart == nil {
						return ErrorResult("explore action=start not implemented"), nil
					}
				data, err := deps.ExploreStart(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("explore start: %w", err)
				}
				return TextResult(string(data)), nil
				case "status":
					if deps.ExploreStatus == nil {
						return ErrorResult("explore action=status not implemented"), nil
					}
				if p.ID == "" {
					return ErrorResult("id is required for action=status"), nil
				}
				data, err := deps.ExploreStatus(ctx, p.ID)
				if err != nil {
					return nil, fmt.Errorf("explore status: %w", err)
				}
				return TextResult(string(data)), nil
				case "stop":
					if deps.ExploreStop == nil {
						return ErrorResult("explore action=stop not implemented"), nil
					}
				if p.ID == "" {
					return ErrorResult("id is required for action=stop"), nil
				}
				data, err := deps.ExploreStop(ctx, p.ID)
				if err != nil {
					return nil, fmt.Errorf("explore stop: %w", err)
				}
				return TextResult(string(data)), nil
				case "result":
					if deps.ExploreResult == nil {
						return ErrorResult("explore action=result not implemented"), nil
					}
				if p.ID == "" {
					return ErrorResult("id is required for action=result"), nil
				}
				data, err := deps.ExploreResult(ctx, p.ID)
				if err != nil {
					return nil, fmt.Errorf("explore result: %w", err)
				}
				return TextResult(string(data)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown action %q; supported: start, status, stop, result", p.Action)), nil
			}
		},
	})

	// tuning — start/status/stop/results via action param
	s.RegisterTool(&Tool{
		Name:        "tuning",
		Description: "Auto-tuning session management. action=start: start an auto-tuning session (iterates config combos, benchmarks, promotes best). action=status: get session progress (configs tested, current best, ETA). action=stop: cancel the ongoing session (best config found so far is deployed). action=results: get results of the last completed session.",
		InputSchema: schema(
			`"action":{"type":"string","enum":["start","status","stop","results"],"description":"Tuning action"},`+
				`"model":{"type":"string","description":"Model name to tune (required for start)"},`+
				`"hardware":{"type":"string","description":"Hardware profile for benchmark persistence (for start)"},`+
				`"engine":{"type":"string","description":"Engine type, auto-detect if empty (for start)"},`+
				`"endpoint":{"type":"string","description":"Inference endpoint override for benchmarking (for start)"},`+
				`"parameters":{"type":"array","items":{"type":"object"},"description":"Tunable parameter definitions (for start)"},`+
				`"max_configs":{"type":"integer","description":"Maximum configs to test, default 20 (for start)"}`,
			"action"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Action string `json:"action"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			switch p.Action {
			case "start":
				if deps.TuningStart == nil {
					return ErrorResult("tuning not available"), nil
				}
				data, err := deps.TuningStart(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("tuning start: %w", err)
				}
				return TextResult(string(data)), nil
			case "status":
				if deps.TuningStatus == nil {
					return ErrorResult("tuning not available"), nil
				}
				data, err := deps.TuningStatus(ctx)
				if err != nil {
					return nil, fmt.Errorf("tuning status: %w", err)
				}
				return TextResult(string(data)), nil
			case "stop":
				if deps.TuningStop == nil {
					return ErrorResult("tuning not available"), nil
				}
				data, err := deps.TuningStop(ctx)
				if err != nil {
					return nil, fmt.Errorf("tuning stop: %w", err)
				}
				return TextResult(string(data)), nil
			case "results":
				if deps.TuningResults == nil {
					return ErrorResult("tuning not available"), nil
				}
				data, err := deps.TuningResults(ctx)
				if err != nil {
					return nil, fmt.Errorf("tuning results: %w", err)
				}
				return TextResult(string(data)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown action %q; supported: start, status, stop, results", p.Action)), nil
			}
		},
	})

	// explorer — status/config/trigger via action param
	s.RegisterTool(&Tool{
		Name:        "explorer",
		Description: "Autonomous Explorer management. action=status: show current state (tier, active plan, schedule config, last run). action=config: get or update Explorer schedule configuration. action=trigger: manually trigger an Explorer gap scan cycle.",
		InputSchema: schema(
			`"action":{"type":"string","enum":["status","config","trigger"],"description":"Explorer action"},`+
				`"config_action":{"type":"string","enum":["get","set"],"description":"Config sub-action (for action=config)"},`+
				`"key":{"type":"string","description":"Config key: gap_scan_interval, sync_interval, full_audit_interval, quiet_start, quiet_end, max_concurrent_runs, enabled (for action=config, config_action=set)"},`+
				`"value":{"type":"string","description":"New value (for action=config, config_action=set)"}`,
			"action"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Action string `json:"action"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
				switch p.Action {
				case "status":
					if deps.ExplorerStatus == nil {
						return ErrorResult("explorer action=status not implemented"), nil
					}
				data, err := deps.ExplorerStatus(ctx)
				if err != nil {
					return nil, fmt.Errorf("explorer status: %w", err)
				}
				return TextResult(string(data)), nil
				case "config":
					if deps.ExplorerConfig == nil {
						return ErrorResult("explorer action=config not implemented"), nil
					}
				// Remap config_action → action for the underlying dep
				var rawP map[string]json.RawMessage
				if err := json.Unmarshal(params, &rawP); err != nil {
					return nil, fmt.Errorf("parse params: %w", err)
				}
				configParams := map[string]json.RawMessage{}
				if ca, ok := rawP["config_action"]; ok {
					configParams["action"] = ca
				} else {
					configParams["action"] = json.RawMessage(`"get"`)
				}
				if k, ok := rawP["key"]; ok {
					configParams["key"] = k
				}
				if v, ok := rawP["value"]; ok {
					configParams["value"] = v
				}
				innerParams, _ := json.Marshal(configParams)
				data, err := deps.ExplorerConfig(ctx, innerParams)
				if err != nil {
					return nil, fmt.Errorf("explorer config: %w", err)
				}
				return TextResult(string(data)), nil
				case "trigger":
					if deps.ExplorerTrigger == nil {
						return ErrorResult("explorer action=trigger not implemented"), nil
					}
				data, err := deps.ExplorerTrigger(ctx)
				if err != nil {
					return nil, fmt.Errorf("explorer trigger: %w", err)
				}
				return TextResult(string(data)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown action %q; supported: status, config, trigger", p.Action)), nil
			}
		},
	})
}
