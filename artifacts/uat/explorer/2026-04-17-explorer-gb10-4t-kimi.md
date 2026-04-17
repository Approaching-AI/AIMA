# Explorer End-to-End on gb10-4t with Kimi Coding API

Date: 2026-04-17
Machine: gb10-4t (`qujing@100.91.39.109`, NVIDIA GB10, Ubuntu 24.04 aarch64, 120 GB unified, 2 TB NVMe)
Binary under test: working-tree of `develop` (includes uncommitted explorer coverage/tuner changes)
LLM: `https://api.kimi.com/coding/v1`, model `kimi-for-coding`, UA `claude-code/2.1.5`

## Goal

Run a real end-to-end exploration cycle and capture:

1. Observations of agent behaviour (plan → execute → check)
2. Bugs surfaced in AIMA mechanism / execution program
3. Design concerns
4. Quality of knowledge generated (configurations / benchmark_results / workspace docs)

## Setup chain (done)

- [x] Cross-compile `build/aima-linux-arm64` (32 MB statically-linked aarch64 ELF, go1.26.1)
- [x] `scp` to `qujing@100.91.39.109:~/aima`, `hal detect` OK (NVIDIA GB10 Blackwell, 122570 MiB unified, driver 580.126.09, CUDA 13.0)
- [x] `aima config set llm.endpoint/api_key/model/user_agent` — hot-reload confirmed in slog
- [x] `aima serve --mcp --addr 127.0.0.1:6188 --mcp-addr 127.0.0.1:9090` — proxy+MCP+mDNS all up
- [x] `explorer.config set rounds_used=0 mode=once max_rounds=1 max_cycles=1 max_tasks=2`
- [x] `explorer.trigger` — cycle started

## Preconditions observed

- Machine already had a mature explorer workspace from 2026-04-14 cycle: 30 local models, 6-8 engines, 148 Ready/85 Blocked/7 Already Explored combos, 24 experiment files, comprehensive blocker list (transformers mismatches on gemma-4/qwen3_5/glm4_moe_lite, deploy timeouts on sglang/qwen-tts, llamacpp broken image).
- The test therefore measures an **incremental cycle on a hot workspace**, not cold start. Explorer's Pending-Work semantics are exercised against real prior evidence.

## LLM probe (pre-serve)

- `GET /v1/models` → 200, `kimi-for-coding`, `context_length: 262144`, `supports_reasoning/image_in/video_in: true`
- `POST /v1/chat/completions` with `max_tokens=16` → 200 but `content=""`, `reasoning_content="..."`, `finish_reason=length`. Kimi coding is a **reasoning model**: output is split into `reasoning_content` (thinking) and `content` (answer). A tight `max_tokens` budget can leave `content` empty.
- AIMA's `internal/agent/openai.go` already has `ReasoningContent` field (`agent.go:35,50`; `openai.go:525,1178`) and `explorer_agent_planner.go:101,115` passes it through turns. ✓

---

## Running log

### 16:40:01 — plan phase starts (tier=2)

- `plan input ready gaps=44 deploys=0 models=30 engines=6 history=29`
- 5 LLM turns, spanning 16:40:01 → 16:43:06 (~3 min 5 s elapsed), ~145 K total tokens.
- Turn latencies: 3 s, 82 s, 5 s, 88 s, 7 s. The long turns (82/88 s) are Kimi `reasoning_content` generation; short turns are tool-call round-trips.

### 16:43:06 — plan generated (id=9b514637, 5 tasks)

| # | kind | model | engine | params / search_space | reason |
|---|------|-------|--------|------------------------|--------|
| 1 | validate | GLM-4.1V-9B-Thinking-FP4 | vllm-nightly | gmu=0.85, max_model_len=8192 | **Pending Work validate_baseline** |
| 2 | validate | MiniCPM-o-4_5 | vllm-nightly | gmu=0.85, max_model_len=8192 | **Pending Work validate_baseline** |
| 3 | validate | Qwen3-ASR-1.7B | vllm-nightly | gmu=0.85, max_model_len=8192 | **Pending Work validate_baseline** |
| 4 | tune | GLM-4.6V-Flash-FP4 | vllm-nightly | search_space: `gmu=[0.7,0.75,0.8,0.85,0.9]`, `max_model_len=[8192]` | **Pending Work tune** (baseline 32.5 TPS) |
| 5 | tune | Qwen2.5-Coder-3B-Instruct | vllm-nightly | search_space: `gmu=[0.7,0.75,0.8,0.85,0.9]`, `max_model_len=[8192]` | **Pending Work tune** (baseline 29.0 TPS) |

### 16:43:13 — task 1/5 deploy started

- `docker run -d ... vllm/vllm-openai:qwen3_5-cu130 serve /models --port 8000 --gpu-memory-utilization 0.85 --max-model-len 8192 --served-model-name GLM-4.1V-9B-Thinking-FP4`
- Endpoint ready at 16:46:11 → ~3 min cold start (weight load + CUDA graph).
- `WARN deploy fitness warning="unified memory system has swap enabled (16383 MiB); high gmu may cause swap thrashing instead of clean OOM-kill"` — useful hardware-aware advisory, surfaces on every deploy to unified-memory machines.

### 16:46:11 — benchmark matrix starts

- Profile enriched from empty task spec: `profile=latency concurrency=[1] input_tokens=[128,512,1024,2048,4096] output_tokens=[256,1024]` → 10 cells.
- Input ceiling 4096 = largest ladder point ≤ (`max_model_len=8192` − `minOutput=128`). The spec's "long-context anchor" mechanism works, but the ceiling is entirely determined by the **task's max_model_len**, not the model's actual capacity.

### 16:46–17:09 — task 1 latency profile (10 cells)

- All 10 cells stable, error_rate=0, TPS 31.0–33.2 at concurrency=1.
- TTFT scales linearly: 47 ms @ in=128 → 447 ms @ in=4096.
- Per cell: out=256 ≈ 38 s, out=1024 ≈ 155 s. **Zero per-cell log lines in `aima.serve.log`.**

### 17:09–17:39 — task 1 throughput profile (12 cells)

- conc=1 cells re-measured (overlap with latency profile for `in=512/2048/4096 out=1024` — **Bug-5 duplicate**).
- conc=2: 53–57 TPS (`stability=fluctuating/unstable`), conc=4: 77–83 TPS, conc=8: 143–165 TPS.
- 8× concurrency delivered 5× throughput (165/33) — batching works, not linear scaling.

### 17:39:46 — task 1 harvest

- `harvest action type=note detail="GLM-4.1V-9B-Thinking-FP4 on vllm-nightly@nightly (22 cells, 22 ok) ..."` — full per-cell benchmark summary written to `knowledge_notes` (id `7e44e66d...`).
- `harvest action type=sync_push detail="incremental push"` — attempted to push to central (server not reachable at `aimaservice.ai/central` from this lab, but the call fires).
- Experiment file `006-GLM-4.1V-9B-Thinking-FP4-vllm-nightly.md` written (17 KB, full YAML Result + markdown matrix, 22 rows).

### 17:39:47 — task 2 MiniCPM-o-4_5 FAILS PRE-FLIGHT (8 s)

- `pre-flight deploy: ... container compatibility check failed for MiniCPM-o-4_5 with vllm/vllm-openai:qwen3_5-cu130: ValueError: Unrecognized model in /models. Should have a model_type key ...`
- **AIMA's pre-flight compat check is excellent**: 8 s vs the 5-min deploy timeout that earlier cycles hit.
- Experiment `007-MiniCPM-o-4_5-vllm-nightly.md` written (6 KB with full error YAML).

### 17:39:55 — task 3 Qwen3-ASR-1.7B FAILS PRE-FLIGHT (7 s)

- `model type qwen3_asr but Transformers does not recognize this architecture` — same `transformers_version_mismatch` family as prior `qwen3_5` / `glm4_moe_lite` blockers.
- Experiment `008-Qwen3-ASR-1.7B-vllm-nightly.md` written.

### 17:40–18:04 — task 4 tune GLM-4.6V-Flash-FP4 (5 variants)

- `tuning: testing config progress=N/5` for `gmu = 0.7, 0.75, 0.8, 0.85, 0.9` — `search_space` correctly iterated.
- For `gmu=0.9` the actual docker deploy used `--gpu-memory-utilization 0.86` (resolver safety cap `maxSafeGMU = (free − 512) / total`, `math.Floor(*100)/100` — `resolver.go:1434-1442`). **Experiment 009 records `gmu=0.9 tested`, masking the real value.**
- Best config selected: `gmu=0.85 @ 33.28 TPS`. Deploy kept running for "best config" deployment at the end.
- LLM Agent Note (check phase): "Performance is remarkably flat across 0.70-0.90 (32.8-33.3 TPS), with 0.85 yielding the best result … memory reservation changes within this band do not materially affect throughput for this 8.25 GiB model on GB10." — **accurate, concise, actionable**.

### 18:05:00 — task 4 harvest

- `harvest action type=note detail="Benchmark b6400d4fec32e009 … reported zero requests, zero rounds, and zero resource utilization … while still logging a throughput of 33.3 tok/s … Because the benchmark profile shows requests=0, rounds=0, and no resource usage, re-run the benchmark …"` — **LLM correctly flags a zero-counter benchmark record it does not trust** (Bug-7).

### 18:05:02–18:05:20 — task 5 tune Qwen2.5-Coder-3B-Instruct — ALL 5 VARIANTS FAIL

- `deploy run Qwen2.5-Coder-3B-Instruct: hardware not compatible: GPU memory insufficient: only 3.2% usable (need ≥10%); 7994 MiB free / 122570 MiB total, 4096 MiB safety reserve`.
- Root cause: **task 4's best-config deploy was still running** (`glm-4-6v-flash-fp4-vllm-nightly` holding ~85 % of 122 GB unified memory). No pre-task teardown (Bug-8).
- LLM Agent Note: "The failure mode is likely environmental (e.g., transient port conflict, OOM during parallel tuning deployments, or container startup flakiness) rather than a model/architecture incompatibility." — **correctly diagnoses environmental, cannot see the actual system-level cause**.

### 18:05:23 → 18:09:56 — check phase (6 turns, ~224 K tokens)

- Turn latencies: 2 s, 4 s, 32 s, 155 s, 76 s, 4 s.
- LLM filled all 5 `## Agent Notes` sections in `experiments/00{6..10}*.md`.
- Rewrote `summary.md`: new throughput champion (GLM-4.1V-9B-Thinking-FP4, 165 TPS), updated cross-engine comparison, 12 confirmed blockers (added MiniCPM + Qwen3-ASR), 11 Do-Not-Retry entries.
- Classified Qwen2.5-Coder-3B tune failure as environmental → **correctly left off Do-Not-Retry list**, preserving retry headroom.
- `verdict=done extra_tasks=0 cycle=1` → PDCA done, no act-phase extension.
- `knowledge-base.md` now shows `## Pending Work: _No pending work_`.

### 18:05:20 — plan completed in DB

- `exploration_plans.progress = 5/5`, `completed_at` set.
- **`tuning_sessions` table is EMPTY** despite task 4 completing successfully (Bug-9).

### 18:26 — cleanup via MCP

- `explorer.action=cleanup` stopped 1 deploy. `deploy.list` → `[]`.

### Final token accounting

- Plan phase (5 turns): 144,878 tokens (~29 K/turn — workspace reads + reasoning).
- Check phase (6 turns): +223,952 tokens → 368,830 total.
- **~370 K tokens consumed for 1 cycle of 5 tasks on a hot workspace**.

---

## Bugs

### BUG-1 (high): `max_tasks`/`max_cycles` runtime updates are no-ops

- What: `explorer.config set max_tasks=2` updates `e.config.MaxTasks` but does not rebuild the planner. The `ExplorerAgentPlanner` captured the value via `WithAgentMaxTasks(...)` at startup (`explorer.go:273-289`). `UpdateConfig` does not call `setupPlannerLocked`.
- Result: planner still uses default 5, plan comes back with 5 tasks despite cap=2.
- Fix: rebuild the planner on relevant config keys, or pass a getter closure instead of a captured int.

### BUG-2 (medium): runaway INFO spam in hot path

- `INFO resolve: merged local engine overlay overlay_assets=4` logs once per resolve call. During `buildPlanInput` the explorer resolves many combos and emits 50+ identical lines in < 2 s.
- `INFO model not in catalog, using auto-detected config model=<X>` repeats 3-4× for the same model in the same cycle — redundant resolves per combo-adjacent lookup.
- Drowns signal; move to Debug.

### BUG-3 (medium): benchmark cell progress not in slog

- During a validate task the benchmark ran 22 cells (~53 min) with no intermediate slog entries between `running benchmark matrix` and the next task's `executing task` line. Cells are only visible via `benchmark.list` MCP.
- During any active task, operators cannot tell from logs alone whether work is progressing or stuck.

### BUG-4 (low): `rounds_used` is a settable config key

- MCP schema exposes `rounds_used` as settable (`tools_automation.go:242`). Setting it trivially resets the budget counter — conflates a persistent counter with a user-editable setting.

### BUG-5 (low): benchmark profile cell overlap is not deduped

- Default `latency` profile (`conc=1, in=[128,512,1024,2048,4096], out=[256,1024]`) overlaps `throughput` profile (`conc=[1,2,4,8], in=[512,2048,4096], out=[1024]`) on `conc=1, in∈{512,2048,4096}, out=1024`. Those 3 cells were measured twice — ~8 min wasted per validate task.

### BUG-6 (medium): tuning result records planner-requested gmu, not deploy-applied gmu

- `search_space.gpu_memory_utilization=[0.7, 0.75, 0.8, 0.85, 0.9]`. For `0.9`, the resolver's safety cap silently reduced it to `0.86` in the actual docker command (`resolver.go:1434-1442` — `maxSafeGMU = (free − 512)/total`, `math.Floor(*100)/100`).
- `experiments/009-*.md` records `gpu_memory_utilization=0.9` as tested — **the record is wrong**.
- Impact: subsequent LLM reasoning will believe 0.9 was measured at 32.58 TPS; it actually measured 0.86 at 32.58 TPS. If the model/hardware allowed 0.9 under different conditions (e.g., more free memory), the LLM would have no way to know 0.9 was never really tried.
- Fix: tuner should read the actual applied config from the deploy response and use that as the row key, not the requested value.

### BUG-7 (medium): zero-counter benchmark record is persisted after tune-best-config redeploy

- Harvester's own note on task 4: `Benchmark b6400d4fec32e009 reported zero requests, zero rounds, and zero resource utilization … while still logging a throughput of 33.3 tok/s`.
- After a tune selects the best config, it redeploys that config but apparently writes a benchmark_results row with zero samples (but copies the throughput/TTFT fields from the best-scoring cell).
- Downstream consumers treat "rows with throughput>0" as valid — a zero-sample record shaped like a real one is dangerous.
- Fix: don't write the post-tune redeploy as a benchmark row, or mark it with `stability='unverified'` and null throughput.

### BUG-8 (high): task handoff does not tear down the previous task's deploy

- End of task 4 (tune): a "best config" deploy is left running.
- Task 5 (tune) starts 16 s later and hits `hardware not compatible: GPU memory insufficient: only 3.2% usable` on **every one of 5 variants** because the GLM-4.6V-Flash-FP4 deploy is still holding ~104 GiB of the 120 GiB unified memory.
- All 5 Qwen2.5-Coder-3B variants failed in 4 s each; the full task wasted in 19.7 s.
- Fix: between `task finished` and `executing task N+1`, teardown the previous deploy, wait for GPU release (already have `waitForGPURelease` helper — it was called during tune loop but not at task boundary).

### BUG-9 (medium): `kind=tune` exploration task does not write `tuning_sessions` row

- Task 4 (tune) completed with a harvest note, full experiment file, best config identified, but `SELECT * FROM tuning_sessions WHERE started_at > "2026-04-16 16:40"` returns 0 rows.
- Automation's `tuning.start` path writes `tuning_sessions`; Explorer's `kind=tune` path does not. Two parallel persistence paths for the same conceptual entity.
- Fix: route explorer tune through the same session recorder, or document the split and add an `explorer_tune_sessions` abstraction.

### BUG-10 (low): sync push fires against unreachable central without clear warning

- `harvest action type=sync_push detail="incremental push"` is logged unconditionally; if the default central endpoint `aimaservice.ai/central` is unreachable (e.g., network-isolated lab), the push silently fails.
- Fix: surface push errors at Warn level with reason.

---

## Design concerns

### DC-1: planner chooses conservative `max_model_len`, which caps long-context anchor

- `adaptBenchmarkProfiles` derives the context ceiling from **task-level `max_model_len`**, not from `LocalModel.MaxContextLen`. Kimi set `max_model_len=8192` for every validate task. For Kimi's own 256 K-context reasoning, the long-context anchor in any validate under this prompt will be ≤ 4096.
- Spec 2026-04-16 §PendingWork.B says `validate_long_context` triggers when "effective context ceiling > current successful upper bound". But the ceiling is the planner's choice, not the model's — so the LLM can silently opt out of long-context exploration by choosing a small `max_model_len`.
- Fix: either planner prompt forces `max_model_len` close to `model.MaxContextLen`, or enrichment ignores task `max_model_len` in favor of the LocalModel's declared context window.

### DC-2: `search_space` with single-element arrays is ambiguous

- Tune tasks use `search_space: {gmu: [0.7,...,0.9], max_model_len: [8192]}` per planner prompt ("固定参数写成 search_space 里的单元素数组"). Tuner iterates a 1-dim degenerate dimension.
- Reader cannot tell "fixed" vs "searched" without counting array lengths.
- Keep `engine_params` for fixed + `search_space` for real candidates; planner prompt already supports this — reinforce.

### DC-3: task's benchmark spec usually left empty; enrichment carries all weight

- All 3 validate tasks had `benchmark: {...empty...}`. Enrichment produced 22-cell matrices (latency + throughput).
- Planner loses the ability to target a specific load point. If planner should target load, solicit benchmark shape explicitly or declare `force_default: true` vs. `force_matrix: {...}` in the schema.

### DC-4: model-not-in-catalog fallback is silent

- 8/30 local models take the "auto-detected" path (no YAML). Their `MaxContextLen` etc. come from directory probe.
- This plays into DC-1: without declared `MaxContextLen`, the long-context anchor derivation degrades.
- Add YAMLs AND flag known-unknown metadata in `device-profile.md` so the planner sees it.

### DC-5: LLM burns 370 K tokens per cycle

- Plan (145 K) + Check (224 K) = 370 K tokens for one 5-task cycle on a hot workspace.
- Default `max_tokens_per_day=0` (unlimited). An unattended instance on a large workspace can blow through quota quickly at reasoning-model pricing.
- Add a sane default cap; add quick-plan fallback when budget tightens.

### DC-6: `configurations` table semantics is "benchmark cell anchor", not "deploy configuration"

- Every benchmark cell creates a `configurations` row with `source="benchmark"` and `config = {concurrency, input_tokens, max_tokens}` — cell parameters, not engine/deploy parameters.
- The `configurations` table is being used as a cell-anchor more than as a deploy-config repository. Actual deploy config is only stored inside each experiment file's YAML `deploy_config` field.
- Either rename the table, or add a separate `deploy_configurations` abstraction.

### DC-7: validate task triggers BOTH `latency` and `throughput` default profiles (~24 cells)

- `defaultBenchmarkProfiles` for VRAM ≥ 40000 MiB returns two profiles. For a validate task with empty benchmark, both run.
- Spec §4.3 only discussed "don't explode a single profile into a full ladder"; the two-profile explosion doubles the work per validate — ~20–25 min/task.
- Consider: one profile for `validate_baseline`, both profiles only when `validate_long_context` or `validate_stress` is explicitly requested.

### DC-8: three frontier counters are inconsistent

- `plan input gaps=44` (knowledge-level gaps)
- `explorer.status` shows `Ready Combos: 148` (via prior workspace)
- `plan.md` fact snapshot says `Ready Combos: 17`
- All three are "valid" from their own abstraction level, but logs don't explain the relationship. An operator reading only logs sees inconsistency.

### DC-9: LLM cannot see system state; environmental failures are misdiagnosed

- Task 5 failure was caused by task 4's best-config deploy still holding GPU memory. LLM could not observe `docker ps` or free-memory metrics, so it wrote "likely transient port conflict / OOM during parallel deployments / startup flakiness". That is a reasonable guess that missed the real cause.
- This is a fundamental observability gap: the Explorer agent reasons on artifacts (experiment files, DB rows) but cannot see live process state.
- Fix: expose a "system snapshot" tool to the planner (deploy.list + docker ps + GPU free mem) so it can reason about handoff effects.

---

## Agent behaviour notes

- **PendingWork semantics are faithfully honored.** Every task's `reason` cited "Pending Work validate_baseline" or "Pending Work tune" and matched the spec's intent (validate for combos without baseline; tune for combos with baseline + tunable params).
- **`search_space` is correctly produced.** Tune tasks carried true multi-value search spaces (5 gmu values). The Tuner consumed them and iterated.
- **Denylist adherence was perfect.** None of the 5 tasks touched known-blocked combos.
- **Family caps were self-imposed.** The LLM added its own `GLM ≤ 2, Qwen ≤ 2, MiniCPM ≤ 1` limit that is **not in any spec**, indicating reasonable self-diversification.
- **Environmental-vs-structural classification worked.** Pre-flight compat failures (tasks 2, 3) were correctly added to Confirmed Blockers / Do Not Retry. Qwen2.5-Coder-3B tune failure (task 5) was correctly classified as environmental and left retriable.
- **Conservative `max_model_len=8192` across all tasks** is an anti-pattern (DC-1). The planner inherited this convention from prior plans and did not probe higher context ceilings.
- **Comprehensive check-phase synthesis.** The LLM filled all 5 Agent Notes blocks, updated the summary and cross-engine table, promoted the new champion (GLM-4.1V-9B-Thinking-FP4 @ 165 TPS) — all without external prompting.

---

## Knowledge quality assessment

- **Experiment files**: 5 new files (`006–010`). Successful tasks produce 17 KB–6 KB files with full per-cell YAML + markdown matrix. Failed tasks produce 1-6 KB files with complete error message. All are human-readable and LLM-ingestable.
- **`benchmark_results` rows**: 22 new rows with all percentile stats (TTFT p50/p95/p99, TPOT p50/p95, throughput, stability, error rate). Low-noise, reliable.
- **`configurations` rows**: 22 new rows (1 per cell). Semantics is "cell anchor", not "deploy config" — see DC-6.
- **`knowledge_notes` rows**: 1 row for task 1 (benchmark summary) + 1 row for task 4 (the "zero-sample record" note). No note for failed tasks 2/3/5 — harvester only saves notes for non-empty successful runs.
- **`exploration_plans` row**: 1 row, plan JSON preserved, `progress=5/5 completed_at=...`.
- **`tuning_sessions`**: **empty** (Bug-9).
- **`summary.md`**: high-quality cross-engine synthesis, ranks by `TPS/GiB` (20.05 vs 5.04 vs 4.04), 12 confirmed blockers and 11 Do-Not-Retry entries with YAML family classifications. Actively useful for next cycle's planning.
- **`knowledge-base.md` Pending Work**: cleared to `_No pending work_` — spec closure signal.

**Verdict**: knowledge output is of the shape and quality the spec aims for. The primary quality risk is Bug-6 (falsified tuning record) and DC-1 (capped long-context coverage), both of which can mislead future cycles.

---

## Final verdict & residual risks

**Spec validation — PASS**

- Explorer Coverage/Tuner spec (2026-04-16): both `PendingWork` and `search_space` are end-to-end wired. LLM uses them. Workspace reflects them. DB stores them (except the `tuning_sessions` gap in Bug-9). The "frontier semantics = combo's pending work complete" model works in practice.

**End-to-end execution — PASS**

- On a realistic workspace (30 models / 6 engines / 148 Ready combos) the cycle completed in ~1 h 50 min with 5 tasks (1 full validate, 2 fast structural-fail, 1 full tune, 1 environmental-fail tune) for ~370 K Kimi tokens.

**Knowledge quality — PASS (with caveats)**

- Experiment files, summary, blockers, and cross-engine comparison are all high-quality and re-ingestable.
- Bug-6 (tuning records don't reflect deploy-applied params) and DC-1 (planner caps context ceiling) are the two defects that most erode knowledge fidelity.

**Residual risks**

1. Bug-6 + DC-1 together: future cycles may think some configs were tested when they weren't, and never probe the long context that hardware allows.
2. Bug-8: any cycle with back-to-back tune tasks on large models will cascade-fail — the problem compounds the bigger the model.
3. DC-5: a 370 K-token cycle at reasoning-model prices is non-trivial; `max_tokens_per_day=0` default is unsafe for multi-cycle scheduling.
4. Bug-9 + DC-6: divergent persistence paths make cross-subsystem queries (e.g., "all tune history") unreliable.

**Recommended next steps (in order of blast-radius impact)**

1. Fix Bug-8 (task-boundary teardown) — production-critical: without it, any multi-tune plan fails.
2. Fix Bug-6 (record deploy-applied params) — corrupts knowledge reliability.
3. Fix Bug-9 (route explorer tune through tuning_sessions) — unifies the persistence model.
4. Address DC-1 — make long-context exploration actually happen on high-context models.
5. Address DC-5 — add a sane default token cap.
6. Bugs-1,2,3,4,5,7,10 — cleanup / polish.

## Bugs

### BUG-1 (high): `max_tasks`/`max_cycles` runtime updates are no-ops

- What: `explorer.config set max_tasks=2` updates `e.config.MaxTasks` but does not rebuild the planner. The `ExplorerAgentPlanner` captured the value via `WithAgentMaxTasks(...)` at startup (`explorer.go:273-289`). `UpdateConfig` does not call `setupPlannerLocked`. Result: planner still uses the default 5, plan comes back with 5 tasks despite cap=2.
- Impact: silent cap violation. Any budget or scope tightening set via `explorer.config` after startup is ignored for the current planner lifetime.
- Fix: either rebuild the planner on relevant config keys, or inject a getter closure (`func() int`) instead of a captured int for `maxTasks` / `maxCycles`.

### BUG-2 (medium): runaway INFO spam in hot path

- What: `INFO resolve: merged local engine overlay overlay_assets=4` logged once per resolve call. During `buildPlanInput` the explorer resolves many combos and emits ~50+ identical lines in < 2 s.
- Also: `INFO model not in catalog, using auto-detected config model=<X>` repeats 3-4× for the same model in the same cycle — resolve is called redundantly per combo-adjacent lookup.
- Impact: drowns real signal; makes log grep noisy.
- Fix: lower these to Debug, or emit once-per-cycle at Info.

### BUG-3 (medium): benchmark cell progress not in slog

- What: during a validate task the benchmark ran and completed cells (evidenced by `benchmark.list`), but `aima.serve.log` had **no** new lines between `running benchmark matrix` and the next task's deploy. No cell-start/cell-done or interim logs.
- Impact: during the 15-20 min that a 10-cell matrix takes, operators have no way to tell if it is progressing or stuck short of tailing benchmarks via MCP.

### BUG-4 (low): `rounds_used` is a settable config key

- What: `rounds_used` is exposed as an MCP settable config key (enum in `tools_automation.go:242`). Setting it directly resets the budget counter. It conflates a persistent counter with a user-editable setting.
- Impact: not dangerous, but a user can silently erase budget exhaustion state.

---

## Design concerns

### DC-1: planner chooses conservative `max_model_len` per task, which caps long-context anchor

- `adaptBenchmarkProfiles` derives the context ceiling from **task-level `max_model_len`**, not from `LocalModel.MaxContextLen`. The Kimi planner set `max_model_len=8192` for every validate task, regardless of the target model's real capacity. For an 128K-context-capable model, the long-context anchor under this prompt will always be ≤ 4096.
- Spec 2026-04-16 §PendingWork.B says `validate_long_context` triggers when "effective context ceiling > current successful upper bound". But the ceiling is the planner's choice, not the model's — so the LLM can silently opt out of long-context exploration by choosing a small `max_model_len`.
- Either the planner prompt must require picking `max_model_len` close to `model.MaxContextLen`, or enrichment should ignore task `max_model_len` and use the LocalModel's declared context window when probing the anchor.

### DC-2: `search_space` with single-element arrays is ambiguous

- Plan has tune tasks with `search_space: {gmu: [0.7,...,0.9], max_model_len: [8192]}`. Prompt told LLM to write fixed params as single-element arrays inside `search_space`. Tuner will iterate a 1-dim degenerate dimension. Works correctly, but makes the plan harder to read: reader cannot distinguish "fixed" vs "searched" without counting array lengths.
- Consider splitting: keep `engine_params` for fixed values and `search_space` for real candidates; planner prompt already supports this — reinforce it.

### DC-3: task's benchmark spec usually left empty; enrichment carries all weight

- All 3 validate tasks have `benchmark: {concurrency: [], input_tokens: [], max_tokens: [], requests_per_combo: 0}`. Planner trusts enrichment entirely. This is fine for coverage but loses the planner's ability to target a specific load point (e.g. "only concurrency=4 because last cycle was single-concurrency").
- If planner should be able to target load, the prompt should explicitly solicit benchmark shape, or the task should be `force_default: true` vs. `force_matrix: {...}` rather than degrade silently.

### DC-4: model-not-in-catalog fallback is silent

- 8 of 30 local models (GLM-4.7-Flash-NVFP4, GLM-5-NVFP4, MiniMax-M2.5, Qwen2.5-Omni-7B, Qwen3.5-122B-A10B-FP8, Step-3.5-Flash-FP8, FLUX.2-dev, MiniCPM-o-4_5) take the "auto-detected" path. Their `MaxContextLen`, `Format` details, `Tunable/Arch` info come from directory probe, not from curated YAML. This plays into DC-1: without declared `MaxContextLen`, the long-context anchor derivation degrades.
- The catalog gap is a content problem (add YAMLs) but also a mechanism problem (system should flag known-unknown metadata in `device-profile.md` so the planner sees it).

### DC-5: LLM burns 145K tokens in plan phase alone

- Single plan phase = 145K tokens. At Kimi coding pricing (≈ $7-15 / M output tokens for reasoning models), that is real cost. Explorer default `max_tokens_per_day=0` (unlimited) means an unattended instance can blow through quota on large workspaces.
- Either a sane default cap, or a quick-plan fallback when token budget tightens, would be prudent.

---

## Agent behaviour notes

- Plan quality is good: all 5 tasks reference concrete prior evidence (experiment 002/003 baselines, specific confirmed blockers avoided, ~GB sizes cited). PendingWork semantics are honored — LLM explicitly cites "Pending Work validate_baseline" / "Pending Work tune" in `reason`.
- Task selection correctly **mixes** Ready-untouched (validate_baseline) and Ready-with-baseline-but-no-tune (tune). Spec behaviour confirmed.
- `max_model_len=8192` hardcoded across all tasks is a conservatism anti-pattern (see DC-1).
- LLM correctly avoided the denylist: no gemma-4, no Qwen3.5-27B, no Qwen3-Coder-Next-FP8, no sglang, no broken llamacpp.

---

## Knowledge quality assessment (in progress)

(to be filled after cycle completes)

---

## Final verdict & residual risks

(pending)
