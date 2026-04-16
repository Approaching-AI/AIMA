package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
)

// handleStopContainer stops a Docker container by name. Used by the onboarding
// wizard to let users free GPU resources occupied by non-AIMA containers.
func handleStopContainer(ac *appContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireOnboardingMutation(ac, w, r) {
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		var req struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		name := strings.TrimSpace(req.Name)
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}

		// Validate the name contains only safe characters (alphanumeric, dash, underscore, dot)
		for _, c := range name {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
				http.Error(w, "invalid container name", http.StatusBadRequest)
				return
			}
		}

		// Use short timeout (-t 3) so the UI doesn't hang for 10s default grace period
		out, err := exec.CommandContext(r.Context(), "docker", "stop", "-t", "3", name).CombinedOutput()
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "failed to stop container: " + strings.TrimSpace(string(out)),
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status": "stopped",
			"name":   name,
		})
	}
}
