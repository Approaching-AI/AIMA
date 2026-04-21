# U1 on `w7900d`

- Date: 2026-04-21
- Device: `w7900d` (`root@36.151.243.68:21985`, host `wx-ms-w7900d-0003`)
- Binary: patched local workspace build, `aima v0.4-dev`
- Runtime: Docker
- Explorer tier: 1
- Remote serve / MCP: `127.0.0.1:6297` / `127.0.0.1:9197`
- Isolated data dir: `/root/aima-uat-rerun/u1-w7900d/data`

## Verdict

`PASS`

This rerun proved that the current U1 tune path can complete benchmark cells on a third device once the RDNA3 ROCm llama.cpp container path is corrected.

What this round established:

- the first attempt failed immediately for a concrete asset issue, not a readiness hang:
  - `ggml-org/llama.cpp:full-rocm` contains `llama-server` at `/app/llama-server`, not on `PATH`
  - the pre-fix tuning session therefore failed both cells with `exec: "llama-server": executable file not found in $PATH`
- after overriding `llamacpp-rocm-rdna3` to use `/app/llama-server`, the same isolated rerun completed `2/2`
- both cells reached real benchmark and persisted benchmark/config rows
- the best config was promoted and the perf observation file was updated

## What Was Verified

1. The original failure mode was a concrete container entrypoint mismatch.
   - `06-serve-log-tail-prefix.txt` from the pre-fix attempt showed:
     - `deploy failed ... exec: "llama-server": executable file not found in $PATH`
   - direct inspection confirmed the ROCm image actually ships the binary at `/app/llama-server`

2. The patched rerun started a clean 2-cell tune.
   - tuning session: `531cc06928183275`
   - model / engine: `Qwen3-30B-A3B-Q4_K_M` + `llamacpp-rocm-rdna3`
   - search space: `ctx_size=[2048,4096]`
   - benchmark profile: `concurrency=1`, `input_tokens=128`, `max_tokens=64`, `num_requests=1`, `warmup_count=1`

3. Both cells completed real benchmark.
   - `13-tuning-results-patched.json` shows:
     - cell 1: `ctx_size=2048`, `throughput_tps=62.82665695670213`
     - cell 2: `ctx_size=4096`, `throughput_tps=62.927365302008084`
   - final session state:
     - `status=completed`
     - `progress=2`
     - `total=2`
     - `results_len=2`

4. The isolated store persisted the successful results.
   - `16-db-counts-patched.txt` shows:
     - `benchmarks = 2`
     - `configs = 2`
   - `18-tuning-session-db.txt` shows:
     - `best_config = {"ctx_size":4096,"n_gpu_layers":999,"port":8080}`
     - `best_score = 62.927365302008084`

5. Post-benchmark promotion happened as expected.
   - `14-serve-log-tail-patched.txt` records:
     - `auto-promote: first golden config`
     - `tuning: best config already deployed`
   - `17-perf-observation-patched.json` contains the updated planner-consumable perf observation

## Key Evidence

- `09-binary-version.txt`: rebuilt patched binary version
- `06-serve-log-tail-prefix.txt`: pre-fix failure showing `llama-server` not found on `PATH`
- `10-serve-start-patched.txt`: isolated patched `serve --mcp` startup
- `11-tuning-start-patched.json`: raw MCP tuning start response
- `12-tuning-polls-patched.txt`: compact poll log showing `0/2 -> 1/2 -> completed 2/2`
- `13-tuning-results-patched.json`: final completed tuning session with two benchmark results
- `14-serve-log-tail-patched.txt`: deploy commands, first-cell success, auto-promote, second-cell success
- `16-db-counts-patched.txt`: persisted benchmark/config counts
- `17-perf-observation-patched.json`: updated performance observation
- `18-tuning-session-db.txt`: final `tuning_sessions` row with best config and embedded results

## Interpretation

For `w7900d`, U1 is no longer blocked by readiness or warmup behavior. The only device-local issue uncovered here was the ROCm llama.cpp container entrypoint path, and the patched rerun immediately converted that into a clean `2/2` tuning completion.
