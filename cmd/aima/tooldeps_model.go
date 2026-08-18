package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/model"

	state "github.com/jguan/aima/internal"
)

func registerCatalogLocalModels(ctx context.Context, cat *knowledge.Catalog, db *state.DB) error {
	if cat == nil {
		return nil
	}
	existingSizes, err := modelSizesByPath(ctx, db)
	if err != nil {
		return err
	}
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]
		if err := registerCatalogLocalModel(ctx, ma, db, existingSizes); err != nil {
			return err
		}
	}
	return nil
}

func modelSizesByPath(ctx context.Context, db *state.DB) (map[string]int64, error) {
	models, err := db.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list existing model sizes: %w", err)
	}
	sizes := make(map[string]int64, len(models))
	for _, m := range models {
		if m == nil || strings.TrimSpace(m.Path) == "" || m.SizeBytes <= 0 {
			continue
		}
		sizes[m.Path] = m.SizeBytes
	}
	return sizes, nil
}

func registerCatalogLocalModel(ctx context.Context, ma *knowledge.ModelAsset, db *state.DB, existingSizes map[string]int64) error {
	if ma == nil {
		return nil
	}
	for _, candidate := range catalogLocalModelCandidates(ma) {
		info, err := os.Stat(candidate.path)
		if err != nil || !info.IsDir() {
			continue
		}
		return db.UpsertScannedModel(ctx, &state.Model{
			ID:                 fmt.Sprintf("%x", sha256.Sum256([]byte(candidate.path+"|"+ma.Metadata.Name))),
			Name:               ma.Metadata.Name,
			Type:               ma.Metadata.Type,
			Path:               candidate.path,
			Format:             candidate.format,
			SizeBytes:          existingSizes[candidate.path],
			DetectedArch:       candidate.detectedArch,
			ModelClass:         strings.TrimSpace(ma.Metadata.ModelClass),
			UIRole:             strings.TrimSpace(ma.UI.Role),
			UIDisplayNote:      strings.TrimSpace(ma.UI.DisplayNote),
			UIDisplayNoteZh:    strings.TrimSpace(ma.UI.DisplayNoteZh),
			StandaloneDeploy:   ma.Capabilities.StandaloneDeploy,
			DeploymentScenario: strings.TrimSpace(ma.Capabilities.DeploymentScenario),
			Status:             "registered",
		})
	}
	return nil
}

type catalogLocalModelCandidate struct {
	path         string
	detectedArch string
	format       string
}

func catalogLocalModelCandidates(ma *knowledge.ModelAsset) []catalogLocalModelCandidate {
	if ma == nil {
		return nil
	}
	defaultFormat := firstCatalogFormat(ma)
	detectedArch := inferDetectedArch(ma)
	seen := make(map[string]struct{})
	var candidates []catalogLocalModelCandidate
	add := func(path, candidateFormat string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		candidates = append(candidates, catalogLocalModelCandidate{
			path:         path,
			detectedArch: detectedArch,
			format:       firstNonEmpty(strings.TrimSpace(candidateFormat), defaultFormat),
		})
	}
	for _, variant := range ma.Variants {
		if variant.Source != nil && variant.Source.Type == "local_path" && strings.TrimSpace(variant.Source.Path) != "" {
			add(variant.Source.Path, variant.Format)
		}
	}
	for _, src := range ma.Storage.Sources {
		if src.Type == "local_path" && strings.TrimSpace(src.Path) != "" {
			add(src.Path, src.Format)
		}
	}
	return candidates
}

func inferDetectedArch(ma *knowledge.ModelAsset) string {
	if ma == nil {
		return ""
	}
	family := strings.TrimSpace(strings.ToLower(ma.Metadata.Family))
	if family != "" {
		return family
	}
	return strings.TrimSpace(strings.ToLower(ma.Metadata.Name))
}

func firstCatalogFormat(ma *knowledge.ModelAsset) string {
	if ma == nil {
		return ""
	}
	if len(ma.Storage.Formats) > 0 {
		return strings.TrimSpace(ma.Storage.Formats[0])
	}
	return ""
}

func annotateModelsFromCatalog(models []*state.Model, cat *knowledge.Catalog) {
	if cat == nil {
		return
	}
	assetsByName := make(map[string]*knowledge.ModelAsset)
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]
		assetsByName[strings.ToLower(strings.TrimSpace(ma.Metadata.Name))] = ma
		for _, alias := range ma.Metadata.Aliases {
			assetsByName[strings.ToLower(strings.TrimSpace(alias))] = ma
		}
	}
	draftKeys := cat.SpeculativeDraftModelKeys()

	for _, m := range models {
		if m == nil {
			continue
		}
		if ma := assetsByName[strings.ToLower(strings.TrimSpace(m.Name))]; ma != nil {
			if strings.TrimSpace(m.ModelClass) == "" {
				m.ModelClass = strings.TrimSpace(ma.Metadata.ModelClass)
			}
			if strings.TrimSpace(m.UIRole) == "" {
				m.UIRole = strings.TrimSpace(ma.UI.Role)
			}
			if strings.TrimSpace(m.UIDisplayNote) == "" {
				m.UIDisplayNote = strings.TrimSpace(ma.UI.DisplayNote)
			}
			if strings.TrimSpace(m.UIDisplayNoteZh) == "" {
				m.UIDisplayNoteZh = strings.TrimSpace(ma.UI.DisplayNoteZh)
			}
			if m.StandaloneDeploy == nil {
				m.StandaloneDeploy = ma.Capabilities.StandaloneDeploy
			}
			if strings.TrimSpace(m.DeploymentScenario) == "" {
				m.DeploymentScenario = strings.TrimSpace(ma.Capabilities.DeploymentScenario)
			}
		}

		// Speculative draft heads (e.g. DFlash/MTP) are companions of their
		// parent model — the catalog names them via each variant's
		// speculative_config.model — not independently deployable models.
		if draftKeys[knowledge.NormalizeModelKey(m.Name)] {
			if m.StandaloneDeploy == nil {
				notStandalone := false
				m.StandaloneDeploy = &notStandalone
			}
			if strings.TrimSpace(m.UIRole) == "" {
				m.UIRole = "draft"
			}
		}
	}
}

func isDeployableModelRecord(m *state.Model) bool {
	if m == nil || strings.TrimSpace(m.Name) == "" {
		return false
	}
	if m.StandaloneDeploy != nil {
		return *m.StandaloneDeploy
	}

	modelType := strings.ToLower(strings.TrimSpace(m.Type))
	format := strings.ToLower(strings.TrimSpace(m.Format))
	modelClass := strings.ToLower(strings.TrimSpace(m.ModelClass))
	uiRole := strings.ToLower(strings.TrimSpace(m.UIRole))
	if uiRole == "component" || uiRole == "draft" || modelClass == "component" {
		return false
	}
	if uiRole == "deployable" {
		return true
	}

	switch modelType {
	case "llm", "vlm", "embedding", "image_gen":
		return true
	case "asr", "tts":
		return modelClass == "pipeline"
	}
	if format == "gguf" {
		return true
	}
	if format == "safetensors" {
		switch modelClass {
		case "dense", "moe", "hybrid", "diffusion", "pipeline":
			return true
		}
	}
	return (format == "onnx" || format == "mnn") && modelClass == "pipeline"
}

func listDeployableModelRecords(ctx context.Context, db *state.DB, cat *knowledge.Catalog) ([]*state.Model, error) {
	models, err := db.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	models = foldCatalogAliasModels(cat, models)
	annotateModelsFromCatalog(models, cat)
	deployable := make([]*state.Model, 0, len(models))
	for _, m := range models {
		if isDeployableModelRecord(m) {
			deployable = append(deployable, m)
		}
	}
	return deployable, nil
}

// buildModelDeps wires model.scan, model.list, model.pull, model.import,
// model.info, and model.remove tools.
func buildModelDeps(ac *appContext, deps *mcp.ToolDeps,
	pullModelCore func(ctx context.Context, name string, onStatus func(phase, msg string), onProgress func(downloaded, total int64)) error,
	dlTracker *DownloadTracker,
) {
	cat := ac.cat
	db := ac.db
	dataDir := ac.dataDir
	eventBus := ac.eventBus

	deps.ScanModels = func(ctx context.Context) (json.RawMessage, error) {
		models, err := model.Scan(ctx, model.ScanOptions{})
		if err != nil {
			return nil, err
		}
		for _, m := range models {
			existing, _ := db.GetModel(ctx, m.Name)
			isNew := existing == nil
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
			if isNew && eventBus != nil {
				eventBus.Publish(agent.ExplorerEvent{Type: agent.EventModelDiscovered, Model: m.Name})
			}
		}
		if err := registerCatalogLocalModels(ctx, cat, db); err != nil {
			return nil, fmt.Errorf("register catalog local models: %w", err)
		}
		deployable, err := listDeployableModelRecords(ctx, db, cat)
		if err != nil {
			return nil, fmt.Errorf("list deployable scanned models: %w", err)
		}
		return json.Marshal(deployable)
	}

	deps.ListModels = func(ctx context.Context) (json.RawMessage, error) {
		models, err := db.ListModels(ctx)
		if err != nil {
			return nil, err
		}
		models = foldCatalogAliasModels(cat, models)
		annotateModelsFromCatalog(models, cat)
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
		if eventBus != nil {
			eventBus.Publish(agent.ExplorerEvent{Type: agent.EventModelDiscovered, Model: info.Name})
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
		annotateModelsFromCatalog([]*state.Model{m}, cat)
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

func foldCatalogAliasModels(cat *knowledge.Catalog, models []*state.Model) []*state.Model {
	if cat == nil || len(models) < 2 {
		return models
	}

	canonicalPresent := make(map[string]bool)
	for _, m := range models {
		if m == nil {
			continue
		}
		name := strings.TrimSpace(m.Name)
		canonical := strings.TrimSpace(cat.ResolveCatalogModelName(name))
		if canonical == "" || !sameModelListName(name, canonical) {
			continue
		}
		canonicalPresent[modelListNameKey(canonical)] = true
	}
	if len(canonicalPresent) == 0 {
		return models
	}

	filtered := make([]*state.Model, 0, len(models))
	for _, m := range models {
		if m == nil {
			continue
		}
		name := strings.TrimSpace(m.Name)
		canonical := strings.TrimSpace(cat.ResolveCatalogModelName(name))
		canonicalKey := modelListNameKey(canonical)
		if canonicalPresent[canonicalKey] && canonicalKey != modelListNameKey(name) {
			continue
		}
		filtered = append(filtered, m)
	}
	return filtered
}

func sameModelListName(a, b string) bool {
	return modelListNameKey(a) == modelListNameKey(b)
}

func modelListNameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
