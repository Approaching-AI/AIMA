package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/model"

	state "github.com/jguan/aima/internal"
)

// buildModelDeps wires model.scan, model.list, model.pull, model.import,
// model.info, and model.remove tools.
func buildModelDeps(ac *appContext, deps *mcp.ToolDeps,
	pullModelCore func(ctx context.Context, name string, onStatus func(phase, msg string), onProgress func(downloaded, total int64)) error,
	dlTracker *DownloadTracker,
) {
	cat := ac.cat
	db := ac.db
	dataDir := ac.dataDir

	deps.ScanModels = func(ctx context.Context) (json.RawMessage, error) {
		models, err := model.Scan(ctx, model.ScanOptions{})
		if err != nil {
			return nil, err
		}
		for _, m := range models {
			_ = db.UpsertScannedModel(ctx, &state.Model{
				ID:             m.ID,
				Name:           m.Name,
				Type:           m.Type,
				Path:           m.Path,
				Format:         m.Format,
				SizeBytes:      m.SizeBytes,
				DetectedArch:   m.DetectedArch,
				DetectedParams: m.DetectedParams,
				ModelClass:     m.ModelClass,
				TotalParams:    m.TotalParams,
				ActiveParams:   m.ActiveParams,
				Quantization:   m.Quantization,
				QuantSrc:       m.QuantSrc,
			})
		}
		return json.Marshal(models)
	}

	deps.ListModels = func(ctx context.Context) (json.RawMessage, error) {
		models, err := db.ListModels(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(models)
	}

	deps.PullModel = func(ctx context.Context, name string) error {
		dlID := fmt.Sprintf("model-%s-%d", name, time.Now().UnixMilli())
		dlTracker.Start(dlID, "model", name)
		dlTracker.Update(dlID, "downloading", "Resolving model...", -1, -1, -1)
		keepAliveStop := make(chan struct{})
		go dlTracker.KeepAlive(dlID, keepAliveStop)

		err := func() error {
			defer close(keepAliveStop)
			return pullModelCore(
				ctx,
				name,
				func(phase, msg string) {
					dlTracker.Update(dlID, phase, msg, -1, -1, -1)
				},
				newByteProgressReporter(dlTracker, dlID, "downloading"),
			)
		}()

		dlTracker.Finish(dlID, err)
		return err
	}

	deps.ImportModel = func(ctx context.Context, path string) (json.RawMessage, error) {
		destDir := filepath.Join(dataDir, "models")
		info, err := model.Import(ctx, path, destDir)
		if err != nil {
			return nil, err
		}
		// Register imported model in database
		if err := db.UpsertScannedModel(ctx, &state.Model{
			ID:             info.ID,
			Name:           info.Name,
			Type:           info.Type,
			Path:           info.Path,
			Format:         info.Format,
			SizeBytes:      info.SizeBytes,
			DetectedArch:   info.DetectedArch,
			DetectedParams: info.DetectedParams,
			ModelClass:     info.ModelClass,
			TotalParams:    info.TotalParams,
			ActiveParams:   info.ActiveParams,
			Quantization:   info.Quantization,
			QuantSrc:       info.QuantSrc,
			Status:         "registered",
		}); err != nil {
			return nil, fmt.Errorf("register imported model: %w", err)
		}
		// Wrap info with engine_hint derived from catalog (INV-5: MCP response is the source of truth)
		raw, err := json.Marshal(info)
		if err != nil {
			return nil, err
		}
		var result map[string]any
		json.Unmarshal(raw, &result) //nolint:errcheck
		if hint := cat.FormatToEngine(info.Format); hint != "" {
			result["engine_hint"] = hint
		}
		return json.Marshal(result)
	}

	deps.GetModelInfo = func(ctx context.Context, name string) (json.RawMessage, error) {
		m, err := db.GetModel(ctx, name)
		if err != nil {
			return nil, err
		}
		return json.Marshal(m)
	}

	deps.RemoveModel = func(ctx context.Context, name string, deleteFiles bool) error {
		// First get the model to find its ID and Path
		m, err := db.GetModel(ctx, name)
		if err != nil {
			return fmt.Errorf("find model %s: %w", name, err)
		}
		// Gap 3: Save rollback snapshot before deletion
		if snap, snapErr := json.Marshal(m); snapErr == nil {
			_ = db.SaveSnapshot(ctx, &state.RollbackSnapshot{
				ToolName: "model.remove", ResourceType: "model", ResourceName: m.Name, Snapshot: string(snap),
			})
		}
		// Delete from database
		if err := db.DeleteModel(ctx, m.ID); err != nil {
			return fmt.Errorf("delete model %s from database: %w", name, err)
		}
		// Delete files from disk if requested
		if deleteFiles {
			if m.Path != "" {
				// For GGUF models, Path is the file path itself
				// For other models, Path is the directory
				info, statErr := os.Stat(m.Path)
				if statErr == nil {
					if info.IsDir() {
						os.RemoveAll(m.Path)
					} else {
						os.Remove(m.Path)
					}
				}
			}
		}
		return nil
	}
}
