package knowledge

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// FormatConfigFlag emits CLI tokens for a single config key/value pair.
// Returns tokens to append to args, e.g. ["--flag", "value"], ["--flag"], or ["--no-flag"].
//
// Rules (consistent across all runtimes — K3S podgen and Docker/Native runtime):
//   - true bool                      → "--flag"
//   - false bool                     → "--no-flag"
//   - map / slice                    → "--flag", <JSON-encoded value>
//   - integral floating-point values → "--flag", fixed-point decimal value
//   - other (numbers, strings, etc.)   → "--flag", fmt.Sprintf("%v", value)
//
// String template expansion (e.g. {{.ModelPath}}) is the caller's responsibility.
func FormatConfigFlag(key string, value any) []string {
	dash := strings.ReplaceAll(key, "_", "-")
	flagName := "--" + dash
	switch v := value.(type) {
	case bool:
		if v {
			return []string{flagName}
		}
		return []string{"--no-" + dash}
	case map[string]any, []any:
		// YAML-parsed map/slice values are always JSON-marshalable; error is impossible here.
		b, _ := json.Marshal(value)
		return []string{flagName, string(b)}
	case float64:
		return []string{flagName, formatFloatConfigValue(v, 64)}
	case float32:
		return []string{flagName, formatFloatConfigValue(float64(v), 32)}
	case json.Number:
		return []string{flagName, v.String()}
	default:
		return []string{flagName, fmt.Sprintf("%v", v)}
	}
}

func formatFloatConfigValue(value float64, bitSize int) string {
	// JSON/YAML numbers commonly arrive as float64. Go's default %v formatting
	// switches sufficiently large values to scientific notation, turning a
	// valid CLI value such as 1048576 into 1.048576e+06. Many inference-engine
	// integer flags reject that spelling, so preserve integral values as plain
	// decimals while retaining compact formatting for real fractions.
	if !math.IsNaN(value) && !math.IsInf(value, 0) && math.Trunc(value) == value {
		return strconv.FormatFloat(value, 'f', -1, bitSize)
	}
	return strconv.FormatFloat(value, 'g', -1, bitSize)
}

// ConfigFlagContext describes the runtime command surface used to decide
// whether a resolved config key is a real CLI argument or only a resolver hint.
type ConfigFlagContext struct {
	Command   []string
	ModelPath string
	Engine    string
	ModelType string
}

// ShouldIncludeConfigFlag reports whether a resolved config key should be emitted
// as a runtime CLI flag for the given startup command and local model path.
// Some keys, such as quantization, are selection hints for a model artifact rather
// than portable runtime flags across every engine.
func ShouldIncludeConfigFlag(command []string, modelPath, key string, value any) bool {
	return ShouldIncludeConfigFlagFor(ConfigFlagContext{Command: command, ModelPath: modelPath}, key, value)
}

// ShouldIncludeConfigFlagFor is the engine-aware form of ShouldIncludeConfigFlag.
// It keeps legacy behavior for unknown engines, but prevents LLM-only knobs from
// leaking into image/audio/OCR service wrappers that do not expose those flags.
func ShouldIncludeConfigFlagFor(ctx ConfigFlagContext, key string, value any) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "":
		return false
	case "quantization":
		return shouldIncludeQuantizationFlag(ctx.Command, ctx.ModelPath, value)
	default:
		if isLLMOnlyConfigKey(key) && !commandAcceptsLLMConfig(ctx) {
			return false
		}
		return true
	}
}

func isLLMOnlyConfigKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "max_model_len", "max_seq_len", "max_seq_length", "max_context_len", "max_context_tokens",
		"context_length", "ctx_size", "n_ctx", "gpu_memory_utilization", "mem_fraction_static",
		"tensor_parallel_size", "pipeline_parallel_size", "kv_cache_dtype", "dtype", "trust_remote_code",
		"enforce_eager", "disable_log_stats", "served_model_name", "speculative_config",
		"mm_encoder_attn_backend":
		return true
	default:
		return false
	}
}

func commandAcceptsLLMConfig(ctx ConfigFlagContext) bool {
	if isLLMModelType(ctx.ModelType) {
		return true
	}
	if strings.TrimSpace(ctx.Engine) == "" && strings.TrimSpace(ctx.ModelType) == "" && len(ctx.Command) == 0 {
		return true
	}
	for _, value := range []string{ctx.Engine, strings.Join(ctx.Command, " ")} {
		lower := strings.ToLower(value)
		switch {
		case strings.Contains(lower, "vllm"),
			strings.Contains(lower, "sglang"),
			strings.Contains(lower, "llama"),
			strings.Contains(lower, "ollama"),
			strings.Contains(lower, "transformers serve"),
			strings.Contains(lower, "qwen-asr-serve"):
			return true
		}
	}
	return false
}

func isLLMModelType(modelType string) bool {
	switch strings.ToLower(strings.TrimSpace(modelType)) {
	case "llm", "vlm", "embedding":
		return true
	default:
		return false
	}
}

func shouldIncludeQuantizationFlag(command []string, modelPath string, value any) bool {
	if s, ok := value.(string); ok && strings.TrimSpace(s) == "" {
		return false
	}
	if isSingleFileQuantizedModel(modelPath) || commandBakesInModelQuantization(command) {
		return false
	}
	if declared, known := modelConfigDeclaresQuantization(modelPath); known {
		return declared
	}
	return true
}

func isSingleFileQuantizedModel(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".gguf", ".ggml":
		return true
	default:
		return false
	}
}

func commandBakesInModelQuantization(command []string) bool {
	for _, arg := range command {
		lower := strings.ToLower(arg)
		base := strings.ToLower(filepath.Base(arg))
		if base == "llama-server" || strings.Contains(lower, "llama_cpp.server") {
			return true
		}
	}
	return false
}

func modelConfigDeclaresQuantization(modelPath string) (declared bool, known bool) {
	if modelPath == "" {
		return false, false
	}
	configDir := modelPath
	if fi, err := os.Stat(modelPath); err == nil && !fi.IsDir() {
		configDir = filepath.Dir(modelPath)
	}
	for _, name := range []string{"config.json", "configuration.json"} {
		data, err := os.ReadFile(filepath.Join(configDir, name))
		if err != nil {
			continue
		}
		var cfg map[string]any
		if err := json.Unmarshal(data, &cfg); err != nil {
			return false, true
		}
		if qc, ok := cfg["quantization_config"].(map[string]any); ok {
			return len(qc) > 0, true
		}
		return false, true
	}
	return false, false
}
