package model

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ModelInfo represents a discovered local model.
type ModelInfo struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	Path           string `json:"path"`
	Format         string `json:"format"`
	SizeBytes      int64  `json:"size_bytes"`
	DetectedArch   string `json:"detected_arch"`
	DetectedParams string `json:"detected_params"` // Legacy: e.g., "8B"

	// Enhanced metadata fields
	ModelClass   string `json:"model_class"`   // dense | moe | hybrid | unknown
	TotalParams  int64  `json:"total_params"`  // Exact parameter count (0 = unknown)
	ActiveParams int64  `json:"active_params"` // For MOE: active parameters per token
	Quantization string `json:"quantization"`  // int8 | int4 | fp8 | fp16 | bf16 | nf4 | fp32 | unknown
	QuantSrc     string `json:"quant_src"`     // config | filename | header | unknown
}

// ScanOptions configures which directories to scan.
type ScanOptions struct {
	Paths             []string
	MinModelSizeBytes int64       // override default 10MB floor; 0 means use default
	Config            *ScanConfig // nil means use NewScanConfig() defaults
}

// DefaultScanPaths returns platform-appropriate default scan locations.
func DefaultScanPaths() []string {
	var paths []string

	if dir := os.Getenv("AIMA_MODEL_DIR"); dir != "" {
		paths = append(paths, dir)
	}

	home, err := os.UserHomeDir()
	if err == nil {
		paths = append(paths,
			filepath.Join(home, ".aima", "models"),
			filepath.Join(home, ".cache", "huggingface", "hub"),
			filepath.Join(home, ".ollama", "models"),
		)
	}

	if runtime.GOOS == "linux" {
		paths = append(paths,
			"/mnt/data/models",
			filepath.Join(home, "data/models"),
		)
		// Discover vendor-preloaded model directories under /opt (e.g. /opt/mt-ai/llm/models).
		paths = append(paths, discoverOptModelPaths()...)
	}

	return paths
}

// discoverOptModelPaths finds vendor-preloaded model directories under /opt.
// Checks two levels: /opt/*/models, /opt/*/llm/models, and /opt/*/*/models
// to cover structures like /opt/mt-ai/emb/models/, /opt/mt-ai/llm/models/.
func discoverOptModelPaths() []string {
	entries, err := os.ReadDir("/opt")
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var paths []string
	add := func(p string) {
		if !seen[p] {
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				paths = append(paths, p)
				seen[p] = true
			}
		}
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		base := filepath.Join("/opt", e.Name())
		// Level 1: /opt/*/models
		add(filepath.Join(base, "models"))
		// Level 2: /opt/*/llm/models, /opt/*/emb/models, etc.
		subEntries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, sub := range subEntries {
			if !sub.IsDir() {
				continue
			}
			add(filepath.Join(base, sub.Name(), "models"))
		}
	}
	return paths
}

// Scan discovers models in the given directories.
func Scan(ctx context.Context, opts ScanOptions) ([]*ModelInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("scan models: %w", err)
	}

	cfg := opts.Config
	if cfg == nil {
		cfg = NewScanConfig()
	}

	effectiveMinSize := cfg.MinModelSize
	if opts.MinModelSizeBytes > 0 {
		effectiveMinSize = opts.MinModelSizeBytes
	}

	paths := opts.Paths
	if len(paths) == 0 {
		paths = DefaultScanPaths()
	}

	var models []*ModelInfo
	seen := make(map[string]bool)
	skipParentPaths := make(map[string]bool)

	for _, root := range paths {
		info, err := os.Stat(root)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			continue
		}

		found, err := scanDirectory(ctx, root, 0, seen, skipParentPaths, effectiveMinSize, cfg)
		if err != nil {
			return nil, err
		}
		models = append(models, found...)
	}

	return models, nil
}

func scanDirectory(ctx context.Context, dir string, depth int, seen map[string]bool, skipParentPaths map[string]bool, minSize int64, cfg *ScanConfig) ([]*ModelInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("scan directory %s: %w", dir, err)
	}
	if depth > cfg.MaxScanDepth {
		return nil, nil
	}

	var models []*ModelInfo

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", dir, err)
	}

	// Check if this is a parent pipeline (e.g., diffusion model)
	if isParentPipeline(dir, entries, cfg) {
		skipParentPaths[dir] = true
	}

	// First, recurse into subdirectories
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		subdirName := strings.ToLower(entry.Name())
		subdir := filepath.Join(dir, entry.Name())

		// Skip subdirectories with known component names
		if cfg.SkipSubdirNames[subdirName] {
			continue
		}

		// Skip if parent of this directory is a known parent model
		if shouldSkipParentChild(subdir, skipParentPaths) {
			continue
		}

		subModels, err := scanDirectory(ctx, subdir, depth+1, seen, skipParentPaths, minSize, cfg)
		if err == nil {
			models = append(models, subModels...)
		}
	}

	// Then check if current directory itself is a model (after recursion)
	// Skip if this is a known component subdirectory
	if cfg.SkipSubdirNames[strings.ToLower(filepath.Base(dir))] {
		return models, nil
	}

	// Check if any subdirectory was detected as a model (container directory detection)
	hasModelSubdirs := false
	for _, entry := range entries {
		if entry.IsDir() {
			subdirPath := filepath.Join(dir, entry.Name())
			prefix := subdirPath + string(filepath.Separator)
			for _, m := range models {
				// Match directory path (safetensors/pytorch) or file within it (GGUF)
				if m.Path == subdirPath || strings.HasPrefix(m.Path, prefix) {
					hasModelSubdirs = true
					break
				}
			}
			if hasModelSubdirs {
				break
			}
		}
	}

	// If a subdirectory is a model, don't detect the parent as a model
	if hasModelSubdirs {
		return models, nil
	}

	for _, m := range tryDetectModel(ctx, dir, entries, minSize, cfg) {
		if !seen[m.Path] {
			seen[m.Path] = true
			models = append(models, m)
		}
	}

	return models, nil
}
