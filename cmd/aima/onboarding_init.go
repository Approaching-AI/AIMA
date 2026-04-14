package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/stack"
)

type onboardingInitRequest struct {
	Tier          string `json:"tier,omitempty"`
	AllowDownload *bool  `json:"allow_download,omitempty"`
}

func normalizeOnboardingInitTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "k3s":
		return "k3s"
	default:
		return "docker"
	}
}

func handleOnboardingInit(ac *appContext, deps *mcp.ToolDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireOnboardingMutation(ac, w, r) {
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1*1024*1024))
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		if len(body) == 0 {
			body = []byte(`{}`)
		}

		var req onboardingInitRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
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

		send := func(event string, v any) {
			sseJSON(w, flusher, event, v)
		}
		sendError := func(message string, err error) {
			if err != nil && strings.TrimSpace(err.Error()) != "" {
				message = err.Error()
			}
			send("error", map[string]any{"message": message})
		}

		ctx := r.Context()
		stackStatus, err := buildOnboardingStackStatus(ctx, deps)
		if err != nil {
			sendError("check stack status", err)
			return
		}
		if !stackStatus.NeedsInit {
			send("init_complete", map[string]any{
				"all_ready":    true,
				"stack_status": stackStatus,
			})
			return
		}
		if !stackStatus.CanAutoInit {
			sendError(stackStatus.InitBlockedReason, nil)
			return
		}
		if deps == nil || deps.StackPreflight == nil || deps.StackInit == nil {
			sendError("stack init is not available", nil)
			return
		}

		tier := normalizeOnboardingInitTier(req.Tier)
		if strings.TrimSpace(req.Tier) == "" {
			tier = normalizeOnboardingInitTier(stackStatus.InitTierRecommendation)
		}
		allowDownload := true
		if req.AllowDownload != nil {
			allowDownload = *req.AllowDownload
		}

		send("init_phase", map[string]any{
			"phase":   "preflight",
			"message": fmt.Sprintf("checking %s stack prerequisites", tier),
			"tier":    tier,
		})

		preflightData, err := deps.StackPreflight(ctx, tier)
		if err != nil {
			sendError("stack preflight failed", err)
			return
		}
		var downloads []map[string]any
		_ = json.Unmarshal(preflightData, &downloads)
		send("init_preflight", map[string]any{
			"tier":           tier,
			"allow_download": allowDownload,
			"downloads":      downloads,
			"download_count": len(downloads),
		})
		if len(downloads) > 0 && !allowDownload {
			sendError("stack init requires downloads but allow_download=false", nil)
			return
		}

		send("init_phase", map[string]any{
			"phase":   "init",
			"message": fmt.Sprintf("installing %s stack components", tier),
			"tier":    tier,
		})
		initData, err := deps.StackInit(ctx, tier, allowDownload)
		if err != nil {
			sendError("stack init failed", err)
			return
		}

		var result stack.InitResult
		if err := json.Unmarshal(initData, &result); err != nil {
			sendError("parse stack init result", err)
			return
		}
		for _, comp := range result.Components {
			send("init_component", map[string]any{
				"name":    comp.Name,
				"ready":   comp.Ready,
				"skipped": comp.Skipped,
				"message": comp.Message,
				"pods":    comp.Pods,
			})
		}

		updatedStatus, statusErr := buildOnboardingStackStatus(context.WithoutCancel(ctx), deps)
		if statusErr != nil {
			sendError("refresh stack status", statusErr)
			return
		}
		send("init_complete", map[string]any{
			"all_ready":    result.AllReady,
			"stack_status": updatedStatus,
		})
	}
}
