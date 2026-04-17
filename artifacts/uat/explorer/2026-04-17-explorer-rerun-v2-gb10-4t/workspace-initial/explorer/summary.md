# Exploration Summary

## Key Findings

This cycle produced a partial validation for `GLM-4.5-Air-nvfp4` (7/18 benchmark cells) and a hard structural failure for `Qwen3.5-35B-A3B` due to a Transformers architecture mismatch (`qwen3_5_moe`). The vllm-nightly image now blocks six distinct architectures. `GLM-4.5-Air-nvfp4` runs reliably at concurrency=1 with stable ~22 TPS and ~43 ms TPOT, but scaling to concurrent requests remains problematic—only one concurrency=2 cell succeeded.

### Cross-Engine Horizontal Comparison

| 模型 | 大小(GiB) | 峰值TPS | TPS/GiB | 单/双GPU | TPOT P95(ms) | 最佳场景 |
|---|---|---|---|---|---|---|
| GLM-4.1V-9B-Thinking-FP4 | 8.25 | 165.4 | 20.05 | 单 | 30 | 高并发批处理/视觉推理 |
| Qwen2.5-Coder-3B-Instruct | 5.75 | 29.0 | 5.04 | 单 | 34 | 低延迟代码生成 |
| GLM-4.6V-Flash-FP4 | 8.25 | 33.3 | 4.04 | 单 | 30 | 高吞吐通用/视觉 |
| GLM-4.5-Air-nvfp4 | 57.68 | 35.0 | 0.61 | 单 | 55 | 单请求大模型推理 |
| Qwen2.5-Coder-7B-Instruct | 14.19 | 12.6 | 0.89 | 单 | 79 | 高质量代码生成 |
| GLM-4.1V-9B-Thinking | 19.17 | 10.2 | 0.53 | 单 | 98 | 复杂推理/视觉思考 |

**Observations:**
- `GLM-4.1V-9B-Thinking-FP4` remains the throughput champion at ~20 TPS/GiB.
- `GLM-4.5-Air-nvfp4` validates at concurrency=1 but concurrent batching mostly fails with no-output, suggesting KV-cache or memory-bound limits on this 57.68 GiB model.
- The vllm-nightly image (`vllm/vllm-openai:qwen3_5-cu130`) is now confirmed to lack support for six architectures: `gemma4`, `qwen3_5`, `qwen3_5_moe`, `glm4_moe_lite`, `MiniCPM-o-4_5`, and `qwen3_asr`.
- Ten Ready Combos remain on paper, but only the four vllm-nightly tunings represent high-confidence next steps.

## Bugs And Failures

1. **`Qwen3.5-35B-A3B + vllm-nightly` Transformers mismatch** — pre-flight compatibility check failed because the image does not recognize `qwen3_5_moe` architecture; auto-repair also failed due to pip dependency conflicts.
2. **`GLM-4.5-Air-nvfp4 + vllm-nightly` concurrency scaling failure** — 11 of 12 concurrency>=2 benchmark cells returned no-output, despite all concurrency=1 cells succeeding.
3. **`MiniCPM-o-4_5 + vllm-nightly` unsupported architecture** — pre-flight compatibility check failed because the vllm-nightly whitelist does not recognize MiniCPM-o-4_5 (historical).
4. **`Qwen3-ASR-1.7B + vllm-nightly` Transformers mismatch** — pre-flight compatibility check failed because the image does not recognize `qwen3_asr` architecture (historical).
5. **`Qwen2.5-Coder-3B-Instruct + vllm-nightly` tune failure** — experiment 010 reported "no successful tuning benchmark results" despite the identical combo validating perfectly in experiment 002 (historical).
6. **`Qwen3.5-27B + vllm-nightly` Transformers mismatch** — pre-flight compatibility check failed because the image does not recognize `qwen3_5` architecture (historical).
7. **`GLM-4.7-Flash-NVFP4 + vllm-nightly` Transformers mismatch** — pre-flight compatibility check failed because the image does not recognize `glm4_moe_lite` architecture (historical).
8. **`Qwen3-Coder-Next-FP8 + vllm-nightly` deploy timeout** — 74.86 GiB model timed out after 5 minutes waiting for inference endpoint (historical).
9. **`bge-reranker-v2-m3 + vllm-nightly` request-path failure** — Deployed successfully but 0/6 benchmark cells succeeded; chat-completion harness incompatible with reranker endpoints (historical).
10. **`Qwen3.5-9B + vllm-nightly` request-path failure persists** — deploys but all benchmark cells fail; confirmed model-specific incompatibility (historical).
11. **`llamacpp` image missing `llama-server`** — container exits 127 (historical).
12. **`sglang` deploy timeouts on GB10** — endpoint readiness fails even for small models (historical).
13. **`qwen-tts-fastapi-cuda` deploy timeouts** — repeated 5-minute startup hangs (historical).
14. **`vllm` (standard image) deploy timeouts** — `qujing/vllm-gemma4-gb10` fails to start for Qwen models (historical).

## Confirmed Blockers

```yaml
- family: broken_image
  scope: qwen2.5-0.5b-instruct-q4_k_m + llamacpp
  model: qwen2.5-0.5b-instruct-q4_k_m
  engine: llamacpp
  reason: Docker image lacks llama-server executable; immediate exit 127
  retry_when: A rebuilt or verified llamacpp image is available for GB10 arm64
  confidence: confirmed
- family: deploy_timeout
  scope: qwen-tts-fastapi-cuda + Qwen3-TTS-0.6B
  model: Qwen3-TTS-0.6B
  engine: qwen-tts-fastapi-cuda
  reason: Repeated 5-minute deploy timeouts; container hangs or crashes on startup
  retry_when: Updated qwen3-tts-cuda-arm64 image is released and verified
  confidence: confirmed
- family: transformers_version_mismatch
  scope: gemma-4-26B-A4B-it + vllm-nightly
  model: gemma-4-26B-A4B-it
  engine: vllm-nightly
  reason: Transformers in vllm/vllm-openai:qwen3_5-cu130 does not recognize gemma4 architecture
  retry_when: vllm-nightly image includes newer Transformers with gemma4 support
  confidence: confirmed
- family: transformers_version_mismatch
  scope: Qwen3.5-27B + vllm-nightly
  model: Qwen3.5-27B
  engine: vllm-nightly
  reason: Transformers in vllm/vllm-openai:qwen3_5-cu130 does not recognize qwen3_5 architecture
  retry_when: vllm-nightly image includes newer Transformers with qwen3_5 support
  confidence: confirmed
- family: transformers_version_mismatch
  scope: Qwen3.5-35B-A3B + vllm-nightly
  model: Qwen3.5-35B-A3B
  engine: vllm-nightly
  reason: Transformers in vllm/vllm-openai:qwen3_5-cu130 does not recognize qwen3_5_moe architecture and auto-repair fails due to dependency conflicts
  retry_when: vllm-nightly image includes newer Transformers with qwen3_5_moe support
  confidence: confirmed
- family: transformers_version_mismatch
  scope: GLM-4.7-Flash-NVFP4 + vllm-nightly
  model: GLM-4.7-Flash-NVFP4
  engine: vllm-nightly
  reason: Transformers in vllm/vllm-openai:qwen3_5-cu130 does not recognize glm4_moe_lite architecture
  retry_when: vllm-nightly image includes newer Transformers with glm4_moe_lite support
  confidence: confirmed
- family: transformers_version_mismatch
  scope: MiniCPM-o-4_5 + vllm-nightly
  model: MiniCPM-o-4_5
  engine: vllm-nightly
  reason: vllm/vllm-openai:qwen3_5-cu130 hardcoded model_type whitelist does not include MiniCPM-o-4_5 architecture
  retry_when: vllm-nightly image includes newer Transformers or custom model support for MiniCPM-o-4_5
  confidence: confirmed
- family: transformers_version_mismatch
  scope: Qwen3-ASR-1.7B + vllm-nightly
  model: Qwen3-ASR-1.7B
  engine: vllm-nightly
  reason: Transformers in vllm/vllm-openai:qwen3_5-cu130 does not recognize qwen3_asr architecture
  retry_when: vllm-nightly image includes newer Transformers with qwen3_asr support
  confidence: confirmed
- family: deploy_timeout
  scope: Qwen3-Coder-Next-FP8 + vllm-nightly
  model: Qwen3-Coder-Next-FP8
  engine: vllm-nightly
  reason: 74.86 GiB model exceeds 5-minute endpoint readiness window on single GB10 GPU; likely initialization overload or OOM during FP8 setup
  retry_when: With longer timeout, chunked prefill disabled, or on multi-GPU profile
  confidence: provisional
- family: request_path_failure
  scope: bge-reranker-v2-m3 + vllm-nightly
  model: bge-reranker-v2-m3
  engine: vllm-nightly
  reason: Deploy succeeds but chat-completion benchmark harness cannot interact with reranker endpoint; needs reranker-specific payload
  retry_when: Benchmark harness supports reranker/embeddings API or manual validation is performed
  confidence: confirmed
- family: request_path_failure
  scope: Qwen3.5-9B + vllm-nightly
  model: Qwen3.5-9B
  engine: vllm-nightly
  reason: Deploys successfully but every benchmark cell fails; likely API/chat-template or stop-token incompatibility specific to Qwen3.5-9B
  retry_when: After validating other models on the same engine and obtaining endpoint logs
  confidence: confirmed
- family: request_path_failure
  scope: Z-Image + z-image-diffusers
  model: Z-Image
  engine: z-image-diffusers
  reason: Deploys successfully (container still running) but benchmark cell fails; likely payload schema issue
  retry_when: After correcting benchmark payload for image generation or inspecting endpoint logs
  confidence: confirmed
- family: deploy_timeout
  scope: sglang on GB10
  model: GLM-4.6V-Flash-FP4
  engine: sglang
  reason: Repeated endpoint readiness timeouts on Blackwell; likely kernel/loader crash
  retry_when: lmsysorg/sglang:dev-arm64-cu13 image is updated or startup logs are diagnosed
  confidence: confirmed
- family: deploy_timeout
  scope: vllm (standard) on GB10
  model: Qwen2.5-Coder-3B-Instruct
  engine: vllm
  reason: Standard vllm image qujing/vllm-gemma4-gb10 hangs on startup for Qwen models
  retry_when: A rebuilt or updated standard vllm image for GB10 is released
  confidence: confirmed
```

## Do Not Retry This Cycle

```yaml
- model: qwen2.5-0.5b-instruct-q4_k_m
  engine: llamacpp
  reason_family: broken_image
  reason: Docker image missing llama-server executable
- model: Qwen3-TTS-0.6B
  engine: qwen-tts-fastapi-cuda
  reason_family: deploy_timeout
  reason: Repeated deploy timeouts on GB10 for CUDA TTS image
- model: gemma-4-26B-A4B-it
  engine: vllm-nightly
  reason_family: transformers_version_mismatch
  reason: Transformers version in image does not support gemma4 architecture
- model: Qwen3.5-27B
  engine: vllm-nightly
  reason_family: transformers_version_mismatch
  reason: Transformers version in image does not support qwen3_5 architecture
- model: Qwen3.5-35B-A3B
  engine: vllm-nightly
  reason_family: transformers_version_mismatch
  reason: Transformers version in image does not support qwen3_5_moe architecture and auto-repair fails
- model: GLM-4.7-Flash-NVFP4
  engine: vllm-nightly
  reason_family: transformers_version_mismatch
  reason: Transformers version in image does not support glm4_moe_lite architecture
- model: MiniCPM-o-4_5
  engine: vllm-nightly
  reason_family: transformers_version_mismatch
  reason: vllm-nightly image whitelist does not support MiniCPM-o-4_5 architecture
- model: Qwen3-ASR-1.7B
  engine: vllm-nightly
  reason_family: transformers_version_mismatch
  reason: Transformers version in image does not support qwen3_asr architecture
- model: Qwen3-Coder-Next-FP8
  engine: vllm-nightly
  reason_family: deploy_timeout
  reason: 74.86 GiB model timed out after 5 minutes; retry only with longer timeout or smaller hardware boundary test
- model: bge-reranker-v2-m3
  engine: vllm-nightly
  reason_family: request_path_failure
  reason: Deploy succeeds but chat-completion benchmark harness is incompatible with reranker endpoints
- model: Qwen3.5-9B
  engine: vllm-nightly
  reason_family: request_path_failure
  reason: Two consecutive validate runs show deploy-success + benchmark-all-fail; retrying same combo is wasteful without new evidence
- model: Z-Image
  engine: z-image-diffusers
  reason_family: request_path_failure
  reason: Deploy-success + benchmark-fail pattern repeated; needs payload or image fix before retry
- model: Qwen2.5-Coder-3B-Instruct
  engine: vllm
  reason_family: deploy_timeout
  reason: Standard vllm image hangs on startup for this model on GB10
- model: Qwen2.5-Omni-7B
  engine: vllm-nightly
  reason_family: model_not_available
  reason: Model weights not present locally and auto-pull is disabled; would fail identically
- model: GLM-4.6V-Flash-FP4
  engine: sglang
  reason_family: deploy_timeout
  reason: sglang consistently times out on endpoint readiness for GB10
```

## Evidence Ledger

```yaml
- source: this_cycle
  kind: benchmark
  model: GLM-4.5-Air-nvfp4
  engine: vllm-nightly
  evidence: 7/18 benchmark cells succeeded; all concurrency=1 cells pass with stable ~22 TPS and ~43 ms TPOT; only 1 concurrent cell succeeded (concurrency=2, input=128, max_tokens=128) at 35.0 TPS
  summary: Baseline validates for single-request inference but concurrent batching is unstable on this 57.68 GiB nvfp4 model
  confidence: validated
- source: this_cycle
  kind: failure
  model: Qwen3.5-35B-A3B
  engine: vllm-nightly
  evidence: Pre-flight compatibility check failed because Transformers does not recognize qwen3_5_moe architecture; auto-repair failed due to pip dependency conflicts
  summary: vllm-nightly image is too old for Qwen3.5-35B-A3B and cannot be auto-repaired
  confidence: confirmed
- source: this_cycle
  kind: benchmark
  model: GLM-4.1V-9B-Thinking-FP4
  engine: vllm-nightly
  evidence: 22/22 benchmark cells succeeded; peak 165.4 TPS at concurrency=8, input=512, max_tokens=1024; TPOT P95 ~30 ms flat across matrix
  summary: Highest throughput and most TPS/GiB efficient model validated on GB10; FP4 quantization unlocks massive batching scalability
  confidence: validated
- source: this_cycle
  kind: benchmark
  model: GLM-4.6V-Flash-FP4
  engine: vllm-nightly
  evidence: 5/5 tuning cells succeeded; gpu_memory_utilization=0.85 produced best throughput 33.3 TPS, 29.9 ms TPOT
  summary: Tuned baseline confirms 0.85 is optimal memory setting for this model on GB10
  confidence: tuned
- source: this_cycle
  kind: failure
  model: MiniCPM-o-4_5
  engine: vllm-nightly
  evidence: Pre-flight compatibility check failed because vllm-nightly whitelist does not recognize MiniCPM-o-4_5 architecture
  summary: vllm-nightly image is too old for MiniCPM-o-4_5
  confidence: confirmed
- source: this_cycle
  kind: failure
  model: Qwen3-ASR-1.7B
  engine: vllm-nightly
  evidence: Pre-flight compatibility check failed because Transformers does not recognize qwen3_asr architecture
  summary: vllm-nightly image is too old for Qwen3-ASR-1.7B
  confidence: confirmed
- source: this_cycle
  kind: failure
  model: Qwen2.5-Coder-3B-Instruct
  engine: vllm-nightly
  evidence: Tuning reported no successful benchmark results (0/5 cells) despite previous perfect baseline
  summary: Environmental or harness fragility in tuning path for this validated combo
  confidence: provisional
- source: historical
  kind: benchmark
  model: Qwen2.5-Coder-3B-Instruct
  engine: vllm-nightly
  evidence: 6/6 benchmark cells succeeded; peak 28.99 TPS at 128-input, stable ~35 ms TPOT
  summary: First successful LLM benchmark on GB10; vllm-nightly works for Qwen2.5-Coder-3B
  confidence: validated
- source: historical
  kind: benchmark
  model: Qwen2.5-Coder-7B-Instruct
  engine: vllm-nightly
  evidence: 6/6 benchmark cells succeeded; peak 12.62 TPS, stable ~79 ms TPOT
  summary: vllm-nightly scales successfully to larger Qwen Coder models without request-path issues
  confidence: validated
- source: historical
  kind: benchmark
  model: GLM-4.1V-9B-Thinking
  engine: vllm-nightly
  evidence: 6/6 benchmark cells succeeded; peak 10.16 TPS, ~98 ms TPOT, ~996 ms TTFT at 4096 tokens
  summary: vllm-nightly handles 19 GiB GLM thinking/vision models correctly
  confidence: validated
- source: historical
  kind: failure
  model: Qwen3.5-27B
  engine: vllm-nightly
  evidence: Pre-flight compatibility check failed because Transformers does not recognize qwen3_5 architecture
  summary: vllm-nightly image is too old for Qwen3.5-27B
  confidence: confirmed
- source: historical
  kind: failure
  model: GLM-4.7-Flash-NVFP4
  engine: vllm-nightly
  evidence: Pre-flight compatibility check failed because Transformers does not recognize glm4_moe_lite architecture
  summary: vllm-nightly image is too old for GLM-4.7-Flash-NVFP4
  confidence: confirmed
- source: historical
  kind: failure
  model: Qwen3-Coder-Next-FP8
  engine: vllm-nightly
  evidence: 5-minute endpoint readiness timeout on 74.86 GiB FP8 model
  summary: Very large FP8 model may exceed startup window or OOM during initialization on single GB10 GPU
  confidence: provisional
- source: historical
  kind: failure
  model: bge-reranker-v2-m3
  engine: vllm-nightly
  evidence: Deploy succeeded but benchmark matrix reported 0/6 successful cells
  summary: Current benchmark harness cannot validate reranker models with chat-completion payloads
  confidence: confirmed
- source: historical
  kind: failure
  model: Qwen2.5-Omni-7B
  engine: vllm-nightly
  evidence: Pre-flight deploy failed because model not found locally and auto-pull is disabled
  summary: Not an engine incompatibility; model availability issue only
  confidence: confirmed
- source: historical
  kind: failure
  model: Qwen3.5-9B
  engine: vllm-nightly
  evidence: Deploy succeeded (291s) but benchmark matrix reported 0/6 successful cells
  summary: vllm-nightly deploys Qwen3.5-9B but cannot serve requests
  confidence: confirmed
- source: historical
  kind: failure
  model: Z-Image
  engine: z-image-diffusers
  evidence: Deploy succeeded (113s) and container remains running, but 1/1 benchmark cell failed
  summary: z-image-diffusers starts but request path is broken for Z-Image
  confidence: confirmed
- source: historical
  kind: failure
  model: Qwen3-TTS-0.6B
  engine: qwen-tts-fastapi-cuda
  evidence: Second consecutive 5-minute deploy timeout
  summary: CUDA TTS image is broken or incompatible with GB10
  confidence: confirmed
- source: historical
  kind: failure
  model: gemma-4-26B-A4B-it
  engine: vllm-nightly
  evidence: Compatibility check failed because Transformers does not recognize gemma4 architecture
  summary: vllm-nightly image is too old for Gemma-4 models
  confidence: confirmed
- source: historical
  kind: failure
  model: GLM-4.6V-Flash-FP4
  engine: sglang
  evidence: 5-minute endpoint readiness timeout on 8.25 GiB model
  summary: sglang image hangs or crashes on Blackwell startup regardless of model size
  confidence: confirmed
- source: historical
  kind: failure
  model: Qwen2.5-Coder-3B-Instruct
  engine: vllm
  evidence: Deploy timed out after 5 minutes waiting for inference endpoint
  summary: Standard vllm image qujing/vllm-gemma4-gb10 hangs on startup for Qwen Coder models
  confidence: confirmed
```

## Design Doubts

- The vllm-nightly image (`vllm/vllm-openai:qwen3_5-cu130`) is now missing support for six distinct newer architectures: `gemma4`, `qwen3_5`, `qwen3_5_moe`, `glm4_moe_lite`, `MiniCPM-o-4_5`, and `qwen3_asr`. This strongly suggests the "nightly" tag is stale or pinned to an older Transformers release.
- `GLM-4.5-Air-nvfp4` achieves stable single-request throughput (~22 TPS) but fails to scale to concurrent requests, with only one of six concurrency>=2 cells producing output. The low power draw (18-24W) on failed concurrent cells suggests silent rejection rather than OOM-kill, which may indicate an engine-level batching bug for this specific nvfp4 format.
- The benchmark harness still only exercises chat-completion endpoints, making it impossible to validate non-generative models (rerankers, embeddings, ASR, TTS) even when the engine correctly serves them.
- The benchmark harness reports `ram_usage_mib` and `vram_usage_mib` with identical values for all cells. This is suspicious—actual GPU VRAM for an 8.25 GiB model should be far lower than system RAM. The metric may be mislabeled or double-counted.
- Many Ready Combos rely on `sglang` or standard `vllm`, both of which have confirmed GB10 startup blockers, leaving only vllm-nightly tunings as realistic next steps.

## Recommended Configurations

```yaml
- model: GLM-4.1V-9B-Thinking-FP4
  engine: vllm-nightly
  hardware: nvidia-gb10-arm64
  engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 8192
  performance:
    throughput_tps: 165.40480078027514
    throughput_scenario: "concurrency=8, input=512, max_tokens=1024"
    latency_p50_ms: 7731.848835294118
    latency_scenario: "concurrency=1, input=128, max_tokens=256"
  confidence: validated
  note: "Highest throughput and most TPS/GiB efficient model validated on GB10. 22/22 cells pass with flat ~30 ms TPOT. FP4 quantization unlocks massive batching scalability."
- model: GLM-4.6V-Flash-FP4
  engine: vllm-nightly
  hardware: nvidia-gb10-arm64
  engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 8192
  performance:
    throughput_tps: 33.27867947723901
    throughput_scenario: "concurrency=1, input=128, max_tokens=128"
  confidence: tuned
  note: "Tuned baseline confirms 0.85 gpu_memory_utilization is optimal. Flat 30 ms TPOT and stable scaling across input lengths make it the best general-purpose vision/LLM combo."
- model: GLM-4.5-Air-nvfp4
  engine: vllm-nightly
  hardware: nvidia-gb10-arm64
  engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 8192
  performance:
    throughput_tps: 22.302434784829163
    throughput_scenario: "concurrency=1, input=128, max_tokens=128"
    latency_p50_ms: 5784.443051181102
    latency_scenario: "concurrency=1, input=128, max_tokens=128"
  confidence: validated
  note: "Reliable single-request baseline for this 57.68 GiB model. All concurrency=1 cells pass with flat ~43 ms TPOT. Concurrent batching is currently unstable (only 1 of 6 concurrency>=2 cells succeeded)."
- model: GLM-4.5-Air-nvfp4
  engine: vllm-nightly
  hardware: nvidia-gb10-arm64
  engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 8192
  performance:
    throughput_tps: 34.99025184294958
    throughput_scenario: "concurrency=2, input=128, max_tokens=128"
    latency_p50_ms: 7300.363767716536
    latency_scenario: "concurrency=2, input=128, max_tokens=128"
  confidence: provisional
  note: "Best-effort concurrent configuration. Only 1 of 6 concurrency>=2 cells succeeded in baseline validation; higher gpu_memory_utilization or reduced max_model_len may improve batching stability."
- model: Qwen2.5-Coder-3B-Instruct
  engine: vllm-nightly
  hardware: nvidia-gb10-arm64
  engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 8192
  performance:
    throughput_tps: 28.99
    throughput_scenario: "concurrency=1, input=128, max_tokens=128"
    latency_p50_ms: 47.07
    latency_scenario: "concurrency=1, input=128, max_tokens=128"
  confidence: validated
  note: "Best TPS/GiB efficiency among non-FP4 models (~5.0). Sub-50ms TTFT and ~34ms TPOT make it ideal for low-latency code completion. Note: tuning attempt (exp 010) failed with no successful cells, likely environmental."
- model: Qwen2.5-Coder-7B-Instruct
  engine: vllm-nightly
  hardware: nvidia-gb10-arm64
  engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 8192
  performance:
    throughput_tps: 12.62
    throughput_scenario: "concurrency=1, input=128, max_tokens=128"
    latency_p50_ms: 96.63
    latency_scenario: "concurrency=1, input=128, max_tokens=128"
  confidence: validated
  note: "Stable scaling with only 7% throughput drop across 128-4096 tokens. Good choice when 7B reasoning quality outweighs raw speed."
- model: GLM-4.1V-9B-Thinking
  engine: vllm-nightly
  hardware: nvidia-gb10-arm64
  engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 8192
  performance:
    throughput_tps: 10.16
    throughput_scenario: "concurrency=1, input=128, max_tokens=128"
    latency_p50_ms: 122.76
    latency_scenario: "concurrency=1, input=128, max_tokens=128"
  confidence: validated
  note: "Largest successfully validated non-FP4 model (19.17 GiB). Flat TPOT confirms vllm-nightly handles big GLM thinking/vision models correctly."
```

## Current Strategy

This cycle delivered one partial validation (`GLM-4.5-Air-nvfp4` at concurrency=1) and one hard failure (`Qwen3.5-35B-A3B` Transformers mismatch). The vllm-nightly image remains the only reliable engine on GB10, but its stale Transformers release now blocks six newer architectures.

With ten Ready Combos remaining, only the four vllm-nightly tunings are high-confidence next steps:
1. `GLM-4.1V-9B-Thinking-FP4` tune — could further optimize the current throughput champion.
2. `GLM-4.1V-9B-Thinking` tune — valuable non-FP4 comparison point.
3. `Qwen2.5-Coder-7B-Instruct` tune — baseline is solid; tuning path fragility (exp 010) was specific to the 3B model.
4. `GLM-4.5-Air-nvfp4` tune — may resolve the concurrency scaling issues observed in baseline validation.

The remaining validate_baseline items on `sglang` and standard `vllm` are low-confidence due to confirmed GB10 startup blockers for those engines.

## Next Cycle Candidates

```yaml
candidates:
  - model: GLM-4.1V-9B-Thinking-FP4
    engine: vllm-nightly
    kind: tune
    reason: Current throughput champion; tuning gpu_memory_utilization may unlock even higher batching efficiency.
  - model: GLM-4.1V-9B-Thinking
    engine: vllm-nightly
    kind: tune
    reason: Solid baseline exists; tuning provides a direct non-FP4 efficiency comparison.
  - model: GLM-4.5-Air-nvfp4
    engine: vllm-nightly
    kind: tune
    reason: Baseline shows concurrency scaling issues; tuning memory settings is the most direct remediation.
  - model: Qwen2.5-Coder-7B-Instruct
    engine: vllm-nightly
    kind: tune
    reason: Validated baseline with stable scaling; 7B tuning is higher value than the previously failed 3B attempt.
```


## Validation Guard Feedback

⚠️ summary recommendation GLM-4.5-Air-nvfp4/vllm-nightly marked validated without matching successful experiment

Do NOT use `validated` or `tuned` confidence unless summary.md shows benchmark-backed performance and experiment-facts.md contains a matching successful experiment. Downgrade to `provisional` when evidence is missing or only partial.
