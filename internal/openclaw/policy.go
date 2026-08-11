package openclaw

import (
	"os"
	"strings"
)

const (
	// ConfigKeySyncMode controls implicit OpenClaw syncs such as serve's loop
	// and deploy-after-ready. Explicit `aima openclaw sync` still runs.
	ConfigKeySyncMode = "openclaw.sync"
	SyncModeAuto      = "auto"
	SyncModeManual    = "manual"
)

// AutoSyncEnabledForMode returns whether implicit OpenClaw sync is enabled for
// a configured mode. Empty/unrecognized values preserve the default auto mode.
func AutoSyncEnabledForMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case SyncModeManual, "off", "false", "0", "no":
		return false
	default:
		return true
	}
}

// AutoSyncEnabledFromEnv reads the process-level OpenClaw sync policy.
func AutoSyncEnabledFromEnv() bool {
	return AutoSyncEnabledForMode(os.Getenv("AIMA_OPENCLAW_SYNC"))
}
