# U5 on `gb10-4T` via local mock Central

- Date: 2026-04-21
- Edge host: `qujing@100.91.39.109` (`aitopatom-66c4`, GB10 / Blackwell ARM64)
- Central host: `dev-mac` via local mock Central on `127.0.0.1:18086`, reverse-tunneled into `gb10-4T`
- Binary: `aima v0.4-dev` (`af9ba09`, rebuilt 2026-04-21T03:18:23Z)
- Isolation: separate `AIMA_DATA_DIR` under `~/aima-uat-rerun/u5-fix/data`, separate proxy/MCP ports `6288/9188`

## Verdict

`PASS`

The advisory rejection path now closes end-to-end for a deep execution failure.

This rerun supersedes the earlier stuck-active result captured under `rerun-af9ba09/`. With the patched build, the same "bad advisory reaches a real validation plan" scenario now converges correctly:

- the advisory is delivered through `central.sync pull`;
- Explorer creates a real `central.advisory` validation plan;
- the deploy enters a terminal `failed` phase instead of hanging `active`;
- the local plan is marked `rejected`;
- Central receives rejection feedback with the concrete runtime error;
- a later `central.sync pull` no longer republishes the same advisory as `pending`.

## What Was Verified

1. Isolated edge `serve --mcp` was started on `gb10-4T`.
   - proxy / MCP ports: `6288/9188`
   - `central.endpoint` pointed to `http://127.0.0.1:18087`
   - the mock Central on `dev-mac` was exposed to the remote host through SSH reverse forwarding

2. A deep validation advisory was injected and delivered.
   - advisory id: `adv-u5-reject-u5fix`
   - target: `GLM-4.1V-9B-Thinking + vllm-nightly`
   - bad config: `dtype=definitely-not-real`
   - first `central.sync pull` returned `advisories.count=1` and `published_events=1`

3. Edge created and completed a real advisory validation plan.
   - plan id: `advisory-adv-u5-reject-u5fix`
   - trigger: `central.advisory`
   - final plan status: `rejected`
   - final task status in `summary_json`: `rejected`

4. The execution failure now converges with a concrete diagnostic.
   - final run status: `failed`
   - run error:
     - `deployment GLM-4.1V-9B-Thinking entered terminal phase "failed": vllm serve: error: argument --dtype: invalid choice: 'definitely-not-real'`

5. Central received and stored rejection feedback.
   - final advisory status: `rejected`
   - `feedback` contains the same invalid-`dtype` runtime error
   - `validated_at=2026-04-21T03:23:08Z`

6. The advisory is not re-delivered after rejection.
   - a second `central.sync pull` returned:
     - `advisories.count=0`
     - `published_events=0`

## Key Evidence

- `rerun-u5fix-local/01-version.txt`: rebuilt `af9ba09` binary version
- `rerun-u5fix-local/02-serve-start.txt`: isolated edge `serve --mcp` startup
- `rerun-u5fix-local/03-config-seeded.txt`: seeded `device.id` plus central endpoint/api key
- `rerun-u5fix-local/04-explorer-baseline.json`: baseline explorer status before pull
- `rerun-u5fix-local/05-central-sync-pull.json`: first pull delivers the advisory
- `rerun-u5fix-local/06-plan-run-polls.jsonl`: edge-side convergence from `active/running` to `rejected/failed`
- `rerun-u5fix-local/07-central-polls.jsonl`: Central-side convergence from `delivered` to `rejected`
- `rerun-u5fix-local/08-final-edge-state.json`: final `exploration_plans` / `exploration_runs` rows
- `rerun-u5fix-local/10-central-state-final.json`: final advisory record with stored feedback
- `rerun-u5fix-local/11-central-sync-pull-after-reject.json`: second pull returns no pending advisories

## Historical Note

The pre-fix reproduction remains under `rerun-af9ba09/` as the baseline failure case that motivated this rerun. That evidence is still useful for comparison, but it no longer reflects the current behavior of the patched build.
