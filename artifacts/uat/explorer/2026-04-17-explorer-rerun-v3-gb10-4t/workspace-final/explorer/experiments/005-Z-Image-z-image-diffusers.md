# Experiment 005: Z-Image / z-image-diffusers

## Task

```yaml
kind: validate
model: Z-Image
engine: z-image-diffusers
engine_params: {}
benchmark:
    concurrency: []
    input_tokens: []
    max_tokens: []
    requests_per_combo: 0
reason: This is the sole image-generation ready combo that matches engine to model type (Z-Image is an image_gen model). It tests a completely different modality from all prior experiments. The model fits VRAM (19.11 GiB) and the combo is ready. Although a prior plan entry listed it as failed, no experiment file exists in this workspace, so the failure mode is unrecorded. Not in any current blocker or denylist.
```

## Result

```yaml
status: failed
started_at: "2026-04-14T09:58:10Z"
duration_s: 113.700463808
error: 'benchmark matrix: no successful cells (total=1)'
```

## Benchmark Matrix

_No benchmark data_

## Agent Notes

The z-image-diffusers engine deployed successfully (113s) for Z-Image, but the single benchmark cell failed, yielding `no successful cells`. This pattern—healthy deployment but failed request—matches the vllm-nightly+Qwen3.5-9B behavior and points to a request-path issue, possibly an incorrect image-generation payload schema or missing parameters (e.g., image size, guidance scale). This is a reproducible functional blocker for this specific combo on GB10.
