package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"time"

	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
)

type scenarioDeployResult struct {
	Model  string          `json:"model"`
	Engine string          `json:"engine"`
	Status string          `json:"status"`
	Error  string          `json:"error,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

type orderedScenarioDeploy struct {
	deployment knowledge.ScenarioDeployment
	waitFor    string
	timeoutS   int
}

func applyScenario(ctx context.Context, cat *knowledge.Catalog, rtName string, deps *mcp.ToolDeps, name string, dryRun bool) (json.RawMessage, error) {
	var scenario *knowledge.DeploymentScenario
	for i := range cat.DeploymentScenarios {
		if cat.DeploymentScenarios[i].Metadata.Name == name {
			scenario = &cat.DeploymentScenarios[i]
			break
		}
	}
	if scenario == nil {
		names := make([]string, 0, len(cat.DeploymentScenarios))
		for _, ds := range cat.DeploymentScenarios {
			names = append(names, ds.Metadata.Name)
		}
		return nil, fmt.Errorf("scenario %q not found (available: %v)", name, names)
	}

	var results []scenarioDeployResult

	var hwWarning string
	if !dryRun && scenario.Target.HardwareProfile != "" {
		hwInfo := buildHardwareInfo(ctx, cat, rtName)
		if hwInfo.HardwareProfile != "" && hwInfo.HardwareProfile != scenario.Target.HardwareProfile {
			hwWarning = fmt.Sprintf("hardware mismatch: scenario targets %q but current device is %q",
				scenario.Target.HardwareProfile, hwInfo.HardwareProfile)
			slog.Warn(hwWarning)
		}
	}

	var ordered []orderedScenarioDeploy
	if len(scenario.StartupOrder) > 0 {
		byModel := make(map[string]knowledge.ScenarioDeployment, len(scenario.Deployments))
		for _, d := range scenario.Deployments {
			byModel[d.Model] = d
		}
		steps := make([]knowledge.ScenarioStartupStep, len(scenario.StartupOrder))
		copy(steps, scenario.StartupOrder)
		sort.Slice(steps, func(i, j int) bool { return steps[i].Step < steps[j].Step })
		for _, step := range steps {
			d, ok := byModel[step.Model]
			if !ok {
				results = append(results, scenarioDeployResult{
					Model:  step.Model,
					Status: "error",
					Error:  fmt.Sprintf("startup_order references unknown model %q", step.Model),
				})
				continue
			}
			ordered = append(ordered, orderedScenarioDeploy{
				deployment: d,
				waitFor:    step.WaitFor,
				timeoutS:   step.TimeoutS,
			})
			delete(byModel, step.Model)
		}
		for _, d := range scenario.Deployments {
			if _, remaining := byModel[d.Model]; remaining {
				ordered = append(ordered, orderedScenarioDeploy{deployment: d})
			}
		}
	} else {
		for _, d := range scenario.Deployments {
			ordered = append(ordered, orderedScenarioDeploy{deployment: d})
		}
	}

	blockFurther := false
	blockReason := ""
	for i, od := range ordered {
		d := od.deployment
		if blockFurther && !dryRun {
			results = append(results, scenarioDeployResult{
				Model:  d.Model,
				Engine: d.Engine,
				Status: "skipped",
				Error:  fmt.Sprintf("skipped after earlier deployment failure: %s", blockReason),
			})
			continue
		}
		if dryRun {
			if deps.DeployDryRun == nil {
				results = append(results, scenarioDeployResult{
					Model:  d.Model,
					Engine: d.Engine,
					Status: "error",
					Error:  "deploy.dry_run not available",
				})
				continue
			}
			data, err := deps.DeployDryRun(ctx, d.Engine, d.Model, d.Slot, d.Config)
			if err != nil {
				results = append(results, scenarioDeployResult{
					Model:  d.Model,
					Engine: d.Engine,
					Status: "error",
					Error:  err.Error(),
				})
			} else {
				results = append(results, scenarioDeployResult{
					Model:  d.Model,
					Engine: d.Engine,
					Status: "dry_run",
					Data:   data,
				})
			}
			continue
		}

		if deps.DeployApply == nil {
			blockFurther = true
			blockReason = "deploy.apply not available"
			results = append(results, scenarioDeployResult{
				Model:  d.Model,
				Engine: d.Engine,
				Status: "error",
				Error:  blockReason,
			})
			continue
		}
		data, err := deps.DeployApply(ctx, d.Engine, d.Model, d.Slot, d.Config)
		if err != nil {
			blockFurther = true
			blockReason = err.Error()
			results = append(results, scenarioDeployResult{
				Model:  d.Model,
				Engine: d.Engine,
				Status: "error",
				Error:  blockReason,
			})
			continue
		}

		var raw map[string]any
		if json.Unmarshal(data, &raw) == nil {
			if status, _ := raw["status"].(string); status == "NEEDS_APPROVAL" {
				if id, ok := raw["approval_id"].(float64); ok && deps.DeployApprove != nil {
					if approved, err := deps.DeployApprove(ctx, int64(id)); err == nil {
						data = approved
					}
				}
			}
		}
		results = append(results, scenarioDeployResult{
			Model:  d.Model,
			Engine: d.Engine,
			Status: "ok",
			Data:   data,
		})

		deploymentQuery := knowledge.SanitizePodName(d.Model + "-" + d.Engine)
		var deployStatusTarget struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &deployStatusTarget) == nil && deployStatusTarget.Name != "" {
			deploymentQuery = deployStatusTarget.Name
		}

		shouldWait := i < len(ordered)-1 || od.waitFor != "" || od.timeoutS > 0
		if shouldWait {
			if err := scenarioWaitForReady(ctx, deploymentQuery, od.waitFor, od.timeoutS, deps.DeployStatus); err != nil {
				slog.Warn("startup wait did not complete", "model", d.Model, "wait_for", od.waitFor, "err", err)
				blockFurther = true
				blockReason = err.Error()
				results = append(results, scenarioDeployResult{
					Model:  d.Model + "_wait",
					Status: "warning",
					Error:  err.Error(),
				})
			}
		}
	}

	if !dryRun {
		if blockFurther {
			for _, action := range scenario.PostDeploy {
				results = append(results, scenarioDeployResult{
					Model:  action.Action,
					Status: "skipped",
					Error:  fmt.Sprintf("skipped due to earlier deployment failure: %s", blockReason),
				})
			}
		} else {
			postDeployActions := map[string]func(context.Context) (json.RawMessage, error){
				"openclaw_sync": func(ctx context.Context) (json.RawMessage, error) {
					if deps.OpenClawSync == nil {
						return nil, fmt.Errorf("openclaw_sync not available")
					}
					return deps.OpenClawSync(ctx, false)
				},
			}
			for _, action := range scenario.PostDeploy {
				fn, ok := postDeployActions[action.Action]
				if !ok {
					results = append(results, scenarioDeployResult{
						Model:  action.Action,
						Status: "error",
						Error:  fmt.Sprintf("unknown post-deploy action: %s", action.Action),
					})
					continue
				}
				data, err := fn(ctx)
				if err != nil {
					results = append(results, scenarioDeployResult{
						Model:  action.Action,
						Status: "error",
						Error:  err.Error(),
					})
				} else {
					results = append(results, scenarioDeployResult{
						Model:  action.Action,
						Status: "ok",
						Data:   data,
					})
				}
			}
		}
	}

	resp := map[string]any{
		"scenario":    name,
		"dry_run":     dryRun,
		"deployments": results,
	}
	if hwWarning != "" {
		resp["hardware_warning"] = hwWarning
	}
	return json.Marshal(resp)
}

// scenarioWaitForReady waits for a deployed model to become ready before proceeding.
// waitFor: "health_check" polls deploy.status, "port_open" probes the returned address, "" defaults to 2s sleep.
// On timeout, returns an error (caller treats as warning, continues deployment).
func scenarioWaitForReady(ctx context.Context, query, waitFor string, timeoutS int, deployStatus func(context.Context, string) (json.RawMessage, error)) error {
	if waitFor == "" || timeoutS <= 0 {
		time.Sleep(2 * time.Second)
		return nil
	}
	if deployStatus == nil {
		return fmt.Errorf("deploy.status not available for wait_for=%q", waitFor)
	}

	switch waitFor {
	case "health_check", "port_open":
	default:
		return fmt.Errorf("unknown wait_for %q", waitFor)
	}

	checkReady := func() (bool, error) {
		data, err := deployStatus(ctx, query)
		if err != nil {
			return false, nil
		}
		var s struct {
			Phase          string `json:"phase"`
			Ready          bool   `json:"ready"`
			Address        string `json:"address"`
			Message        string `json:"message,omitempty"`
			StartupMessage string `json:"startup_message,omitempty"`
		}
		if err := json.Unmarshal(data, &s); err != nil {
			return false, nil
		}
		if s.Phase == "failed" {
			msg := s.Message
			if msg == "" {
				msg = s.StartupMessage
			}
			if msg == "" {
				msg = "deployment reported failed phase"
			}
			return false, fmt.Errorf("deployment %s failed: %s", query, msg)
		}
		switch waitFor {
		case "health_check":
			return s.Ready, nil
		case "port_open":
			if s.Address == "" {
				return false, nil
			}
			conn, err := net.DialTimeout("tcp", s.Address, time.Second)
			if err != nil {
				return false, nil
			}
			conn.Close()
			return true, nil
		default:
			return false, nil
		}
	}

	if ready, err := checkReady(); ready || err != nil {
		return err
	}

	timer := time.NewTimer(time.Duration(timeoutS) * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("timeout after %ds waiting for %s (%s)", timeoutS, query, waitFor)
		case <-ticker.C:
			if ready, err := checkReady(); ready || err != nil {
				return err
			}
		}
	}
}
