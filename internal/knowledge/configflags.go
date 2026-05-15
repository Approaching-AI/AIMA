package knowledge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func canonicalConfigKey(key string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "-", "_")
}

// AcceptedConfigKeySet normalizes an optional engine-declared config allowlist.
// Empty means "legacy permissive mode" so older engine YAML stays compatible.
func AcceptedConfigKeySet(keys []string) map[string]struct{} {
	if len(keys) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = canonicalConfigKey(key)
		if key == "" {
			continue
		}
		set[key] = struct{}{}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// ConfigKeyAccepted reports whether an engine should receive the config key as
// a CLI flag. The allowlist is intentionally opt-in to avoid breaking older
// catalog entries that have not declared accepted_config_keys yet.
func ConfigKeyAccepted(key string, accepted map[string]struct{}) bool {
	if len(accepted) == 0 {
		return true
	}
	_, ok := accepted[canonicalConfigKey(key)]
	return ok
}

// FormatConfigFlag emits CLI tokens for a single config key/value pair.
// Returns tokens to append to args, e.g. ["--flag", "value"], ["--flag"], or ["--no-flag"].
//
// Rules (consistent across all runtimes — K3S podgen and Docker/Native runtime):
//   - true bool                      → "--flag"
//   - false bool                     → "--no-flag"
//   - map / slice                    → "--flag", <JSON-encoded value>
//   - other (numbers, strings, etc.) → "--flag", fmt.Sprintf("%v", value)
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
	default:
		return []string{flagName, fmt.Sprintf("%v", v)}
	}
}

// ShouldIncludeConfigFlag reports whether a resolved config key should be emitted
// as a runtime CLI flag for the given startup command and local model path.
// Some keys, such as quantization, are selection hints for a model artifact rather
// than portable runtime flags across every engine.
func ShouldIncludeConfigFlag(command []string, modelPath, key string, value any) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "":
		return false
	case "quantization":
		return shouldIncludeQuantizationFlag(command, modelPath, value)
	default:
		return true
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
