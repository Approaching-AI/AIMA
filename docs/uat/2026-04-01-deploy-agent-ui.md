# Deploy / Agent / UI UAT

Date: 2026-04-01
Branch: `feat/uat-deploy-agent-hardening`
Baseline: `develop`

## Goal

This UAT round focuses on three chains that must behave as one coherent system:

1. `deploy` / `undeploy` truly create and remove backend runtime state.
2. deployed model -> local agent (`agent.ask`) uses the configured local model, endpoint, and extra params without drift.
3. Web UI reflects deployment progress and failure stages from real backend status fields rather than synthetic placeholders.

## Entry Points Under Test

- CLI: `aima deploy`, `aima undeploy`, `aima run`
- MCP: `deploy.apply`, `deploy.run`, `deploy.delete`, `deploy.status`, `agent.ask`
- HTTP UI bridge: `/api/v1/tools/*`, `/api/v1/agent/ask/stream`, `/api/v1/cli/exec`
- UI: `/ui/` deployment panel and agent panel

## Acceptance Criteria

- A `run` / `deploy` request preserves explicit `engine`, `slot`, `config`, and `max_cold_start_s`.
- `undeploy` removes the runtime object, removes the proxy backend, and records tombstones/snapshots.
- `agent.ask` hits the configured `llm.endpoint` with the configured `llm.model`, auth header, user-agent, and extra params.
- UI deployment cards show `startup_phase`, `startup_progress`, `startup_message`, ETA, ready address, and distilled failure detail from `deploy.list`.
- `deploy.list` remains an overview surface; any full deployment detail (`config`, raw labels) comes from `deploy.status`, not from list payload inflation.

## Local Automated Checks

Run on the feature worktree before remote validation:

```bash
go test ./cmd/aima ./internal/cli ./internal/mcp ./internal/ui
```

Coverage added in this round:

- `internal/cli/http_exec_test.go`
  - `run` CLI flag passthrough
  - `undeploy` CLI arg passthrough
- `internal/mcp/tools_deploy_test.go`
  - `deploy.run` MCP config passthrough
- `cmd/aima/uat_chain_test.go`
  - `deploy.delete` cross-runtime cleanup, snapshot, tombstone, proxy removal
  - configured local model -> `agent.ask` request passthrough
- `internal/ui/handler_test.go`
  - deployment stage UI tokens present in embedded SPA

## Manual UI UAT

Use a real browser against a live AIMA instance.

1. Open `/ui/`.
2. Trigger deploy through the same production path you expect users/agents to use:
   - `/cli run <model> --engine ... --slot ... --config ...`
   - or MCP/agent flow that lands on `deploy.run` / `deploy.apply`
3. Observe the deployment panel:
   - initial orange status while backend is not ready
   - progress bar advances using `startup_progress`
   - phase text follows `startup_message` / `startup_phase`
   - ETA renders when `estimated_total_s` is present
   - ready card switches to green and shows `address`
4. Undeploy the same deployment:
   - card disappears after backend removal
   - refresh does not resurrect deleted deployment during tombstone grace window
5. Configure agent:
   - set `llm.endpoint`, `llm.model`, optional `llm.api_key`, `llm.user_agent`, `llm.extra_params`
   - send a simple `agent.ask`
   - verify the response succeeds against the intended local model

## Remote Matrix

Follow `CLAUDE.md` "ALL COLLECT, THEN ANALYZE".

Round 1 target machines:

- `dev-mac`
- `test-win`
- `linux-1`
- `amd395`
- `hygon`
- `qjq2`
- `m1000`
- `metax-n260`
- `aibook`
- `w7900d`

Deferred in round 1:

- `gb10`

Rules:

- if a host is unreachable, record `UNREACHABLE` and keep the matrix intact
- do not patch per-host before collecting the whole first-pass matrix

## Remote Command Set

Build and distribute per `CLAUDE.md`, then run the same focused checks on every reachable target:

```bash
./aima version
./aima deploy list
./aima run <known-local-model> --engine <expected-engine> --config gpu_memory_utilization=0.9 --max-cold-start 30
./aima deploy status <deployment-or-model>
./aima undeploy <deployment-or-model>
./aima config set llm.endpoint http://localhost:6188/v1
./aima config set llm.model <same-model>
./aima agent status
```

For UI-capable targets, additionally validate in browser:

- deployment panel phase transitions
- ready address surfacing
- post-undeploy disappearance
- agent panel switches from direct mode to agent-ready when config is valid

## Result Recording

Record per machine:

- deploy command used
- actual runtime selected
- actual deployment name
- whether config values appear in dry-run / status / backend logs
- undeploy cleanup result
- agent.ask local-model success/failure
- UI stage fidelity notes
