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
	DetectedParams string `json:"detected_params"` // Legacy: e.g., "8B"

	// Enhanced metadata fields
	ModelClass     string `json:"model_class"`     // dense | moe | hybrid | unknown
	TotalParams    int64  `json:"total_params"`    // Exact parameter count (0 = unknown)
	ActiveParams   int64  `json:"active_params"`   // For MOE: active parameters per token
	Quantization   string `json:"quantization"`     // int8 | int4 | fp8 | fp16 | bf16 | nf4 | fp32 | unknown
	QuantSrc       string `json:"quant_src"`        // config | filename | header | unknown
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
	ModelClass       string
	TotalParams      int64
	ActiveParams     int64
	Quantization     string
	QuantSrc         string
	Status           string  // unknown|downloading|registered|failed
	DownloadProgress float64 // 0.0-1.0
}

// ScanOptions configures which directories to scan.
type ScanOptions struct {
	Paths             []string
	MinModelSizeBytes int64 // override default 10MB floor; 0 means use default
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

	if opts.MinModelSizeBytes > 0 {
		orig := minModelSize
		minModelSize = opts.MinModelSizeBytes
		defer func() { minModelSize = orig }()
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

	// Check if any subdirectory was detected as a model (container directory detection)
	hasModelSubdirs := false
	for _, entry := range entries {
		if entry.IsDir() {
			subdirPath := filepath.Join(dir, entry.Name())
			for _, m := range models {
				// Exact match: subdirectory path equals a detected model's path
				if m.Path == subdirPath {
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

	for _, m := range tryDetectModel(ctx, dir, entries) {
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

func tryDetectModel(_ context.Context, dir string, entries []os.DirEntry) []*ModelInfo {
	for _, p := range modelPatterns {
		if ms := detectByPattern(dir, entries, p); len(ms) > 0 {
			return ms
		}
	}
	return nil
}

func detectByPattern(dir string, entries []os.DirEntry, p ModelPattern) []*ModelInfo {
	if !hasConfigFile(entries, p.configFiles) {
		return nil
	}

	// For GGUF format, detect all .gguf files in the directory
	if p.format == "gguf" {
		return detectGGUFModels(dir, entries, p)
	}

	// For other formats, find the first weight file (existing behavior)
	weightPath := findWeightFile(dir, entries, p.weightExts)
	if weightPath == "" {
		return nil
	}

	return buildModelInfo(dir, entries, p, weightPath, "")
}

// detectGGUFModels detects all GGUF models in a directory.
// GGUF models don't have config.json, so we detect one model per .gguf file.
// Each GGUF file gets its own Path (file path, not directory) for uniqueness.
func detectGGUFModels(dir string, entries []os.DirEntry, p ModelPattern) []*ModelInfo {
	weightFiles := findAllWeightFiles(dir, entries, p.weightExts)
	if len(weightFiles) == 0 {
		return nil
	}

	var models []*ModelInfo
	for _, weightPath := range weightFiles {
		// Check individual file size against minimum
		info, err := os.Stat(weightPath)
		if err != nil {
			continue
		}
		if info.Size() < minModelSize {
			continue
		}

		// Use the file path as the model path (unique per GGUF file)
		// This allows multiple GGUF files in the same directory to be detected
		model := &ModelInfo{
			ID:             fmt.Sprintf("%x", sha256.Sum256([]byte(weightPath))),
			Name:           strings.TrimSuffix(filepath.Base(weightPath), ".gguf"),
			Type:           p.typeHint,
			Path:           weightPath, // Use file path for uniqueness
			Format:         p.format,
			SizeBytes:      info.Size(),
			DetectedArch:   "",
			DetectedParams: "",
			ModelClass:     "unknown",
			TotalParams:    0,
			ActiveParams:   0,
		}

		// Detect quantization from filename
		weightName := filepath.Base(weightPath)
		model.Quantization, model.QuantSrc = detectQuantization(nil, weightName, p.format)

		if model.Type == "" {
			model.Type = "llm" // Default GGUF models to LLM
		}

		models = append(models, model)
	}
	return models
}

// buildModelInfo builds a ModelInfo from a single weight file.
// For formats with config files (safetensors, pytorch, etc.).
func buildModelInfo(dir string, entries []os.DirEntry, p ModelPattern, weightPath string, overrideName string) []*ModelInfo {
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
	totalParams := int64(0)
	activeParams := int64(0)

	if modelClass == "moe" {
		baseParams := calculateDenseParams(hiddenSize, numLayers)
		totalParams, activeParams = calculateMOEParams(archConfig, config, baseParams)
	} else if modelClass == "dense" {
		totalParams = calculateDenseParams(hiddenSize, numLayers)
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

// findAllWeightFiles returns all weight files matching the given extensions.
func findAllWeightFiles(dir string, entries []os.DirEntry, exts []string) []string {
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		for _, ext := range exts {
			if strings.HasSuffix(strings.ToLower(name), ext) {
				files = append(files, filepath.Join(dir, name))
				break
			}
		}
	}
	return files
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

// detectModelClass determines if a model is dense, MOE, hybrid, or unknown.
func detectModelClass(config map[string]any) string {
	// MOE indicators from config
	if hasField(config, "num_experts") || hasField(config, "num_local_experts") || hasField(config, "num_experts_per_tok") {
		return "moe"
	}
	if hasField(config, "router_aux_loss_coef") || hasField(config, "router_z_loss_coef") {
		return "moe"
	}

	// Architecture-specific patterns
	modelType := jsonStr(config, "model_type", "")
	archFamily := strings.ToLower(modelType)

	// Known MOE architectures
	moePatterns := []string{
		"mixtral", "deepseek-moe", "deepseek_v2", "deepseek-v2", "deepseekv2",
		"grok", "qwen-moe", "phi-mix", "arctic", "bridgetower",
		"jamba", "aqlm", "moe",
	}
	for _, p := range moePatterns {
		if strings.Contains(archFamily, p) {
			return "moe"
		}
	}

	// Known hybrid architectures (vision-language, multimodal)
	hybridPatterns := []string{
		"phi3_vision", "phi3-vision", "llava", "internvl",
		"minicpm_v", "minicpm-v", "qwen_vl", "qwen-vl",
		"glm_v", "glm-v", "multimodal", "vision",
	}
	for _, p := range hybridPatterns {
		if strings.Contains(archFamily, p) {
			return "hybrid"
		}
	}

	// Default to dense for LLMs
	if isLLMModelType(modelType) {
		return "dense"
	}

	return "unknown"
}

// calculateDenseParams estimates parameter count for dense transformer models.
func calculateDenseParams(hiddenSize, numLayers int) int64 {
	if hiddenSize == 0 || numLayers == 0 {
		return 0
	}
	// Standard transformer formula: ~12 * layers * hidden_size^2
	return int64(12 * float64(numLayers) * float64(hiddenSize) * float64(hiddenSize))
}

// calculateMOEParams estimates total and active parameters for MOE models.
// archConfig contains architecture fields (may be text_config for VLMs).
// topConfig is the original top-level config (for tie_word_embeddings etc.).
// Falls back to rough estimation when intermediate sizes are unavailable.
func calculateMOEParams(archConfig, topConfig map[string]any, baseParams int64) (total, active int64) {
	config := archConfig
	numExperts := jsonInt(config, "num_experts")
	if numExperts == 0 {
		numExperts = jsonInt(config, "num_local_experts")
	}
	expertsPerTok := jsonInt(config, "num_experts_per_tok")
	if expertsPerTok == 0 {
		expertsPerTok = 2
	}
	if numExperts == 0 {
		return baseParams, baseParams
	}

	hiddenSize := jsonInt(config, "hidden_size")
	numLayers := jsonInt(config, "num_hidden_layers")

	// Try to calculate from actual architecture fields
	moeIntermediate := jsonInt(config, "moe_intermediate_size")
	if moeIntermediate == 0 {
		moeIntermediate = jsonInt(config, "intermediate_size")
	}

	if hiddenSize > 0 && numLayers > 0 && moeIntermediate > 0 {
		H := int64(hiddenSize)
		L := int64(numLayers)
		I := int64(moeIntermediate)
		E := int64(numExperts)
		A := int64(expertsPerTok)

		// Per-expert MLP: gate + up + down projections = 3 * H * I
		expertMLP := 3 * H * I
		// Shared expert (if present)
		sharedI := int64(jsonInt(config, "shared_expert_intermediate_size"))
		sharedMLP := int64(0)
		if sharedI > 0 {
			sharedMLP = 3 * H * sharedI
		}

		// Attention per layer (GQA-aware):
		// Q: H * num_heads * head_dim, K: H * num_kv_heads * head_dim, V: same, O: same as Q
		numHeads := int64(jsonInt(config, "num_attention_heads"))
		numKVHeads := int64(jsonInt(config, "num_key_value_heads"))
		headDim := int64(jsonInt(config, "head_dim"))
		if headDim == 0 && numHeads > 0 {
			headDim = H / numHeads
		}
		attnPerLayer := int64(0)
		if numHeads > 0 && headDim > 0 {
			qDim := numHeads * headDim
			kvDim := numKVHeads * headDim
			if numKVHeads == 0 {
				kvDim = qDim
			}
			attnPerLayer = H * (qDim + 2*kvDim + qDim) // Q, K, V, O
		} else {
			attnPerLayer = 4 * H * H // fallback: standard MHA
		}

		// Router gate per layer: H * num_experts
		routerPerLayer := H * E

		// Embedding: vocab_size * hidden_size
		vocabSize := int64(jsonInt(config, "vocab_size"))
		embedding := vocabSize * H
		// LM head (if untied from embedding) — check top-level config
		lmHead := int64(0)
		if tied, ok := topConfig["tie_word_embeddings"].(bool); ok && !tied {
			lmHead = vocabSize * H
		}

		sharedPerLayer := attnPerLayer + sharedMLP + routerPerLayer
		total = embedding + lmHead + L*(sharedPerLayer+E*expertMLP)
		active = embedding + lmHead + L*(sharedPerLayer+A*expertMLP)
		return
	}

	// Fallback: rough estimation
	if baseParams > 0 {
		E := int64(numExperts)
		A := int64(expertsPerTok)
		baseShare := baseParams / 3
		expertShare := baseParams * 2 / 3
		total = baseParams + expertShare*(E-1)
		active = baseShare + (expertShare/E)*A
	} else {
		total = baseParams
		active = baseParams
	}
	return
}

// detectQuantization determines the quantization format of a model.
func detectQuantization(config map[string]any, filename, format string) (quant, src string) {
	// Priority 1: From config.json (HuggingFace models)
	if q := quantFromConfig(config); q != "" {
		return q, "config"
	}

	// Priority 2: From filename (GGUF or directory name)
	if q := quantFromFilename(filename, format); q != "" {
		return q, "filename"
	}

	// Priority 3: From torch_dtype in config
	if q := quantFromTorchDtype(config); q != "" {
		return q, "config"
	}

	return "unknown", "unknown"
}

// quantFromConfig extracts quantization from HuggingFace config.
func quantFromConfig(config map[string]any) string {
	// Check quantization_config
	if qc, ok := config["quantization_config"].(map[string]any); ok {
		if q, ok := qc["quant_method"].(string); ok {
			normalized := normalizeQuantString(q)
			// For compressed-tensors / marlin, extract actual bit depth from config_groups
			if normalized == q && (q == "compressed-tensors" || q == "marlin") {
				if bits := extractBitsFromConfigGroups(qc); bits > 0 {
					return fmt.Sprintf("int%d", bits)
				}
			}
			return normalized
		}
		if q, ok := qc["load_in_8bit"].(bool); ok && q {
			return "int8"
		}
		if q, ok := qc["load_in_4bit"].(bool); ok && q {
			return "int4"
		}
		// Also check top-level "bits" field (used by AWQ/GPTQ configs)
		if bits, ok := qc["bits"].(float64); ok && bits > 0 {
			return fmt.Sprintf("int%d", int(bits))
		}
	}
	// Check for GGUF format specific configs
	if _, ok := config["gguf"]; ok {
		return "unknown" // GGUF quantization is determined from filename
	}
	return ""
}

// extractBitsFromConfigGroups reads num_bits from compressed-tensors config_groups.
func extractBitsFromConfigGroups(qc map[string]any) int {
	groups, ok := qc["config_groups"].(map[string]any)
	if !ok {
		return 0
	}
	// Check first group (typically "group_0")
	for _, g := range groups {
		group, ok := g.(map[string]any)
		if !ok {
			continue
		}
		weights, ok := group["weights"].(map[string]any)
		if !ok {
			continue
		}
		if bits, ok := weights["num_bits"].(float64); ok && bits > 0 {
			return int(bits)
		}
	}
	return 0
}

// quantFromFilename detects quantization from filename patterns.
func quantFromFilename(filename, format string) string {
	lower := strings.ToLower(filename)

	// GGUF quantization codes (llama.cpp naming)
	ggufPatterns := []struct {
		pattern string
		quant   string
	}{
		{"q4_k_m", "int4"},
		{"q4_k_s", "int4"},
		{"q4_0", "int4"},
		{"q4_1", "int4"},
		{"q5_k_m", "int5"},
		{"q5_k_s", "int5"},
		{"q5_0", "int5"},
		{"q5_1", "int5"},
		{"q6_k", "int6"},
		{"q8_0", "int8"},
		{"bf16", "bf16"},  // Match before f16 to avoid false positives
		{"f16", "fp16"},
		{"f32", "fp32"},
	}

	// Check GGUF patterns first (more specific)
	for _, p := range ggufPatterns {
		if strings.Contains(lower, p.pattern) {
			return p.quant
		}
	}

	// General patterns
	generalPatterns := []struct {
		pattern string
		quant   string
	}{
		{"int8", "int8"},
		{"8bit", "int8"},
		{"int4", "int4"},
		{"4bit", "int4"},
		{"fp8", "fp8"},
		{"8bit", "int8"},
		{"fp16", "fp16"},
		{"16bit", "fp16"},
		{"half", "fp16"},
		{"bf16", "bf16"},
		{"bfloat16", "bf16"},
		{"nf4", "nf4"},
	}

	for _, p := range generalPatterns {
		if strings.Contains(lower, p.pattern) {
			return p.quant
		}
	}

	// Check torch_dtype specific to format
	if format == "safetensors" || format == "pytorch" {
		if strings.Contains(lower, "fp32") || strings.Contains(lower, "float32") {
			return "fp32"
		}
		if strings.Contains(lower, "fp16") || strings.Contains(lower, "float16") {
			return "fp16"
		}
	}

	return ""
}

// quantFromTorchDtype extracts quantization from torch_dtype field.
func quantFromTorchDtype(config map[string]any) string {
	dtype := jsonStr(config, "torch_dtype", "")
	switch strings.ToLower(dtype) {
	case "float16", "half":
		return "fp16"
	case "bfloat16":
		return "bf16"
	case "float32":
		return "fp32"
	case "float8":
		return "fp8"
	default:
		return ""
	}
}

// normalizeQuantString normalizes quantization format strings.
func normalizeQuantString(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "bnb", "bitsandbytes", "8bit", "8-bit":
		return "int8"
	case "gptq", "awq", "4bit", "4-bit":
		return "int4"
	case "fp8":
		return "fp8"
	default:
		return s
	}
}

// isLLMModelType checks if a model type is an LLM.
func isLLMModelType(modelType string) bool {
	if modelType == "" {
		return false
	}
	llmTypes := []string{
		"llama", "glm", "qwen", "mistral", "baichuan", "internlm",
		"deepseek", "phi", "gemma", "yi", "bloom", "falcon", "mpt",
		"opt", "gpt2", "gptneox", "stablelm", "minicpm", "roberta",
		"albert", "t5", "bart", "pegasus", "bigbird", "electra",
	}
	lower := strings.ToLower(modelType)
	for _, t := range llmTypes {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// resolveArchConfig returns the config map containing architecture fields
// (hidden_size, num_hidden_layers, num_experts, etc.).
// VLM models nest these inside "text_config"; pure LLMs have them at top level.
func resolveArchConfig(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	// If top-level has hidden_size, use it directly
	if _, ok := config["hidden_size"]; ok {
		return config
	}
	// Fall back to text_config (VLM models like Qwen3.5-MoE)
	if tc, ok := config["text_config"].(map[string]any); ok {
		return tc
	}
	return config
}

// hasField checks if a field exists in a map.
func hasField(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

// jsonStr extracts a string value from a map with default.
func jsonStr(m map[string]any, key, defaultVal string) string {
	v, ok := m[key]
	if !ok {
		return defaultVal
	}
	if s, ok := v.(string); ok {
		return s
	}
	return defaultVal
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
