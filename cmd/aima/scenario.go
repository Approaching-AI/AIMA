package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/recovery"
)

type scenarioDeployResult struct {
	Model  string          `json:"model"`
	Engine string          `json:"engine"`
	Device string          `json:"device,omitempty"`
	Status string          `json:"status"`
	Error  string          `json:"error,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

type orderedScenarioDeploy struct {
	deployment knowledge.ScenarioDeployment
	waitFor    string
	timeoutS   int
}

func applyScenario(ctx context.Context, cat *knowledge.Catalog, rtName string, deps *mcp.ToolDeps, name string, dryRun bool, provided map[string]string) (json.RawMessage, error) {
	var scenario *knowledge.DeploymentScenario
	for i := range cat.DeploymentScenarios {
		if strings.EqualFold(cat.DeploymentScenarios[i].Metadata.Name, name) {
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
	bindings, err := resolveScenarioBindings(scenario.Inputs, provided)
	if err != nil {
		return nil, err
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
		byKey := make(map[string]int, len(scenario.Deployments))
		for i, d := range scenario.Deployments {
			key := scenarioDeploymentKey(d)
			if _, exists := byKey[key]; exists {
				return nil, fmt.Errorf("scenario %q has duplicate deployment key %q; set unique deployment ids", name, key)
			}
			byKey[key] = i
		}
		used := make(map[int]bool, len(scenario.Deployments))
		steps := make([]knowledge.ScenarioStartupStep, len(scenario.StartupOrder))
		copy(steps, scenario.StartupOrder)
		sort.Slice(steps, func(i, j int) bool { return steps[i].Step < steps[j].Step })
		for _, step := range steps {
			ref := step.Deployment
			if ref == "" {
				ref = step.Model
			}
			idx, ok := byKey[strings.ToLower(strings.TrimSpace(ref))]
			if !ok {
				results = append(results, scenarioDeployResult{
					Model:  ref,
					Status: "error",
					Error:  fmt.Sprintf("startup_order references unknown deployment %q", ref),
				})
				continue
			}
			ordered = append(ordered, orderedScenarioDeploy{
				deployment: scenario.Deployments[idx],
				waitFor:    step.WaitFor,
				timeoutS:   step.TimeoutS,
			})
			used[idx] = true
		}
		for i, d := range scenario.Deployments {
			if !used[i] {
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
		d, err := resolveScenarioDeployment(od.deployment, bindings)
		if err != nil {
			return nil, fmt.Errorf("resolve deployment %q: %w", scenarioDeploymentKey(od.deployment), err)
		}
		device := strings.TrimSpace(d.Device)
		remote := device != "" && !strings.EqualFold(device, "local")
		config := cloneScenarioConfig(d.Config)
		if len(d.Env) > 0 {
			env := make(map[string]any, len(d.Env))
			for key, value := range d.Env {
				env[key] = value
			}
			config["_env"] = env
		}
		if blockFurther && !dryRun {
			results = append(results, scenarioDeployResult{
				Model:  d.Model,
				Engine: d.Engine,
				Device: device,
				Status: "skipped",
				Error:  fmt.Sprintf("skipped after earlier deployment failure: %s", blockReason),
			})
			continue
		}
		if dryRun {
			if (!remote && deps.DeployDryRun == nil) || (remote && deps.FleetExecTool == nil) {
				results = append(results, scenarioDeployResult{
					Model:  d.Model,
					Engine: d.Engine,
					Status: "error",
					Error:  "deploy.dry_run not available",
				})
				continue
			}
			var data json.RawMessage
			if remote {
				data, err = scenarioFleetDeploy(ctx, deps, device, "deploy.dry_run", d, config)
			} else {
				data, err = deps.DeployDryRun(ctx, d.Engine, d.Model, d.Slot, config)
			}
			if err != nil {
				results = append(results, scenarioDeployResult{
					Model:  d.Model,
					Engine: d.Engine,
					Device: device,
					Status: "error",
					Error:  err.Error(),
				})
			} else {
				results = append(results, scenarioDeployResult{
					Model:  d.Model,
					Engine: d.Engine,
					Device: device,
					Status: "dry_run",
					Data:   data,
				})
			}
			continue
		}

		if (!remote && deps.DeployApply == nil) || (remote && deps.FleetExecTool == nil) {
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
		var data json.RawMessage
		if remote {
			data, err = scenarioFleetDeploy(ctx, deps, device, "deploy.apply", d, config)
		} else {
			data, err = deps.DeployApply(ctx, d.Engine, d.Model, d.Slot, config, d.NoPull, recovery.PolicyPatch{})
		}
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

		results = append(results, scenarioDeployResult{
			Model:  d.Model,
			Engine: d.Engine,
			Device: device,
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
			deployStatus := deps.DeployStatus
			if remote {
				deployStatus = func(waitCtx context.Context, query string) (json.RawMessage, error) {
					params, _ := json.Marshal(map[string]any{"name": query})
					raw, callErr := deps.FleetExecTool(waitCtx, device, "deploy.status", params)
					if callErr != nil {
						return nil, callErr
					}
					return unwrapFleetToolResult(raw)
				}
			}
			if err := scenarioWaitForReady(ctx, deploymentQuery, od.waitFor, od.timeoutS, deployStatus); err != nil {
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

var scenarioBindingPattern = regexp.MustCompile(`\{\{\.([A-Za-z0-9_-]+)\}\}`)

func scenarioDeploymentKey(d knowledge.ScenarioDeployment) string {
	if strings.TrimSpace(d.ID) != "" {
		return strings.ToLower(strings.TrimSpace(d.ID))
	}
	return strings.ToLower(strings.TrimSpace(d.Model))
}

func resolveScenarioBindings(inputs []knowledge.ScenarioInput, provided map[string]string) (map[string]string, error) {
	bindings := make(map[string]string, len(inputs)+len(provided))
	for _, input := range inputs {
		if input.Default != "" {
			bindings[input.Name] = input.Default
		}
	}
	for key, value := range provided {
		bindings[key] = value
	}
	var missing []string
	for _, input := range inputs {
		if input.Required && strings.TrimSpace(bindings[input.Name]) == "" {
			missing = append(missing, input.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("scenario requires bindings: %s", strings.Join(missing, ", "))
	}
	return bindings, nil
}

func resolveScenarioDeployment(d knowledge.ScenarioDeployment, bindings map[string]string) (knowledge.ScenarioDeployment, error) {
	var err error
	expand := func(value string) string {
		if err != nil {
			return value
		}
		value, err = expandScenarioString(value, bindings)
		return value
	}
	d.Device = expand(d.Device)
	d.Model = expand(d.Model)
	d.Engine = expand(d.Engine)
	d.Slot = expand(d.Slot)
	d.Notes = expand(d.Notes)
	if err != nil {
		return d, err
	}
	resolved, err := expandScenarioValue(d.Config, bindings)
	if err != nil {
		return d, err
	}
	if resolved != nil {
		d.Config = resolved.(map[string]any)
	}
	originalEnv := d.Env
	d.Env = make(map[string]string, len(originalEnv))
	for key, value := range originalEnv {
		d.Env[key], err = expandScenarioString(value, bindings)
		if err != nil {
			return d, err
		}
	}
	return d, nil
}

func expandScenarioString(value string, bindings map[string]string) (string, error) {
	value = scenarioBindingPattern.ReplaceAllStringFunc(value, func(token string) string {
		match := scenarioBindingPattern.FindStringSubmatch(token)
		if len(match) != 2 {
			return token
		}
		if replacement, ok := bindings[match[1]]; ok {
			return replacement
		}
		return token
	})
	if match := scenarioBindingPattern.FindStringSubmatch(value); len(match) == 2 {
		return "", fmt.Errorf("missing binding %q", match[1])
	}
	return value, nil
}

func expandScenarioValue(value any, bindings map[string]string) (any, error) {
	switch typed := value.(type) {
	case string:
		return expandScenarioString(typed, bindings)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			expanded, err := expandScenarioValue(child, bindings)
			if err != nil {
				return nil, err
			}
			out[key] = expanded
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			expanded, err := expandScenarioValue(child, bindings)
			if err != nil {
				return nil, err
			}
			out[i] = expanded
		}
		return out, nil
	default:
		return value, nil
	}
}

func cloneScenarioConfig(config map[string]any) map[string]any {
	out := make(map[string]any, len(config)+1)
	for key, value := range config {
		out[key] = value
	}
	return out
}

func scenarioFleetDeploy(ctx context.Context, deps *mcp.ToolDeps, device, toolName string, d knowledge.ScenarioDeployment, config map[string]any) (json.RawMessage, error) {
	if deps.FleetExecTool == nil {
		return nil, fmt.Errorf("fleet.exec not available for remote device %q", device)
	}
	params, err := json.Marshal(map[string]any{
		"model":   d.Model,
		"engine":  d.Engine,
		"slot":    d.Slot,
		"config":  config,
		"no_pull": d.NoPull,
	})
	if err != nil {
		return nil, err
	}
	raw, err := deps.FleetExecTool(ctx, device, toolName, params)
	if err != nil {
		return nil, err
	}
	return unwrapFleetToolResult(raw)
}

func unwrapFleetToolResult(raw json.RawMessage) (json.RawMessage, error) {
	var result mcp.ToolResult
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Content) == 0 {
		return raw, nil
	}
	var textParts []string
	for _, content := range result.Content {
		if content.Type == "text" {
			textParts = append(textParts, content.Text)
		}
	}
	text := strings.Join(textParts, "\n")
	if result.IsError {
		return nil, fmt.Errorf("remote tool failed: %s", text)
	}
	if json.Valid([]byte(text)) {
		return json.RawMessage(text), nil
	}
	encoded, err := json.Marshal(text)
	return encoded, err
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
