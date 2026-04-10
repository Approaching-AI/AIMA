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
	logsArgs, _ := json.Marshal(map[string]any{"name": deployName, "tail": 100})
	result, err := h.tools.ExecuteTool(ctx, "deploy.logs", logsArgs)
	if err == nil {
		err = toolResultError(result)
	}
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

		deploy, err := h.lookupDeployment(ctx, deployName)
		if err != nil {
			continue
		}
		resolvedConfig, err := h.resolveDeploymentConfig(ctx, deploy)
		if err != nil {
			slog.Warn("self-heal: resolve config failed", "deploy", deployName, "error", err, "attempt", attempt)
			continue
		}

		gmuKey := memoryUtilizationKey(resolvedConfig)
		if gmuKey == "" {
			slog.Warn("self-heal: deployment has no supported memory utilization key",
				"deploy", deployName, "attempt", attempt)
			action.Success = false
			return action, nil
		}
		currentGMU, _ := resolvedConfig[gmuKey].(float64)
		if currentGMU == 0 {
			currentGMU = 0.9
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

		nextConfig := cloneConfigMap(resolvedConfig)
		nextConfig[gmuKey] = newGMU
		redeployArgs, _ := json.Marshal(map[string]any{
			"model":  deploy.Model,
			"engine": deploy.Engine,
			"slot":   deploy.Slot,
			"config": nextConfig,
		})
		redeployResult, err := h.tools.ExecuteTool(ctx, "deploy.apply", redeployArgs)
		if err == nil {
			err = toolResultError(redeployResult)
		}
		if err != nil {
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

	deploy, err := h.lookupDeployment(ctx, deployName)
	if err != nil {
		action.Success = false
		return action, nil
	}

	resolvedConfig, err := h.resolveDeploymentConfig(ctx, deploy)
	if err != nil {
		action.Success = false
		return action, nil
	}

	redeployArgs, _ := json.Marshal(map[string]any{
		"model":  deploy.Model,
		"engine": deploy.Engine,
		"slot":   deploy.Slot,
		"config": cloneConfigMap(resolvedConfig),
	})
	redeployResult, err := h.tools.ExecuteTool(ctx, "deploy.apply", redeployArgs)
	if err == nil {
		err = toolResultError(redeployResult)
	}
	if err != nil {
		action.Success = false
		return action, nil
	}

	action.Success = true
	return action, nil
}

type deploymentMetadata struct {
	Name   string            `json:"name"`
	Model  string            `json:"model"`
	Engine string            `json:"engine"`
	Slot   string            `json:"slot"`
	Labels map[string]string `json:"labels,omitempty"`
}

func (h *Healer) lookupDeployment(ctx context.Context, deployName string) (*deploymentMetadata, error) {
	result, err := h.tools.ExecuteTool(ctx, "deploy.list", nil)
	if err == nil {
		err = toolResultError(result)
	}
	if err != nil {
		return nil, err
	}

	var deploys []deploymentMetadata
	if err := json.Unmarshal([]byte(result.Content), &deploys); err != nil {
		return nil, fmt.Errorf("parse deploy list: %w", err)
	}

	var modelMatches []*deploymentMetadata
	for i := range deploys {
		if deploys[i].Model == "" {
			deploys[i].Model = deploys[i].Labels["aima.dev/model"]
		}
		if deploys[i].Engine == "" {
			deploys[i].Engine = deploys[i].Labels["aima.dev/engine"]
		}
		if deploys[i].Slot == "" {
			deploys[i].Slot = deploys[i].Labels["aima.dev/slot"]
		}
		if deploys[i].Name == deployName {
			return validateDeploymentMetadata(&deploys[i], deployName)
		}
		if deploys[i].Model == deployName {
			modelMatches = append(modelMatches, &deploys[i])
		}
	}
	if len(modelMatches) == 1 {
		return validateDeploymentMetadata(modelMatches[0], deployName)
	}
	if len(modelMatches) > 1 {
		return nil, fmt.Errorf("deployment %s is ambiguous; matches %d active deployments", deployName, len(modelMatches))
	}
	return nil, fmt.Errorf("deployment %s not found", deployName)
}

func validateDeploymentMetadata(deploy *deploymentMetadata, deployRef string) (*deploymentMetadata, error) {
	if deploy.Model == "" || deploy.Engine == "" {
		return nil, fmt.Errorf("deployment %s missing model or engine metadata", deployRef)
	}
	return deploy, nil
}

func (h *Healer) resolveDeploymentConfig(ctx context.Context, deploy *deploymentMetadata) (map[string]any, error) {
	previewArgs, _ := json.Marshal(map[string]any{
		"model":  deploy.Model,
		"engine": deploy.Engine,
		"slot":   deploy.Slot,
	})
	result, err := h.tools.ExecuteTool(ctx, "deploy.dry_run", previewArgs)
	if err == nil {
		err = toolResultError(result)
	}
	if err != nil {
		return nil, err
	}

	var preview struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal([]byte(result.Content), &preview); err != nil {
		return nil, fmt.Errorf("parse deployment preview: %w", err)
	}
	if preview.Config == nil {
		return nil, fmt.Errorf("deployment preview config is empty")
	}
	return preview.Config, nil
}

func memoryUtilizationKey(config map[string]any) string {
	for _, key := range []string{"gpu_memory_utilization", "mem_fraction_static"} {
		if _, ok := config[key]; ok {
			return key
		}
	}
	return ""
}

func cloneConfigMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
