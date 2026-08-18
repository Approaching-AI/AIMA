# U10 First-Run Productization UAT

Date: 2026-04-24
Branch: develop
Tested code commit: 13ae9cc59f84932cc30b3e9866add1f3f18d5356
Host: dev-mac, darwin/arm64, Apple M4, 16 GiB RAM
Scratch data dir during run: `artifacts/uat/v0.4/u10-first-run-productization-20260424/data`
Note: generated binary and SQLite scratch data were intentionally excluded from
this artifact commit; logs and JSON evidence are retained.

## Scope

This run verifies the new first-run/onboarding/diagnostics changes from
`feat(first-run): harden onboarding diagnostics`:

- `aima onboarding` runs the guided first-run flow instead of help text.
- `aima onboarding start` is an explicit alias for the same flow.
- Native-only hosts with Docker/K3S skipped do not report `needs_init=true`.
- First-run recommendation guardrails prefer a credible small/medium model on a
  16 GiB native host.
- `aima deploy qwen3-4b --dry-run` remains executable as the non-mutating
  golden-path deploy check.
- `aima diagnostics export` writes a telemetry-free local diagnostics bundle
  with privacy markers and 0600 file permissions.
- The same onboarding and diagnostics paths work through the MCP HTTP server
  via `--remote`.

## Result

PASS for local dev-mac first-run productization coverage.

This is not a full cross-device U10 closure. Windows/native deployment and the
remaining remote-device matrix still need separate live reruns before the
release UAT item can move from KNOWN ISSUE to PASS.

## Evidence

| Check | Evidence | Result |
| --- | --- | --- |
| Candidate binary builds and runs | `logs/00-version.txt` | PASS |
| HAL detect on clean data dir | `logs/01-hal-detect.txt` | PASS |
| `aima onboarding` guided output | `logs/02-onboarding.txt` | PASS |
| `aima onboarding start` guided output | `logs/03-onboarding-start.txt` | PASS |
| No-subcommand output equals `start` output | `logs/04-onboarding-diff.txt` (0 bytes) | PASS |
| JSON first-run summary | `logs/08-onboarding-summary.json` | PASS |
| Recommendation top 5 | `logs/09-recommend-top5.json` | PASS |
| Oversized model score check | `logs/11-oversized-model-scores.json` | PASS |
| `deploy qwen3-4b --dry-run` | `logs/10-dry-run-summary.json` | PASS |
| Diagnostics stdout no logs | `diagnostics/15-diagnostics-no-logs-summary.json` | PASS |
| Diagnostics stdout with logs | `diagnostics/16-diagnostics-with-logs-summary.json` | PASS |
| Diagnostics default local file | `diagnostics/14-diagnostics-file-summary.json` | PASS |
| Diagnostics secret/path negative scan | `diagnostics/17-secret-negative-scan.txt` (0 bytes) | PASS |
| MCP tool presence | `logs/24-mcp-tool-presence.txt` | PASS |
| MCP remote diagnostics | `diagnostics/22-mcp-diagnostics-summary.json` | PASS |
| MCP remote onboarding | `logs/23-mcp-onboarding-summary.json` | PASS |
| Focused Go tests | `logs/25-focused-go-test.txt` | PASS |
| First-run smoke gate | `logs/26-first-run-smoke.txt` | PASS |
| Full Go test suite | `logs/27-go-test-all.txt` | PASS |

## Key Observations

- On dev-mac, stack status is `docker=skipped`, `k3s=skipped`,
  `init_tier_recommendation=native`, and `needs_init=false`.
- First recommendation is `qwen3-4b` with score 62 and next command
  `aima run qwen3-4b`.
- Oversized native first-run candidates are penalized:
  `qwen3-32b` and `qwen3-30b-a3b` score 8 on this 16 GiB native host.
- Diagnostics privacy block reports:
  `telemetry_free=true`, `sent_to_network=false`, `secrets_redacted=true`,
  `collection_scope=local_only_no_health_probes`.
- The default diagnostics file is written under the local data dir with
  `-rw-------` permissions.
- MCP `tools/list` includes both `onboarding` and `system.diagnostics`.

## Remaining Live UAT

- Rebuild and rerun U10 on Windows/native, especially the real deploy path after
  the first-run recommendation changes.
- Rerun the remote-device portion for `amd395`, `w7900d`, `aibook`, and
  `metax-n260` when those hosts are reachable and safe to disturb.
