# AIMA Tool Map

Use MCP first. Use CLI only when MCP is unavailable, the user is working shell-only, or a local project convention requires CLI verification.

This file is an intent map, not an absolute API schema. Always prefer the tools actually exposed by the current AIMA MCP server or the commands shown by the checked-out AIMA CLI help.

## MCP Domains

Core operations:

- Hardware: `hardware.detect`, `hardware.metrics`
- Model: `model.scan`, `model.list`, `model.pull`, `model.import`, `model.info`, `model.remove`
- Engine: `engine.scan`, `engine.info`, `engine.list`, `engine.pull`, `engine.import`, `engine.remove`
- Deploy: `deploy.apply`, `deploy.approve`, `deploy.dry_run`, `deploy.run`, `deploy.delete`, `deploy.status`, `deploy.list`, `deploy.logs`
- System: `system.status`, `system.config`, `system.diagnostics`

Knowledge and measurement:

- Knowledge: `knowledge.resolve`, `knowledge.search`, `knowledge.analytics`, `knowledge.promote`, `knowledge.save`, `knowledge.evaluate`
- Benchmark: `benchmark.run`, `benchmark.matrix`, `benchmark.record`, `benchmark.list`
- Agent: `agent.ask`, `agent.status`, `agent.rollback`
- Scenario: `scenario.show`, `scenario.apply`

Coordination and integration:

- Catalog: `catalog.list`, `catalog.override`, `catalog.validate`
- Central: `central.sync`, `central.advise`, `central.scenario`
- Data: `data.export`, `data.import`
- Device: `device.register`, `device.status`, `device.renew`, `device.reset`
- Fleet: `fleet.info`, `fleet.exec`
- Onboarding: `onboarding`
- Support: `support`

## Confirm Before These Tools

Ask for explicit user confirmation before using tools that delete, reset, overwrite, push shared state, or affect other machines:

- `deploy.delete`
- `model.remove`
- `engine.remove`
- `device.reset`
- `catalog.override`
- `central.sync` when pushing or publishing local findings
- `fleet.exec` when the command changes state across machines

## Important Contract Details

- Use `deploy.list` for overview: names, model, engine, slot, phase, status, ready, address, runtime, and summarized startup/failure fields.
- Use `deploy.status` for detail: full runtime config, labels, restarts, exit code, startup timestamps, and exact deployment state.
- Do not rely on `deploy.list` for raw config or label maps.
- Use `knowledge.resolve` before deployment or tuning to merge catalog knowledge, user overrides, community notes, partition strategy, and live fit decisions.
- Use `hardware.metrics` near deploy and benchmark time; stale hardware facts can cause bad fit decisions.

## CLI Fallbacks

Prefer the exact command available in the checked-out AIMA version. Common fallbacks include:

- `aima knowledge list`
- `aima knowledge resolve <model>`
- `aima knowledge sync --push`
- `aima knowledge sync --pull`
- `aima knowledge import <path>`
- `aima knowledge export --output <path>`

If a CLI command is missing or differs, inspect the local CLI help instead of guessing:

```bash
aima --help
aima knowledge --help
```

## Safe Tool Sequences

Detect fit:

```text
hardware.detect -> hardware.metrics -> model.scan/list -> engine.scan/list -> knowledge.resolve
```

Deploy:

```text
knowledge.resolve -> deploy.dry_run -> deploy.apply -> deploy.status -> deploy.logs on failure
```

Troubleshoot:

```text
system.status -> system.diagnostics -> deploy.list -> deploy.status -> deploy.logs -> hardware.metrics -> knowledge.resolve
```

Benchmark and learn:

```text
deploy.status -> benchmark.run -> benchmark.record/list -> knowledge.evaluate -> knowledge.save -> knowledge.promote when validated
```
