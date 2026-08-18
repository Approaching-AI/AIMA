package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/jguan/aima/internal/mcp"
)

type onboardingStartRequest struct {
	Locale string `json:"locale,omitempty"`
}

// handleOnboardingStart exposes the same read-only first-run guide used by
// MCP and CLI. The handler stays thin: ToolDeps owns the onboarding/start
// decision surface; HTTP only validates the request and returns JSON.
func handleOnboardingStart(ac *appContext, deps *mcp.ToolDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireOnboardingMutation(ac, w, r) {
			return
		}
		if deps == nil || deps.OnboardingStart == nil {
			http.Error(w, "onboarding start unavailable", http.StatusServiceUnavailable)
			return
		}

		var req onboardingStartRequest
		if r.Body != nil {
			body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
			if err != nil {
				http.Error(w, "failed to read request body", http.StatusBadRequest)
				return
			}
			if len(strings.TrimSpace(string(body))) > 0 {
				if err := json.Unmarshal(body, &req); err != nil {
					http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
					return
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		data, err := deps.OnboardingStart(r.Context(), strings.TrimSpace(req.Locale))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_, _ = w.Write(data)
	}
}
