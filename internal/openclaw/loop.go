package openclaw

import (
	"context"
	"log/slog"
	"time"
)

// StartSyncLoop keeps openclaw.json converged with the current ready local AIMA
// backends. The sync decision lives in the OpenClaw package instead of the CLI.
func StartSyncLoop(ctx context.Context, deps *Deps, interval time.Duration) {
	syncOnce := func() {
		status, err := Inspect(ctx, deps)
		if err != nil {
			slog.Warn("openclaw auto-sync: inspect failed", "error", err)
			return
		}
		if status == nil || status.SyncReady {
			return
		}
		if summaryCount(status.Expected) == 0 && !status.AIMAConfigured && (status.MCPServer == nil || status.MCPServer.Registered) {
			return
		}
		pluginDrift := status.PluginDrift

		if _, err := Sync(ctx, deps, false); err != nil {
			slog.Warn("openclaw auto-sync: sync failed", "error", err)
			return
		}

		if pluginDrift {
			slog.Info("openclaw auto-sync: plugin drift fixed, gateway file watcher will reload config")
		}
	}

	syncOnce()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			syncOnce()
		}
	}
}
