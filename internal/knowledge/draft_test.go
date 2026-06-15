package knowledge

import "testing"

func TestNormalizeModelKey(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "qwen3.6-35b-a3b", "qwen3.6-35b-a3b"},
		{"draft safetensors", "Qwen3.6-35B-A3B-DFlash", "qwen3.6-35b-a3b-dflash"},
		{"draft gguf quant", "Qwen3.6-35B-A3B-DFlash-Q4_K_M", "qwen3.6-35b-a3b-dflash"},
		{"bf16", "qwen3.6-35b-a3b-bf16", "qwen3.6-35b-a3b"},
		{"bf16 unfused layout", "qwen3.6-35b-a3b-bf16-unfused", "qwen3.6-35b-a3b"},
		{"q4 quant", "qwen3.6-35b-a3b-q4_k_m", "qwen3.6-35b-a3b"},
		{"unsloth dynamic quant", "Qwen3.6-35B-A3B-UD-Q4_K_M", "qwen3.6-35b-a3b"},
		{"flash is identity not quant", "glm-4.7-flash", "glm-4.7-flash"},
		{"embedding q8", "qwen3-embedding-4b-q8_0", "qwen3-embedding-4b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeModelKey(c.in); got != c.want {
				t.Errorf("NormalizeModelKey(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSpeculativeDraftModelKeys(t *testing.T) {
	cat := &Catalog{
		ModelAssets: []ModelAsset{{
			Metadata: ModelMetadata{Name: "qwen3.6-35b-a3b"},
			Variants: []ModelVariant{
				{Name: "plain"}, // no speculative_config
				{Name: "dflash", DefaultConfig: map[string]any{
					"speculative_config": map[string]any{
						"method": "dflash",
						"model":  "/models/Qwen3.6-35B-A3B-DFlash",
					},
				}},
			},
		}},
	}
	keys := cat.SpeculativeDraftModelKeys()
	if !keys["qwen3.6-35b-a3b-dflash"] {
		t.Fatalf("expected draft key %q in %v", "qwen3.6-35b-a3b-dflash", keys)
	}
	if keys["qwen3.6-35b-a3b"] {
		t.Errorf("base model must not be a draft key: %v", keys)
	}
}

func TestSpeculativeDraftModelKeys_NilCatalog(t *testing.T) {
	var c *Catalog
	if got := c.SpeculativeDraftModelKeys(); len(got) != 0 {
		t.Errorf("nil catalog should yield no draft keys, got %v", got)
	}
}
