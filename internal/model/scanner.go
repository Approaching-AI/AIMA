package model

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
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
		paths = append(paths, "/mnt/data/models")
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

	for _, root := range paths {
		info, err := os.Stat(root)
		if err != nil {
			slog.Debug("skip scan path", "path", root, "error", err)
			continue
		}
		if !info.IsDir() {
			continue
		}

		found, err := scanDirectory(ctx, root, seen)
		if err != nil {
			return nil, err
		}
		models = append(models, found...)
	}

	return models, nil
}

func scanDirectory(ctx context.Context, root string, seen map[string]bool) ([]*ModelInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("scan directory %s: %w", root, err)
	}

	var models []*ModelInfo

	// Check if the root directory itself contains model files
	if m := tryDetectModel(ctx, root); m != nil {
		if !seen[m.Path] {
			seen[m.Path] = true
			models = append(models, m)
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", root, err)
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("scan directory %s: %w", root, err)
		}

		if !entry.IsDir() {
			continue
		}

		subdir := filepath.Join(root, entry.Name())
		if m := tryDetectModel(ctx, subdir); m != nil {
			if !seen[m.Path] {
				seen[m.Path] = true
				models = append(models, m)
			}
		}
	}

	return models, nil
}

func tryDetectModel(_ context.Context, dir string) *ModelInfo {
	// Try HuggingFace format: config.json + *.safetensors
	if m := detectHuggingFace(dir); m != nil {
		return m
	}
	// Try GGUF format: *.gguf with valid magic
	if m := detectGGUF(dir); m != nil {
		return m
	}
	return nil
}

func detectHuggingFace(dir string) *ModelInfo {
	configPath := filepath.Join(dir, "config.json")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}

	// Check for safetensors files
	safetensorsSize := sumFilesByExtension(dir, ".safetensors")
	if safetensorsSize == 0 {
		return nil
	}

	var config map[string]any
	if err := json.Unmarshal(configData, &config); err != nil {
		return nil
	}

	modelType, _ := config["model_type"].(string)
	hiddenSize := jsonInt(config, "hidden_size")
	numLayers := jsonInt(config, "num_hidden_layers")

	arch := detectArch(modelType)
	params := estimateParams(hiddenSize, numLayers)
	mType := detectModelType(arch)

	return &ModelInfo{
		ID:             fmt.Sprintf("%x", sha256.Sum256([]byte(dir))),
		Name:           filepath.Base(dir),
		Type:           mType,
		Path:           dir,
		Format:         "safetensors",
		SizeBytes:      safetensorsSize,
		DetectedArch:   arch,
		DetectedParams: params,
	}
}

func detectGGUF(dir string) *ModelInfo {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".gguf") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		if !validateGGUFMagic(path) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		return &ModelInfo{
			ID:             fmt.Sprintf("%x", sha256.Sum256([]byte(dir))),
			Name:           filepath.Base(dir),
			Type:           "llm",
			Path:           dir,
			Format:         "gguf",
			SizeBytes:      info.Size(),
			DetectedArch:   "",
			DetectedParams: "",
		}
	}

	return nil
}

func validateGGUFMagic(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	var magic [4]byte
	if _, err := f.Read(magic[:]); err != nil {
		return false
	}

	// GGUF magic: bytes "GGUF" = 0x47 0x47 0x55 0x46
	m := binary.LittleEndian.Uint32(magic[:])
	return m == 0x46554747
}

func sumFilesByExtension(dir, ext string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ext) {
			info, err := entry.Info()
			if err == nil {
				total += info.Size()
			}
		}
	}
	return total
}

// detectArch maps a model_type string from config.json to a canonical architecture name.
func detectArch(modelType string) string {
	if modelType == "" {
		return ""
	}

	lower := strings.ToLower(modelType)

	archPatterns := []struct {
		substr string
		arch   string
	}{
		{"llama", "llama"},
		{"chatglm", "glm"},
		{"glm", "glm"},
		{"qwen", "qwen"},
		{"whisper", "whisper"},
		{"mistral", "mistral"},
		{"baichuan", "baichuan"},
		{"internlm", "internlm"},
		{"deepseek", "deepseek"},
		{"llava", "llava"},
		{"internvl", "internvl"},
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
		{"bark", "bark"},
		{"speecht5", "speecht5"},
		{"stable_diffusion", "stable_diffusion"},
	}

	for _, p := range archPatterns {
		if strings.Contains(lower, p.substr) {
			return p.arch
		}
	}

	return modelType
}

// detectModelType infers the broad model category from architecture name.
func detectModelType(arch string) string {
	switch arch {
	case "whisper":
		return "asr"
	case "bark", "speecht5":
		return "tts"
	case "stable_diffusion":
		return "diffusion"
	case "llava", "internvl":
		return "vlm"
	default:
		return "llm"
	}
}

// estimateParams estimates parameter count from hidden_size and num_hidden_layers.
// Uses the standard transformer formula: params ~= 12 * L * d^2
// and rounds to the nearest standard size bucket.
func estimateParams(hiddenSize, numLayers int) string {
	if hiddenSize == 0 || numLayers == 0 {
		return ""
	}

	rawParams := 12.0 * float64(numLayers) * float64(hiddenSize) * float64(hiddenSize)
	billions := rawParams / 1e9

	if billions < 0.5 {
		return "<1B"
	}

	// Standard parameter count buckets
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
