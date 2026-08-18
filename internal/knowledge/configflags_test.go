package knowledge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFormatConfigFlag(t *testing.T) {
	t.Run("true bool emits flag only", func(t *testing.T) {
		got := FormatConfigFlag("enforce_eager", true)
		want := []string{"--enforce-eager"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("false bool emits --no- prefix", func(t *testing.T) {
		got := FormatConfigFlag("async_scheduling", false)
		want := []string{"--no-async-scheduling"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("map value emits JSON, not Go map repr", func(t *testing.T) {
		got := FormatConfigFlag("speculative_config", map[string]any{
			"method":                 "mtp",
			"num_speculative_tokens": 1,
		})
		if len(got) != 2 || got[0] != "--speculative-config" {
			t.Fatalf("expected --speculative-config + JSON value, got %v", got)
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(got[1]), &parsed); err != nil {
			t.Fatalf("value should be valid JSON, got %q: %v", got[1], err)
		}
		if parsed["method"] != "mtp" {
			t.Fatalf("expected method=mtp, got %v", parsed["method"])
		}
		// num_speculative_tokens is decoded as float64 by encoding/json.
		if parsed["num_speculative_tokens"].(float64) != 1 {
			t.Fatalf("expected num_speculative_tokens=1, got %v", parsed["num_speculative_tokens"])
		}
		if strings.HasPrefix(got[1], "map[") {
			t.Fatalf("value should not be Go map repr, got %q", got[1])
		}
	})

	t.Run("slice value emits JSON array", func(t *testing.T) {
		got := FormatConfigFlag("disable_log_stats", []any{"step1", "step2"})
		if len(got) != 2 {
			t.Fatalf("expected 2 tokens, got %v", got)
		}
		if got[1] != `["step1","step2"]` {
			t.Fatalf("expected JSON array, got %q", got[1])
		}
	})

	t.Run("scalar values pass through fmt", func(t *testing.T) {
		cases := []struct {
			key   string
			value any
			want  []string
		}{
			{"max_model_len", 131072, []string{"--max-model-len", "131072"}},
			{"gpu_memory_utilization", 0.8, []string{"--gpu-memory-utilization", "0.8"}},
			{"dtype", "bfloat16", []string{"--dtype", "bfloat16"}},
		}
		for _, c := range cases {
			got := FormatConfigFlag(c.key, c.value)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("key=%s value=%v: got %v, want %v", c.key, c.value, got, c.want)
			}
		}
	})

	t.Run("integral JSON numbers stay in decimal notation", func(t *testing.T) {
		cases := []struct {
			value any
			want  string
		}{
			{float64(1048576), "1048576"},
			{float32(1048576), "1048576"},
			{json.Number("1048576"), "1048576"},
			{float64(0.835), "0.835"},
		}
		for _, tc := range cases {
			got := FormatConfigFlag("max_model_len", tc.value)
			if len(got) != 2 || got[1] != tc.want {
				t.Fatalf("value=%v: got %v, want decimal value %q", tc.value, got, tc.want)
			}
		}
	})

	t.Run("underscore keys become hyphenated flags", func(t *testing.T) {
		got := FormatConfigFlag("mem_fraction_static", 0.7)
		if got[0] != "--mem-fraction-static" {
			t.Fatalf("expected --mem-fraction-static, got %q", got[0])
		}
	})
}

func TestShouldIncludeConfigFlag(t *testing.T) {
	t.Run("skip quantization for gguf llama server", func(t *testing.T) {
		if ShouldIncludeConfigFlag(
			[]string{"llama-server", "--model", "{{.ModelPath}}"},
			"/models/qwen3/Qwen3-4B-Q4_K_M.gguf",
			"quantization",
			"int4",
		) {
			t.Fatal("quantization flag should be omitted for llama.cpp GGUF deployments")
		}
	})

	t.Run("skip quantization when local config has no quantization metadata", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"model_type":"qwen3","torch_dtype":"bfloat16"}`), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		if ShouldIncludeConfigFlag(
			[]string{"vllm", "serve", "{{.ModelPath}}"},
			dir,
			"quantization",
			"gptq",
		) {
			t.Fatal("quantization flag should be omitted when config.json does not declare quantization_config")
		}
	})

	t.Run("keep quantization when local config declares quantization metadata", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"quantization_config":{"quant_method":"gptq","bits":4}}`), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		if !ShouldIncludeConfigFlag(
			[]string{"vllm", "serve", "{{.ModelPath}}"},
			dir,
			"quantization",
			"gptq",
		) {
			t.Fatal("quantization flag should be kept when config.json declares quantization_config")
		}
	})

	t.Run("skip LLM-only flags for non-LLM service wrappers", func(t *testing.T) {
		for _, key := range []string{"max_model_len", "gpu_memory_utilization", "mem_fraction_static"} {
			if ShouldIncludeConfigFlagFor(
				ConfigFlagContext{
					Command:   []string{"python3", "server.py"},
					Engine:    "z-image-diffusers",
					ModelType: "image_gen",
				},
				key,
				8192,
			) {
				t.Fatalf("%s should be omitted for image service wrappers", key)
			}
		}
	})

	t.Run("keep LLM-only flags for vLLM-like service wrappers", func(t *testing.T) {
		if !ShouldIncludeConfigFlagFor(
			ConfigFlagContext{
				Command:   []string{"qwen-asr-serve", "{{.ModelPath}}"},
				Engine:    "vllm-nightly-audio",
				ModelType: "asr",
			},
			"max_model_len",
			8192,
		) {
			t.Fatal("max_model_len should be kept for vLLM audio wrappers")
		}
	})
}
