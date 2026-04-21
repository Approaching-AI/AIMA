# U9 on `gb10-4T`

- Initial run: 2026-04-20
- Current rerun: 2026-04-21
- Host: `qujing@100.91.39.109` (`aitopatom-66c4`, GB10 / Blackwell ARM64)
- Latest binary: `aima v0.4-dev` from repo `HEAD=af9ba09`
- Latest isolation: `AIMA_DATA_DIR=~/aima-uat-rerun/u9/data`

## Verdict

`PASS`

The 2026-04-20 false-ready result is superseded by the rerun on `af9ba09`.

## What Was Verified

1. Type-alias path now resolves to the concrete engine asset.
   - Command: `aima deploy qwen3.5-9b --engine vllm-nightly --config port=18083`
   - The deploy command labeled the container with `aima.dev/engine=vllm-nightly-blackwell`.
   - First visible status (`2026-04-20T17:03:32Z`) already reported `engine=vllm-nightly-blackwell`, `ready=false`.

2. Warmup/health gating stays active through the normal startup phases.
   - Status sequence progressed `initializing` -> `model_init` -> `loading_shards`.
   - There was no premature `ready=true` while the service was still in repair-init or shard loading.

3. The alias path now reaches a real ready state.
   - Final `deploy status qwen3-5-9b-vllm-nightly` returned `ready=true`.
   - `/health` returned `200`.
   - `/v1/models` returned `200` with `id=qwen3.5-9b`.
   - The final container labels still carried `aima.dev/engine=vllm-nightly-blackwell`, so runtime health/warmup lookup was using the concrete asset metadata.

4. The previously collected exact-asset and forced-timeout evidence remains valid.
   - Exact asset `vllm-nightly-blackwell` already proved the normal ready path.
   - Overlay asset `vllm-nightly-blackwell-u9-fail` already proved the bad-health timeout path.

## Evidence

- `rerun-af9ba09/01-alias-deploy.stdout` / `01-alias-deploy.stderr`: current-head alias deploy kickoff
- `rerun-af9ba09/02-status-initial.txt`: alias path visible as `ready=false` with concrete asset label
- `rerun-af9ba09/03-status-model-init.txt`: `model_init`
- `rerun-af9ba09/04-status-loading-shards.txt`: `loading_shards`
- `rerun-af9ba09/05-status-ready.txt`: final `ready=true`
- `rerun-af9ba09/06-health.txt`: `/health` `200 OK`
- `rerun-af9ba09/07-models-headers.txt` / `08-models-body.json`: `/v1/models` `200 OK`
- `rerun-af9ba09/09-labels.json`: final container labels (`aima.dev/engine=vllm-nightly-blackwell`)
- `10-normal-exact-docker-logs-2.txt` / `11-normal-exact-docker-logs-3.txt`: prior exact-asset success path
- `14-fail-run.stdout` / `14-fail-run.stderr`: prior bad-health timeout path
- `16-fail-correct-health.txt` / `17-fail-bad-health.txt`: prior timeout-path health contrast
