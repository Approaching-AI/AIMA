package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/mcp"
)

// buildAgentDeps wires patrol.status, patrol.alerts, patrol.config, patrol.actions,
// tuning.start/status/stop/results, explore.start/start_and_wait/status/stop/result,
// and open_questions tools.
func buildAgentDeps(ac *appContext, deps *mcp.ToolDeps,
	patrol *agent.Patrol,
	tuner *agent.Tuner,
	explorationMgr *agent.ExplorationManager,
) {
	db := ac.db

	deps.PatrolStatus = func(ctx context.Context) (json.RawMessage, error) {
		return json.Marshal(patrol.Status())
	}
	deps.PatrolAlerts = func(ctx context.Context) (json.RawMessage, error) {
		alerts := patrol.ActiveAlerts()
		if alerts == nil {
			alerts = []agent.Alert{}
		}
		return json.Marshal(alerts)
	}
	deps.PatrolConfig = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Action string `json:"action"`
			Key    string `json:"key"`
			Value  string `json:"value"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.Action == "get" {
			return json.Marshal(patrol.Config())
		}
		switch p.Key {
		case "interval":
			d, err := time.ParseDuration(p.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid duration: %w", err)
			}
			if d < 0 {
				return nil, fmt.Errorf("interval must be >= 0")
			}
			patrol.SetInterval(d)
		case "gpu_temp_warn":
			v, err := strconv.Atoi(p.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid integer: %w", err)
			}
			if v < 0 {
				return nil, fmt.Errorf("gpu_temp_warn must be >= 0")
			}
			patrol.SetGPUTempWarn(v)
		case "gpu_idle_pct":
			v, err := strconv.Atoi(p.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid integer: %w", err)
			}
			if v < 0 || v > 100 {
				return nil, fmt.Errorf("gpu_idle_pct must be between 0 and 100")
			}
			patrol.SetGPUIdle(v, patrol.Config().GPUIdleMinutes)
		case "gpu_idle_minutes":
			v, err := strconv.Atoi(p.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid integer: %w", err)
			}
			if v < 0 {
				return nil, fmt.Errorf("gpu_idle_minutes must be >= 0")
			}
			patrol.SetGPUIdle(patrol.Config().GPUIdlePct, v)
		case "vram_opportunity_pct":
			v, err := strconv.Atoi(p.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid integer: %w", err)
			}
			if v < 0 || v > 100 {
				return nil, fmt.Errorf("vram_opportunity_pct must be between 0 and 100")
			}
			patrol.SetVRAMOpportunity(v)
		case "self_heal":
			patrol.SetSelfHeal(p.Value == "true" || p.Value == "1")
		default:
			return nil, fmt.Errorf("unknown patrol config key: %s", p.Key)
		}
		return json.Marshal(map[string]string{"status": "updated"})
	}
	deps.PatrolActions = func(ctx context.Context, limit int) (json.RawMessage, error) {
		actions := patrol.RecentActions(limit)
		if actions == nil {
			actions = []agent.PatrolAction{}
		}
		return json.Marshal(actions)
	}
	deps.TuningStart = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var config agent.TuningConfig
		if err := json.Unmarshal(params, &config); err != nil {
			return nil, err
		}
		if config.MaxConfigs == 0 {
			config.MaxConfigs = 20
		}
		// Detach from the MCP request context so the tune goroutine survives
		// after the HTTP response is written. Without this, net/http cancels
		// the request context as soon as TuningStart returns, propagating
		// through Tuner.run and failing progress=0/N before any config runs.
		// Stop() remains the way to cancel a running session.
		session, err := tuner.Start(context.WithoutCancel(ctx), config)
		if err != nil {
			return nil, err
		}
		return json.Marshal(session)
	}
	deps.TuningStatus = func(ctx context.Context) (json.RawMessage, error) {
		s := tuner.CurrentSession()
		if s == nil {
			return json.Marshal(map[string]string{"status": "no session"})
		}
		return json.Marshal(s)
	}
	deps.TuningStop = func(ctx context.Context) (json.RawMessage, error) {
		tuner.Stop()
		return json.Marshal(map[string]string{"status": "stopped"})
	}
	deps.TuningResults = func(ctx context.Context) (json.RawMessage, error) {
		s := tuner.CurrentSession()
		if s == nil {
			return json.Marshal(map[string]string{"status": "no session"})
		}
		return json.Marshal(s)
	}
	deps.ExploreStart = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var req agent.ExplorationStart
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		run, err := explorationMgr.Start(ctx, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(run)
	}
	deps.ExploreStartAndWait = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var req agent.ExplorationStart
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		status, err := explorationMgr.StartAndWait(ctx, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(status)
	}
	deps.ExploreStatus = func(ctx context.Context, runID string) (json.RawMessage, error) {
		status, err := explorationMgr.Status(ctx, runID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(status)
	}
	deps.ExploreStop = func(ctx context.Context, runID string) (json.RawMessage, error) {
		status, err := explorationMgr.Stop(ctx, runID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(status)
	}
	deps.ExploreResult = func(ctx context.Context, runID string) (json.RawMessage, error) {
		result, err := explorationMgr.Result(ctx, runID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	deps.ExploreListRuns = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Status string `json:"status"`
			Kind   string `json:"kind"`
			Limit  int    `json:"limit"`
		}
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse list_runs params: %w", err)
			}
		}
		if p.Limit <= 0 {
			p.Limit = 50
		}
		if p.Limit > 500 {
			p.Limit = 500 // Cap runaway requests; UI pages locally.
		}
		runs, err := db.ListExplorationRuns(ctx, p.Status, p.Limit)
		if err != nil {
			return nil, err
		}
		if p.Kind != "" {
			filtered := runs[:0]
			for _, r := range runs {
				if r.Kind == p.Kind {
					filtered = append(filtered, r)
				}
			}
			runs = filtered
		}
		return json.Marshal(runs)
	}
	deps.ExploreRunDetail = func(ctx context.Context, runID string) (json.RawMessage, error) {
		run, err := db.GetExplorationRun(ctx, runID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(run)
	}
	deps.ExploreRunEvents = func(ctx context.Context, runID string) (json.RawMessage, error) {
		events, err := db.ListExplorationEvents(ctx, runID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(events)
	}
	deps.OpenQuestions = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Action      string `json:"action"`
			Status      string `json:"status"`
			ID          string `json:"id"`
			Result      string `json:"result"`
			Hardware    string `json:"hardware"`
			Model       string `json:"model"`
			Engine      string `json:"engine"`
			Endpoint    string `json:"endpoint"`
			RequestedBy string `json:"requested_by"`
			Concurrency int    `json:"concurrency"`
			Rounds      int    `json:"rounds"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		switch p.Action {
		case "resolve":
			if p.ID == "" {
				return nil, fmt.Errorf("id required for resolve action")
			}
			status := "confirmed"
			if p.Status != "" {
				status = p.Status
			}
			if err := db.ResolveOpenQuestion(ctx, p.ID, status, p.Result, p.Hardware); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]string{"status": "resolved", "id": p.ID})
		case "run", "validate":
			if explorationMgr == nil {
				return nil, fmt.Errorf("exploration manager unavailable")
			}
			if p.ID == "" {
				return nil, fmt.Errorf("id required for %s action", p.Action)
			}
			question, err := db.GetOpenQuestion(ctx, p.ID)
			if err != nil {
				return nil, err
			}
			hardware := p.Hardware
			if hardware == "" {
				hardware = question.Hardware
			}
			requestedBy := p.RequestedBy
			if requestedBy == "" {
				requestedBy = "user"
			}
			run, err := explorationMgr.Start(ctx, agent.ExplorationStart{
				Kind: "open_question",
				Goal: fmt.Sprintf("validate open question: %s", question.Question),
				Target: agent.ExplorationTarget{
					Hardware: hardware,
					Model:    p.Model,
					Engine:   p.Engine,
				},
				RequestedBy:  requestedBy,
				SourceRef:    p.ID,
				ApprovalMode: "none",
				BenchmarkProfiles: []agent.ExplorationBenchmarkProfile{{
					Endpoint:    p.Endpoint,
					Concurrency: p.Concurrency,
					Rounds:      p.Rounds,
				}},
			})
			if err != nil {
				return nil, err
			}
			return json.Marshal(map[string]any{
				"status":   "queued",
				"question": question,
				"run":      run,
			})
		default:
			questions, err := db.ListOpenQuestions(ctx, p.Status)
			if err != nil {
				return nil, err
			}
			if questions == nil {
				questions = []map[string]any{}
			}
			return json.Marshal(questions)
		}
	}
}
