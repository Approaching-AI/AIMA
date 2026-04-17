# Experiment 003: Qwen3-TTS-0.6B / qwen-tts-fastapi

## Task

```yaml
kind: validate
model: Qwen3-TTS-0.6B
engine: qwen-tts-fastapi
engine_params: {}
benchmark:
    concurrency: []
    input_tokens: []
    max_tokens: []
    requests_per_combo: 0
reason: The CUDA variant (qwen-tts-fastapi-cuda) has twice timed out and is effectively broken on GB10, but the CPU-only engine was only cancelled by an outer deadline, never proven non-functional. Validating the CPU path with the tiny 1.70 GiB TTS model tests whether TTS is viable at all on this node. Ready combo and not blocked.
```

## Result

```yaml
status: failed
started_at: "2026-04-14T09:51:14Z"
duration_s: 308.763343132
error: 'pre-flight deploy: wait for deployed endpoint Qwen3-TTS-0.6B: timeout waiting for inference endpoint Qwen3-TTS-0.6B (5m0s)'
```

## Benchmark Matrix

_No benchmark data_

## Agent Notes

_To be filled by agent after analysis._
