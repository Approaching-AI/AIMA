# U1 on `gb10-4T` — current HEAD rerun

- Date: 2026-04-21
- Device: `gb10-4T` (`qujing@100.91.39.109`, host `aitopatom-66c4`)
- Binary: current local workspace build, `aima v0.4-dev` (`7dc8718`)
- Runtime: Docker
- Explorer tier: 1
- Remote serve / MCP: `127.0.0.1:6298` / `127.0.0.1:9198`
- Isolated data dir: `~/aima-uat-rerun/u1-gb10-final/data`

## Verdict

`PASS`

This rerun closes the last missing U1 host. On current `HEAD`, `gb10-4T` can now complete real benchmark cells without reproducing the old `deploy result missing ready endpoint` fast-fail loop.

## What Was Verified

1. A fresh isolated tuning session started successfully on current `HEAD`.
   - tuning session: `29741d52116fbc06`
   - model / engine: `GLM-4.1V-9B-Thinking` + `vllm-nightly`
   - search space: `gpu_memory_utilization=[0.85] × max_model_len=[384,512]`
   - benchmark profile: `concurrency=1`, `input_tokens=128`, `max_tokens=64`, `rounds=1`, `num_requests=1`, `warmup_count=1`

2. Both cells completed real benchmark and the session reached a clean terminal state.
   - `06-tuning-results.json` shows `status=completed`, `progress=2`, `total=2`
   - cell 1: `max_model_len=384`, `throughput_tps=10.135701012649557`
   - cell 2: `max_model_len=512`, `throughput_tps=10.132992106259662`
   - `11-tuning-session-db.txt` confirms `completed_at=2026-04-21T07:56:43Z`

3. Successful results were persisted into the isolated store.
   - `07-db-counts.txt` shows:
     - `benchmarks = 2`
     - `configs = 2`
     - `sessions = 1`
   - best config was kept at `max_model_len=384`, `gpu_memory_utilization=0.85`

4. The best config was redeployed successfully at the end.
   - `09-serve-tail-final.txt` records:
     - first benchmark writeback and first `auto-promote`
     - second benchmark writeback
     - final `tuning: deployed best config`
   - final vLLM logs show the best deployment became `healthy` again and served `/health`, `/v1/models`, and warmup chat completions

5. The old U1 failure shape did not recur in this rerun.
   - no `deploy result missing ready endpoint` loop
   - no stuck-active tuning session
   - the earlier 2026-04-20 `qwen3.5-9b` repair-init incident remains useful as historical asset-specific evidence, but it is no longer the current U1 verdict for `gb10-4T`

## Key Evidence

- `01-binary-version.txt`: rebuilt binary version on remote host
- `02-model-import.txt`: isolated model import for `GLM-4.1V-9B-Thinking`
- `04-tuning-start.json`: raw MCP tuning start response
- `05-status-polls.txt`: full status polling history from `0/2` through `completed 2/2`
- `06-tuning-results.json`: final completed tuning session with both benchmark results
- `07-db-counts.txt`: persisted benchmark/config/session counts
- `09-serve-tail-final.txt`: tuning progression, benchmark completion, best-config redeploy
- `11-tuning-session-db.txt`: final `tuning_sessions` row from SQLite
- `12-remote-cleanup.txt`: remote cleanup proof after evidence collection

## Interpretation

With this rerun, U1 now has successful benchmark evidence on all three intended host classes:

- `linux-1`
- `gb10-4T`
- `w7900d`

That satisfies the current U1 acceptance bar. The original warmup/readiness blocker is now closed.
