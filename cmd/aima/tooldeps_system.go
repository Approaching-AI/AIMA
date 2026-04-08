package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/jguan/aima/internal/buildinfo"
	"github.com/jguan/aima/internal/hal"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/runtime"
	"github.com/jguan/aima/internal/stack"
)

// buildSystemDeps wires hal.detect, hal.metrics,
// stack.preflight/init/status, system.config (get/set),
// and system.status tools.
func buildSystemDeps(ac *appContext, deps *mcp.ToolDeps) {
	cat := ac.cat
	db := ac.db
	rt := ac.rt
	nativeRt := ac.nativeRt
	k3sClient := ac.k3s
	dataDir := ac.dataDir

	// Hardware
	deps.DetectHardware = func(ctx context.Context) (json.RawMessage, error) {
		hw, err := hal.Detect(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(hw)
	}
	deps.CollectMetrics = func(ctx context.Context) (json.RawMessage, error) {
		m, err := hal.CollectMetrics(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(m)
	}


	// Stack management
	deps.StackPreflight = func(ctx context.Context, tier string) (json.RawMessage, error) {
		installer := stack.NewInstaller(&execRunner{}, dataDir).
			WithPodQuerier(&podQuerierAdapter{client: k3sClient})
		hwProfile := detectHWProfile(ctx, cat)
		components := stack.FilterByTier(cat.StackComponents, tier)
		items := installer.Preflight(ctx, components, hwProfile)
		return json.Marshal(items)
	}
	deps.StackInit = func(ctx context.Context, tier string, allowDownload bool) (json.RawMessage, error) {
		installer := stack.NewInstaller(&execRunner{}, dataDir).
			WithPodQuerier(&podQuerierAdapter{client: k3sClient})
		components := stack.FilterByTier(cat.StackComponents, tier)
		if err := installer.PreCheck(ctx, components); err != nil {
			return nil, err
		}
		hwProfile := detectHWProfile(ctx, cat)
		if allowDownload {
			missing := installer.Preflight(ctx, components, hwProfile)
			if err := stack.DownloadItems(ctx, missing); err != nil {
				return nil, fmt.Errorf("download: %w", err)
			}
		}
		result, err := installer.Init(ctx, components, hwProfile)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	deps.StackStatus = func(ctx context.Context) (json.RawMessage, error) {
		installer := stack.NewInstaller(&execRunner{}, dataDir).
			WithPodQuerier(&podQuerierAdapter{client: k3sClient})
		hwProfile := detectHWProfile(ctx, cat)
		result, err := installer.Status(ctx, cat.StackComponents, hwProfile)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}

	deps.GetConfig = func(ctx context.Context, key string) (string, error) {
		return db.GetConfig(ctx, key)
	}
	deps.SetConfig = func(ctx context.Context, key, value string) error {
		return db.SetConfig(ctx, key, value)
	}
	// SystemStatus reads deps.OpenClawStatus which is set after this builder
	// runs. This is safe because SystemStatus is a closure — it captures the
	// deps pointer and dereferences it at call time, not at definition time.
	deps.SystemStatus = func(ctx context.Context) (json.RawMessage, error) {
		status := map[string]json.RawMessage{}
		if hw, err := hal.Detect(ctx); err == nil {
			if b, e := json.Marshal(hw); e == nil {
				status["hardware"] = b
			}
		} else {
			return nil, fmt.Errorf("detect hardware: %w", err)
		}
		// Non-fatal: K3S may not be running
		pods, _ := rt.List(ctx)
		if pods == nil {
			pods = make([]*runtime.DeploymentStatus, 0)
		}
		if b, e := json.Marshal(pods); e == nil {
			status["deployments"] = b
		}
		if nativeRt != nil && nativeRt != rt {
			if nativePods, err := nativeRt.List(ctx); err == nil && len(nativePods) > 0 {
				if b, e := json.Marshal(nativePods); e == nil {
					status["native_deployments"] = b
				}
			}
		}
		if m, err := hal.CollectMetrics(ctx); err == nil {
			if b, e := json.Marshal(m); e == nil {
				status["metrics"] = b
			}
		}
		// Add hostname, version, and primary IP for device identification
		if hostname, err := os.Hostname(); err == nil {
			if b, e := json.Marshal(hostname); e == nil {
				status["hostname"] = b
			}
		}
		if b, e := json.Marshal(buildinfo.Version); e == nil {
			status["version"] = b
		}
		if b, e := json.Marshal(ac.support.Status(ctx)); e == nil {
			status["support"] = b
		}
		if deps.OpenClawStatus != nil {
			if b, e := deps.OpenClawStatus(ctx); e == nil {
				status["openclaw"] = b
			}
		}
		return json.Marshal(status)
	}
}
