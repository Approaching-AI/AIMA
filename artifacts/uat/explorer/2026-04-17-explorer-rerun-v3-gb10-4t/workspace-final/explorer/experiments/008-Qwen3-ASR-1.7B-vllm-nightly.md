# Experiment 008: Qwen3-ASR-1.7B / vllm-nightly

## Task

```yaml
kind: validate
model: Qwen3-ASR-1.7B
engine: vllm-nightly
engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 8192
search_space: {}
benchmark:
    concurrency: []
    input_tokens: []
    max_tokens: []
    requests_per_combo: 0
reason: Pending Work validate_baseline for this Ready combo. Small 4.38 GiB model; recent history only shows failure on glm-asr-fastapi, not on vllm-nightly. Low-risk expansion of Qwen family coverage. Not blocked and not in denylist.
```

## Result

```yaml
status: failed
started_at: "2026-04-16T17:39:55Z"
duration_s: 7.028378597
error: 'pre-flight deploy: deploy Qwen3-ASR-1.7B on vllm-nightly: deploy apply Qwen3-ASR-1.7B: container compatibility check failed for qwen3-asr-1.7b with vllm/vllm-openai:qwen3_5-cu130: ValueError: The checkpoint you are trying to load has model type `qwen3_asr` but Transformers does not recognize this architecture. This could be because of an issue with the checkpoint, or because your version of Transformers is out of date.'
```

## Benchmark Matrix

_No benchmark data_

## Agent Notes

Pre-flight compatibility check failed because the vllm-nightly image lacks support for the `qwen3_asr` model_type in Transformers. This is the same root cause class as the Qwen3.5-27B and GLM-4.7-Flash-NVFP4 blockers: the "nightly" image is pinned to an older Transformers release. ASR models may also require a non-chat-completion benchmark harness, making this a dual blocker.
