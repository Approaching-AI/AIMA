# U2 on `gb10-4T`

- Initial run: 2026-04-20
- Current rerun: 2026-04-21
- Host: `qujing@100.91.39.109` (`aitopatom-66c4`, GB10 / Blackwell ARM64)
- Latest binary: `aima v0.4-dev` from repo `HEAD=af9ba09`
- Latest isolation: `AIMA_DATA_DIR=~/aima-uat-rerun/u2-qwen/data`, proxy/MCP ports `6291/9191`

## Verdict

`PASS`

The 2026-04-20 Gemma result is superseded by the patched rerun on `af9ba09`.

This rerun validated the actual "partial preserve" branch instead of getting stuck behind the older summary/stall behavior:

- the first candidate completed and wrote a real benchmark/configuration row;
- the second candidate failed naturally at deploy time with an invalid `dtype`;
- the tuning session still finished `completed` with `total_cells=2` and `success_cells=1`;
- only the successful cell was persisted to `benchmark_results` / `configurations`;
- the surviving result was promoted into planner-consumable state (`golden` config + perf observation file).

## What Was Verified

1. A real 2-cell tune run was started through isolated MCP `explore.start`.
   - run id: `3c0b3ba2679bb423`
   - tuning session: `7fc5a13a4fd72e9c`
   - model / engine: `qwen3.5-9b` + `vllm-nightly`
   - search space: `dtype=[bfloat16, definitely-not-real]`
   - benchmark profile: single-point `concurrency=1`, `input_tokens=128`, `max_tokens=32`, `num_requests=1`

2. The first cell completed successfully.
   - `serve.log` recorded `tuning: testing config progress=1/2 config=map[dtype:bfloat16]`
   - the proxy served a real request at `03:42:32Z`
   - the isolated DB kept one real benchmark/configuration row:
     - `benchmark_id=8928d721e8010732`
     - `config_id=a76d78bb3e5381a7`
     - `throughput_tps=12.307901395267972`

3. The second cell failed naturally and was skipped.
   - `serve.log` recorded `tuning: testing config progress=2/2 config=map[dtype:definitely-not-real]`
   - deploy then failed with:
     - `vllm serve: error: argument --dtype: invalid choice: 'definitely-not-real'`
   - the run did not hang after that deploy failure

4. The partial summary is now correct.
   - final run status: `completed`
   - final `summary_json` reports:
     - `total_cells=2`
     - `success_cells=1`
   - embedded `tuning_session` reports:
     - `status=completed`
     - `progress=2`
     - `total=2`
     - `results_len=1`

5. Only the successful cell was promoted.
   - DB counts at the end:
     - `benchmark_results=1`
     - `configurations=1`
   - the saved configuration status is:
     - `golden`

6. The preserved partial result is available to later planning.
   - `serve.log` recorded:
     - `auto-promote: first golden config`
     - `perf observation updated`
   - the isolated data dir now contains:
     - `observations/models/qwen3.5-9b-perf.json`

## Key Evidence

- `rerun-u2fix-qwen/00-version.txt`: rebuilt `af9ba09` binary version
- `rerun-u2fix-qwen/01-serve-start.txt`: isolated `serve --mcp` startup
- `rerun-u2fix-qwen/02-explorer-status-raw.json`: baseline explorer status
- `rerun-u2fix-qwen/03-explore-start.json`: raw MCP start response
- `rerun-u2fix-qwen/04-status-polls.jsonl`: run/session progress over time
- `rerun-u2fix-qwen/04a-status-compact.txt`: compact progress summary showing `0/2 -> 1/2 -> completed`
- `rerun-u2fix-qwen/05-run-result.json`: final run payload with `total_cells=2`, `success_cells=1`
- `rerun-u2fix-qwen/06-db-counts.txt`: only one benchmark/config row persisted
- `rerun-u2fix-qwen/07-serve-tail.txt`: first-cell success path and second-cell invalid-`dtype` deploy failure
- `rerun-u2fix-qwen/08-docker-after-run.txt`: no leftover `qwen3-5-9b` container after completion
- `rerun-u2fix-qwen/09-remote-cleanup.txt`: isolated remote cleanup
- `rerun-u2fix-qwen/10-perf-observation.json`: persisted performance observation for later planner input
- `rerun-u2fix-qwen/11-config-status.txt`: saved config status is `golden`

## Historical Note

The original 2026-04-20 Gemma run and the intermediate 2026-04-21 Gemma warmup-only rerun remain on disk as earlier attempts:

- `status/` + legacy files under this directory: original stuck summary/stall evidence
- `rerun-u2fix-local/`: a patched Gemma retry that still spent most of its time in first-cell startup

They are useful as baseline context, but the current verdict is based on `rerun-u2fix-qwen/`.
