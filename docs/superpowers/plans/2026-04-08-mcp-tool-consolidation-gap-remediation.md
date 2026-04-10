# MCP Tool Consolidation Gap Remediation

> Date: 2026-04-08
> Branch: `refactor/mcp-tool-consolidation`
> Status: In Progress
> Source review: branch-wide architecture + implementation review against `develop`

## 1. Goal

Close the gaps introduced by the MCP tool consolidation refactor so that:

1. The new 56-tool surface is internally consistent.
2. First-party clients do not call deleted tool names.
3. Guardrails remain effective after action-based tool merging.
4. Agent/Profile behavior matches the consolidation design.
5. Core docs and tests describe and protect the new surface.

This remediation is not a new redesign. It is a correctness and consistency pass on top of the existing consolidation work.

## 2. Confirmed Findings

### P0-1. Guardrail regression after tool-name consolidation

The adapter and fleet guardrails still match deleted tool names instead of the new merged surfaces.

Confirmed examples:
- `cmd/aima/adapters.go`
- `cmd/aima/tooldeps_fleet.go`

Concrete regressions:
- `fleet.exec` replaced `fleet.exec_tool`, but guardrail logic still keys on `fleet.exec_tool`.
- `stack.init` is now `stack` with `action=init`, but blocklists still key on the old name.
- `explore.start` is now `explore` with `action=start`, but blocklists still key on the old name.
- Remote approval flow for `deploy.apply` through `fleet.exec` is still wired to the deleted fleet tool name.

Impact:
- Agent-side and fleet-side destructive-operation protection is no longer aligned with the actual MCP surface.
- Remote execution can bypass the intended consolidated-name checks.

### P0-2. First-party UI still calls deleted MCP tool names

The embedded Web UI still calls removed tools directly.

Confirmed examples:
- `internal/ui/static/index.html`

Concrete breakage:
- `openclaw.claim` / `openclaw.sync` instead of `openclaw{action=...}`
- `download.list` even though the tool was removed
- `fleet.list_devices` instead of `fleet.info`
- `support.askforhelp` instead of `support`

Impact:
- Runtime breakage in first-party UI even if backend MCP registration is correct.
- Violates the "no compatibility layer" rule unless explicit aliases are intentionally added.

### P1-1. `deploy.dry_run(output=pod_yaml)` is semantically inconsistent

The consolidated `deploy.dry_run(output=pod_yaml)` path does not use the same resolved config path as the normal dry-run report.

Confirmed examples:
- `internal/mcp/tools_deploy.go`
- `internal/mcp/tools_deps.go`
- `cmd/aima/tooldeps_knowledge.go`
- `cmd/aima/tooldeps_deploy.go`

Current mismatch:
- Full dry-run computes a resolved config and can include generated `pod_yaml`.
- `output=pod_yaml` calls the old `GeneratePod(model, engine, slot)` dependency without passing config overrides or `max_cold_start_s`.

Impact:
- Two forms of the same dry-run request can describe different deployments.
- This is a correctness bug, not just a documentation problem.

### P1-2. Profile redesign only partially landed

The new profile-aware tool discovery is implemented for `agent.ask`, but not for the Explorer planning path described in the design.

Confirmed examples:
- `cmd/aima/main.go`
- `internal/agent/agent.go`
- `internal/agent/explorer_llmplanner.go`
- `docs/superpowers/specs/2026-04-08-mcp-tool-consolidation-design.md`

Current mismatch:
- Main Go Agent is created with `operator` profile.
- Explorer reuses that same agent.
- Explorer's LLM planner bypasses `Ask()` and directly calls `ChatCompletion(..., nil)`, so it does not consume profile-filtered tools at all.

Impact:
- The design claim "Explorer planning uses ProfileExplorer" is not true in the current implementation.
- Internal-consumer de-noising is only partially delivered.

### P1-3. Core docs still describe the pre-consolidation tool surface

The consolidation changed the MCP surface, but multiple canonical docs still describe the old tool inventory.

Confirmed examples:
- `design/ARCHITECTURE.md`
- `docs/mcp.md`
- `docs/knowledge.md`
- `docs/engine.md`
- `catalog/agent-guide.md`
- `internal/openclaw/skills/aima-control/SKILL.md`

Impact:
- First-party guidance is internally contradictory.
- Human operators and agents reading embedded guides receive outdated tool names.

### P1-4. Tests pass, but many assertions still target deleted tool names

The repo is green, but a meaningful part of the protection still targets the removed MCP surface.

Confirmed examples:
- `cmd/aima/main_test.go`
- `internal/mcp/mcp_test.go`

Examples:
- tests still assert `explore.start`, `tuning.start`, `shell.exec`, `stack.init`
- guardrail tests do not prove that merged action-based tools are protected

Impact:
- Green tests overstate confidence in the refactor.
- Regressions on merged tools can slip through.

### P2-1. Spec/code mismatch on `catalog.list(kind=partitions)`

The consolidation design explicitly includes `partitions`, but the implementation does not.

Confirmed examples:
- `docs/superpowers/specs/2026-04-08-mcp-tool-consolidation-design.md`
- `internal/mcp/tools_catalog.go`

Impact:
- Either the spec is wrong or the code is incomplete.
- This must be resolved explicitly, not left ambiguous.

### P2-2. CLI consolidation is intentionally incomplete or undocumented

MCP tools were consolidated aggressively, but several CLI surfaces still expose the older lifecycle-shaped subcommands.

Examples:
- `internal/cli/explore.go`
- `internal/cli/tuning.go`
- `internal/cli/scenario.go`

This is not automatically a bug. It becomes a problem only if:
- docs claim CLI was consolidated when it was not, or
- CLI names drift away from the MCP wrappers badly enough to violate INV-5.

Required decision:
- either keep current CLI shape and document it as a thin wrapper over granular ToolDeps, or
- continue the CLI consolidation intentionally.

## 3. Remediation Strategy

We will fix this in four parallel workstreams plus one integration sweep.

### Workstream A. Guardrails and fleet boundary correctness

Owner scope:
- `cmd/aima/adapters.go`
- `cmd/aima/tooldeps_fleet.go`
- related tests in `cmd/aima/main_test.go`

Required outcomes:
- action-aware block/allow checks for merged tools
- `fleet.exec` handled as the new remote entrypoint
- remote approval flow updated to the new fleet tool name
- guardrail tests rewritten around current MCP names and action payloads

Acceptance:
- local Agent cannot call blocked merged operations
- remote fleet path cannot bypass the same restrictions
- `deploy.apply` still goes through approval both locally and remotely

### Workstream B. First-party client migration

Owner scope:
- `internal/ui/static/index.html`
- `catalog/agent-guide.md`
- `internal/openclaw/skills/aima-control/SKILL.md`

Required outcomes:
- first-party UI uses only current MCP names
- embedded guides stop teaching deleted names
- no first-party client depends on removed tools like `download.list`

Acceptance:
- UI references only registered tool names
- OpenClaw and support flows use consolidated action/name forms
- fleet list flow uses `fleet.info`

### Workstream C. Dry-run semantic consistency and MCP surface cleanup

Owner scope:
- `internal/mcp/tools_deploy.go`
- `internal/mcp/tools_catalog.go`
- `internal/mcp/tools_deps.go`
- `cmd/aima/tooldeps_knowledge.go`
- related MCP tests

Required outcomes:
- `deploy.dry_run(output=pod_yaml)` uses the same resolved config path as the full dry-run
- resolve spec/code mismatch for `catalog.list(kind=partitions)`
- ensure consolidation-level schemas and handlers reflect the real supported surface

Acceptance:
- identical input yields semantically identical dry-run and pod_yaml content
- spec and implementation agree on `catalog.list`

### Workstream D. Profile/design alignment, docs, and tests

Owner scope:
- `cmd/aima/main.go`
- `internal/agent/explorer_llmplanner.go`
- `design/ARCHITECTURE.md`
- `docs/mcp.md`
- `docs/knowledge.md`
- `docs/engine.md`
- `internal/mcp/mcp_test.go`

Required outcomes:
- either implement the designed Explorer profile split, or update the design to match the actual architecture
- canonical docs reflect the 56-tool consolidated surface
- MCP/profile tests stop asserting deleted names

Acceptance:
- no contradiction between architecture/spec/docs and code
- profile tests validate the current tool names
- Explorer profile story is explicit and true

### Workstream E. Final integration sweep

Owner:
- main rollout

Required outcomes:
- resolve merge conflicts between workstreams
- run repo-wide verification
- close any leftovers not suitable for parallel ownership

Acceptance:
- `go test ./...` passes
- no obvious stale references remain in non-archival docs and first-party clients

## 4. Proposed Agent Team

### Agent 1: Guardrails/Fleet

Mission:
- Fix action-aware guardrails and fleet execution path.

Files:
- `cmd/aima/adapters.go`
- `cmd/aima/tooldeps_fleet.go`
- `cmd/aima/main_test.go`

### Agent 2: UI/Embedded Clients

Mission:
- Migrate first-party UI and embedded guides to the consolidated MCP names.

Files:
- `internal/ui/static/index.html`
- `catalog/agent-guide.md`
- `internal/openclaw/skills/aima-control/SKILL.md`

### Agent 3: MCP Semantics

Mission:
- Fix `deploy.dry_run` semantic mismatch and resolve catalog consolidation gaps.

Files:
- `internal/mcp/tools_deploy.go`
- `internal/mcp/tools_catalog.go`
- `internal/mcp/tools_deps.go`
- `cmd/aima/tooldeps_knowledge.go`
- related tests under `internal/mcp/`

### Agent 4: Docs/Profile/Test Alignment

Mission:
- Align profile/design behavior, canonical docs, and MCP tests with the new surface.

Files:
- `cmd/aima/main.go`
- `internal/agent/explorer_llmplanner.go`
- `design/ARCHITECTURE.md`
- `docs/mcp.md`
- `docs/knowledge.md`
- `docs/engine.md`
- `internal/mcp/mcp_test.go`

## 5. Integration Rules

1. Do not reintroduce compatibility aliases unless explicitly required.
2. Do not revert unrelated branch work.
3. Prefer updating first-party clients to the new names over adding backend shims.
4. Keep CLI thin-wrapper behavior intact unless an explicit CLI consolidation decision is made.
5. If design and implementation diverge, resolve the divergence explicitly in docs or code in the same change set.

## 6. Verification Checklist

### Required

- `go test ./...`
- `rg` sweep for deleted MCP names in first-party runtime paths:
  - UI
  - MCP registration/tests
  - active docs and guides
  - adapter/guardrail code

### Focused checks

- guardrail behavior for:
  - `stack{action:init}`
  - `explore{action:start}`
  - `fleet.exec(tool_name=deploy.apply, ...)`
  - `system.config` write attempts
- UI MCP references contain only registered tool names
- `deploy.dry_run` default output and `output=pod_yaml` remain consistent for the same input

## 7. Done Definition

This remediation is done when:

1. No first-party runtime path calls a deleted tool name.
2. Guardrails protect the merged action-based tools as strongly as the old split tools.
3. Consolidated dry-run behavior is semantically consistent.
4. Profile/design claims are true, not aspirational.
5. Canonical docs and tests reflect the current MCP surface.
