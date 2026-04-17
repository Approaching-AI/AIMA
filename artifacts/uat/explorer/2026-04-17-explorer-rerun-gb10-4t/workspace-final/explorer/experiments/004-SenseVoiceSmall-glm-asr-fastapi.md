# Experiment 004: SenseVoiceSmall / glm-asr-fastapi

## Task

```yaml
kind: validate
model: SenseVoiceSmall
engine: glm-asr-fastapi
engine_params: {}
benchmark:
    concurrency: []
    input_tokens: []
    max_tokens: []
    requests_per_combo: 0
reason: Untried ASR modality on the CPU-only ASR engine; 0.87 GiB model validates the non-GPU inference path and expands coverage beyond LLMs. Not blocked and not in denylist.
```

## Result

```yaml
status: failed
started_at: "2026-04-14T07:24:02Z"
duration_s: 2.040391991
error: 'pre-flight deploy: deploy SenseVoiceSmall on glm-asr-fastapi: deploy apply SenseVoiceSmall: model SenseVoiceSmall not found locally and auto-pull is disabled'
```

## Benchmark Matrix

_No benchmark data_

## Agent Notes

The deploy failed immediately because the SenseVoiceSmall model artifact is missing from the local cache and the runtime is configured with auto-pull disabled. This is a pure environment/configuration blocker, not an engine or model incompatibility. There is no point retrying this combo in the current cycle unless the model is staged manually or the no-pull policy is relaxed. If the artifact becomes available, the combo is low-risk because the engine is CPU-only and the model is tiny.
