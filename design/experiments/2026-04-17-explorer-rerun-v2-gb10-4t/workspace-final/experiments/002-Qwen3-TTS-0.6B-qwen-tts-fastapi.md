# Experiment 002: Qwen3-TTS-0.6B / qwen-tts-fastapi

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
reason: qwen-tts-fastapi-cuda is confirmed blocked for this model (repeated deploy timeout). Switching to the CPU-only qwen-tts-fastapi engine bypasses the broken CUDA image and tests whether the TTS model itself is viable.
```

## Result

```yaml
status: cancelled
started_at: "2026-04-14T07:40:42Z"
duration_s: 164.357856332
error: 'exploration 05a6133b6c27bc0c canceled: context deadline exceeded'
```

## Benchmark Matrix

_No benchmark data_

## Agent Notes

The CPU-only qwen-tts-fastapi engine was cancelled by an outer exploration deadline after 164s, with no explicit engine error. This suggests either slow container startup or prolonged model loading on CPU for the 1.70 GiB TTS model. A prior attempt on the same combo was also cancelled, indicating the CPU path may simply be too slow for the current timeout budget. It is not proven non-functional, but retry without a longer timeout is unlikely to yield data.
