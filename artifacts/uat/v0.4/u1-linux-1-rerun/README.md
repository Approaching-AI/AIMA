# U1 on `linux-1`

- Date: 2026-04-21
- Device: `linux-1` (`cjwx@linux-1`, host `qujing24`)
- Binary: current local workspace build, `aima v0.4-dev` from `af9ba09`
- Runtime: Docker
- Explorer tier: 1
- Remote serve / MCP: `127.0.0.1:6296` / `127.0.0.1:9196`
- Isolated data dir: `~/aima-uat-rerun/u1-final/data`

## Verdict

`PASS`

This final rerun closed the `linux-1` portion of U1.

What changed versus the earlier failed reruns:

- the host's existing GPU workloads were explicitly stopped first;
- both RTX 4090s returned to essentially full free VRAM;
- under the same narrow 2-cell tune structure, the session now completed with one real benchmark result instead of dying on shared-host memory pressure.

## What Was Verified

1. The blocker on the earlier `linux-1` attempts was indeed live host occupancy.
   - `17-stop-current-models.txt` shows the starting state:
     - GPU0 free `6124 MiB`
     - GPU1 free `6943 MiB`
     - active workloads included `qwen3-4b-vllm` and `sglang-kt` serving `Qwen3.5-122B-A10B`
   - after explicit cleanup, both GPUs returned to `48508 MiB` free

2. A fresh isolated rerun was started with the current workspace binary.
   - model: `qwen3-4b`
   - engine: `vllm`
   - hardware: `nvidia-rtx4090-x86`
   - search space: `gpu_memory_utilization=[0.20,0.25]`
   - benchmark profile: `concurrency=1`, `input_tokens=128`, `max_tokens=64`, `num_requests=1`

3. The tune path advanced through both cells and completed cleanly.
   - session id: `285ee8967d35cf2d`
   - `22-tuning-polls-final.txt` shows:
     - `0/2`
     - then `1/2`
     - finally `completed 2/2`

4. One cell failed for a concrete deploy/runtime reason, not a readiness hang.
   - `25-serve-tail-final.txt` records the first candidate (`gpu_memory_utilization=0.2`) failed with:
     - `ValueError: ... max seq len (8192) ... available KV cache memory ... estimated maximum model length is 7728`

5. The second cell completed a real benchmark.
   - `23-tuning-results-final.json` shows the successful candidate:
     - `gpu_memory_utilization=0.25`
     - `throughput_tps=102.55098162616815`
     - `ttft_p95_ms=21.322`
     - `benchmark_id=26dbf541072e8629`
     - `config_id=843da040f2cc42ee`

6. The successful result was persisted and promoted.
   - `24-db-counts-final.txt` shows:
     - `benchmarks = 1`
     - `configs = 1`
     - `status = completed`
     - `best_score = 102.55098162616815`
   - `25-serve-tail-final.txt` records:
     - `auto-promote: first golden config`
     - `tuning: best config already deployed`
   - `26-perf-observation-final.json` contains the updated planner-consumable perf observation

## Key Evidence

- `17-stop-current-models.txt`: pre-cleanup GPU occupancy and post-cleanup free VRAM
- `18-binary-version-final.txt`: patched current binary version
- `19-model-import-final.txt`: isolated model import
- `20-serve-start-final.txt`: isolated `serve --mcp` startup
- `21-tuning-start-final.json`: raw MCP tuning start response
- `22-tuning-polls-final.txt`: compact session progress
- `23-tuning-results-final.json`: final completed tuning session
- `24-db-counts-final.txt`: persisted benchmark/config counts and tuning session row
- `25-serve-tail-final.txt`: first-cell KV-cache failure plus second-cell success and promotion
- `26-perf-observation-final.json`: final performance observation
- `27-remote-cleanup-final.txt`: post-run cleanup and restored free VRAM

## Historical Note

Earlier same-day reruns remain useful as baseline context:

- the first isolated attempts failed because the isolated DB had no imported model facts;
- after model import, the subsequent reruns still failed because shared host workloads left too little free VRAM.

Those earlier failures are still on disk, but the current verdict for `linux-1` is based on the cleaned-host rerun above.
