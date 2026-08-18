# AIMA First-Run Usability Remediation

Date: 2026-04-24
Branch: develop
Owner: AIMA product/runtime

## Problem

AIMA already has a strong technical promise: one binary, hardware-aware model
deployment, agent-driven tuning, OpenAI-compatible serving, and MCP-native
automation. The main adoption blocker is not raw capability. The blocker is
that a first-time user cannot reliably move from "I installed AIMA" to "I have
a local model serving requests" without crossing several product gaps:

- The README describes commands that do not match the CLI contract.
- The first visible path asks users to initialize infrastructure before proving
  a useful local inference path.
- `aima onboarding` is documented as a wizard, but the root command only shows
  subcommands instead of running a guided flow.
- macOS, Windows, CPU-only, and native llama.cpp paths can be shown as needing
  stack initialization even when Docker/K3S are intentionally skipped.
- The onboarding recommendation score can prefer a huge "largest fittable"
  model over a model that is credible for a first run on a 16 GB local machine.
- The Web UI getting-started manifest still advertises stale commands and
  emphasizes inspection over the main user outcome: run a model.

These gaps create the "half-finished" feeling: the platform is powerful, but
the first-run surface does not collapse that power into one confident path.

## Product Bar

The default experience should be:

1. Install the binary.
2. Confirm hardware detection.
3. Run one safe local model with a single command.
4. See how to keep the API server open.
5. Discover advanced server, fleet, and tuning features only after the first
   local success.

AIMA can still expose the high-performance path. It should not make the high-
performance path the user's first obstacle.

## Golden Path

The primary first-run path is:

```bash
aima hal detect
aima onboarding
aima run qwen3-4b
aima serve
```

`aima onboarding` is a read-only guided check by default. It runs status, scan,
and recommendation, then prints the next concrete command. It must not install
system services or deploy a large model without explicit user action.

For users who already know the model:

```bash
aima run qwen3-4b
```

For Linux servers that should become shared inference hosts:

```bash
sudo aima init
aima deploy qwen3-4b
aima serve
```

## Scope For This Remediation

### P0: make the documented path executable

- Support `aima onboarding` with no subcommand as the guided start.
- Add `aima onboarding start` as an explicit alias for the same flow.
- Expose the same flow as MCP `onboarding` action `start`; CLI must not own
  the start decision logic.
- Replace invalid docs/examples such as `aima deploy apply --model ...`.
- Replace stale discovery examples with the current `aima fleet devices` path.

### P0: prevent misleading setup prompts

- Treat Docker/K3S components marked `skipped` as native-only, not broken.
- Do not mark native-only hosts as needing initialization just because
  container infrastructure is absent.

### P0: make first recommendation feel credible

- Penalize RAM-overflow and oversized wildcard/native recommendations on
  small local machines.
- Keep those guardrails in `catalog/onboarding-policy.yaml` so first-run
  product policy is reviewable as catalog data.
- Keep the existing high-capacity behavior for real accelerator hosts.
- Ensure a small/medium model outranks a huge local model on 16 GB machines.

### P1: align UI copy

- Update the getting-started manifest so quick start starts with onboarding and
  run commands.
- Remove stale `/cli engine plan` and `/cli discover` examples.

## Acceptance Criteria

- `aima onboarding` prints a guided first-run summary, not help text.
- `aima onboarding start` prints the same guided summary.
- On macOS/native-only status, skipped Docker/K3S does not produce
  `needs_init=true`.
- On a 16 GB no-GPU/native profile, an oversized 30B/80B style model receives a
  much lower first-run score than an 8B or smaller local model.
- README English and Chinese quick starts only include commands that exist.
- Web UI onboarding manifest only includes valid command examples.
- Focused tests pass for CLI and onboarding packages.

## Out Of Scope

- Rebuilding the Web UI visual layout.
- Implementing a full interactive TUI wizard.
- Changing model download hosting.
- Changing the serving API contract.
- Running large live model downloads as part of unit verification.

## Follow-Up Product Work

- Add a first-run integration gate that runs the installed binary in a clean
  data directory and verifies `hal detect`, `onboarding`, `recommend`, and
  `deploy --dry-run` as the current non-mutating run-plan equivalent.
  (Implemented via `scripts/first-run-smoke.sh`, `make first-run-smoke`, and
  the `First Run Smoke` GitHub Actions workflow.)
- Add a Web UI "Run recommended model" action that maps exactly to the CLI
  golden path. (Implemented after the initial remediation commit via
  `/ui/api/onboarding-start` + the existing deploy SSE flow.)
- Add release-note examples for three personas: local Mac/Windows user, Linux
  workstation owner, and multi-node operator.
- Add telemetry-free local diagnostics export so failed first runs are easy to
  attach to support requests. (Implemented via `system.diagnostics` and
  `aima diagnostics export`; bundles are local-only and redact secrets before
  writing.)
