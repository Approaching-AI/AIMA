package runtime

import (
	"strings"
	"testing"
)

// TestQwen36CachePromptFlagEmitted verifies the hybrid-recurrent crash fix:
// the resolved llama.cpp config for Qwen3.6-35B-A3B (cache_prompt:false) must
// emit "--no-cache-prompt" so llama-server disables in-slot prefix reuse and
// never hits the recurrent partial seq_rm GGML_ABORT.
func TestQwen36CachePromptFlagEmitted(t *testing.T) {
	config := map[string]any{
		"quantization": "int4",
		"n_gpu_layers": 999,
		"ctx_size":     262144,
		"parallel":     1,
		"cache_ram":    0,
		"cache_prompt": false,
	}
	command := []string{"llama-server", "--model", "x.gguf", "--host", "0.0.0.0"}
	flags := configToFlags(config, command, "x.gguf", map[string]struct{}{"port": {}})
	joined := strings.Join(flags, " ")
	if !strings.Contains(joined, "--no-cache-prompt") {
		t.Fatalf("expected --no-cache-prompt in flags, got: %s", joined)
	}
}
