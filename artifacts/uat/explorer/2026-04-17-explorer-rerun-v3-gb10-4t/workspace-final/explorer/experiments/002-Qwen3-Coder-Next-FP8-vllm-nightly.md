# Experiment 002: Qwen3-Coder-Next-FP8 / vllm-nightly

## Task

```yaml
kind: validate
model: Qwen3-Coder-Next-FP8
engine: vllm-nightly
engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 8192
benchmark:
    concurrency: []
    input_tokens: []
    max_tokens: []
    requests_per_combo: 0
reason: Newer FP8 Qwen Coder architecture at 74.86 GiB. Pushes the size boundary even further and checks whether vllm-nightly correctly handles FP8 weights. Ready combo, not blocked, not in denylist.
```

## Result

```yaml
status: failed
started_at: "2026-04-14T10:48:14Z"
duration_s: 316.185932464
error: 'pre-flight deploy: wait for deployed endpoint Qwen3-Coder-Next-FP8: timeout waiting for inference endpoint Qwen3-Coder-Next-FP8 (5m0s)'
```

## Benchmark Matrix

_No benchmark data_

## Agent Notes

The 74.86 GiB model hit a 5-minute deploy timeout. While the weights should fit in the ~101 GiB available VRAM (gpu_memory_utilization=0.85), FP8 weight decompression, CUDA graph compilation, or tensor parallelism setup may exceed the readiness window. Without startup logs we cannot distinguish slow initialization from an OOM crash. Retry only with a longer timeout, chunked prefill disabled, or on a multi-GPU profile; otherwise treat as a size-boundary environmental blocker.
