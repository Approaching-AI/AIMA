
## Update (2026-02-26, MLIR-AIE validated path integrated)

### What was validated on amd395

1. `mlir-aie` toolchain is usable from `~/mlir-aie/ironenv`.
2. `single_core` GEMM example runs on NPU with PASS:
   - path: `~/mlir-aie/programming_examples/basic/matrix_multiplication/single_core`
   - required env when building/running make target: `PEANO_INSTALL_DIR=~/mlir-aie/ironenv/lib/python3.12/site-packages/llvm-aie`
3. `single_core.exe` executes NPU GEMM reliably with existing `64x64x64` xclbin/insts artifacts.

### XDNA backend changes made

File: `ggml/src/ggml-xdna/ggml-xdna.cpp`

- Switched default NPU trigger command away from failing `xrt-runner` recipe path to a working MLIR-AIE host binary command:
  - default `GGML_XDNA_NPU_CMD` now points to `single_core.exe` with `final_64x64x64_32x32x32.xclbin`.
- Added configurable command template placeholders support (`{M}`, `{K}`, `{N}`) for future dynamic-shape path.
- Added `MUL_MAT_ID` MoE prefill op shape/type logging:
  - `GGML_XDNA_MOE_LOG_LIMIT` controls max logs.
- Kept correctness path unchanged: graph compute still executes fully on fallback backend (CPU/Vulkan), so numerical correctness and stability are preserved.

### Verification results

1. Backend init selftest now passes:
   - `ggml_xdna: selftest passed`
   - `/tmp/ggml-xdna-selftest.log` shows NPU GEMM `PASS!`
2. `test-backend-ops` with XDNA backend:
   - `test-backend-ops test -b XDNA -o MUL_MAT_ID` passed (618/618)
   - logs confirm MoE-like `MUL_MAT_ID` patterns observed and NPU trigger command executed.

### Current limitation (important)

- This stage is **triggered sidecar NPU GEMM**, not full numerical replacement of GGML `MUL_MAT_ID` outputs.
- Therefore it demonstrates stable CPU/GPU+NPU orchestration and verified NPU execution, but does **not yet deliver true prefill latency reduction from replacing MoE compute**.

### Next required step for true speedup

1. Build a dedicated NPU host runner that accepts external A/B buffers and returns C (binary I/O).
2. Implement quantized MoE path support (at minimum q4_K/f16/f32 combinations observed in tests).
3. In `ggml_xdna` compute path, replace selected `MUL_MAT_ID` outputs with NPU results and bypass fallback for those ops.
4. Benchmark with a real MoE GGUF model prefill workload to measure end-to-end tokens/s and prefill latency.
