# Experiment 002: gemma-4-26B-A4B-it / vllm-nightly

## Task

```yaml
kind: validate
model: gemma-4-26B-A4B-it
engine: vllm-nightly
engine_params:
    enable_chunked_prefill: true
    gpu_memory_utilization: 0.9
    max_model_len: 32768
benchmark:
    concurrency: []
    input_tokens: []
    max_tokens: []
    requests_per_combo: 0
reason: Untried large non-Qwen model on vllm-nightly; 48.07 GiB fits within 122 GB VRAM and tests nightly engine generality and chunked-prefill stability on Blackwell. Not blocked and not in denylist.
```

## Result

```yaml
status: failed
started_at: "2026-04-14T07:18:40Z"
duration_s: 9.034383622
error: 'pre-flight deploy: deploy gemma-4-26B-A4B-it on vllm-nightly: deploy apply gemma-4-26B-A4B-it: container compatibility check failed for gemma-4-26b-a4b-it with vllm/vllm-openai:qwen3_5-cu130: ValueError: The checkpoint you are trying to load has model type `gemma4` but Transformers does not recognize this architecture. This could be because of an issue with the checkpoint, or because your version of Transformers is out of date.'
```

## Benchmark Matrix

_No benchmark data_

## Agent Notes

The failure is a clear model-engine version mismatch: the `vllm/vllm-openai:qwen3_5-cu130` image bundles a Transformers library that predates `gemma4` architecture support, so the container aborts during its compatibility check. This is a confirmed structural blocker for the `gemma-4-26B-A4B-it / vllm-nightly` combo in the current environment. No amount of parameter tuning will fix it; the engine image must be refreshed to a nightly build that includes a newer Transformers version before this combo can be retried.
