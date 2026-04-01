package model

import (
	"fmt"
	"math"
	"strings"
)

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

// calculateDenseParams is a rough estimate using 12*L*H² (used as MoE fallback base).
func calculateDenseParams(hiddenSize, numLayers int) int64 {
	if hiddenSize == 0 || numLayers == 0 {
		return 0
	}
	return int64(12 * float64(numLayers) * float64(hiddenSize) * float64(hiddenSize))
}

// attnParamsPerLayer calculates GQA-aware attention parameter count per layer.
// Returns 4*H² fallback when head counts are unavailable.
func attnParamsPerLayer(config map[string]any) int64 {
	H := int64(jsonInt(config, "hidden_size"))
	if H == 0 {
		return 0
	}
	numHeads := int64(jsonInt(config, "num_attention_heads"))
	numKVHeads := int64(jsonInt(config, "num_key_value_heads"))
	headDim := int64(jsonInt(config, "head_dim"))
	if headDim == 0 && numHeads > 0 {
		headDim = H / numHeads
	}
	if numHeads > 0 && headDim > 0 {
		qDim := numHeads * headDim
		kvDim := numKVHeads * headDim
		if numKVHeads == 0 {
			kvDim = qDim
		}
		return H * (qDim + 2*kvDim + qDim) // Q, K, V, O
	}
	return 4 * H * H
}

// calculateDenseParamsFromConfig computes parameter count for dense models
// using actual FFN intermediate_size, GQA attention, and vocab embedding.
func calculateDenseParamsFromConfig(archConfig, topConfig map[string]any) int64 {
	H := int64(jsonInt(archConfig, "hidden_size"))
	L := int64(jsonInt(archConfig, "num_hidden_layers"))
	if H == 0 || L == 0 {
		return 0
	}

	// FFN per layer: SwiGLU = 3 * H * I (gate + up + down)
	I := int64(jsonInt(archConfig, "intermediate_size"))
	ffnPerLayer := int64(0)
	if I > 0 {
		ffnPerLayer = 3 * H * I
	} else {
		ffnPerLayer = 8 * H * H // fallback: vanilla FFN 2*H*4H
	}

	vocabSize := int64(jsonInt(archConfig, "vocab_size"))
	return vocabSize*H + L*(attnParamsPerLayer(archConfig)+ffnPerLayer)
}

// calculateMOEParams estimates total and active parameters for MOE models.
// archConfig contains architecture fields (may be text_config for VLMs).
// topConfig is the original top-level config (for tie_word_embeddings etc.).
func calculateMOEParams(archConfig, topConfig map[string]any, baseParams int64) (total, active int64) {
	numExperts := jsonInt(archConfig, "num_experts")
	if numExperts == 0 {
		numExperts = jsonInt(archConfig, "num_local_experts")
	}
	expertsPerTok := jsonInt(archConfig, "num_experts_per_tok")
	if expertsPerTok == 0 {
		expertsPerTok = 2
	}
	if numExperts == 0 {
		return baseParams, baseParams
	}

	H := int64(jsonInt(archConfig, "hidden_size"))
	L := int64(jsonInt(archConfig, "num_hidden_layers"))

	moeIntermediate := jsonInt(archConfig, "moe_intermediate_size")
	if moeIntermediate == 0 {
		moeIntermediate = jsonInt(archConfig, "intermediate_size")
	}

	if H > 0 && L > 0 && moeIntermediate > 0 {
		I := int64(moeIntermediate)
		E := int64(numExperts)
		A := int64(expertsPerTok)

		expertMLP := 3 * H * I
		sharedI := int64(jsonInt(archConfig, "shared_expert_intermediate_size"))
		sharedMLP := int64(0)
		if sharedI > 0 {
			sharedMLP = 3 * H * sharedI
		}

		routerPerLayer := H * E

		vocabSize := int64(jsonInt(archConfig, "vocab_size"))
		embedding := vocabSize * H
		lmHead := int64(0)
		if tied, ok := topConfig["tie_word_embeddings"].(bool); ok && !tied {
			lmHead = vocabSize * H
		}

		shared := attnParamsPerLayer(archConfig) + sharedMLP + routerPerLayer
		total = embedding + lmHead + L*(shared+E*expertMLP)
		active = embedding + lmHead + L*(shared+A*expertMLP)
		return
	}

	// Fallback: rough estimation when intermediate sizes unavailable
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
		{"bf16", "bf16"}, // Match before f16 to avoid false positives
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
