# Real Validation Report — 2026-03-10

## Scope

This round validates the PRD 1.0 shortest-path work that was just wired and then repaired after first-pass real testing:

1. `knowledge.open_questions` -> `ExplorationRun` automatic validation
2. ZeroClaw persistent sidecar protocol and planner wiring
3. ZeroClaw one-shot planner fallback
4. Cross-device smoke suite from `CLAUDE.md`: `version`, `hal detect`, `engine list`, `model list`, `deploy list`

Tunnel remained out of scope.

## Final Verdict

- PASS: `knowledge.open_questions(action=run)` launches a real persisted `ExplorationRun`, executes a real `benchmark.run`, and auto-resolves the question to `tested`.
- PASS: ZeroClaw persistent mode now works against the real installed ZeroClaw binary by starting `zeroclaw daemon` and using its localhost gateway.
- PASS: `planner=zeroclaw` now works in the real persistent path and completes an end-to-end open-question validation run.
- PASS: ZeroClaw one-shot planner fallback no longer fails at plan creation on the validated local setup.
- PASS: YAML-declared open-question `status` / `finding` now survive catalog sync into SQLite.
- PASS: Smoke suite completed on 6/9 machines.
- BLOCKED: 3/9 machines remain environment-blocked by SSH/auth/connectivity issues, not AIMA runtime failures.

## Build Under Test

- Commit: `4ed44de`
- Repair verification local build time: `2026-03-10T12:30:08Z`
- Earlier smoke-suite binaries on remote machines were also commit `4ed44de`
- SHA256 from the validated rebuilt artifacts:
  - `build/aima-darwin-arm64`: `7654b882c1014ce98afe0485afb157d61687026acf9b0920791c0f44fa27178a`
  - `build/aima.exe`: `c9208d8544b886b5462d8576123bbd8f576f2fd0d5dd9e7f15348b0675e54026`
  - `build/aima-linux-arm64`: `009d5a9e76c7e561f527bbf1b56a014ed935b28014677aa942e6b2cfbb826d63`
  - `build/aima-linux-amd64`: `deb5078033332692a9616c51e15c139c5da0adfdaeb17cd1c4d6819377c35e17`

Local repair verification used:

- `AIMA_DATA_DIR=/tmp/aima-realtest-20260310`
- Real backend: manual `llama-server` on `http://127.0.0.1:18092`

## Repair Verification

### 1. YAML open-question state now syncs correctly

Real MCP `knowledge.open_questions(list)` after the fix returned catalog-resolved items with their intended status:

- `7e80a049939d090a` -> `confirmed`
- `d44b21439cfc17f2` -> `confirmed_incompatible`

This verifies that YAML `status` / `finding` are no longer flattened to `untested`.

### 2. ZeroClaw persistent sidecar now starts correctly

Real command:

- `aima serve --mcp --addr 127.0.0.1:16188 --mcp-addr 127.0.0.1:19090`

Observed:

- `zeroclaw started pid=83542 gateway=http://127.0.0.1:49848`
- Process command line:
  - `/tmp/aima-realtest-20260310/bin/zeroclaw daemon --config-dir /tmp/aima-realtest-20260310/zeroclaw -p 49848 --host 127.0.0.1`

The original failure mode from the first pass (`unexpected argument '--provider'`) is gone.

### 3. `planner=zeroclaw` persistent path now passes end-to-end

Real MCP call:

- `knowledge.open_questions(action=run, id=28f473eabe852b73, planner=zeroclaw, hardware=apple-m4-arm64, model=Qwen3-0.6B-Q8_0, engine=llamacpp, endpoint=http://127.0.0.1:18092/v1/chat/completions)`

Observed run:

- `run_id`: `bcdefacce5bed2e2`
- `planner`: `zeroclaw`
- `status`: `completed`

Produced artifacts:

- `benchmark_id`: `326af375173196c1`
- `config_id`: `bbaf3b4d242a6281`

Auto-resolution side effect:

- Question `28f473eabe852b73` moved to `tested`
- `actual_result` was populated with the benchmark summary JSON

Measured benchmark summary:

- `throughput_tps`: `93.80`
- `ttft_p50_ms`: `103.1585`
- `tpot_p50_ms`: `10.7630`

### 4. `knowledge.open_questions -> ExplorationRun` remains good on the non-planner path

The earlier real MCP validation still stands:

- `run_id`: `66a53908ca1530bd`
- `question_id`: `9682da03d0e3b08e`
- `benchmark_id`: `c9428eda50574449`
- Final question status: `tested`

### 5. ZeroClaw one-shot fallback plan creation now succeeds

Real CLI command outside `serve`:

- `aima explore start --kind validate --planner zeroclaw ...`

Observed:

- Planner no longer fails with the earlier `400 Bad Request` context overflow.
- A valid queued run was created:
  - `run_id`: `1e1fba8c644c7a86`
  - `planner`: `zeroclaw`

Because this path was invoked from a short-lived CLI process, the important validated outcome here is successful plan creation. Long-running execution is already covered by the persistent `serve --mcp` validation above.

## Device Matrix

| Device | Status | Build Seen | Smoke Result | Notes |
|--------|--------|------------|--------------|-------|
| `dev-mac` | PASS | `4ed44de` / local repair build | PASS | Full local end-to-end completed, including MCP, ZeroClaw daemon, planner path, and open-question auto-validation |
| `test-win` | PASS | `4ed44de` / `2026-03-10T11:59:34Z` | PASS | Fresh `scp` hash matched; `hal detect`, `engine list`, `model list`, `deploy list` all ran |
| `gb10` | PASS | `4ed44de` / `2026-03-10T11:59:34Z` | PASS | Fresh streamed copy hash matched; old copy had been corrupted and segfaulted |
| `linux-1` | PASS | `4ed44de` / `2026-03-10T10:45:34Z` | PASS | Runtime selected `k3s`; smoke commands all returned |
| `amd395` | PASS | `4ed44de` / `2026-03-10T10:45:34Z` | PASS | Runtime selected `k3s`; smoke commands all returned |
| `m1000` | PASS | `4ed44de` / `2026-03-10T10:45:34Z` | PASS | Runtime selected `docker`; engines empty but command path is healthy |
| `hygon` | BLOCKED | n/a | NOT RUN | `Permission denied (publickey)` |
| `qjq2` | BLOCKED | n/a | NOT RUN | `ssh: Could not resolve hostname qjq2`; local SSH alias/proxy not configured |
| `metax-n260` | BLOCKED | n/a | NOT RUN | `ssh: connect to host 100.94.119.128 port 22: Operation timed out` |

## Fixes Applied

### A. ZeroClaw startup contract

Changed from:

- Unsupported stdio launch with top-level `--provider`

Changed to:

- `zeroclaw daemon --config-dir ... -p <port> --host 127.0.0.1`
- AIMA uses the daemon's localhost `/webhook` and `/health` HTTP gateway

### B. Managed ZeroClaw config

AIMA-managed config now explicitly sets:

- `[gateway] host = "127.0.0.1"`
- `[gateway] require_pairing = false`
- `[agent] compact_context = true`
- `[agent] max_history_messages = 8`

This keeps the sidecar local-only and reduces planner context pressure.

### C. Planner request size

The planner payload now excludes bulky `actual_result` history from open questions and uses a shorter planning prompt.

### D. Open-question catalog sync

Catalog sync now carries:

- `status`
- `finding`

And it preserves already-resolved runtime state instead of overwriting it on refresh.

## Remaining Blockers

Only environment blockers remain:

1. `hygon` SSH key auth
2. `qjq2` SSH alias / proxy configuration
3. `metax-n260` network reachability

## Delivery Recommendation

Current recommendation:

- `open_questions -> ExplorationRun` automatic validation: deliverable
- ZeroClaw persistent sidecar + planner wiring: deliverable on the validated local path
- ZeroClaw one-shot planner fallback: deliverable for plan creation on the validated local path
- Cross-device smoke health: acceptable on reachable hosts

Final sign-off still requires unblocking `hygon`, `qjq2`, and `metax-n260`.
