package knowledge

import (
	"path"
	"regexp"
	"strings"
)

// quantSuffixToken matches a single '-'-delimited trailing token that denotes a
// quantization, precision, or storage-layout variant rather than model identity
// (e.g. "q4_k_m", "bf16", "ud", "unfused"). Role-bearing tokens such as
// "dflash"/"mtp"/"flash" are deliberately excluded so a draft head keeps its
// identity.
var quantSuffixToken = regexp.MustCompile(`^(?:q\d[\dkmsl_]*|iq\d[\dxsa_]*|bf16|fp16|fp32|fp8|f16|f32|int4|int8|nf4|mxfp4|ud|awq|gptq|gguf|mlx|unfused|fused)$`)

// NormalizeModelKey lowercases a model name and strips trailing
// quantization/precision/layout tokens so different on-disk artifacts of one
// logical model share a key. It keeps role-bearing tokens like "dflash" so a
// draft head normalizes to "<base>-dflash", distinct from its parent "<base>".
//
//	"Qwen3.6-35B-A3B-UD-Q4_K_M"      -> "qwen3.6-35b-a3b"
//	"qwen3.6-35b-a3b-bf16-unfused"   -> "qwen3.6-35b-a3b"
//	"Qwen3.6-35B-A3B-DFlash-Q4_K_M"  -> "qwen3.6-35b-a3b-dflash"
//	"glm-4.7-flash"                  -> "glm-4.7-flash"
func NormalizeModelKey(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return ""
	}
	tokens := strings.Split(name, "-")
	for len(tokens) > 1 && quantSuffixToken.MatchString(tokens[len(tokens)-1]) {
		tokens = tokens[:len(tokens)-1]
	}
	return strings.Join(tokens, "-")
}

// SpeculativeDraftModelKeys harvests every variant's speculative_config.model
// reference across all model assets and returns the set of normalized draft
// model keys. A scanned model whose NormalizeModelKey is in this set is a
// speculative draft head (e.g. DFlash/MTP) — a companion of its parent model,
// not an independently deployable model.
func (c *Catalog) SpeculativeDraftModelKeys() map[string]bool {
	keys := make(map[string]bool)
	if c == nil {
		return keys
	}
	for i := range c.ModelAssets {
		for _, v := range c.ModelAssets[i].Variants {
			ref := speculativeModelRef(v.DefaultConfig)
			if ref == "" {
				continue
			}
			// The reference may be a path ("/models/X", "D:\models\X") or a
			// bare name; reduce it to the artifact base name first.
			base := path.Base(strings.ReplaceAll(ref, `\`, "/"))
			if key := NormalizeModelKey(base); key != "" {
				keys[key] = true
			}
		}
	}
	return keys
}

func speculativeModelRef(dc map[string]any) string {
	sc, ok := dc["speculative_config"].(map[string]any)
	if !ok {
		return ""
	}
	model, _ := sc["model"].(string)
	return strings.TrimSpace(model)
}
