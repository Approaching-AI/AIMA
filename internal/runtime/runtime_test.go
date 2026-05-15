package runtime

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigToFlagsSkipsSelectionOnlyQuantization(t *testing.T) {
	flags := configToFlags(
		map[string]any{
			"quantization": "int4",
			"ctx_size":     8192,
		},
		[]string{"llama-server", "--model", "{{.ModelPath}}"},
		"/models/qwen3/Qwen3-4B-Q4_K_M.gguf",
		nil,
		nil,
	)
	got := strings.Join(flags, " ")
	if strings.Contains(got, "--quantization") {
		t.Fatalf("flags should not contain quantization for llama.cpp GGUF models, got %q", got)
	}
	if !strings.Contains(got, "--ctx-size 8192") {
		t.Fatalf("flags should retain normal runtime args, got %q", got)
	}
}

// TestConfigToFlagsFalseBoolEmitsNoPrefix verifies parity with K3S podgen:
// a false bool must emit "--no-flag", not be silently dropped.
func TestConfigToFlagsFalseBoolEmitsNoPrefix(t *testing.T) {
	flags := configToFlags(
		map[string]any{
			"async_scheduling": false,
			"enforce_eager":    true,
		},
		nil, "", nil,
		nil,
	)
	got := strings.Join(flags, " ")
	if !strings.Contains(got, "--no-async-scheduling") {
		t.Fatalf("false bool should emit --no- prefix, got %q", got)
	}
	if !strings.Contains(got, "--enforce-eager") {
		t.Fatalf("true bool should emit flag, got %q", got)
	}
	if strings.Contains(got, "--enforce-eager false") || strings.Contains(got, "--async-scheduling") {
		t.Fatalf("bools should not emit values, got %q", got)
	}
}

// TestConfigToFlagsMapEmitsJSON verifies nested YAML maps (e.g. speculative_config)
// are serialized as JSON, not Go map repr.
func TestConfigToFlagsMapEmitsJSON(t *testing.T) {
	flags := configToFlags(
		map[string]any{
			"speculative_config": map[string]any{
				"method":                 "mtp",
				"num_speculative_tokens": 1,
			},
		},
		nil, "", nil,
		nil,
	)
	// Find the value following --speculative-config.
	var value string
	for i, f := range flags {
		if f == "--speculative-config" && i+1 < len(flags) {
			value = flags[i+1]
			break
		}
	}
	if value == "" {
		t.Fatalf("--speculative-config not found in flags: %v", flags)
	}
	if strings.HasPrefix(value, "map[") {
		t.Fatalf("map should not be rendered as Go repr, got %q", value)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		t.Fatalf("value should be valid JSON, got %q: %v", value, err)
	}
	if parsed["method"] != "mtp" {
		t.Fatalf("expected method=mtp, got %v", parsed["method"])
	}
}

func TestConfigToFlagsHonorsAcceptedConfigKeys(t *testing.T) {
	flags := configToFlags(
		map[string]any{
			"max_model_len": 8192,
			"port":          8188,
			"device_map":    "auto",
		},
		[]string{"python3", "server.py"},
		"/models/z-image",
		map[string]struct{}{"port": {}},
		[]string{"port", "device_map"},
	)
	got := strings.Join(flags, " ")
	if strings.Contains(got, "--max-model-len") {
		t.Fatalf("accepted_config_keys should filter unsupported flags, got %q", got)
	}
	if !strings.Contains(got, "--device-map auto") {
		t.Fatalf("accepted config key should be emitted, got %q", got)
	}
	if strings.Contains(got, "--port") {
		t.Fatalf("reserved port key should not be emitted by configToFlags, got %q", got)
	}
}
