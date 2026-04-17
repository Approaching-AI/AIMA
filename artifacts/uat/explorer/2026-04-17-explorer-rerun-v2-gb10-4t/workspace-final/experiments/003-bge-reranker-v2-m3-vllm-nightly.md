# Experiment 003: bge-reranker-v2-m3 / vllm-nightly

## Task

```yaml
kind: validate
model: bge-reranker-v2-m3
engine: vllm-nightly
engine_params:
    gpu_memory_utilization: 0.85
    max_model_len: 8192
benchmark:
    concurrency: []
    input_tokens: []
    max_tokens: []
    requests_per_combo: 0
reason: Tiny 2.12 GiB embedding/reranker model. Verifies that vllm-nightly can serve non-generative endpoints, expanding capability coverage beyond chat-completion LLMs. Ready combo, not blocked, not in denylist.
```

## Result

```yaml
status: failed
started_at: "2026-04-14T10:53:30Z"
duration_s: 36.764319166
error: 'benchmark matrix: no successful cells (total=6)'
```

## Benchmark Matrix

_No benchmark data_

## Agent Notes

The container deployed successfully but all six benchmark cells failed, indicating a request-path mismatch rather than a deploy issue. bge-reranker-v2-m3 is a cross-encoder reranker that expects a `/rerank` or `/embeddings` endpoint schema, whereas the benchmark harness issues chat-completion requests. This is a benchmark-harness limitation, not an engine bug. Validating this model requires a reranker-specific payload or a different evaluation strategy.
