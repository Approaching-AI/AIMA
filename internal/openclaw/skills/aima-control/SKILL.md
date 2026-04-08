---
name: aima-control
description: Manage local AIMA deployments and device state through the built-in AIMA MCP server.
metadata: {"openclaw":{"emoji":"🛠️","always":true}}
---

# AIMA Control Plane

Use AIMA MCP tools for local inference operations on this device.

## Preferred tools

- `system.status` for overall device and runtime state
- `hardware.detect` and `hardware.metrics` for capability and live load checks
- `model.list` and `engine.list` before planning changes
- `deploy.list`, `deploy.status`, `deploy.apply`, `deploy.delete`, and `deploy.logs` for deployment work
- `knowledge.resolve` before changing engine/model combinations
- `benchmark.run` after major deployment changes
- `openclaw` with `action=status|sync|claim` for OpenClaw integration drift

## Operating rules

- Prefer MCP tools over shell commands when both are available.
- Treat `tools/list` as discovery only; use the listed tools as the supported day-to-day surface.
- Before deploying or replacing a model, inspect the current deployment state.
- After changing deployments, verify readiness with `deploy.status` or `deploy.list`.
