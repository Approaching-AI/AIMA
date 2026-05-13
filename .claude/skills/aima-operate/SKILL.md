---
name: aima-operate
description: Operate AIMA through its MCP tools and knowledge domain. Use when Codex needs to deploy, debug, inspect, tune, benchmark, troubleshoot, or review AIMA; when working with AIMA MCP tools such as hardware.detect, knowledge.resolve, deploy.apply, deploy.status, deploy.logs, benchmark.run, knowledge.save, knowledge.evaluate, knowledge.promote, or central.sync; and when turning real AIMA usage into evidence-backed reusable knowledge.
---

# AIMA Operate

Use this skill as the agent-facing operating layer for AIMA. Keep AIMA itself as the runtime and knowledge source; use this skill to choose the path, call the right tools, interpret results, and decide what evidence should be saved.

## Example Prompts

- "Check what this machine can run with AIMA."
- "Deploy this model through AIMA and verify it is ready."
- "AIMA will not start; inspect status and logs."
- "Run a benchmark for this deployment and tell me whether the result is reusable."
- "Save this fix so AIMA can avoid the same problem next time."

## Non-Negotiables

- Treat AIMA MCP tools as the primary interface and source of truth.
- Use CLI commands only when MCP is unavailable, incomplete, or the user is explicitly working in a shell-only context.
- Do not create a separate long-term knowledge store inside this skill.
- Save reusable findings through AIMA's knowledge domain, not through ad hoc notes.
- Promote knowledge only when deployment, benchmark, hardware, model, engine, and config evidence are present.
- Keep central sync optional. A local workflow must still succeed when central is offline.
- Prefer small, reversible steps: inspect, resolve, dry run when useful, apply, verify, record.

## Bootstrap

Before taking action, establish the current operating surface:

1. Confirm whether the current workspace is an AIMA checkout or a consumer project that has AIMA configured.
2. Check whether AIMA MCP tools are available in the active agent environment.
3. If MCP tools are available, use the actual exposed tool list as the API contract.
4. If MCP tools are not available, check whether the `aima` CLI exists and inspect local help before guessing commands.
5. Capture the AIMA version, target runtime, and deployment context when available.
6. If neither MCP nor CLI is available, explain what is missing and stop before inventing commands.

## Risk Gates

Pause for explicit user confirmation before destructive or broad-impact actions:

- Delete operations: `deploy.delete`, `model.remove`, `engine.remove`.
- Reset or identity operations: `device.reset`, credential/token rotation, enrollment reset.
- Overrides or shared state changes: `catalog.override`, central push/sync, global config writes.
- Any action that may stop a running model service or replace a known-good deployment.

When fixing failures:

- Prefer `deploy.dry_run` before apply when deployment changes are non-trivial.
- Change one variable at a time, then verify.
- Do not remove models, engines, images, or historical knowledge as an automatic cleanup step.
- Treat central sync as a final optional step, not part of the critical path.

## First Move

1. Identify the user's intent: detect, deploy, inspect, troubleshoot, benchmark, tune, fleet, or knowledge capture.
2. Run the Bootstrap checks above.
3. If MCP is available, use the actual exposed tools and the intent mapping from `references/tool-map.md`.
4. If MCP is not available, use the nearest AIMA CLI fallback from `references/tool-map.md`.
5. Apply Risk Gates before destructive, shared-state, or broad-impact actions.
6. Read `references/knowledge-contract.md` before saving, evaluating, promoting, or syncing knowledge.

## Intent Router

### Detect Capability

Use when the user asks what the machine can run, whether hardware fits a model, or why a target does not fit.

Default path:

1. Collect hardware and runtime facts with `hardware.detect` and `hardware.metrics`.
2. Scan known models and engines with `model.scan` / `model.list` and `engine.scan` / `engine.list`.
3. Resolve candidate config with `knowledge.resolve`.
4. Summarize fit, blockers, and the lowest-risk next action.

### Deploy Or Start AIMA Workload

Use when the user asks to deploy, run, start, or make a model service available through AIMA.

Default path:

1. Resolve the configuration with `knowledge.resolve`.
2. Inspect planned runtime changes with `deploy.dry_run` when a dry run would reduce risk.
3. Apply with `deploy.apply` or run with `deploy.run` depending on the project convention.
4. Verify with `deploy.status`, `deploy.list`, and `system.status`.
5. If not ready, immediately switch to the troubleshoot path.
6. If ready, propose or run benchmark capture when the result may become reusable knowledge.

### Inspect Status Or Logs

Use when the user asks whether AIMA is healthy, what is running, or why a deployment behaves oddly.

Default path:

1. Query `system.status` and `deploy.list`.
2. Query `deploy.status` for exact runtime config, labels, restarts, exit code, and detailed state.
3. Query `deploy.logs` for failure context.
4. Avoid assuming `deploy.list` contains raw config or labels; use `deploy.status` for detail.

### Troubleshoot Failure

Use when deployment fails, readiness stalls, throughput is bad, logs show errors, or AIMA cannot find a model or engine.

Default path:

1. Capture status: `system.status`, `system.diagnostics`, `deploy.status`, `deploy.logs`.
2. Capture resource state: `hardware.metrics`.
3. Re-resolve config: `knowledge.resolve` with the current model, engine, slot, and overrides.
4. Classify the failure: hardware fit, model asset, engine asset, container/runtime, config, port/API, benchmark profile, or knowledge mismatch.
5. Make one minimal fix at a time.
6. Re-run verification after each fix.
7. If a fix works, create an evidence-backed knowledge note candidate.

### Benchmark Or Tune

Use when the user asks to measure performance, compare configs, tune parameters, or decide which deployment is better.

Default path:

1. Reuse a ready deployment if possible.
2. Run `benchmark.run` or record externally measured results with `benchmark.record`.
3. Keep benchmark profile fields: concurrency, requests, warmup, rounds, input/output token shape, and duration.
4. Capture resource observations during the benchmark window.
5. Evaluate with `knowledge.evaluate`.
6. Save with `knowledge.save` only when the evidence contract is satisfied.

### Knowledge Capture

Use when the user asks to remember a fix, make a rule, update the knowledge base, sync knowledge, or avoid repeating a problem.

Default path:

1. Read `references/knowledge-contract.md`.
2. Decide whether the finding is a draft note, candidate, validated config, or golden rule.
3. Save locally with `knowledge.save` when structured evidence exists.
4. Evaluate with `knowledge.evaluate`.
5. Promote with `knowledge.promote` only after validation.
6. Sync with `central.sync` only as an optional final step.

## Output Style

When reporting back, keep the result operational:

- What was checked.
- What AIMA reported.
- What action was taken or should be taken next.
- Whether evidence is strong enough to save or promote.
- Any residual risk or manual decision needed.

Use this compact template when useful:

```text
Conclusion:
Checked:
Found:
Action:
Knowledge:
Next:
```

Avoid long explanations of AIMA internals unless the user asks. The useful answer is usually the next safe action plus the evidence behind it.

## References

- Use `references/tool-map.md` for AIMA MCP tool and CLI fallback selection.
- Use `references/knowledge-contract.md` before saving or promoting knowledge.
