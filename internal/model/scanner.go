package model

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jguan/aima/catalog"
	"gopkg.in/yaml.v3"
)

// ScannerConfig is the top-level YAML structure.
type ScannerConfig struct {
	Kind     string              `yaml:"kind"`
	Metadata map[string]any      `yaml:"metadata"`
	Config   Config              `yaml:"config"`
}

// Config contains scanner settings.
type Config struct {
	MaxScanDepth      int                   `yaml:"max_scan_depth"`
	MinModelSizeBytes int64                 `yaml:"min_model_size_bytes"`
	SkipSubdirNames   []string              `yaml:"skip_subdir_names"`
	ParentPatterns    []ConfigParentPattern `yaml:"parent_patterns"`
	ModelPatterns     []ConfigPattern       `yaml:"model_patterns"`
}

// ConfigParentPattern defines parent model detection.
type ConfigParentPattern struct {
	IndicatorFiles       []string `yaml:"indicator_files"`
	IndicatorField       string   `yaml:"indicator_field"`
	IndicatorValueContains string  `yaml:"indicator_value_contains"`
}

// ConfigPattern defines model detection from YAML.
type ConfigPattern struct {
	Name        string   `yaml:"name"`
	ConfigFiles []string `yaml:"config_files"`
	WeightExts  []string `yaml:"weight_exts"`
	Format      string   `yaml:"format"`
	TypeHint    string   `yaml:"type_hint"`
}

// ParentPattern is internal representation.
type ParentPattern struct {
	IndicatorFiles       []string
	IndicatorField       string
	IndicatorValueContains string
}

// Runtime configuration (loaded from YAML or defaults).
var (
	maxScanDepth      int
	minModelSize     int64
	skipSubdirNames  map[string]bool  // Set for O(1) lookup
	parentPatterns    []ParentPattern
	modelPatterns     []ModelPattern
)

// init loads scanner configuration from embedded catalog.
func init() {
	applyConfig()
}

func applyConfig() {
	data, err := catalog.FS.ReadFile("scanner.yaml")
	if err != nil {
		applyDefaultConfig()
		return
	}

	var yamlConfig ScannerConfig
	if err := yaml.Unmarshal(data, &yamlConfig); err != nil {
		applyDefaultConfig()
		return
	}

	if yamlConfig.Kind != "scanner_config" {
		applyDefaultConfig()
		return
	}

	cfg := yamlConfig.Config

	// Apply values with fallback to defaults
	if cfg.MaxScanDepth > 0 {
		maxScanDepth = cfg.MaxScanDepth
	} else {
		maxScanDepth = 4
	}

	if cfg.MinModelSizeBytes > 0 {
		minModelSize = cfg.MinModelSizeBytes
	} else {
		minModelSize = 10 * 1024 * 1024 // 10MB
	}

	// Load skip subdir names as a set for O(1) lookup
	skipSubdirNames = make(map[string]bool)
	for _, name := range cfg.SkipSubdirNames {
		skipSubdirNames[strings.ToLower(name)] = true
	}

	// Convert parent patterns
	for _, p := range cfg.ParentPatterns {
		if len(p.IndicatorFiles) == 0 {
			continue
		}
		parentPatterns = append(parentPatterns, ParentPattern{
			IndicatorFiles:       p.IndicatorFiles,
			IndicatorField:       p.IndicatorField,
			IndicatorValueContains: p.IndicatorValueContains,
		})
	}

	// Convert model patterns
	for _, p := range cfg.ModelPatterns {
		if p.Name == "" || p.Format == "" {
			continue
		}
		modelPatterns = append(modelPatterns, ModelPattern{
			name:        p.Name,
			configFiles: p.ConfigFiles,
			weightExts:  p.WeightExts,
			format:      p.Format,
			typeHint:    p.TypeHint,
		})
	}

	// Fallback to defaults if empty
	if len(modelPatterns) == 0 {
		applyDefaultPatterns()
	}
}

func applyDefaultConfig() {
	maxScanDepth = 4
	minModelSize = 10 * 1024 * 1024 // 10MB
	skipSubdirNames = make(map[string]bool)
	defaultSkipNames := []string{
		"text_encoder", "transformer", "vae", "unet", "controlnet",
		"scheduler", "feature_extractor", "speech_tokenizer", "tokenizer",
		"tokenizer_config", "processor", "onnx", "gguf-fp16", "gguf-q4", "gguf-q8",
		"fp16", "fp32", "quantized", "mmproj", "encoder", "decoder",
		"postprocessor", "preprocessor", "vision_model", "audio_encoder", "projection",
	}
	for _, name := range defaultSkipNames {
		skipSubdirNames[name] = true
	}
	parentPatterns = []ParentPattern{
		{
			IndicatorFiles:       []string{"model_index.json"},
			IndicatorField:       "_class_name",
			IndicatorValueContains: "Pipeline",
		},
	}
	applyDefaultPatterns()
}

func applyDefaultPatterns() {
	modelPatterns = []ModelPattern{
		{
			name:        "huggingface_safetensors",
			configFiles: []string{"config.json"},
			weightExts:  []string{".safetensors"},
			format:      "safetensors",
		},
		{
			name:        "huggingface_pytorch",
			configFiles: []string{"config.json", "configuration.json"},
			weightExts:  []string{"pytorch_model.bin", ".bin"},
			format:      "pytorch",
		},
		{
			name:        "pytorch_pt",
			configFiles: []string{"config.json", "configuration.json"},
			weightExts:  []string{".pt"},
			format:      "pytorch",
		},
		{
			name:        "pytorch_pth",
			configFiles: []string{"config.json", "configuration.json"},
			weightExts:  []string{".pth"},
			format:      "pytorch",
		},
		{
			name:        "funasr",
			configFiles: []string{"configuration.json"},
			weightExts:  []string{".pt"},
			format:      "pytorch",
			typeHint:    "asr",
		},
		{
			name:        "onnx",
			configFiles: []string{"config.json"},
			weightExts:  []string{".onnx"},
			format:      "onnx",
		},
		{
			name:        "gguf",
			configFiles: []string{},
			weightExts:  []string{".gguf"},
			format:      "gguf",
			typeHint:    "llm",
		},
	}
}

// ModelPattern defines how to detect a model format (internal).
type ModelPattern struct {
	name        string   // Pattern name for debugging
	configFiles []string // Possible config filenames (empty = no config needed)
	weightExts  []string // Possible weight file extensions
	format      string   // Output format name
	typeHint    string   // Type hint when detectArch fails
}

// ModelInfo represents a discovered local model.
type ModelInfo struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	Path           string `json:"path"`
	Format         string `json:"format"`
	SizeBytes      int64  `json:"size_bytes"`
	DetectedArch   string `json:"detected_arch"`
	DetectedParams string `json:"detected_params"`
}

// Store is the persistence interface for model records.
type Store interface {
	InsertModel(ctx context.Context, m *StoreModel) error
	GetModel(ctx context.Context, id string) (*StoreModel, error)
	ListModels(ctx context.Context) ([]*StoreModel, error)
	UpdateModelStatus(ctx context.Context, id, status string) error
	DeleteModel(ctx context.Context, id string) error
}

// StoreModel is the persistence representation of a model.
type StoreModel struct {
	ID               string
	Name             string
	Type             string
	Path             string
	Format           string
	SizeBytes        int64
	DetectedArch     string
	DetectedParams   string
	Status           string  // unknown|downloading|registered|failed
	DownloadProgress float64 // 0.0-1.0
}

// ScanOptions configures which directories to scan.
type ScanOptions struct {
	Paths []string
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
	}

	return paths
}

// Scan discovers models in the given directories.
func Scan(ctx context.Context, opts ScanOptions) ([]*ModelInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("scan models: %w", err)
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

		found, err := scanDirectory(ctx, root, 0, seen, skipParentPaths)
		if err != nil {
			return nil, err
		}
		models = append(models, found...)
	}

	return models, nil
}

func scanDirectory(ctx context.Context, dir string, depth int, seen map[string]bool, skipParentPaths map[string]bool) ([]*ModelInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("scan directory %s: %w", dir, err)
	}
	if depth > maxScanDepth {
		return nil, nil
	}

	var models []*ModelInfo

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", dir, err)
	}

	// Check if this is a parent pipeline (e.g., diffusion model)
	if isParentPipeline(dir, entries) {
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
		if skipSubdirNames[subdirName] {
			continue
		}

		// Skip if parent of this directory is a known parent model
		if shouldSkipParentChild(subdir, skipParentPaths) {
			continue
		}

		subModels, err := scanDirectory(ctx, subdir, depth+1, seen, skipParentPaths)
		if err == nil {
			models = append(models, subModels...)
		}
	}

	// Then check if current directory itself is a model (after recursion)
	// Skip if this is a known component subdirectory
	if skipSubdirNames[strings.ToLower(filepath.Base(dir))] {
		return models, nil
	}
	if m := tryDetectModel(ctx, dir, entries); m != nil {
		if !seen[m.Path] {
			seen[m.Path] = true
			models = append(models, m)
		}
	}

	return models, nil
}

func isParentPipeline(dir string, entries []os.DirEntry) bool {
	for _, pp := range parentPatterns {
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

func tryDetectModel(_ context.Context, dir string, entries []os.DirEntry) *ModelInfo {
	for _, p := range modelPatterns {
		if m := detectByPattern(dir, entries, p); m != nil {
			return m
		}
	}
	return nil
}

func detectByPattern(dir string, entries []os.DirEntry, p ModelPattern) *ModelInfo {
	if !hasConfigFile(entries, p.configFiles) {
		return nil
	}

	weightPath := findWeightFile(dir, entries, p.weightExts)
	if weightPath == "" {
		return nil
	}

	var config map[string]any
	configPath := findConfigFile(dir, entries, p.configFiles)
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err == nil {
			json.Unmarshal(data, &config)
		}
	}

	modelType, _ := config["model_type"].(string)
	arch := detectArch(modelType)
	mType := p.typeHint
	if mType == "" {
		mType = detectModelType(arch)
	}

	sizeBytes := calculateModelSize(dir, entries, p.weightExts)

	// Filter out incomplete models (below minimum size)
	if sizeBytes < minModelSize {
		return nil
	}

	hiddenSize := jsonInt(config, "hidden_size")
	numLayers := jsonInt(config, "num_hidden_layers")
	params := estimateParams(hiddenSize, numLayers)

	name := filepath.Base(dir)
	if p.format == "gguf" {
		name = strings.TrimSuffix(filepath.Base(weightPath), ".gguf")
	} else {
		name = normalizeModelName(dir)
	}

	return &ModelInfo{
		ID:             fmt.Sprintf("%x", sha256.Sum256([]byte(dir))),
		Name:           name,
		Type:           mType,
		Path:           dir,
		Format:         p.format,
		SizeBytes:      sizeBytes,
		DetectedArch:   arch,
		DetectedParams: params,
	}
}

func normalizeModelName(path string) string {
	name := filepath.Base(path)

	// HF Hub cache: models--<org>--<repo> -> <repo>
	if strings.HasPrefix(name, "models--") {
		parts := strings.SplitN(name, "--", 3)
		if len(parts) == 3 {
			return parts[2]
		}
	}

	// If name looks like a hash, try to extract from parent path
	// (e.g., models--openbmb--MiniCPM-o-4_5/snapshots/<hash>)
	if isHexString(name) && strings.Contains(path, "models--") {
		parts := strings.Split(path, "/")
		for _, part := range parts {
			if strings.HasPrefix(part, "models--") {
				subParts := strings.SplitN(part, "--", 3)
				if len(subParts) == 3 {
					return subParts[2]
				}
			}
		}
	}

	return name
}

func isHexString(s string) bool {
	if len(s) < 8 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
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

func findWeightFile(dir string, entries []os.DirEntry, exts []string) string {
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		for _, ext := range exts {
			if strings.HasSuffix(strings.ToLower(name), ext) {
				return filepath.Join(dir, name)
			}
		}
	}
	return ""
}

func calculateModelSize(dir string, entries []os.DirEntry, exts []string) int64 {
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		for _, ext := range exts {
			if strings.HasSuffix(strings.ToLower(name), ext) {
				// Use os.Stat to follow symlinks (HF Hub cache uses symlinks to blobs)
				fullPath := filepath.Join(dir, name)
				info, err := os.Stat(fullPath)
				if err == nil {
					total += info.Size()
				}
				break
			}
		}
	}
	return total
}

func detectArch(modelType string) string {
	if modelType == "" {
		return ""
	}

	lower := strings.ToLower(modelType)

	archPatterns := []struct {
		substr string
		arch   string
	}{
		// --- LLM ---
		{"llama", "llama"},
		{"chatglm", "glm"},
		{"glm", "glm"},
		{"qwen", "qwen"},
		{"mistral", "mistral"},
		{"baichuan", "baichuan"},
		{"internlm", "internlm"},
		{"deepseek", "deepseek"},
		{"phi", "phi"},
		{"gemma", "gemma"},
		{"yi", "yi"},
		{"bloom", "bloom"},
		{"falcon", "falcon"},
		{"mpt", "mpt"},
		{"opt", "opt"},
		{"gpt2", "gpt2"},
		{"gptneox", "gptneox"},
		{"stablelm", "stablelm"},
		{"minicpm", "minicpm"},
		// --- ASR ---
		{"whisper", "whisper"},
		{"wav2vec2", "wav2vec2"},
		{"hubert", "hubert"},
		{"wavlm", "wavlm"},
		{"wenet", "wenet"},
		{"conformer", "conformer"},
		{"unispeech", "unispeech"},
		{"funasr", "funasr"},
		// --- TTS ---
		{"bark", "bark"},
		{"speecht5", "speecht5"},
		{"vits", "vits"},
		{"fastspeech2", "fastspeech2"},
		{"coqui", "coqui"},
		{"tacotron", "tacotron"},
		{"gpt_sovits", "gpt_sovits"},
		{"styletts2", "styletts2"},
		{"vallex", "vallex"},
		{"glow", "glow"},
		{"tortoise", "tortoise"},
		{"cosyvoice", "cosyvoice"},
		// --- Diffusion ---
		{"stable_diffusion", "stable_diffusion"},
		{"flux", "flux"},
		{"sdxl", "sdxl"},
		{"latent_diffusion", "latent_diffusion"},
		{"ddim", "ddim"},
		{"eulercfg", "eulercfg"},
		// --- VLM ---
		{"llava", "llava"},
		{"internvl", "internvl"},
		{"phi3_vision", "phi3_vision"},
		{"qwen_vl", "qwen_vl"},
		{"glm_v", "glm_v"},
		{"minicpm_v", "minicpm_v"},
		// --- Embedding/Reranker ---
		{"clip", "clip"},
		{"bert", "bert"},
		{"roberta", "roberta"},
		{"xlm_roberta", "xlm_roberta"},
		{"e5", "e5"},
		{"bge", "bge"},
		{"jina", "jina"},
		{"sentence_t5", "sentence_t5"},
		{"colbert", "colbert"},
		{"cross_encoder", "cross_encoder"},
	}

	for _, p := range archPatterns {
		if strings.Contains(lower, p.substr) {
			return p.arch
		}
	}

	return modelType
}

func detectModelType(arch string) string {
	switch arch {
	case "whisper", "wav2vec2", "hubert", "wavlm", "wenet", "conformer", "unispeech", "funasr":
		return "asr"
	case "bark", "speecht5", "vits", "fastspeech2", "coqui", "tacotron",
		"gpt_sovits", "styletts2", "vallex", "glow", "tortoise", "cosyvoice":
		return "tts"
	case "stable_diffusion", "flux", "sdxl", "latent_diffusion", "ddim", "eulercfg":
		return "diffusion"
	case "llava", "internvl", "phi3_vision", "qwen_vl", "glm_v", "minicpm_v":
		return "vlm"
	case "clip", "bert", "roberta", "xlm_roberta", "e5", "bge",
		"jina", "sentence_t5", "colbert", "cross_encoder":
		return "embedding"
	default:
		return "llm"
	}
}

func estimateParams(hiddenSize, numLayers int) string {
	if hiddenSize == 0 || numLayers == 0 {
		return ""
	}

	rawParams := 12.0 * float64(numLayers) * float64(hiddenSize) * float64(hiddenSize)
	billions := rawParams / 1e9

	if billions < 0.5 {
		return "<1B"
	}

	buckets := []float64{1, 3, 7, 8, 13, 14, 22, 32, 34, 70, 72, 110, 200, 400}
	closest := buckets[0]
	closestDist := math.Abs(billions - closest)

	for _, b := range buckets[1:] {
		dist := math.Abs(billions - b)
		if dist < closestDist {
			closest = b
			closestDist = dist
		}
	}

	return fmt.Sprintf("%.0fB", closest)
}

func jsonInt(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}
