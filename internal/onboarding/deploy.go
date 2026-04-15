package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jguan/aima/internal/engine"
)

// internal helper mirroring cmd/aima's onboardingDeployResult status shape so
// we can detect success/failure consistently.
type deployRaw struct {
	Name    string `json:"name"`
	Model   string `json:"model"`
	Engine  string `json:"engine"`
	Address string `json:"address"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func (r deployRaw) ready() bool {
	status := strings.ToLower(strings.TrimSpace(r.Status))
	switch status {
	case "ready":
		return true
	case "":
		return strings.TrimSpace(r.Address) != ""
	default:
		return false
	}
}

func (r deployRaw) failureMessage() string {
	if msg := strings.TrimSpace(r.Message); msg != "" {
		return msg
	}
	status := strings.TrimSpace(r.Status)
	if status == "" {
		return "deployment did not report a ready status"
	}
	return fmt.Sprintf("deployment ended with status %s", status)
}

func normalizeEndpoint(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	if strings.Contains(address, "://") {
		return address
	}
	return "http://" + address
}

// RunDeploy wraps DeployRun with the onboarding-specific phase mapping (3 big
// user-visible steps: engine pull / model pull / deploy) and on success marks
// onboarding_completed=true in the SQLite config store. Events are collected
// into a slice and returned alongside the final DeployResult; HTTP handlers
// stream them via SSE while MCP/CLI callers process them in-memory.
func RunDeploy(
	ctx context.Context,
	deps *Deps,
	model, engineType, slot string,
	configOverrides map[string]any,
	noPull bool,
	sink EventSink,
) (DeployResult, []Event, error) {
	if deps == nil || deps.ToolDeps == nil {
		return DeployResult{}, nil, fmt.Errorf("onboarding deploy: deps not initialized")
	}
	td := deps.ToolDeps

	if strings.TrimSpace(model) == "" {
		return DeployResult{}, nil, fmt.Errorf("model is required")
	}
	if td.DeployRun == nil {
		return DeployResult{}, nil, fmt.Errorf("deploy.run not available")
	}

	var (
		mu     sync.Mutex
		events []Event
	)
	emit := func(t string, data map[string]any) {
		ev := Event{Type: t, Timestamp: time.Now(), Data: data}
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
		if sink != nil {
			sink(ev)
		}
	}

	const totalSteps = 3

	emit("deploy_start", map[string]any{
		"model":  model,
		"engine": engineType,
	})

	onPhase := func(phase, msg string) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		switch phase {
		case "resolving":
			emit("step", map[string]any{
				"step": 1, "total": totalSteps,
				"name": "engine_check", "status": "resolving",
				"message": msg,
			})
		case "resolved":
			emit("step", map[string]any{
				"step": 1, "total": totalSteps,
				"name": "engine_check", "status": "resolved",
				"message": msg,
			})
		case "warning":
			emit("step", map[string]any{
				"step": 1, "total": totalSteps,
				"name": "engine_check", "status": "warning",
				"message": msg,
			})
		case "pulling_engine":
			emit("step", map[string]any{
				"step": 1, "total": totalSteps,
				"name": "engine_pull", "status": "downloading",
				"message": msg,
			})
		case "pulling_model":
			emit("step", map[string]any{
				"step": 2, "total": totalSteps,
				"name": "model_pull", "status": "downloading",
				"message": msg,
			})
		case "model_skip":
			emit("step", map[string]any{
				"step": 2, "total": totalSteps,
				"name": "model_check", "status": "skipped",
				"message": msg,
			})
		case "deploying":
			emit("step", map[string]any{
				"step": 3, "total": totalSteps,
				"name": "deploy", "status": "starting",
				"message": msg,
			})
		case "waiting":
			emit("step", map[string]any{
				"step": 3, "total": totalSteps,
				"name": "deploy", "status": "waiting",
				"message": msg,
			})
		case "startup":
			emit("step", map[string]any{
				"step": 3, "total": totalSteps,
				"name": "deploy", "status": "starting",
				"message": msg,
			})
		case "reusing":
			// Reusing an existing ready deployment — emit a skipped marker for
			// each earlier step so the wizard progress bar lights up all three
			// lanes instead of jumping from step 1 straight to step 3.
			emit("step", map[string]any{
				"step": 1, "total": totalSteps,
				"name": "engine_check", "status": "reusing",
				"message": msg,
			})
			emit("step", map[string]any{
				"step": 2, "total": totalSteps,
				"name": "model_check", "status": "reusing",
				"message": msg,
			})
			emit("step", map[string]any{
				"step": 3, "total": totalSteps,
				"name": "deploy", "status": "reusing",
				"message": msg,
			})
		case "ready":
			emit("step", map[string]any{
				"step": 3, "total": totalSteps,
				"name": "deploy", "status": "ready",
				"endpoint": msg,
			})
		}
	}

	onEngineProgress := func(ev engine.ProgressEvent) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		data := map[string]any{
			"step": 1, "total": totalSteps,
			"name": "engine_pull", "status": ev.Phase,
		}
		if ev.Message != "" {
			data["message"] = ev.Message
		}
		if ev.Total > 0 {
			data["downloaded_bytes"] = ev.Downloaded
			data["total_bytes"] = ev.Total
			data["progress"] = float64(ev.Downloaded) / float64(ev.Total)
		}
		if ev.Speed > 0 {
			data["speed_bytes_per_sec"] = ev.Speed
		}
		emit("step", data)
	}

	// Mirror engine_pull byte progress for the model_pull step. Without this
	// the wizard progress bar froze at "downloading model..." while a multi-GB
	// transfer ran for tens of minutes — UAT users assumed the deploy hung.
	onModelProgress := func(downloaded, total int64) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		data := map[string]any{
			"step": 2, "total": totalSteps,
			"name": "model_pull", "status": "downloading",
		}
		if total > 0 {
			data["downloaded_bytes"] = downloaded
			data["total_bytes"] = total
			data["progress"] = float64(downloaded) / float64(total)
		} else if downloaded > 0 {
			data["downloaded_bytes"] = downloaded
		}
		emit("step", data)
	}

	raw, err := td.DeployRun(ctx, model, engineType, slot, configOverrides, noPull, onPhase, onEngineProgress, onModelProgress)
	if err != nil {
		slog.Warn("onboarding deploy failed", "model", model, "error", err)
		emit("error", map[string]any{"step": 3, "name": "deploy", "message": err.Error()})
		return DeployResult{}, events, err
	}

	var deployRes deployRaw
	if err := json.Unmarshal(raw, &deployRes); err != nil {
		emit("error", map[string]any{"step": 3, "name": "deploy", "message": err.Error()})
		return DeployResult{}, events, fmt.Errorf("parse deploy result: %w", err)
	}

	if !deployRes.ready() {
		msg := deployRes.failureMessage()
		emit("error", map[string]any{"step": 3, "name": "deploy", "message": msg})
		return DeployResult{
			Name:    deployRes.Name,
			Model:   deployRes.Model,
			Engine:  deployRes.Engine,
			Status:  deployRes.Status,
			Message: deployRes.Message,
		}, events, fmt.Errorf("%s", msg)
	}

	// Persist onboarding completion (best effort — previously lived in the
	// buildOnboardingDeps decorator; inlined here so the side effect fires for
	// every successful onboarding deploy regardless of call path).
	if td.SetConfig != nil {
		persistCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if cfgErr := td.SetConfig(persistCtx, "onboarding_completed", "true"); cfgErr != nil {
			slog.Warn("onboarding deploy: failed to mark onboarding completed", "error", cfgErr)
		}
		cancel()
	}

	endpoint := normalizeEndpoint(deployRes.Address)
	result := DeployResult{
		Name:     deployRes.Name,
		Model:    deployRes.Model,
		Engine:   deployRes.Engine,
		Endpoint: endpoint,
		Status:   "ready",
	}
	emit("deploy_complete", map[string]any{
		"model":    result.Model,
		"engine":   result.Engine,
		"endpoint": endpoint,
		"status":   "ready",
	})
	return result, events, nil
}
