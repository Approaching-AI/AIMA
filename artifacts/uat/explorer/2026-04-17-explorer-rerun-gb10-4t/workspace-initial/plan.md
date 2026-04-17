# Exploration Plan

## Objective
Execute up to 2 high-information-gain experiments for `nvidia-gb10-arm64`, prioritizing pending `validate_baseline` obligations on unexplored Ready combos using the most reliable engine.

## Fact Snapshot
- **Hardware:** `nvidia-gb10-arm64`, 1× Blackwell GPU, 122570 MiB VRAM
- **Ready Combos:** 11 (all carry pending work)
- **Blocked Combos:** 124
- **Pending validate_baseline:** 7 items across 5 models
- **Pending tune:** 3 items across 3 models (baseline already exists)
- **Models NOT in Recent History:** `GLM-4.5-Air-nvfp4`, `Qwen3.5-35B-A3B`
- **Most reliable engine:** `vllm-nightly` — every successful LLM benchmark on this hardware has used it
- **Confirmed general blockers to avoid:** `sglang` deploy-timeout on GB10; `vllm` (qujing image) deploy-timeout for several models; `vllm-nightly` Transformers mismatch for `qwen3_5`, `gemma4`, `glm4_moe_lite`, `MiniCPM-o-4_5`, `qwen3_asr` architectures

## Task Board
- [ ] Validate baseline for **Qwen3.5-35B-A3B** on `vllm-nightly` — new model coverage; only ready engine available.
- [ ] Validate baseline for **GLM-4.5-Air-nvfp4** on `vllm-nightly` — new model coverage; highest-success engine for this hardware family.

## Tasks
```yaml
- kind: validate
  model: Qwen3.5-35B-A3B
  engine: vllm-nightly
  engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 8192
  search_space: {}
  benchmark:
    concurrency: [1, 2, 4]
    input_tokens: [128]
    max_tokens: [128, 256]
    requests_per_combo: 50
  reason: "Pending validate_baseline obligation for this unexplored 66.97 GiB MoE model. vllm-nightly is the only ready engine and has successfully served other Qwen models on GB10. The Qwen3.5-27B+vllm-nightly denial is for a different model variant/architecture; this exact combo is explicitly Ready and absent from every denylist."
- kind: validate
  model: GLM-4.5-Air-nvfp4
  engine: vllm-nightly
  engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 8192
  search_space: {}
  benchmark:
    concurrency: [1, 2, 4]
    input_tokens: [128]
    max_tokens: [128, 256]
    requests_per_combo: 50
  reason: "Pending validate_baseline obligation for this unexplored 57.68 GiB nvfp4-quantized GLM model. vllm-nightly has the highest success rate on GB10 and has already validated other GLM families (GLM-4.1V, GLM-4.6V). The GLM-4.5-Air-FP8 blockers are for a different format/variant; this exact combo is explicitly Ready and absent from every denylist."
```
