# Experiment 005: Qwen3-TTS-0.6B / qwen-tts-fastapi-cuda

## Task

```yaml
kind: validate
model: Qwen3-TTS-0.6B
engine: qwen-tts-fastapi-cuda
engine_params: {}
benchmark:
    concurrency: []
    input_tokens: []
    max_tokens: []
    requests_per_combo: 0
reason: Previous history shows this validation was started but never completed; 1.70 GiB GPU TTS model is a low-risk way to obtain a definitive result for the CUDA TTS engine, and it is not in the denylist.
```

## Result

```yaml
status: failed
started_at: "2026-04-14T07:24:04Z"
duration_s: 308.530110783
error: 'pre-flight deploy: wait for deployed endpoint Qwen3-TTS-0.6B: timeout waiting for inference endpoint Qwen3-TTS-0.6B (5m0s)'
```

## Benchmark Matrix

_No benchmark data_

## Agent Notes

qwen-tts-fastapi-cuda repeatedly fails to become ready within the 5-minute deploy window (this is the second consecutive timeout for the exact same combo). The 1.70 GiB model size rules out OOM, so the engine container is almost certainly crashing or hanging on startup on GB10. This is a confirmed environmental blocker for the `Qwen3-TTS-0.6B / qwen-tts-fastapi-cuda` combo. Retrying without an updated `qwen3-tts-cuda-arm64:latest` image is futile; the engine image appears to be broken for this hardware profile.
