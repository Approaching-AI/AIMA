
## Update (2026-02-26, real 30B MoE benchmark on amd395)

Model used:
- `/home/quings/data/models/Qwen3-30B-A3B-Q4_K_M.gguf` (18G)

Benchmark command:
- `llama-bench -m <model> -r 2 -n 1 -p 2048 -b 2048 -ub 512 -ngl 999 -fa 1 -t 16 -dev Vulkan0/XDNA`
- Script: `scripts/bench-30b-moe-xdna.sh`

Results:
- `offload_off` (`GGML_XDNA_OFFLOAD_MOE=0`):
  - prefill: **948.82 t/s**
  - decode: **45.12 t/s**
  - wall: **9.35s**
- `offload_on_e8` (`OFFLOAD_MOE=1, RUNNER_ASYNC=1, TRIGGER_EVERY=8`):
  - prefill: **223.81 t/s**
  - decode: **23.78 t/s**
  - wall: **36.67s**
- `offload_on_e1` (`OFFLOAD_MOE=1, RUNNER_ASYNC=1, TRIGGER_EVERY=1`):
  - prefill: **219.01 t/s**
  - decode: **16.82 t/s**
  - wall: **35.23s**

Conclusion from 30B benchmark:
- Current XDNA sidecar-trigger architecture causes major regression on this model.
- Performance hypothesis (NPU improves prefill) is **not met** in current implementation.

Additional finding:
- Forcing XDNA fallback backend to Vulkan (`GGML_XDNA_ALLOW_UNSAFE_VK_FALLBACK=1` + `GGML_XDNA_FALLBACK_BACKEND=Vulkan0`) currently crashes (`signal 11`) in this path.

Status:
- Correctness/stability guardrails are in place.
- NPU binary I/O runner is available and verified for standalone GEMM.
- To achieve real gain, we need true in-process MoE matmul replacement (not sidecar trigger) with a backend path that avoids CPU fallback and avoids duplicate work.
