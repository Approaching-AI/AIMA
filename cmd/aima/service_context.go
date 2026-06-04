package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

func serviceContextStatus(ac *appContext) map[string]any {
	dataDir := ""
	var loadedAt time.Time
	if ac != nil {
		dataDir = strings.TrimSpace(ac.dataDir)
		loadedAt = ac.catalogLoadedAt
	}
	overlayDir := ""
	if dataDir != "" {
		overlayDir = filepath.Join(dataDir, "catalog")
	}
	latestOverlay := latestModTime(overlayDir)

	status := map[string]any{
		"data_dir":    dataDir,
		"overlay_dir": overlayDir,
	}
	if !loadedAt.IsZero() {
		status["catalog_loaded_at"] = loadedAt.Format(time.RFC3339)
	}
	if !latestOverlay.IsZero() {
		status["overlay_latest_mtime"] = latestOverlay.Format(time.RFC3339)
		if !loadedAt.IsZero() && latestOverlay.After(loadedAt) {
			status["overlay_newer_than_catalog"] = true
			status["reload_hint"] = "catalog overlays changed after this AIMA process loaded; restart aima-serve or reload catalog before trusting UI dry-run results"
		}
	}
	if user := os.Getenv("USER"); user != "" {
		status["user"] = user
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		status["home_dir"] = home
	}
	return status
}

func latestModTime(root string) time.Time {
	if strings.TrimSpace(root) == "" {
		return time.Time{}
	}
	var latest time.Time
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	return latest
}
