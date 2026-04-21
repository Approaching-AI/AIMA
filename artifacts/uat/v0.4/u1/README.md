# U1 — Tune Warmup Readiness (gb10-4T)

Historical note: this README captures the original `gb10-4T` reproduction only. For later follow-up runs and the current cross-device conclusion, also see:

- `../u1-gb10-4t-rerun/README.md`
- `../u1-linux-1-rerun/README.md`
- `../u1-w7900d-rerun/README.md`

- Date: 2026-04-20
- Device: `gb10-4T` (`qujing@100.91.39.109`, host `aitopatom-66c4`)
- Binary: `v0.4-dev` (`44bc4c7`)
- Runtime: Docker
- Explorer tier: 2
- MCP run id: `2103c0f57a5baea4`
- Tuning session id: `40b7449fda622f3b`

## Test Setup

- Model: `qwen3.5-9b`
- Engine: `vllm-nightly`
- Hardware: `nvidia-gb10-arm64`
- Search space: `gpu_memory_utilization=[0.25, 0.30]`
- Benchmark profile: `concurrency=1`, `input_tokens=128`, `max_tokens=64`, `rounds=1`, `requests_per_combo=2`
- Remote serve: `127.0.0.1:6288`
- Remote MCP: `127.0.0.1:9190`
- Isolated data dir: `~/aima-uat/u1/data`

## Evidence

- [start.json](/Users/jguan/projects/AIMA/artifacts/uat/v0.4/u1/start.json)
- [serve.log](/Users/jguan/projects/AIMA/artifacts/uat/v0.4/u1/serve.log)
- [serve.tail.log](/Users/jguan/projects/AIMA/artifacts/uat/v0.4/u1/serve.tail.log)
- [aima.db.snapshot](/Users/jguan/projects/AIMA/artifacts/uat/v0.4/u1/aima.db.snapshot)
- [status/001.json](/Users/jguan/projects/AIMA/artifacts/uat/v0.4/u1/status/001.json)
- [status/031.json](/Users/jguan/projects/AIMA/artifacts/uat/v0.4/u1/status/031.json)

## Findings

- Tune started successfully and remained alive for about 11m25s (`05:50:28Z` → `06:01:53Z`).
- The old fast-fail symptom did not recur during this window: `serve.log` contains no `deploy result missing ready endpoint`.
- The first cell never reached benchmark. `status/001.json` through `status/031.json` all show `progress=0/2`, `results=0`, `best_score=0`.
- `serve.log` shows the blocker shifted earlier in the startup chain:
  - `05:53:36` selected `container compatibility auto-repair`
  - the deploy command injected three `repair_init_commands`
  - the run was still pre-listen when cancelled, so no benchmark artifact was written
- Manual live inspection during the run showed the container remained in the repair-init stage (`pip install transformers==5.4.0`) for the whole observation window, with health checks still returning connection-refused on `:8000`.

## Verdict

`KNOWN ISSUE` / release blocker for U1 on `gb10-4T`.

What improved:
- The tuner no longer failed fast with `deploy result missing ready endpoint` while the container was still cold-starting.

What still blocks PASS:
- No cell reached benchmark, so U1 acceptance (`throughput > 0`) was not met.
- `experiment-facts.md` was not produced because this run never progressed past the first deploy startup.

## Recommended Next Step

- Keep this README as the historical record for the original `qwen3.5-9b` / repair-init observation on `gb10-4T`.
- Use the combined follow-up evidence from `../u1-gb10-4t-rerun/README.md`, `../u1-linux-1-rerun/README.md`, and `../u1-w7900d-rerun/README.md` for the current cross-device U1 conclusion.
