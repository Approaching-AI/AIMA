package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/mcp"
)

// onboardingDeployRequest is the JSON body for the onboarding deploy endpoint.
type onboardingDeployRequest struct {
	Model  string `json:"model"`
	Engine string `json:"engine,omitempty"`
}

type onboardingDeployResult struct {
	Name    string `json:"name"`
	Model   string `json:"model"`
	Engine  string `json:"engine"`
	Address string `json:"address"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func decodeOnboardingDeployResult(raw json.RawMessage) (onboardingDeployResult, error) {
	var result onboardingDeployResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return onboardingDeployResult{}, err
	}
	return result, nil
}

func (r onboardingDeployResult) ready() bool {
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

func (r onboardingDeployResult) failureMessage() string {
	if msg := strings.TrimSpace(r.Message); msg != "" {
		return msg
	}
	status := strings.TrimSpace(r.Status)
	if status == "" {
		return "deployment did not report a ready status"
	}
	return fmt.Sprintf("deployment ended with status %s", status)
}

func normalizeOnboardingEndpoint(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	if strings.Contains(address, "://") {
		return address
	}
	return "http://" + address
}

// handleOnboardingDeploy returns an HTTP handler that orchestrates engine pull +
// model pull + deploy.run with SSE progress streaming.
func handleOnboardingDeploy(ac *appContext, deps *mcp.ToolDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireOnboardingMutation(ac, w, r) {
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1*1024*1024))
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		var req onboardingDeployRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Model == "" {
			http.Error(w, "model is required", http.StatusBadRequest)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		ctx := r.Context()

		// Mutex protects writes to the ResponseWriter.
		var mu sync.Mutex
		send := func(event string, v any) {
			mu.Lock()
			defer mu.Unlock()
			sseJSON(w, flusher, event, v)
		}
		sendError := func(step int, name string, err error) {
			send("error", map[string]any{
				"step":    step,
				"name":    name,
				"message": err.Error(),
			})
		}

		const totalSteps = 3
		modelName := req.Model
		engineType := req.Engine

		send("deploy_start", map[string]string{
			"model":  modelName,
			"engine": engineType,
		})

		// Use DeployRun which orchestrates: resolve -> pull engine -> pull model -> deploy -> wait.
		// We hook into its onPhase and onEngineProgress callbacks to produce SSE events.
		if deps.DeployRun == nil {
			sendError(1, "deploy", fmt.Errorf("deploy.run not available"))
			return
		}

		// Track current step based on phase callbacks from deployRunCore.
		onPhase := func(phase, msg string) {
			select {
			case <-ctx.Done():
				return
			default:
			}

			switch phase {
			case "resolving":
				send("step", map[string]any{
					"step": 1, "total": totalSteps,
					"name": "engine_check", "status": "resolving",
					"message": msg,
				})
			case "resolved":
				send("step", map[string]any{
					"step": 1, "total": totalSteps,
					"name": "engine_check", "status": "resolved",
					"message": msg,
				})
			case "warning":
				send("step", map[string]any{
					"step": 1, "total": totalSteps,
					"name": "engine_check", "status": "warning",
					"message": msg,
				})
			case "pulling_engine":
				send("step", map[string]any{
					"step": 1, "total": totalSteps,
					"name": "engine_pull", "status": "downloading",
					"message": msg,
				})
			case "pulling_model":
				send("step", map[string]any{
					"step": 2, "total": totalSteps,
					"name": "model_pull", "status": "downloading",
					"message": msg,
				})
			case "model_skip":
				send("step", map[string]any{
					"step": 2, "total": totalSteps,
					"name": "model_check", "status": "skipped",
					"message": msg,
				})
			case "deploying":
				send("step", map[string]any{
					"step": 3, "total": totalSteps,
					"name": "deploy", "status": "starting",
					"message": msg,
				})
			case "waiting":
				send("step", map[string]any{
					"step": 3, "total": totalSteps,
					"name": "deploy", "status": "waiting",
					"message": msg,
				})
			case "startup":
				send("step", map[string]any{
					"step": 3, "total": totalSteps,
					"name": "deploy", "status": "starting",
					"message": msg,
				})
			case "reusing":
				send("step", map[string]any{
					"step": 3, "total": totalSteps,
					"name": "deploy", "status": "reusing",
					"message": msg,
				})
			case "ready":
				send("step", map[string]any{
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
			send("step", data)
		}

		result, err := deps.DeployRun(ctx, modelName, engineType, "", nil, false, onPhase, onEngineProgress)
		if err != nil {
			slog.Warn("onboarding deploy failed", "model", modelName, "error", err)
			sendError(3, "deploy", err)
			return
		}

		deployResult, err := decodeOnboardingDeployResult(result)
		if err != nil {
			sendError(3, "deploy", fmt.Errorf("parse deploy result: %w", err))
			return
		}

		if !deployResult.ready() {
			sendError(3, "deploy", fmt.Errorf("%s", deployResult.failureMessage()))
			return
		}

		send("deploy_complete", map[string]any{
			"model":    deployResult.Model,
			"engine":   deployResult.Engine,
			"endpoint": normalizeOnboardingEndpoint(deployResult.Address),
			"status":   "ready",
		})
	}
}
