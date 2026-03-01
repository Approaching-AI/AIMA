
## Update (2026-02-26, performance matrix + binary I/O runner)

### Performance matrix (current triggered-sidecar design)

Benchmark command family:
- `test-backend-ops test -b XDNA -o MUL_MAT_ID`
- env baseline: `GGML_XDNA_OFFLOAD_MOE=1 GGML_XDNA_SELFTEST=0 GGML_XDNA_PREFILL_N_MIN=1`

Results (wall time):
- `npu_off` (`GGML_XDNA_RUNNER_ENABLE=0`): **16.73s**
- `npu_async_e1` (`RUNNER_ENABLE=1 RUNNER_ASYNC=1 TRIGGER_EVERY=1`): **17.81s**
- `npu_async_e8` (`RUNNER_ENABLE=1 RUNNER_ASYNC=1 TRIGGER_EVERY=8`): **16.92s**
- `npu_sync_e1` (`RUNNER_ENABLE=1 RUNNER_ASYNC=0 TRIGGER_EVERY=1`): **25.48s**

All variants preserved correctness: `618/618 tests passed`.

Interpretation:
- Current design does **not** improve throughput; sync trigger is clearly worse.
- This confirms the hypothesis is **not satisfied yet** under sidecar-trigger architecture.

### New capability added to MLIR-AIE host runner

Files modified:
- `~/mlir-aie/programming_examples/basic/matrix_multiplication/common.h`
- `~/mlir-aie/programming_examples/basic/matrix_multiplication/test.cpp`

Added CLI options:
- `--a_bin`: load raw A matrix bytes
- `--b_bin`: load raw B matrix bytes
- `--c_bin`: write raw C matrix bytes

Validation:
- Generated external A/B (int16) and CPU reference C (int32) for `64x64x64`.
- Ran `single_core.exe` on NPU with `--verify=false --a_bin --b_bin --c_bin`.
- Compared output C with CPU reference: `neq=0`, `max_abs_diff=0`.

This is the required foundation for true in-process numerical offload from `ggml_xdna`.

### Next engineering step (to achieve real prefill acceleration)

1. In `ggml_xdna`, implement selective `MUL_MAT_ID` data-path offload:
   - marshal A/B from ggml tensors to binary buffers,
   - call NPU runner with `a_bin/b_bin/c_bin`,
   - write C back into destination tensor,
   - bypass fallback compute for that op.
2. Initially gate to a strict safe subset (e.g., f16/f32 + shape constraints), then expand.
3. After correctness lock, run real MoE prefill benchmark (model-level) and compare tokens/s + prefill latency.
