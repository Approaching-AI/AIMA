# AMD395 XDNA + Vulkan MoE Progress (2026-02-26, update)

## New updates in this round

1. Added `GGML_XDNA_SELFTEST` (default `1`) in `ggml-xdna`.
2. On backend init, a synchronous `xrt-runner` selftest is executed.
3. If selftest fails, backend now auto-disables NPU path and force-disables MoE offload (`GGML_XDNA_OFFLOAD_MOE=0`) to avoid hidden performance regressions.
4. Init log now prints both requested and effective MoE offload status.

## Why this was necessary

- The current host still fails NPU runner execution:
  - `mmap_range(len=0) failed (err=-22): Invalid argument`
- Direct XRT C++ xclbin path still fails:
  - `load_axlf: Operation not supported`
- Without auto-disable, enabling XDNA offload can silently route MoE through fallback and produce unstable or misleading perf behavior.

## Additional runtime discovery

- Queried `pypi.amd.com` package indexes from this Linux host.
- Key RyzenAI NPU runtime packages discovered (`onnxruntime-vitisai`, `voe`, `onnxruntime-genai-directml-ryzenai`) are currently published as `win_amd64` wheels.
- This Ubuntu host cannot directly install the official Windows-only RyzenAI OGA runtime path.

## Current status

- `llama.cpp` Vulkan path is stable.
- `ggml-xdna` now has stronger safety controls and clearer observability.
- True NPU MoE prefill acceleration remains blocked by missing usable Linux NPU runtime/kernel execution path.

## Highest-probability next path

1. Obtain a Linux-usable NPU runtime stack/artifacts for Strix (driver + EP/runtime + compatible model artifacts).
2. Validate a standalone NPU execution that runs non-trivial matmul/subgraph successfully (not only validation/no-op runner).
3. Re-enable `GGML_XDNA_OFFLOAD_MOE` and implement in-process NPU kernel dispatch for `GGML_OP_MUL_MAT_ID` with correctness/perf A/B.