package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jguan/aima/catalog"
	"github.com/jguan/aima/internal/knowledge"

	state "github.com/jguan/aima/internal"
)

// initCatalog loads the embedded YAML catalog, merges disk overlays, syncs
// static knowledge into the SQLite relational tables, and returns the merged
// catalog together with the factory digests used for overlay staleness detection.
func initCatalog(ctx context.Context, db *state.DB, dataDir string) (*knowledge.Catalog, map[string]string, error) {
	// 3. Load knowledge catalog (embedded YAML -> in-memory structs)
	cat, err := knowledge.LoadCatalog(catalog.FS)
	if err != nil {
		return nil, nil, fmt.Errorf("load catalog: %w", err)
	}

	// 3b. Merge overlay catalog from disk (if present) with staleness detection
	overlayDir := filepath.Join(dataDir, "catalog")
	factoryDigests := knowledge.ComputeDigests(catalog.FS)
	if info, e := os.Stat(overlayDir); e == nil && info.IsDir() {
		overlayFS := os.DirFS(overlayDir)
		overlayCat, parseWarnings := knowledge.LoadCatalogLenient(overlayFS)
		for _, w := range parseWarnings {
			slog.Warn("overlay file skipped", "reason", w)
		}
		before := catalogSize(cat)
		cat, staleWarnings := knowledge.MergeCatalogWithDigests(cat, overlayCat, factoryDigests, overlayFS)
		// UAT noise reduction: per-file stale warnings spammed startup logs on
		// machines with large overlays. Aggregate to a single summary line and
		// emit individual warnings at Debug level for diagnostics.
		for _, w := range staleWarnings {
			slog.Debug("overlay stale detail", "detail", w)
		}
		if len(staleWarnings) > 0 {
			slog.Info("catalog overlay has stale entries; review recommended",
				"stale_count", len(staleWarnings),
				"dir", overlayDir,
			)
		}
		slog.Info("catalog overlay merged",
			"dir", overlayDir,
			"overlay_assets", catalogSize(overlayCat),
			"new_assets", catalogSize(cat)-before,
		)
	}

	// 4. Load static knowledge into SQLite relational tables (only when catalog changes).
	if err := syncCatalogToSQLite(ctx, db, cat); err != nil {
		return nil, nil, err
	}
	if err := db.Analyze(ctx); err != nil {
		slog.Warn("analyze failed", "error", err)
	}

	return cat, factoryDigests, nil
}
