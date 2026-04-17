# Exploration Summary

## Key Findings

This cycle again produced no new successful benchmarks. The tuning attempt for the sole remaining Ready Combo `GLM-4.5-Air-nvfp4 + vllm-nightly` timed out after 30 minutes with zero cells (experiment 019), and the follow-up concurrency-validation task was automatically skipped due to the prior timeout (experiment 020). Consequently, `available-combos.md` now lists zero Ready Combos, and the only Pending Work items (`GLM-4.7-Flash-NVFP4` and `MiniCPM-o-4_5` on `sglang`) are blocked by confirmed GB10 startup timeouts. The exploration frontier is fully stalled until a new engine image or harness fix is delivered.

### Cross-Engine Horizontal Comparison

| 模型 | 大小(GiB) | 峰值TPS | TPS/GiB | 单/双GPU | TPOT P95(ms) | 最佳场景 |
|---|---|---|---|---|---|---|
| GLM-4.1V-9B-Thinking-FP4 | 8.25 | 165.4 | 20.05 | 单 | 30 | 高并发批处理/视觉推理 |
| Qwen2.5-Coder-3B-Instruct | 5.75 | 29.0 | 5.04 | 单 | 34 | 低延迟代码生成 |
| GLM-4.6V-Flash-FP4 | 8.25 | 33.3 | 4.04 | 单 | 30 | 高吞吐通用/视觉 |
| Qwen2.5-Coder-7B-Instruct | 14.19 | 12.6 | 0.89 | 单 | 79 | 高质量代码生成 |
| GLM-4.5-Air-nvfp4 | 57.68 | 35.0 | 0.61 | 单 | 55 | 单请求大模型推理(16K已验证) |
| GLM-4.1V-9B-Thinking | 19.17 | 10.2 | 0.53 | 单 | 98 | 复杂推理/视觉思考 |

**Observations:**
- `GLM-4.1V-9B-Thinking-FP4` remains the throughput champion at ~20 TPS/GiB.
- `GLM-4.5-Air-nvfp4` has validated long-context support up to 16K tokens at concurrency=1 (experiment 013), though throughput drops to ~11.7 TPS due to prefill overhead. The latest attempts to tune or re-validate concurrency scaling both failed, so no new data was added.
- The systematic vllm-nightly tuning harness timeout is now confirmed across Qwen Coder, GLM-4.1V, and GLM-4.5-Air models, making tune tasks on this engine unproductive until the root cause is fixed.
- Standard `vllm` remains blocked for NVFP4 models (missing `compressed_tensors`) and Qwen models (deploy timeout), and `sglang` remains blocked on GB10.

## Bugs And Failures

1. **`GLM-4.5-Air-nvfp4 + vllm-nightly` tuning timeout** — experiment 019 timed out after 30 minutes with zero benchmark cells despite a minimal search space (5 gpu_memory_utilization values × 1 cell). Matches the identical timeout pattern in experiments 014 and 018 (this_cycle).
2. **`GLM-4.5-Air-nvfp4 + vllm-nightly` validate skip** — experiment 020 was automatically skipped because experiment 019 timed out, so no concurrency-scaling evidence was gathered (this_cycle).
3. **`GLM-4.7-Flash-NVFP4 + vllm` missing dependency** — experiment 017 failed with `ModuleNotFoundError: No module named 'compressed_tensors'` during startup, masked as a 10-minute deploy timeout (historical).
4. **`GLM-4.1V-9B-Thinking + vllm-nightly` tuning timeout** — experiment 018 timed out after 30 minutes with zero benchmark cells despite a validated baseline in experiment 005 (historical).
5. **`Qwen2.5-Coder-7B-Instruct + vllm-nightly` tuning timeout** — experiment 014 timed out after 30 minutes with zero benchmark cells, despite the identical combo validating successfully in experiment 004 (historical).
6. **`Qwen3.5-35B-A3B + vllm-nightly` Transformers mismatch** — pre-flight compatibility check failed because the image does not recognize `qwen3_5_moe` architecture; auto-repair also failed due to pip dependency conflicts (historical).
7. **`GLM-4.5-Air-nvfp4 + vllm-nightly` concurrency scaling failure** — historical baseline shows only 1 of 6 concurrency>=2 benchmark cells returned output, despite all concurrency=1 cells succeeding (historical).
8. **`MiniCPM-o-4_5 + vllm-nightly` unsupported architecture** — pre-flight compatibility check failed because the vllm-nightly whitelist does not recognize MiniCPM-o-4_5 architecture (historical).
9. **`Qwen3-ASR-1.7B + vllm-nightly` Transformers mismatch** — pre-flight compatibility check failed because the image does not recognize `qwen3_asr` architecture (historical).
10. **`Qwen2.5-Coder-3B-Instruct + vllm-nightly` tune failure** — experiment 010 reported "no successful tuning benchmark results" despite the identical combo validating perfectly in experiment 002 (historical).
11. **`Qwen3.5-27B + vllm-nightly` Transformers mismatch** — pre-flight compatibility check failed because the image does not recognize `qwen3_5` architecture (historical).
12. **`GLM-4.7-Flash-NVFP4 + vllm-nightly` Transformers mismatch** — pre-flight compatibility check failed because the image does not recognize `glm4_moe_lite` architecture (historical).
13. **`Qwen3-Coder-Next-FP8 + vllm-nightly` deploy timeout** — 74.86 GiB model timed out after 5 minutes waiting for inference endpoint (historical).
14. **`bge-reranker-v2-m3 + vllm-nightly` request-path failure** — Deployed successfully but 0/6 benchmark cells succeeded; chat-completion harness incompatible with reranker endpoints (historical).
15. **`Qwen3.5-9B + vllm-nightly` request-path failure persists** — deploys but all benchmark cells fail; confirmed model-specific incompatibility (historical).
16. **`llamacpp` image missing `llama-server`** — container exits 127 (historical).
17. **`sglang` deploy timeouts on GB10** — endpoint readiness fails even for small models (historical).
18. **`qwen-tts-fastapi-cuda` deploy timeouts** — repeated 5-minute startup hangs (historical).
19. **`vllm` (standard image) deploy timeouts for Qwen models** — `qujing/vllm-gemma4-gb10` fails to start for Qwen Coder models (historical).

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
  scope: vllm (standard) on GB10 for Qwen models
  model: Qwen2.5-Coder-3B-Instruct
  engine: vllm
  reason: Standard vllm image qujing/vllm-gemma4-gb10 hangs on startup for Qwen Coder models
  retry_when: A rebuilt or updated standard vllm image for GB10 is released
  confidence: confirmed
- family: missing_dependency
  scope: GLM-4.7-Flash-NVFP4 + vllm
  model: GLM-4.7-Flash-NVFP4
  engine: vllm
  reason: Standard vllm image qujing/vllm-gemma4-gb10 lacks the compressed_tensors package required for NVFP4 model loading
  retry_when: A rebuilt standard vllm image includes compressed_tensors or a GB10-specific vllm image with NVFP4 support is released
  confidence: confirmed
- family: tuning_timeout
  scope: GLM-4.5-Air-nvfp4 + vllm-nightly
  model: GLM-4.5-Air-nvfp4
  engine: vllm-nightly
  reason: Tuning exploration timed out after 30m with zero cells on a minimal search space; same systematic pattern as experiments 014 and 018
  retry_when: Tuning harness timeout issue is diagnosed or resolved, or single-config validation path is available
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
- model: GLM-4.7-Flash-NVFP4
  engine: vllm
  reason_family: missing_dependency
  reason: Standard vllm image missing compressed_tensors module for NVFP4 loading
- model: GLM-4.1V-9B-Thinking
  engine: vllm-nightly
  reason_family: tuning_timeout
  reason: Tuning exploration timed out after 30m with zero cells; retry only with reduced search space or diagnosed harness
- model: GLM-4.5-Air-nvfp4
  engine: vllm-nightly
  reason_family: tuning_timeout
  reason: Tuning exploration timed out after 30m with zero cells on minimal search space; retrying tune is wasteful without harness fix
```

## Evidence Ledger

```yaml
- source: this_cycle
  kind: failure
  model: GLM-4.5-Air-nvfp4
  engine: vllm-nightly
  evidence: Tuning exploration timed out after 30m0s with zero benchmark cells despite minimal 5-config search space
  summary: Systematic tuning harness timeout on vllm-nightly affects GLM-4.5-Air-nvfp4
  confidence: confirmed
- source: this_cycle
  kind: failure
  model: GLM-4.5-Air-nvfp4
  engine: vllm-nightly
  evidence: Validation task skipped due to prior deploy failure from experiment 019 timeout
  summary: Infrastructure skip logic blocked follow-up validation; no new concurrency-scaling data obtained
  confidence: confirmed
- source: historical
  kind: failure
  model: GLM-4.7-Flash-NVFP4
  engine: vllm
  evidence: Deploy timed out after 10m with underlying error ModuleNotFoundError: No module named 'compressed_tensors'
  summary: Standard vllm image is missing the compressed_tensors dependency required for NVFP4 models
  confidence: confirmed
- source: historical
  kind: failure
  model: GLM-4.1V-9B-Thinking
  engine: vllm-nightly
  evidence: Tuning exploration timed out after 30m0s with zero benchmark cells; baseline validated successfully in exp 005
  summary: Environmental or harness fragility in tuning path for GLM-4.1V-9B-Thinking on vllm-nightly
  confidence: provisional
- source: historical
  kind: benchmark
  model: GLM-4.5-Air-nvfp4
  engine: vllm-nightly
  evidence: 3/3 benchmark cells succeeded at concurrency=1 with input_tokens=[128,512,16384] and max_tokens=256; 16K context throughput=11.70 TPS, TTFT=7757 ms, TPOT=55.4 ms
  summary: Long-context validation confirms the 57.68 GiB nvfp4 model can serve its rated 16K window on vllm-nightly at single-request concurrency
  confidence: validated
- source: historical
  kind: failure
  model: Qwen2.5-Coder-7B-Instruct
  engine: vllm-nightly
  evidence: Tuning exploration timed out after 30m0s with zero benchmark cells despite validated baseline in experiment 004
  summary: Environmental/harness fragility in tuning path for Qwen Coder models on vllm-nightly
  confidence: provisional
- source: historical
  kind: benchmark
  model: GLM-4.5-Air-nvfp4
  engine: vllm-nightly
  evidence: 7/18 benchmark cells succeeded; all concurrency=1 cells pass with stable ~22 TPS and ~43 ms TPOT; only 1 concurrent cell succeeded (concurrency=2, input=128, max_tokens=128) at 35.0 TPS
  summary: Baseline validates for single-request inference but concurrent batching is unstable on this 57.68 GiB nvfp4 model
  confidence: validated
- source: historical
  kind: failure
  model: Qwen3.5-35B-A3B
  engine: vllm-nightly
  evidence: Pre-flight compatibility check failed because Transformers does not recognize qwen3_5_moe architecture; auto-repair failed due to pip dependency conflicts
  summary: vllm-nightly image is too old for Qwen3.5-35B-A3B and cannot be auto-repaired
  confidence: confirmed
- source: historical
  kind: benchmark
  model: GLM-4.1V-9B-Thinking-FP4
  engine: vllm-nightly
  evidence: 22/22 benchmark cells succeeded; peak 165.4 TPS at concurrency=8, input=512, max_tokens=1024; TPOT P95 ~30 ms flat across matrix
  summary: Highest throughput and most TPS/GiB efficient model validated on GB10; FP4 quantization unlocks massive batching scalability
  confidence: validated
- source: historical
  kind: benchmark
  model: GLM-4.6V-Flash-FP4
  engine: vllm-nightly
  evidence: 5/5 tuning cells succeeded; gpu_memory_utilization=0.85 produced best throughput 33.3 TPS, 29.9 ms TPOT
  summary: Tuned baseline confirms 0.85 is optimal memory setting for this model on GB10
  confidence: tuned
- source: historical
  kind: failure
  model: MiniCPM-o-4_5
  engine: vllm-nightly
  evidence: Pre-flight compatibility check failed because vllm-nightly whitelist does not recognize MiniCPM-o-4_5 architecture
  summary: vllm-nightly image is too old for MiniCPM-o-4_5
  confidence: confirmed
- source: historical
  kind: failure
  model: Qwen3-ASR-1.7B
  engine: vllm-nightly
  evidence: Pre-flight compatibility check failed because Transformers does not recognize qwen3_asr architecture
  summary: vllm-nightly image is too old for Qwen3-ASR-1.7B
  confidence: confirmed
- source: historical
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

- The vllm-nightly tuning harness has now failed with identical 30-minute timeouts on three unrelated models (Qwen2.5-Coder-7B, GLM-4.1V-9B-Thinking, GLM-4.5-Air-nvfp4). The search spaces were small, yet zero cells were measured. This strongly suggests a systematic harness or infrastructure bug (e.g., slow container teardown, benchmark orchestrator deadlock) rather than model-specific pathology.
- With zero Ready Combos remaining and the only Pending Work items tied to `sglang` (blocked by confirmed GB10 startup timeouts), the entire exploration frontier is stalled. No productive local experimentation is possible until a new engine image or harness fix is delivered.
- The automatic skip of experiment 020 based on experiment 019’s timeout is overly conservative. A tuning timeout should not necessarily invalidate a follow-up validation task that uses a fixed, historically successful configuration.
- The standard vllm image (`qujing/vllm-gemma4-gb10`) is missing `compressed_tensors`, a dependency required for NVFP4 quantized models. This suggests the GB10-specific vllm build is stale and incomplete.
- The vllm-nightly image (`vllm/vllm-openai:qwen3_5-cu130`) is now missing support for six distinct newer architectures: `gemma4`, `qwen3_5`, `qwen3_5_moe`, `glm4_moe_lite`, `MiniCPM-o-4_5`, and `qwen3_asr`. This strongly suggests the "nightly" tag is stale or pinned to an older Transformers release.
- `GLM-4.5-Air-nvfp4` achieves stable single-request throughput (~22 TPS) but fails to scale to concurrent requests, with only one of six concurrency>=2 cells producing output. The low power draw (18-24W) on failed concurrent cells suggests silent rejection rather than OOM-kill, which may indicate an engine-level batching bug for this specific nvfp4 format.
- The benchmark harness still only exercises chat-completion endpoints, making it impossible to validate non-generative models (rerankers, embeddings, ASR, TTS) even when the engine correctly serves them.
- The benchmark harness reports `ram_usage_mib` and `vram_usage_mib` with identical values for all cells. This is suspicious—actual GPU VRAM for an 8.25 GiB model should be far lower than system RAM. The metric may be mislabeled or double-counted.

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
  note: Highest throughput and most TPS/GiB efficient model validated on GB10. 22/22 cells pass with flat ~30 ms TPOT. FP4 quantization unlocks massive batching scalability.
- model: GLM-4.6V-Flash-FP4
  engine: vllm-nightly
  hardware: nvidia-gb10-arm64
  engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 8192
  performance:
    throughput_tps: 33.27867947723901
    throughput_scenario: "concurrency=1, input=128, max_tokens=128"
    latency_p50_ms: 0
  confidence: tuned
  note: Tuned baseline confirms 0.85 gpu_memory_utilization is optimal. Flat 30 ms TPOT and stable scaling across input lengths make it the best general-purpose vision/LLM combo.
- model: GLM-4.5-Air-nvfp4
  engine: vllm-nightly
  hardware: nvidia-gb10-arm64
  engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 16896
  performance:
    throughput_tps: 22.836216261356345
    throughput_scenario: "concurrency=1, input=128, max_tokens=256"
    latency_p50_ms: 11245.433539215686
    latency_scenario: "concurrency=1, input=128, max_tokens=256"
  confidence: validated
  note: Reliable single-request baseline for this 57.68 GiB model validated in experiment 013. All short-context concurrency=1 cells pass with flat ~43 ms TPOT.
- model: GLM-4.5-Air-nvfp4
  engine: vllm-nightly
  hardware: nvidia-gb10-arm64
  engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 16896
  performance:
    throughput_tps: 11.703432180176657
    throughput_scenario: "concurrency=1, input=16384, max_tokens=256"
    latency_p50_ms: 21940.177356862747
    latency_scenario: "concurrency=1, input=16384, max_tokens=256"
  confidence: validated
  note: Validated long-context configuration from experiment 013. Throughput halves vs short-context due to prefill overhead, but TPOT stays flat at ~55 ms and the model reliably serves its full 16K rated window.
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
  note: Best-effort concurrent configuration from historical baseline. Only 1 of 6 concurrency>=2 cells succeeded; latest tuning (exp 019) and validation (exp 020) both failed, so this config remains unverified.
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
  confidence: provisional
  note: "validation_guard: downgraded to provisional (Qwen2.5-Coder-3B-Instruct/vllm-nightly latency is not grounded by a matching benchmark scenario); Best TPS/GiB efficiency among non-FP4 models (~5.0). Sub-50ms TTFT and ~34ms TPOT make it ideal for low-latency code completion. Note: tuning attempt (exp 010) failed with no successful cells, likely environmental."
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
  confidence: provisional
  note: "validation_guard: downgraded to provisional (Qwen2.5-Coder-7B-Instruct/vllm-nightly latency is not grounded by a matching benchmark scenario); Stable scaling with only 7% throughput drop across 128-4096 tokens. Good choice when 7B reasoning quality outweighs raw speed. Tuning attempt (exp 014) timed out without cells, so no tuned config yet."
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
  confidence: provisional
  note: "validation_guard: downgraded to provisional (GLM-4.1V-9B-Thinking/vllm-nightly latency is not grounded by a matching benchmark scenario); Largest successfully validated non-FP4 model (19.17 GiB). Flat TPOT confirms vllm-nightly handles big GLM thinking/vision models correctly. Tuning attempt (exp 018) timed out, so no tuned config yet."
```

## Current Strategy

The exploration frontier is completely stalled. `available-combos.md` shows zero Ready Combos, and the only Pending Work items (`GLM-4.7-Flash-NVFP4` and `MiniCPM-o-4_5` on `sglang`) are blocked by confirmed GB10 startup timeouts. The most recent cycle demonstrated that tune tasks on `vllm-nightly` now fail systematically with 30-minute timeouts across multiple model families, and the cascade skip logic prevents follow-up validations. No further local experimentation is productive until one of the following external conditions is met:
- A refreshed `vllm-nightly` image with newer Transformers (supporting `gemma4`, `qwen3_5`, `glm4_moe_lite`, `MiniCPM-o-4_5`, `qwen3_asr`).
- A fixed `sglang` image that boots reliably on Blackwell/GB10.
- A rebuilt standard `vllm` image that includes `compressed_tensors` for NVFP4 support.
- A diagnosed and fixed tuning-harness timeout or skip-logic bug.

Until then, the recommended action is to pause the exploration cycle and wait for an updated environment.

## Next Cycle Candidates

```yaml
candidates: []
```

_No viable candidates remain: zero Ready Combos, no unblocked Pending Work, and confirmed systematic tuning timeouts make any retry futile without an infrastructure fix._
