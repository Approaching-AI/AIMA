package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"

	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/model"
	"github.com/jguan/aima/internal/runtime"

	state "github.com/jguan/aima/internal"
)

func findModelAsset(cat *knowledge.Catalog, name string) (*knowledge.ModelAsset, *knowledge.ModelSource) {
	// 1. Exact catalog name
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]
		if ma.Metadata.Name == name && len(ma.Storage.Sources) > 0 {
			return ma, &ma.Storage.Sources[0]
		}
	}
	// 2. Case-insensitive catalog name
	lower := strings.ToLower(name)
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]
		if strings.ToLower(ma.Metadata.Name) == lower && len(ma.Storage.Sources) > 0 {
			return ma, &ma.Storage.Sources[0]
		}
	}
	// 3. Exact source repo match
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]
		for j := range ma.Storage.Sources {
			src := &ma.Storage.Sources[j]
			if src.Repo == name {
				return ma, src
			}
		}
	}
	// 4. Source repo prefix match (e.g. "Qwen/Qwen3-8B-GGUF" matches repo "Qwen/Qwen3-8B")
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]
		for j := range ma.Storage.Sources {
			src := &ma.Storage.Sources[j]
			if src.Repo != "" && strings.HasPrefix(name, src.Repo) {
				return ma, src
			}
		}
	}
	return nil, nil
}

// findModelAssetOrVariant resolves a name to a model asset, optionally via variant name.
// Priority: model name (via findModelAsset) -> variant name match.
// When matched by variant name, the returned variant pointer is non-nil.
func findModelAssetOrVariant(cat *knowledge.Catalog, name string) (*knowledge.ModelAsset, *knowledge.ModelVariant) {
	// First try as model name
	if ma, _ := findModelAsset(cat, name); ma != nil {
		return ma, nil
	}
	// Then try as variant name
	lower := strings.ToLower(name)
	for i := range cat.ModelAssets {
		ma := &cat.ModelAssets[i]
		for j := range ma.Variants {
			if strings.ToLower(ma.Variants[j].Name) == lower {
				return ma, &ma.Variants[j]
			}
		}
	}
	return nil, nil
}

// registerPulledModel scans and registers a downloaded model in the database.
func registerPulledModel(ctx context.Context, destPath, dataDir string, db *state.DB) error {
	modelsDir := filepath.Join(dataDir, "models")
	info, err := model.Import(ctx, destPath, modelsDir)
	if err != nil {
		slog.Warn("model downloaded but scan/register failed", "path", destPath, "err", err)
		return nil // download succeeded; registration failure is non-fatal
	}
	return upsertScannedModelInfo(ctx, info, db)
}

func registerExistingModel(ctx context.Context, modelPath string, db *state.DB) error {
	absPath, err := filepath.Abs(modelPath)
	if err != nil {
		return fmt.Errorf("resolve local model path %s: %w", modelPath, err)
	}
	scanRoot := absPath
	if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
		scanRoot = filepath.Dir(absPath)
	} else {
		scanRoot = filepath.Dir(absPath)
	}

	models, err := model.Scan(ctx, model.ScanOptions{
		Paths:             []string{scanRoot},
		MinModelSizeBytes: 1,
	})
	if err != nil {
		return fmt.Errorf("scan existing model %s: %w", modelPath, err)
	}

	targetDir := absPath + string(filepath.Separator)
	for _, m := range models {
		if m.Path == absPath || strings.HasPrefix(m.Path, targetDir) {
			return upsertScannedModelInfo(ctx, m, db)
		}
	}
	return nil
}

func upsertScannedModelInfo(ctx context.Context, info *model.ModelInfo, db *state.DB) error {
	if info == nil {
		return nil
	}
	return db.UpsertScannedModel(ctx, &state.Model{
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
	})
}

func variantQuantizationHint(variant *knowledge.ModelVariant) string {
	if variant == nil {
		return ""
	}
	if variant.Source != nil && variant.Source.Quantization != "" {
		return strings.ToLower(variant.Source.Quantization)
	}
	if q, ok := variant.DefaultConfig["quantization"].(string); ok && q != "" {
		return strings.ToLower(q)
	}
	return ""
}

// catalogModelNames returns a comma-separated list of available model names.
func catalogModelNames(cat *knowledge.Catalog) string {
	names := make([]string, 0, len(cat.ModelAssets))
	for _, ma := range cat.ModelAssets {
		names = append(names, ma.Metadata.Name)
	}
	return strings.Join(names, ", ")
}

var overlayAssetNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// validateOverlayAssetName ensures the user-provided override name stays inside
// the overlay directory and is safe as a file basename.
func validateOverlayAssetName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("invalid name %q: path traversal is not allowed", name)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid name %q: path separators are not allowed", name)
	}
	if filepath.IsAbs(name) {
		return fmt.Errorf("invalid name %q: absolute paths are not allowed", name)
	}
	if !overlayAssetNamePattern.MatchString(name) {
		return fmt.Errorf("invalid name %q: only letters, digits, dot, underscore, and dash are allowed", name)
	}
	return nil
}

// findModelFileInDir returns the first model file found inside dir.
// Only called for native binary engines (where the engine YAML has a source: field);
// container engines receive the directory path directly.
func findModelFileInDir(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".gguf", ".ggml", ".bin", ".safetensors":
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

func deploymentMatchesQuery(d *runtime.DeploymentStatus, query string) bool {
	if d == nil {
		return false
	}
	if d.Name == query {
		return true
	}
	modelName := strings.TrimSpace(d.Model)
	engineName := strings.TrimSpace(d.Engine)
	if modelName == "" {
		modelName = strings.TrimSpace(d.Labels["aima.dev/model"])
	}
	if engineName == "" {
		engineName = strings.TrimSpace(d.Labels["aima.dev/engine"])
	}
	if modelName == query {
		return true
	}
	if modelName != "" && engineName != "" {
		return knowledge.SanitizePodName(modelName+"-"+engineName) == query
	}
	return false
}

func dirRequiresSingleFileModelPath(dir string) bool {
	if !model.PathLooksUsable(dir, "") {
		return false
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err == nil {
		return false
	}
	return findModelFileInDir(dir) != ""
}

// findModelDir searches alternative well-known locations for a model directory.
// Returns the first path that contains model files, or "" if none found.
// Because the primary dataDir is user-specific (~/.aima), models downloaded by
// a different user (e.g. root via systemd) may be inaccessible to the current user.
// For paths we can read, we verify model files exist. For paths we can't read
// (e.g. /root/.aima when running as non-root), we accept them if the directory
// exists -- Docker/K3S run as root and can access them.
func findModelDir(modelName, primaryDataDir, format, quantization string) string {
	parents := candidateModelParents(primaryDataDir)
	unreadableExact := make([]string, 0, len(parents))
	seen := make(map[string]bool)
	consider := func(path string, exact bool) string {
		if path == "" || seen[path] {
			return ""
		}
		seen[path] = true
		if model.PathLooksCompatible(path, format, quantization) {
			return path
		}
		if exact {
			if fi, err := os.Stat(path); err == nil && fi.IsDir() {
				if looksUnreadableModelDir(path) {
					unreadableExact = append(unreadableExact, path)
				}
			}
		}
		return ""
	}

	for _, parent := range parents {
		if found := consider(filepath.Join(parent, modelName), true); found != "" {
			return found
		}
	}

	for _, parent := range parents {
		entries, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !modelAliasMatches(entry.Name(), modelName) {
				continue
			}
			if found := consider(filepath.Join(parent, entry.Name()), false); found != "" {
				return found
			}
		}
	}

	if len(unreadableExact) > 0 {
		return unreadableExact[0]
	}
	return ""
}

func looksUnreadableModelDir(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		f, err := os.Open(filepath.Join(path, entry.Name()))
		if err != nil {
			return true
		}
		_ = f.Close()
		return false
	}
	return false
}

func candidateModelParents(primaryDataDir string) []string {
	parents := []string{
		filepath.Join(primaryDataDir, "models"),
		"/root/.aima/models",
		"/data/models",
		"/mnt/data/models",
	}
	if goruntime.GOOS == "linux" {
		if entries, err := os.ReadDir("/opt"); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				base := filepath.Join("/opt", entry.Name())
				parents = append(parents, filepath.Join(base, "models"))
				subEntries, err := os.ReadDir(base)
				if err != nil {
					continue
				}
				for _, sub := range subEntries {
					if sub.IsDir() {
						parents = append(parents, filepath.Join(base, sub.Name(), "models"))
					}
				}
			}
		}
	}
	uniq := make([]string, 0, len(parents))
	seen := make(map[string]bool)
	for _, parent := range parents {
		if parent == "" || seen[parent] {
			continue
		}
		seen[parent] = true
		uniq = append(uniq, parent)
	}
	return uniq
}

func modelAliasMatches(candidate, modelName string) bool {
	candidateNorm := normalizeModelAlias(candidate)
	modelNorm := normalizeModelAlias(modelName)
	if candidateNorm == "" || modelNorm == "" {
		return false
	}
	return candidateNorm == modelNorm ||
		strings.Contains(candidateNorm, modelNorm) ||
		strings.Contains(modelNorm, candidateNorm)
}

func normalizeModelAlias(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
