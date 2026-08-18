package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/proxy"
)

type onboardingUseExistingRequest struct {
	Model    string `json:"model"`
	Endpoint string `json:"endpoint,omitempty"`
}

func handleOnboardingUseExisting(ac *appContext, deps *mcp.ToolDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireOnboardingMutation(ac, w, r) {
			return
		}
		if deps == nil || deps.SetConfig == nil {
			http.Error(w, "config writer is not available", http.StatusInternalServerError)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		var req onboardingUseExistingRequest
		if len(strings.TrimSpace(string(body))) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
		}

		model := strings.TrimSpace(req.Model)
		endpoint := strings.TrimRight(strings.TrimSpace(req.Endpoint), "/")
		if model != "" {
			if b, ok := findProxyBackendByModel(ac, model); ok {
				if !proxyBackendUsable(b) {
					http.Error(w, "matched backend is not ready", http.StatusConflict)
					return
				}
				endpoint = defaultLLMEndpoint()
			}
		} else {
			if b := findExistingProxyBackend(ac); b != nil {
				model = strings.TrimSpace(b.ModelName)
				endpoint = defaultLLMEndpoint()
			}
		}
		if model == "" {
			http.Error(w, "model is required", http.StatusBadRequest)
			return
		}
		if endpoint == "" {
			http.Error(w, "endpoint is required", http.StatusBadRequest)
			return
		}

		for _, kv := range []struct {
			key   string
			value string
		}{
			{"llm.endpoint", endpoint},
			{"llm.model", model},
			{"onboarding_completed", "true"},
		} {
			if err := deps.SetConfig(r.Context(), kv.key, kv.value); err != nil {
				http.Error(w, "failed to save "+kv.key+": "+err.Error(), http.StatusInternalServerError)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":   "configured",
			"model":    model,
			"endpoint": endpoint,
		})
	}
}

func findProxyBackendByModel(ac *appContext, model string) (*proxy.Backend, bool) {
	if ac == nil || ac.proxy == nil {
		return nil, false
	}
	backends := ac.proxy.ListBackends()
	model = strings.TrimSpace(model)
	for key, b := range backends {
		if b == nil {
			continue
		}
		if strings.EqualFold(key, model) || strings.EqualFold(b.ModelName, model) {
			return b, true
		}
	}
	return nil, false
}

func findExistingProxyBackend(ac *appContext) *proxy.Backend {
	if ac == nil || ac.proxy == nil {
		return nil
	}
	backends := ac.proxy.ListBackends()
	keys := make([]string, 0, len(backends))
	for key := range backends {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if b := backends[key]; proxyBackendUsable(b) {
			return b
		}
	}
	return nil
}

func proxyBackendUsable(b *proxy.Backend) bool {
	return b != nil && b.Ready && strings.TrimSpace(b.Address) != ""
}
