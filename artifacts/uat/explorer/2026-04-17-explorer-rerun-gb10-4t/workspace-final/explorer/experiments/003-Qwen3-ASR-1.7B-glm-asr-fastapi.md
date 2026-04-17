# Experiment 003: Qwen3-ASR-1.7B / glm-asr-fastapi

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
reason: SenseVoiceSmall is missing locally and blocked for glm-asr-fastapi. Qwen3-ASR-1.7B is present locally and is a ready combo, allowing us to validate the ASR engine itself without the missing-artifact failure.
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

This validate task was skipped entirely due to a pre-execution timeout (`skipped: timeout before execution`), meaning the exploration runner ran out of budget before reaching this combo. No container was started and no inference was attempted. Consequently, there is no evidence of engine or model failure. It should remain a candidate for the next cycle with adequate time allocation.
