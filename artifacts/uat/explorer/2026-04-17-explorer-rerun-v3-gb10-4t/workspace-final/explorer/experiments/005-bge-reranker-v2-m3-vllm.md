# Experiment 005: bge-reranker-v2-m3 / vllm

## Task

```yaml
kind: validate
model: bge-reranker-v2-m3
engine: vllm
engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 8192
benchmark:
    concurrency: []
    input_tokens: []
    max_tokens: []
    requests_per_combo: 0
reason: Small embedding model (2.12 GiB) with low startup and memory pressure. If this succeeds, it narrows the vllm failure domain away from 'all models' and toward 'larger or specific-architecture models'. Ready combo and not blocked.
```

## Result

```yaml
status: skipped_timeout
started_at: "2026-04-14T07:43:26Z"
duration_s: 0
error: 'skipped: timeout before execution'
```

## Benchmark Matrix

_No benchmark data_

## Agent Notes

_To be filled by agent after analysis._
