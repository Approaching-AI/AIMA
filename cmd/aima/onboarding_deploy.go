package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/onboarding"
)

// onboardingDeployRequest is the JSON body for the onboarding deploy endpoint.
type onboardingDeployRequest struct {
	Model  string `json:"model"`
	Engine string `json:"engine,omitempty"`
}

// handleOnboardingDeploy is the thin SSE wrapper around onboarding.RunDeploy.
// Events are streamed to the client in real time via an EventSink, so the
// wizard UI sees per-step progress (engine_pull, model_pull, deploy) while the
// deployment is happening, not after it has finished.
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

		// When engine is not specified, prefer an installed engine that supports
		// the model's format. This avoids pulling a large new engine image during
		// onboarding when a compatible one is already on disk.
		engineType := req.Engine
		if engineType == "" && ac != nil && ac.db != nil && ac.cat != nil {
			engineType = preferInstalledEngine(r.Context(), ac, req.Model)
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

		stream := newSSEStream(w, flusher)
		stream.startHeartbeat(r.Context(), sseHeartbeatInterval)

		var sawError atomic.Bool
		baseSink := stream.sink()
		sink := func(ev onboarding.Event) {
			if ev.Type == "error" {
				sawError.Store(true)
			}
			baseSink(ev)
		}

		obDeps := buildOnboardingDepsStruct(ac, deps)
		_, _, runErr := onboarding.RunDeploy(r.Context(), obDeps, req.Model, engineType, "", nil, false, sink)
		if runErr != nil && !sawError.Load() {
			stream.writeJSON("error", map[string]any{
				"step":    3,
				"name":    "deploy",
				"message": fmt.Sprintf("%s", runErr),
			})
		}
	}
}

// preferInstalledEngine checks if an engine that supports the model's format
// is already installed locally. Returns the engine type if found, "" otherwise.
func preferInstalledEngine(ctx context.Context, ac *appContext, modelName string) string {
	// Determine model format: check catalog first, then scan DB
	var format string
	if ac.cat != nil {
		for _, ma := range ac.cat.ModelAssets {
			if strings.EqualFold(ma.Metadata.Name, modelName) {
				if len(ma.Storage.Formats) > 0 {
					format = ma.Storage.Formats[0]
				}
				break
			}
		}
	}
	if format == "" && ac.db != nil {
		if m, err := ac.db.FindModelByName(ctx, modelName); err == nil && m != nil {
			format = m.Format
		}
	}
	if format == "" {
		return ""
	}

	// Find which engine types support this format
	supportedTypes := map[string]bool{}
	if ac.cat != nil {
		for _, ea := range ac.cat.EngineAssets {
			if strings.EqualFold(ea.Metadata.Status, "blocked") {
				continue
			}
			for _, f := range ea.Metadata.SupportedFormats {
				if strings.EqualFold(f, format) {
					supportedTypes[ea.Metadata.Type] = true
				}
			}
		}
	}
	if len(supportedTypes) == 0 {
		return ""
	}

	// Check installed engines for a match
	installed, err := ac.db.ListEngines(ctx)
	if err != nil {
		return ""
	}
	for _, eng := range installed {
		if eng == nil || !eng.Available {
			continue
		}
		if supportedTypes[eng.Type] {
			slog.Info("onboarding deploy: preferring installed engine",
				"model", modelName, "format", format, "engine_type", eng.Type,
				"image", eng.Image+":"+eng.Tag)
			return eng.Type
		}
	}
	return ""
}

// Ensure state package is available for type reference
var _ *state.Engine
