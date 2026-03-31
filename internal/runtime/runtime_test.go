package runtime

import (
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
	)
	got := strings.Join(flags, " ")
	if strings.Contains(got, "--quantization") {
		t.Fatalf("flags should not contain quantization for llama.cpp GGUF models, got %q", got)
	}
	if !strings.Contains(got, "--ctx-size 8192") {
		t.Fatalf("flags should retain normal runtime args, got %q", got)
	}
}
