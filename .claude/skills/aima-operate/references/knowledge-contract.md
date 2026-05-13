# AIMA Knowledge Contract

Do not treat every useful observation as reusable knowledge. AIMA knowledge should be local-first, evidence-backed, and promoted only after validation.

## Principle

The skill does not own long-term knowledge. Save reusable findings through AIMA's knowledge domain:

- `knowledge.save` for structured notes and findings.
- `knowledge.evaluate` for validation, engine switch cost, or open questions.
- `knowledge.promote` for validated configurations or rules.
- `central.sync` only after local evidence is stable and sync is useful.

## Knowledge States

Use this lifecycle when describing or saving operational findings:

```text
draft -> candidate -> validated -> golden -> deprecated
```

- `draft`: raw observation, incomplete evidence, or one-off debugging note.
- `candidate`: likely useful, but not yet proven across a benchmark or repeated run.
- `validated`: backed by deploy artifacts, benchmark results, and hardware/model/engine context.
- `golden`: repeatedly useful enough to become a default recommendation.
- `deprecated`: obsolete because of newer engine, model, hardware, benchmark, or AIMA behavior.

## Knowledge Types

Use different evidence thresholds for different kinds of knowledge:

### Incident Note

Use for debugging history, failure symptoms, local workarounds, and lessons learned.

Minimum evidence:

- symptom or error signal
- deployment or command context
- observed environment
- action taken
- result after the action

Incident notes may remain `draft` or `candidate`. They do not require benchmark evidence.

### Config Candidate

Use for a configuration that appears reusable for a model, engine, hardware profile, or deployment shape.

Minimum evidence:

- hardware profile
- model
- engine and version when known
- deploy config
- readiness result
- relevant logs or status output
- at least one resource observation

Config candidates can become `validated` after successful deployment evidence and at least one meaningful measurement. Benchmark evidence is preferred, but a lightweight measured run may be enough for early validation if the limitation is recorded.

### Golden Rule

Use only for stable defaults, recommended parameters, or high-confidence compatibility rules.

Minimum evidence:

- complete deployment evidence
- benchmark profile and results
- resource observations
- repeated success or strong reason to trust one result
- clear applicability scope and limitations

Golden rules must not be created from a single log interpretation or unmeasured workaround.

## Minimum Evidence For Golden Promotion

Promote only when these fields are known or explicitly unavailable with a reason:

Identity:

- hardware profile or GPU architecture
- model
- engine asset ID
- engine version
- engine image when containerized
- benchmark ID
- config ID

Deployment:

- real deploy config, not just a benchmark profile
- important engine parameters, such as tensor parallelism, memory fractions, offload settings, max running requests, or GPU layers
- deployment phase/status and readiness outcome

Benchmark profile:

- concurrency
- number of requests
- warmup count
- rounds
- input token shape
- output token shape
- duration

Performance:

- TTFT p50/p95/p99 when available
- TPOT p50/p95 when available
- throughput
- QPS
- error rate
- sample count
- stability

Resource observation:

- peak VRAM during benchmark window
- peak RAM during benchmark window
- average GPU utilization during benchmark window
- average CPU utilization during benchmark window
- average power draw when available

## Save Pattern

Use this shape when constructing a knowledge note or summarizing what should be saved:

```yaml
kind: knowledge_note
title: "<hardware + model + engine + outcome>"
status: candidate
context:
  hardware_profile: "<profile or observed hardware>"
  model: "<model>"
  engine: "<engine>"
  engine_version: "<version>"
  engine_image: "<image, if known>"
evidence:
  benchmark_id: "<benchmark id>"
  config_id: "<config id>"
  deploy_status: "<ready|failed|degraded>"
  logs: "<short failure or success signal>"
result:
  ttft_p95_ms: null
  throughput_tps: null
  error_rate: null
  stability: "<stable|unstable|unknown>"
resources:
  vram_peak_mib: null
  ram_peak_mib: null
  gpu_utilization_avg_pct: null
  cpu_utilization_avg_pct: null
lesson:
  summary: "<what changed or what was learned>"
  recommendation: "<reuse guidance>"
scope:
  applicable_to:
    - "<hardware or scenario>"
  limitations:
    - "<where not to apply>"
```

## What Not To Promote

Keep these as `draft` or plain run notes:

- A fix with no benchmark or deploy evidence.
- A guess based only on a log line.
- A result that depends on an undocumented local hack.
- A one-time workaround with unknown version or hardware scope.
- A note that conflicts with newer validated evidence.

## Conflict Handling

When findings conflict, do not overwrite older knowledge blindly.

Rank candidates by:

1. Hardware and model match.
2. Benchmark evidence quality.
3. Deployment readiness and stability.
4. Recency.
5. Repeat count.

Mark stale findings as `deprecated` only when there is clearer evidence, not just a newer opinion.
