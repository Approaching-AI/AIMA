package model

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func tryDetectModel(_ context.Context, dir string, entries []os.DirEntry, minSize int64, cfg *ScanConfig) []*ModelInfo {
	for _, p := range cfg.ModelPatterns {
		if ms := detectByPattern(dir, entries, p, minSize); len(ms) > 0 {
			return ms
		}
	}
	return nil
}

func detectByPattern(dir string, entries []os.DirEntry, p ModelPattern, minSize int64) []*ModelInfo {
	if !hasConfigFile(entries, p.configFiles) {
		return nil
	}
	config := loadPatternConfig(dir, entries, p.configFiles)
	if !patternMatchesIndicator(config, p) {
		return nil
	}

	// For GGUF format, detect all .gguf files in the directory
	if p.format == "gguf" {
		return detectGGUFModels(dir, entries, p, minSize)
	}

	weightPath := findWeightFile(dir, entries, p.weightExts)
	if p.recursiveWeights {
		weightPath = findWeightFileRecursive(dir, p.weightExts)
	}
	if weightPath == "" {
		return nil
	}

	return buildModelInfo(dir, entries, p, config, weightPath, "", minSize)
}

func isParentPipeline(dir string, entries []os.DirEntry, cfg *ScanConfig) bool {
	for _, pp := range cfg.ParentPatterns {
		for _, indicatorFile := range pp.IndicatorFiles {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				if entry.Name() != indicatorFile {
					continue
				}

				data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
				if err != nil {
					continue
				}

				var config map[string]any
				if err := json.Unmarshal(data, &config); err != nil {
					continue
				}

				if classValue, ok := config[pp.IndicatorField].(string); ok {
					if strings.Contains(classValue, pp.IndicatorValueContains) {
						return true
					}
				}
			}
		}
	}
	return false
}

func shouldSkipParentChild(dir string, skipParentPaths map[string]bool) bool {
	for parentPath := range skipParentPaths {
		if strings.HasPrefix(dir, parentPath+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// buildModelInfo builds a ModelInfo from a single weight file.
// For formats with config files (safetensors, pytorch, etc.).
func buildModelInfo(dir string, entries []os.DirEntry, p ModelPattern, config map[string]any, weightPath string, overrideName string, minSize int64) []*ModelInfo {
	modelType, _ := config["model_type"].(string)
	arch := detectArch(modelType)
	mType := p.typeHint
	if mType == "" && arch != "" {
		mType = detectModelType(arch)
	}

	// Task-field type mapping (driven by pattern's taskMappings from YAML)
	if (mType == "" || mType == "llm") && config != nil && len(p.taskMappings) > 0 {
		if task, _ := config["task"].(string); task != "" {
			for substring, modelType := range p.taskMappings {
				if strings.Contains(task, substring) {
					mType = modelType
					break
				}
			}
		}
	}

	sizeBytes := calculateModelSize(dir, entries, p.weightExts, p.recursiveWeights)

	// Filter out incomplete models (below minimum size).
	// Patterns with skip_min_size (e.g. ONNX edge models) are exempt.
	if sizeBytes < minSize && !p.skipMinSize {
		return nil
	}

	// VLM models (e.g. Qwen3.5-MoE) nest arch fields inside text_config
	archConfig := resolveArchConfig(config)
	hiddenSize := jsonInt(archConfig, "hidden_size")
	numLayers := jsonInt(archConfig, "num_hidden_layers")
	params := estimateParams(hiddenSize, numLayers)

	// Enhanced metadata detection — also check text_config for MOE fields
	modelClass := detectModelClass(archConfig)
	if modelClass == "unknown" {
		modelClass = detectModelClass(config)
	}
	if modelClass == "unknown" && p.modelClassHint != "" {
		modelClass = p.modelClassHint
	}
	totalParams := int64(0)
	activeParams := int64(0)

	if modelClass == "moe" {
		baseParams := calculateDenseParams(hiddenSize, numLayers)
		totalParams, activeParams = calculateMOEParams(archConfig, config, baseParams)
	} else if modelClass == "dense" {
		totalParams = calculateDenseParamsFromConfig(archConfig, config)
		activeParams = totalParams
	}

	// Detect quantization
	name := filepath.Base(dir)
	if overrideName != "" {
		name = overrideName
	} else if p.format == "gguf" {
		name = strings.TrimSuffix(filepath.Base(weightPath), ".gguf")
	} else {
		name = normalizeModelName(dir)
	}
	weightName := filepath.Base(weightPath)
	quantization, quantSrc := detectQuantization(config, weightName, p.format)
	if (mType == "" || mType == "embedding") && strings.Contains(strings.ToLower(name), "reranker") {
		mType = "reranker"
	}

	return []*ModelInfo{
		{
			ID:             fmt.Sprintf("%x", sha256.Sum256([]byte(dir))),
			Name:           name,
			Type:           mType,
			Path:           dir,
			Format:         p.format,
			SizeBytes:      sizeBytes,
			DetectedArch:   arch,
			DetectedParams: params,
			ModelClass:     modelClass,
			TotalParams:    totalParams,
			ActiveParams:   activeParams,
			Quantization:   quantization,
			QuantSrc:       quantSrc,
		},
	}
}

func hasConfigFile(entries []os.DirEntry, files []string) bool {
	if len(files) == 0 {
		return true
	}
	for _, entry := range entries {
		for _, cfg := range files {
			if !entry.IsDir() && entry.Name() == cfg {
				return true
			}
		}
	}
	return false
}

func findConfigFile(dir string, entries []os.DirEntry, files []string) string {
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		for _, cfg := range files {
			if entry.Name() == cfg {
				return filepath.Join(dir, cfg)
			}
		}
	}
	return ""
}

func loadPatternConfig(dir string, entries []os.DirEntry, files []string) map[string]any {
	configPath := findConfigFile(dir, entries, files)
	if configPath == "" {
		return nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	var config map[string]any
	if strings.HasSuffix(configPath, ".yaml") || strings.HasSuffix(configPath, ".yml") {
		if err := yaml.Unmarshal(data, &config); err != nil {
			return nil
		}
		return config
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}
	return config
}

func patternMatchesIndicator(config map[string]any, p ModelPattern) bool {
	if p.indicatorField == "" {
		return true
	}
	if len(config) == 0 {
		return false
	}
	value, _ := config[p.indicatorField].(string)
	if value == "" {
		return false
	}
	if p.indicatorValueContains == "" {
		return true
	}
	return strings.Contains(value, p.indicatorValueContains)
}
