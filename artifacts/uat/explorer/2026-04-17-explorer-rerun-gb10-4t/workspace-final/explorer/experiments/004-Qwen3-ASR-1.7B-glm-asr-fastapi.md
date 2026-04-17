# Experiment 004: Qwen3-ASR-1.7B / glm-asr-fastapi

## Task

```yaml
kind: validate
model: Qwen3-ASR-1.7B
engine: glm-asr-fastapi
engine_params: {}
benchmark:
    concurrency: []
    input_tokens: []
    max_tokens: []
    requests_per_combo: 0
reason: The only prior ASR attempt (SenseVoiceSmall on glm-asr-fastapi) failed due to a missing local artifact, not the engine. Qwen3-ASR-1.7B is confirmed present locally, is a ready combo, and its previous attempt was skipped due to batch timeout rather than engine error. This is the cleanest path to validate CPU ASR inference on GB10. Not blocked.
```

## Result

```yaml
status: failed
started_at: "2026-04-14T09:56:23Z"
duration_s: 106.681297952
error: 'pre-flight deploy: wait for deployed service Qwen3-ASR-1.7B: deployment Qwen3-ASR-1.7B stalled at ready (100%)'
```

## Benchmark Matrix

_No benchmark data_

## Agent Notes

_To be filled by agent after analysis._
