package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// Diagnosis describes a deployment failure cause.
type Diagnosis struct {
	Type   string // "oom", "crash_loop", "image_pull", "port_conflict", "unknown"
	Cause  string
	Remedy string
}

// HealAction records a self-healing attempt.
type HealAction struct {
	DeployName string `json:"deploy_name"`
	Diagnosis  string `json:"diagnosis"`
	Cause      string `json:"cause"`
	Action     string `json:"action"`
	Success    bool   `json:"success"`
	Attempt    int    `json:"attempt"`
}

// failurePattern maps log patterns to diagnoses (table-driven, INV-2).
var failurePatterns = []struct {
	Pattern string
	Type    string
	Remedy  string
}{
	{"CUDA out of memory", "oom", "reduce_gmu"},
	{"torch.cuda.OutOfMemoryError", "oom", "reduce_gmu"},
	{"Cannot allocate memory", "oom", "reduce_gmu"},
	{"OutOfMemoryError", "oom", "reduce_gmu"},
	{"No such file or directory", "missing_file", "check_model_path"},
	{"Address already in use", "port_conflict", "kill_conflicting"},
	{"ImagePullBackOff", "image_pull", "retry_pull"},
	{"ErrImagePull", "image_pull", "retry_pull"},
	{"unauthorized", "auth_error", "check_credentials"},
}

// Healer performs automatic failure diagnosis and recovery.
type Healer struct {
	tools      ToolExecutor
	maxRetries int
}

// NewHealer creates a healer with default max retries.
func NewHealer(tools ToolExecutor) *Healer {
	return &Healer{tools: tools, maxRetries: 3}
}

// Diagnose inspects a failed deployment and returns a diagnosis.
func (h *Healer) Diagnose(ctx context.Context, deployName string) (*Diagnosis, error) {
	// Get deploy logs
	logsArgs, _ := json.Marshal(map[string]any{"name": deployName, "lines": 100})
	result, err := h.tools.ExecuteTool(ctx, "deploy.logs", logsArgs)
	if err != nil {
		return &Diagnosis{Type: "unknown", Cause: "could not fetch logs: " + err.Error(), Remedy: "escalate"}, nil
	}

	logs := result.Content

	// Pattern match
	for _, fp := range failurePatterns {
		if strings.Contains(logs, fp.Pattern) {
			return &Diagnosis{
				Type:   fp.Type,
				Cause:  fp.Pattern,
				Remedy: fp.Remedy,
			}, nil
		}
	}

	return &Diagnosis{Type: "unknown", Cause: "no recognized failure pattern in logs", Remedy: "escalate"}, nil
}

// Heal attempts to recover a failed deployment based on diagnosis.
// Returns the action taken and whether it succeeded.
func (h *Healer) Heal(ctx context.Context, deployName string, diag *Diagnosis) (*HealAction, error) {
	action := &HealAction{
		DeployName: deployName,
		Diagnosis:  diag.Type,
		Cause:      diag.Cause,
	}

	switch diag.Type {
	case "oom":
		return h.healOOM(ctx, deployName, action)
	case "image_pull":
		return h.healImagePull(ctx, deployName, action)
	default:
		action.Action = "escalate"
		action.Success = false
		slog.Warn("self-heal: unrecoverable failure, escalating",
			"deploy", deployName, "diagnosis", diag.Type, "cause", diag.Cause)
		return action, nil
	}
}

func (h *Healer) healOOM(ctx context.Context, deployName string, action *HealAction) (*HealAction, error) {
	action.Action = "reduce_gmu"

	for attempt := 1; attempt <= h.maxRetries; attempt++ {
		action.Attempt = attempt

		// Get current config
		listArgs, _ := json.Marshal(map[string]any{"name": deployName})
		result, err := h.tools.ExecuteTool(ctx, "deploy.list", listArgs)
		if err != nil {
			continue
		}

		// Extract current gmu from deploy info
		var deploys []struct {
			Config map[string]any `json:"config"`
			Model  string         `json:"model"`
			Engine string         `json:"engine"`
		}
		if err := json.Unmarshal([]byte(result.Content), &deploys); err != nil || len(deploys) == 0 {
			continue
		}

		deploy := deploys[0]
		currentGMU := 0.9 // default
		for _, key := range []string{"gpu_memory_utilization", "mem_fraction_static"} {
			if v, ok := deploy.Config[key]; ok {
				if f, ok := v.(float64); ok {
					currentGMU = f
					break
				}
			}
		}

		newGMU := currentGMU - 0.1
		if newGMU < 0.3 {
			slog.Warn("self-heal: gmu already at minimum, cannot reduce further",
				"deploy", deployName, "current_gmu", currentGMU)
			action.Success = false
			return action, nil
		}

		slog.Info("self-heal: reducing gmu for OOM recovery",
			"deploy", deployName, "old_gmu", currentGMU, "new_gmu", newGMU, "attempt", attempt)

		// Redeploy with reduced gmu
		redeployArgs, _ := json.Marshal(map[string]any{
			"model":  deploy.Model,
			"engine": deploy.Engine,
			"config_overrides": map[string]any{
				"gpu_memory_utilization": newGMU,
			},
		})
		if _, err := h.tools.ExecuteTool(ctx, "deploy.apply", redeployArgs); err != nil {
			slog.Warn("self-heal: redeploy failed", "deploy", deployName, "error", err, "attempt", attempt)
			continue
		}

		action.Success = true
		slog.Info("self-heal: OOM recovery successful",
			"deploy", deployName, "new_gmu", newGMU, "attempt", attempt)
		return action, nil
	}

	action.Success = false
	return action, fmt.Errorf("self-heal: exhausted %d retries for OOM recovery on %s", h.maxRetries, deployName)
}

func (h *Healer) healImagePull(ctx context.Context, deployName string, action *HealAction) (*HealAction, error) {
	action.Action = "retry_pull"
	action.Attempt = 1

	// Simply retry the deployment — the runtime may pick a different registry
	redeployArgs, _ := json.Marshal(map[string]any{"name": deployName})
	if _, err := h.tools.ExecuteTool(ctx, "deploy.apply", redeployArgs); err != nil {
		action.Success = false
		return action, nil
	}

	action.Success = true
	return action, nil
}
