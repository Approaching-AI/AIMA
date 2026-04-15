package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jguan/aima/internal/stack"
)

// NormalizeInitTier turns a user-supplied tier name into the canonical token
// understood by stack init. An empty or unrecognised value defaults to docker.
func NormalizeInitTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "k3s":
		return "k3s"
	default:
		return "docker"
	}
}

// RunInit orchestrates the stack-init pipeline: check status -> preflight ->
// install. Events are accumulated in a slice (streamed to SSE by the HTTP
// handler, returned atomically to MCP/CLI callers). For a k3s install that
// finishes successfully, we trigger a best-effort engine import scan so the
// wizard can immediately propose recommendations.
func RunInit(ctx context.Context, deps *Deps, tier string, allowDownload bool, sink EventSink) (InitResult, []Event, error) {
	var events []Event
	emit := func(t string, data map[string]any) {
		ev := Event{Type: t, Timestamp: time.Now(), Data: data}
		events = append(events, ev)
		if sink != nil {
			sink(ev)
		}
	}

	if deps == nil || deps.ToolDeps == nil {
		return InitResult{}, nil, fmt.Errorf("onboarding init: deps not initialized")
	}
	td := deps.ToolDeps

	stackStatus, err := BuildStackStatus(ctx, deps)
	if err != nil {
		return InitResult{}, events, fmt.Errorf("check stack status: %w", err)
	}
	if !stackStatus.NeedsInit {
		// Emit per-component status so the wizard progress bar reflects the
		// real stack state instead of jumping straight to "complete". UAT
		// showed users couldn't tell whether docker/k3s were skipped because
		// they were ready or because the init silently bailed out.
		emit("init_component", map[string]any{
			"name":    "docker",
			"ready":   strings.EqualFold(stackStatus.Docker, "ready"),
			"skipped": true,
			"message": fmt.Sprintf("docker: %s", stackStatus.Docker),
		})
		emit("init_component", map[string]any{
			"name":    "k3s",
			"ready":   strings.EqualFold(stackStatus.K3S, "ready"),
			"skipped": true,
			"message": fmt.Sprintf("k3s: %s", stackStatus.K3S),
		})
		result := InitResult{AllReady: true, StackStatus: stackStatus, Tier: NormalizeInitTier(tier)}
		emit("init_complete", map[string]any{
			"all_ready":    true,
			"stack_status": stackStatus,
		})
		return result, events, nil
	}
	if !stackStatus.CanAutoInit {
		reason := strings.TrimSpace(stackStatus.InitBlockedReason)
		if reason == "" {
			reason = fmt.Sprintf("stack init not supported on this platform (tier=%s, docker=%s, k3s=%s)",
				NormalizeInitTier(tier), stackStatus.Docker, stackStatus.K3S)
		}
		return InitResult{StackStatus: stackStatus}, events, fmt.Errorf("stack init blocked: %s", reason)
	}
	if td.StackPreflight == nil || td.StackInit == nil {
		return InitResult{StackStatus: stackStatus}, events, fmt.Errorf("stack init is not available")
	}

	normalizedTier := NormalizeInitTier(tier)
	if strings.TrimSpace(tier) == "" {
		normalizedTier = NormalizeInitTier(stackStatus.InitTierRecommendation)
	}

	emit("init_phase", map[string]any{
		"phase":   "preflight",
		"message": fmt.Sprintf("checking %s stack prerequisites", normalizedTier),
		"tier":    normalizedTier,
	})

	preflightData, err := td.StackPreflight(ctx, normalizedTier)
	if err != nil {
		return InitResult{StackStatus: stackStatus, Tier: normalizedTier}, events, fmt.Errorf("stack preflight failed: %w", err)
	}
	var downloads []map[string]any
	_ = json.Unmarshal(preflightData, &downloads)
	emit("init_preflight", map[string]any{
		"tier":           normalizedTier,
		"allow_download": allowDownload,
		"downloads":      downloads,
		"download_count": len(downloads),
	})
	if len(downloads) > 0 && !allowDownload {
		return InitResult{StackStatus: stackStatus, Tier: normalizedTier}, events, fmt.Errorf("stack init requires downloads but allow_download=false")
	}

	emit("init_phase", map[string]any{
		"phase":   "init",
		"message": fmt.Sprintf("installing %s stack components", normalizedTier),
		"tier":    normalizedTier,
	})
	initData, err := td.StackInit(ctx, normalizedTier, allowDownload)
	if err != nil {
		return InitResult{StackStatus: stackStatus, Tier: normalizedTier}, events, fmt.Errorf("stack init failed: %w", err)
	}

	var result stack.InitResult
	if err := json.Unmarshal(initData, &result); err != nil {
		return InitResult{StackStatus: stackStatus, Tier: normalizedTier}, events, fmt.Errorf("parse stack init result: %w", err)
	}
	for _, comp := range result.Components {
		emit("init_component", map[string]any{
			"name":    comp.Name,
			"ready":   comp.Ready,
			"skipped": comp.Skipped,
			"message": comp.Message,
			"pods":    comp.Pods,
		})
	}

	// Best-effort post-init engine import (previously implemented in the
	// buildOnboardingDeps decorator; inlined here so MCP/CLI callers benefit
	// from the same side effect without the decorator indirection).
	if result.AllReady && normalizedTier == "k3s" && td.ScanEngines != nil {
		importCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		if _, scanErr := td.ScanEngines(importCtx, "auto", true); scanErr != nil {
			slog.Warn("onboarding init: post-init engine import failed", "tier", normalizedTier, "error", scanErr)
			emit("init_warning", map[string]any{
				"phase":   "engine_import",
				"message": scanErr.Error(),
			})
		}
		cancel()
	}

	updatedStatus, statusErr := BuildStackStatus(context.WithoutCancel(ctx), deps)
	if statusErr != nil {
		return InitResult{StackStatus: stackStatus, Tier: normalizedTier}, events, fmt.Errorf("refresh stack status: %w", statusErr)
	}
	emit("init_complete", map[string]any{
		"all_ready":    result.AllReady,
		"stack_status": updatedStatus,
	})

	return InitResult{
		AllReady:    result.AllReady,
		StackStatus: updatedStatus,
		Tier:        normalizedTier,
	}, events, nil
}
