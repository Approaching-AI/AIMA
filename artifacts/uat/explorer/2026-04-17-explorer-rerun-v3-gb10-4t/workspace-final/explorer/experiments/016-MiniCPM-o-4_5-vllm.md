# Experiment 016: MiniCPM-o-4_5 / vllm

## Task

```yaml
kind: validate
model: MiniCPM-o-4_5
engine: vllm
engine_params:
    gpu_memory_utilization: 0.85
search_space: {}
benchmark:
    concurrency: []
    input_tokens: []
    max_tokens: []
    requests_per_combo: 0
reason: Pending Work validate_baseline obligation for an unexplored Ready combo. MiniCPM-o-4_5 has never been benchmarked successfully on any engine. Standard vllm is chosen because its confirmed GB10 startup blocker is specific to Qwen models, whereas sglang has a general GB10 deploy-timeout blocker. This combo is Ready and not in Do Not Retry This Cycle.
```

## Result

```yaml
status: failed
started_at: "2026-04-17T05:43:26Z"
duration_s: 11.556627802
error: 'pre-flight deploy: deploy MiniCPM-o-4_5 on vllm: deploy apply MiniCPM-o-4_5: container compatibility check failed for MiniCPM-o-4_5 with qujing/vllm-gemma4-gb10:0.19.0-torchmoe2: ValueError: Unrecognized model in /models. Should have a `model_type` key in its config.json.'
```

## Benchmark Matrix

_No benchmark data_

## Agent Notes

_To be filled by agent after analysis._
