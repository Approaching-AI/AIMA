# U3 on `gb10-4T` via isolated local Central

- Initial run: 2026-04-20
- Current rerun: 2026-04-21
- Edge host: `qujing@100.91.39.109` (`aitopatom-66c4`, GB10 / Blackwell ARM64)
- Central host: `dev-mac` running repo `cmd/central` on reverse-tunneled local ports
- Support host: local mock `aima-service`, also reverse-tunneled to the remote edge
- Latest isolation: `~/aima-uat-rerun/u3-fast/data` on edge, `artifacts/uat/v0.4/u3-gb10-4t/rerun-u3fast-local/central.db` on local Central

## Verdict

`PASS`

The 2026-04-20 deep-validation stall is superseded by the 2026-04-21 fast rerun under the current patched workspace build.

This rerun closed the full onboarding loop:

- a fresh isolated device registered through mock support;
- the first `central.sync push` seeded Central with the new device;
- a deep advisory was delivered to the live Explorer through direct MCP pull;
- edge validation deployed, reached ready, ran a real benchmark cell, and persisted `1` benchmark plus `1` configuration;
- Central received terminal feedback and marked the advisory `validated`.

## What Was Verified

1. Fresh device registration still worked from a clean isolated identity.
   - mock support returned canonical credentials for `dev-u3-gb10-fast`
   - the isolated edge moved from unregistered to registered without touching the host's real `~/.aima`

2. First knowledge push seeded Central with the new device.
   - `rerun-u3fast-local/10-sync-push.json` shows the first isolated edge push succeeded
   - this fast rerun intentionally started from zero exported knowledge so the onboarding signal was only the new device itself

3. The live Explorer received a deep advisory through direct MCP pull.
   - advisory id: `adv_u3_glm_validate_fast2`
   - target: `GLM-4.1V-9B-Thinking + vllm-nightly`
   - advisory content pinned `max_model_len=384` so validation stayed at a single feasible benchmark cell
   - `rerun-u3fast-local/19-sync-pull-direct-mcp.json` confirms `published_events=1`

4. Edge executed a real deep validation to completion.
   - `rerun-u3fast-local/23-serve-tail-final-fast2.txt` shows:
     - deploy started at `05:07:14Z`
     - service ready at `05:11:32Z`
     - benchmark matrix `1/1` started at `05:11:33Z`
     - benchmark matrix `1/1` ended at `05:14:27Z`
   - final benchmark result:
     - `throughput_tps=10.28502283151757`
     - `ttft_p95_ms=102.0084`

5. Edge persisted terminal validation artifacts.
   - `rerun-u3fast-local/24-edge-counts-final-fast2.txt` shows:
     - `benchmarks = 1`
     - `configs = 1`
     - plan `advisory-adv_u3_glm_validate_fast2` finished `status=completed`

6. Central received terminal feedback and closed the advisory lifecycle.
   - `rerun-u3fast-local/25-fast2-final-status.txt` shows:
     - `status = validated`
     - `accepted = 1`
     - `feedback = validated: 10.3 tok/s, TTFT P95 102ms`
   - `rerun-u3fast-local/22-central-advisories-final-fast2.json` records `validated_at=2026-04-21T05:14:33Z`

## Key Evidence

- `rerun-u3fast-local/10-sync-push.json`: first push from the new isolated edge
- `rerun-u3fast-local/11-central-stats-after-push.json`: Central stats after onboarding push
- `rerun-u3fast-local/13-central-advisories-after-manual-insert.json`: inserted fast deep advisory
- `rerun-u3fast-local/19-sync-pull-direct-mcp.json`: direct MCP pull delivered the advisory into the live Explorer
- `rerun-u3fast-local/21-polls-fast2.txt`: central/edge state during active validation
- `rerun-u3fast-local/22-central-advisories-final-fast2.json`: final Central advisory window with `status=validated`
- `rerun-u3fast-local/23-serve-tail-final-fast2.txt`: deploy, ready, benchmark start/end, and knowledge overlay creation
- `rerun-u3fast-local/24-edge-counts-final-fast2.txt`: final edge counts and completed plan/run state
- `rerun-u3fast-local/25-fast2-final-status.txt`: compact final advisory status

## Historical Note

The earlier 2026-04-20 failure evidence remains useful as baseline context:

- top-level files in this directory plus `rerun-u3fix-local/` capture the original stalled deep-validation path

They are no longer the current verdict. The release conclusion for U3 now comes from `rerun-u3fast-local/`.
