# Changelog

All notable changes to AIMA are documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/). Versioning follows [SemVer](https://semver.org/).

## [v0.4.0] - 2026-04-21 — "Knowledge Autonomy"

176 commits since v0.3.3. v0.4 closes the Edge↔Central automation loop first sketched in `docs/superpowers/specs/2026-04-07-v0.4-knowledge-automation-design.md`: Explorer now autonomously discovers work, executes benchmark/tune tasks, harvests knowledge, and syncs upstream; Central generates advisories and scenarios with a stable lifecycle; the edge device has a unified cloud identity via `aima-service`.

v0.3.4 (Explorer Agent Planner, dated 2026-04-09 in prior CHANGELOG but never tagged) is folded into this release.

### Added

- **v0.4 Knowledge Autonomy core** — Explorer orchestrator with tier detection, Scheduler (gap-scan/sync/audit timer loops with quiet hours), in-process EventBus (pub/sub for `deploy.completed`, `patrol.alert.oom/idle`, `model.discovered`, `central.advisory`, `central.scenario`), Planner interface (`RulePlanner` for Tier 1, replaced by `ExplorerAgentPlanner` for Tier 2), Harvester (template + LLM modes, `maybeAutoPromote` with zero-throughput guard), `exploration_plans` SQLite table (migrateV12).
- **Explorer Agent Planner** — document-driven PDCA agent workflow (`ExplorerAgentPlanner`) replaces the single-shot JSON LLM planner. LLM operates as a research agent reading/writing documents in `~/.aima/explorer/` workspace via 7 bash-like tools (cat/ls/write/append/grep/query/done), with three-phase Chinese system prompts (Plan/Check/Act). `ExplorerWorkspace` manages fact documents (`device-profile.md`, `available-combos.md`, `knowledge-base.md`), analysis documents (`plan.md`, `summary.md`), and experiment results (`experiments/*.md`) with read-only guards and path safety. `query` tool wired to SQLite knowledge store (search/compare/gaps/aggregate) enables LLM to pull historical benchmark data during planning. `AnalyzablePlanner` extends `Planner` with `Analyze()` for PDCA Check+Act phases.
- **Explorer evidence contract (2026-04-16 design)** — `PendingWork` derivation from durable local facts (`configurations` + `benchmark_results` + `exploration_runs`) keeps completed combos in Ready frontier when baseline/long-context/tune debt remains; `TaskSpec.search_space` contract gives `kind=tune` real parameter-search semantics; adaptive benchmark profile enrichment adds a long-context anchor when `MaxContextLen` allows without exploding the matrix; RulePlanner consumes PendingWork alongside gaps/advisories. Structured decision-trace logging (`tier`, `tasks`, `task_list`, `llm_tokens`, `proposed_tasks`, `dedup_dropped`, `ready_combos_seen`, `blocked_combos_seen`, `knowledge_gaps`) on every `explorer: plan generated` event.
- **Explorer Web UI MVP** — embedded Explorer screens now expose live status without shell access: Overview tab (tier, planner state, DB delta, manual trigger), Runs tab with run-detail drawer, and read-only Explorer inspector MCP tools that back the UI. The end-to-end five-step closure was live-smoked on `gb10-4T` before release.
- **Central Advisor Engine + Analyzer** — `CentralStore` interface decouples storage from logic with two implementations (`store_sqlite.go`, `store_postgres.go` using `pgx/v5`, nil-CGO). `Advisor` wraps an `LLMCompleter` (OpenAI-compatible, custom headers via `WithOpenAIHeaders`/`CENTRAL_LLM_HEADERS`) to power `Recommend` / `OptimizeScenario` / `GenerateScenario`. `Analyzer` owns scheduled gap scans, pattern discovery, scenario health checks, and post-ingest delayed analysis with real `analysis_runs` lifecycle transitions. Prompt templates in `advisor_prompts.go`. New API endpoints `POST /api/v1/advise`, `GET /api/v1/advisories`, `POST /api/v1/advisory/feedback`, `POST /api/v1/scenario/generate`, `GET /api/v1/scenarios`, `GET /api/v1/analysis`. (See note in Changed: Central implementation has moved to a separate repo.)
- **Advisory lifecycle** — `pending` → `delivered` (on `sync pull`) → `validated` / `rejected` (on edge feedback) → `expired` (30d untouched). Advisory/scenario pulls are hardware-aware and lifecycle-filtered.
- **Sync v2 protocol** — `knowledge.sync_pull` now returns advisories + scenarios alongside configurations/benchmarks/notes; `knowledge.sync_push` carries advisory feedback; CLI `aima knowledge advise` and `aima scenario generate/list --source central` exposed. End-to-end integration test covering advisory creation → delivery → validation → feedback.
- **aima-service Device Registry Integration (Phase 1)** — edge devices obtain a unified cloud identity (`device_id` + `token` + `recovery_code`) from the `aima-service` device-registry on first boot, flowing through every Central call. Registration is a non-blocking goroutine with exponential backoff so offline edges keep serving local traffic. `internal/cloud/device.go` exposes the canonical `device.*` config keys plus `RequireRegistered` / `ReadIdentity`; `internal/support/bootstrap.go` provides `Service.Bootstrap`, `StartRegistrationWorker`, `RenewToken`, `ResetIdentity`; `internal/support/state.go::mirrorCanonical` mirrors identity into `device.*` keys so the rest of AIMA reads one source of truth. `--invite-code` persistent flag; `AIMA_INVITE_CODE` env takes priority. `aima device register/status/renew/reset` CLI + 4 matching MCP tools. All 10 outbound Central closures gate on `cloud.RequireRegistered`; URLs carry `?device_id=`.
- **Onboarding Cold-Start Wizard** — single guided flow for new users: hardware detection → scan existing assets → recommend top models/engines → deploy with progress. `internal/onboarding` package owns business logic; `aima onboarding` CLI wraps the `onboarding` MCP tool (5 actions). Real-time SSE streaming with heartbeat; security + i18n + INV-8 (offline-first) hardening; agent destructive-action gating; reuse trail for previously-scanned assets. GPU occupancy panel with Stop buttons for non-AIMA containers; clickable phase dots for back-nav; scan-complete summary with Back/Continue; actionable engine-blocked / GPU-busy error copy; "Skip for now" button on the support page.
- **Onboarding Recommend Scoring v2** — 5-dimension 0-100 scoring (modality, local readiness, VRAM fit, largest-fittable, recency) with bandwidth+RAM aware paths for MoE vs dense inference on unified-memory SoCs (GB10) vs high-bandwidth discrete GPUs (RTX 4090). `released_at` catalog metadata fuels the recency bonus. Recommend scoring spec lives in `design/onboarding-recommend-scoring-v2.md`.
- **Multi-modal Benchmark System** — `Requester` interface + `Sample` struct generalize benchmark adapters beyond chat. Adapters for TTS, ASR, T2I (text-to-image), T2V (text-to-video); `matrix` MCP dispatches the right requester per modality. V14 SQLite migration adds multi-modal benchmark columns. Smoke-test servers for T2I/T2V. `RunResult` carries modality-specific fields. Design spec: `design/smart-synthetic-deploy.md` and `docs/superpowers/specs/2026-04-13-multimodal-benchmark-design.md`.
- **Model `metadata.aliases` in catalog YAML** — `ModelAsset` honors a `metadata.aliases` list so scan-name → canonical-name matching is catalog-driven, not hardcoded. `qwen3-emb-0.6b.yaml` gains `Qwen3-Embedding-0.6B` / `qwen3-embedding-0.6b`; `qwen3-8b.yaml` gains `Qwen3-8B-junhowie` / `gptq-Qwen3-8B-junhowie`. Adding a new alias is a YAML-only change (honors INV-1/2).
- **MCP profile-aware tool filtering** — `ListToolsForProfile` on `Server`; `WithProfile` + `ProfiledToolExecutor` on `Agent`; system prompt rewritten to cover all 39 ProfileOperator tools, reducing token overhead per agent call.
- **Catalog expansion** — FLUX.2-dev full-stack deploy with TeaCache + ROCm engine split; LTX-2.3 22B for AMD RDNA3; xDiT Wan2.2 engine/model hotfix.
- **Central production deployment** — default Central endpoint is now `https://aimaservice.ai/central`; deployment integration design (`design/superpowers/plans/2026-04-13-central-deployment-integration.md`) documents the gateway-proxied topology (Rust gateway `/central/*` → `http://central:8081`), PostgreSQL `aima_central` database, and upgrade workflow from the production server.

### Changed

- **MCP tool consolidation 101 → 61** — merged redundant tools into action-param unified tools, removed 45+ legacy and unused tools. Current surface (61): 8 deploy · 6 model · 6 engine · 6 knowledge · 4 benchmark · 4 automation (patrol/tuning/explore/explorer) · 4 agent (support/ask/status/rollback) · 4 device (register/status/renew/reset) · 3 catalog · 3 central · 2 fleet · 2 hardware · 2 data · 2 scenario · 2 system · 1 onboarding · 1 stack · 1 openclaw. App register/provision/list and power mode/history were removed in consolidation (unused by external consumers; power is subsumed by `hardware.metrics` + `GET /api/v1/power`). Test naming and tooldeps wiring updated.
- **Central Knowledge Server strict mode** — every scoped endpoint now requires a `device_id` query parameter, returning 400 when missing; `/healthz` and `/api/v1/stats` remain exempt. Edge is expected to have completed aima-service registration before issuing any Central request.
- **Central Knowledge Server moved to a separate repo** — server code and deployment live in `github.com/Approaching-AI/aima-central-knowledge`. Do not add `internal/central/` or `cmd/central/` to this repo; Edge↔Central communication is HTTP REST only, payloads are built with `map[string]any` in `cmd/aima/tooldeps_integration.go`, and the API contract is owned by `aima-central-knowledge/api/openapi.yaml`.
- **Onboarding engine selection hardened (#36)** — `FormatToEngine` prefers general-purpose LLM engines over specialized ones (safetensors → vllm instead of mooer-asr); `InferEngineType` enforces format compatibility; blocked engines (status: `blocked`) fail fast instead of hanging a 15-minute docker pull. Three `vllm-nightly-*` assets marked blocked where the referenced image `qwen3_5-cu130` does not exist.
- **Knowledge resolver scan-name resolution (#39)** — `resolveCatalogModelName` matches scan inputs against catalog via the new `Aliases` field; hardcoded `gptq-` prefix strip retired. Synthetic fallback no longer auto-selects the ASR-only `mooer` engine for `safetensors` llm/embedding models; redundant guard checks in `BuildSyntheticModelAsset` collapsed into a single `substituteDisallowedMooer` helper.
- **Explorer frontier grounded in executable facts** — `available-combos.md` is regenerated from live data; structural blockers, fail-count blockers, and no-pending-work completed combos drive dedup instead of "completed means done". Engine-wide blocker propagation prevents the planner from proposing tasks onto an engine scope that has been confirmed broken (GB10 sm_121 × sglang, vllm-standard on GLM models, etc.).
- **CLI `--remote` / `--api-key` hoisted** to root persistent flags so all subcommands can drive a remote `aima serve` consistently.

### Fixed

- **MCP-initiated tune detached from HTTP request context** — tuning runs initiated via MCP no longer get cancelled when the triggering HTTP request ends; a dedicated background context with its own lifecycle manages long-running tune runs.
- **Engine health_check timeout honored + vllm-nightly warmup wired** — deploys no longer block indefinitely on engines that never become ready; warmup/readiness is first-class for native runtime and now also covers docker runtime for vllm-nightly.
- **Unblock engines whose image is cached locally** — `status: blocked` is automatically bypassed when `docker images` shows the referenced tag is already on disk, unblocking fast-path deploys on dev labs.
- **Edge HTTP timeout extended to 600s for LLM reasoning** — Kimi and other reasoning-capable LLM endpoints routinely exceed the prior 30s/120s edge sync timeout; 600s is now the ceiling for LLM-bound outbound calls.
- **mDNS log filter flipped to allow-list** — deny-list model was leaking noise from Docker veth/br-* and unrelated interfaces; allow-list now matches only known-good interface patterns.
- **`aima init` systemd unit ExecStart path (#38)** — the stable installer was writing a user-dir binary path (e.g. `/home/qujing/aima`) into `aima-serve.service`, causing `203/EXEC` startup failure even after docker/k3s installed successfully. Bare command names are now resolved via `exec.LookPath`; absolute paths pass through unchanged.
- **Tuner aligned with deploy.run contract** — tuning no longer skews deploy config with benchmark-profile fields; `configurations.config` reflects real deploy config.
- **Warmup readiness + served model label normalization** — deploy.apply waits for an actual ready endpoint before returning; benchmark uses `deploy.status` to resolve the real endpoint address instead of defaulting to `localhost:6188`.
- **Partial-matrix narrative + overlay profile spurious warnings** — matrix harvest documents zero-cell and partial-cell outcomes without flooding logs with overlay-profile false positives.
- **Explorer evidence/frontier/blocker coverage** — frontier coverage grounded in durable local facts; blocker learning uses exploration_runs history not just in-memory counters; retracted three design-philosophy violations from the explorer cleanup run (cross-boundary writes in the dedup path).
- **Explorer E2E closure (seven rounds from 2026-03-XX through 2026-04-17)** — fixes cover: OpenAI User-Agent (Kimi 403 `access_terminated_error`), code-fence stripping in Advisor/Analyzer response parsing, `/v1` double-prefix URL bug, advisor `[]json.RawMessage` → yaml.Marshal as `[]any`, `compare` plan kind removed from LLMPlanner prompt, auto-promote zero-throughput guard, plan input filtering (local-only models/engines/gaps, max caps for gaps/open_questions/history), `ensureDeployed` + `waitForReady` + `resolveDeployEndpoint` + deploy-status precheck for "already running", sglang-kt engine-specific parameter substitution (no `--gpu-memory-utilization` leak), synthetic fallback `"no variant of model"` trigger + variant merge, failure notes not persisted to DB (keep central sync clean), hardware-based benchmark profile defaults, matrix-aware harvest notes with per-cell throughput/TTFT summary, synchronous event loop (fixes budget race/redundant planning/stale input), plan_id propagation for traceability, harvester template vs LLM split for validate vs tune, promotion quality gate blocking auto-promote on zero benchmark metadata, N1+prefill dedup feeding explored combos to LLM prompt.
- **Central-edge automation loop** — column-order bug in SQLite inserts, context leak in advisor LLM calls, advisory EventBus bridge ordering, and deploy strategy hardening across the sync v2 path.

### Removed

- **LLMPlanner** — replaced by `ExplorerAgentPlanner`; `explorer_llmplanner.go` and its test deleted.
- **45+ legacy MCP tools** — replaced by action-param unified tools during the 101→61 consolidation.
- **Hardcoded `gptq-` prefix strip** — superseded by catalog `metadata.aliases`.
- **`buildOnboardingDeps` decorator** — onboarding moved to `internal/onboarding` with direct DI.

### Infrastructure

- 61 MCP tools, 3 runtimes (K3S/Docker/Native), 11 hardware profiles, 32 engine YAMLs, 28 model YAMLs, 3 deployment scenarios, 3 partition strategies, 5 stack components.
- Central Knowledge Server: separate repo, PostgreSQL `aima_central` in production behind Rust gateway at `https://aimaservice.ai/central`.
- Edge ↔ aima-service: `device_id + token + recovery_code` identity; canonical `device.*` config keys; `RequireRegistered` gate on all outbound Central calls.
- New SQLite migrations: V12 (`exploration_plans`), V14 (multi-modal benchmark columns).

### Notes for operators

- Documented env-var matrix (edge + central) lives in `design/superpowers/specs/2026-04-07-v0.4-knowledge-automation-design.md` §7.4. `AIMA_EXPLORER_*` env vars drive Explorer scheduling; `CENTRAL_*` env vars configure the standalone Central service (now in `aima-central-knowledge`).
- The v0.3.4 CHANGELOG entry (Explorer Agent Planner, 2026-04-09) was never tagged; its content is consolidated into v0.4.0 above.

### Known issues

- **U10 onboarding smoke coverage** — `test-win`, `aibook`, `m1000`, and `metax-n260` were not refreshed in this release window; this is a device-coverage gap, not a reproduced functional failure.
- **U11 multi-modal benchmark evidence** — the final evidence chain still needs reachable `w7900d` and `aibook` hosts to complete the remaining coverage.
- **U13 smoke matrix coverage** — current-head smoke still lacks `test-win` refresh, while `aibook`, `m1000`, and `metax-n260` remain unreachable.
- **U14 Central cross-repo evidence split** — the Central SQLite/Postgres contract is closed in production, but the final regression proof lives in `aima-central-knowledge`, not in this repo.
- **U16 overlay hardware identity granularity** — overlay YAML identity matching remains intentionally coarse in v0.4; the follow-up spec is deferred to v0.5.

## [v0.3.3] - 2026-04-09

### Fixed

- **Support service default endpoint** — changed the built-in support base URL from `https://aimaserver.com/platform` to `https://aimaserver.com`, restoring default `aima askforhelp` and `support.askforhelp` connectivity to the live `/api/v1` support API
- **Support docs** — updated CLI and MCP documentation to describe the corrected default support endpoint and `support.endpoint` override behavior

## [v0.3.0] - 2026-04-03 — "Edge Intelligence"

94 commits, 333 files changed, 45,468 insertions, 15,350 deletions since v0.2.0.

### Added

- **OpenClaw Full-Stack Integration** — stdio MCP control plane for bidirectional agent-to-AIMA communication, plugins managed as synced assets with drift auto-fix, local speech providers on AIBook, TTS voice cloning end-to-end pipeline, ASR auth provider, image model agent defaults, and YAML-driven request rewriter pipeline replacing hardcoded patches
- **Smart Agent System** — auto-detect tool mode with graceful fallback to context-only chat when LLM lacks tool support, proxy API key sync to LLM client for local endpoint auth, model ranking for optimal selection
- **Smart Synthetic Deploy** — VRAM estimation for unknown models without catalog entries, synthetic config refresh on redeploy, TP (tensor parallel) VRAM honoring for multi-GPU splits
- **Engine Profile System** — YAML deduplication via shared profile inheritance, catalog integrity validation (`aima catalog validate`), overlay staleness tracking with automatic profile-based rebuild
- **MCP Profile Tool Filtering** — reduce agent token overhead by exposing only relevant tool subsets based on device hardware profile
- **SGLang-KT Engine** — KTransformers v0.5.2 integration with GPTQ_INT4 quantization variants, benchmarked at 8.53 tok/s on RTX 4060 (+31% over baseline), WSL variant hardening
- **RDNA3 Full Support** — AMD Radeon Pro W7900D 8-GPU server validated end-to-end, vLLM RDNA3 engine YAML, W7900D hardware profile, Qwen3.5-122B-A10B validated at 13.2 tok/s via vLLM 0.18.1
- **Per-Card GPU Metrics** — individual GPU utilization, temperature, and memory in HAL detect and Web UI with collapsible card panels, multi-socket CPU topology fix
- **Web UI Enhancements** — onboarding drawer for new users, engine/model download progress display, Settings modal redesigned with 4-tab structure, `/cli` page executes real Cobra CLI commands, AIMA logo in topbar and agent avatar, Support first-level page with auto-open browser, hover-deploy for local-only model startup, `/cli` hint tooltip
- **`aima run` Command** — single command to launch inference with engine download progress tracking and automatic image/binary pull when missing
- **Windows GPU Deploy** — native `schtasks` scheduling for GPU workloads on Windows without Docker or WSL
- **Native Engine Scanner** — auto-discover pre-installed inference engines and ONNX/MNN model formats on disk, aligned with design principles for knowledge-driven detection
- **AIBook M1000 Knowledge** — full benchmark data for Moore Threads M1000 SoC, native engine support for pre-installed MUSA vLLM, work_dir support for native engine startup
- **Cross-Platform Packaging** — app icons for macOS (icns), Linux (hicolor), and Windows (ico/rc), desktop integration files for all three platforms
- **Catalog Expansion** — Wan2.2-T2V-A14B text-to-video model with Ulysses variants, Gemma 4 model entry, Z-Image server with full hyperparameter support, Chinese voice reference configs for TTS engines, FunASR ONNX engine, GLM-ASR-Nano HuggingFace source

### Changed

- **God file refactor** — split 5 oversized files (14,231 lines) into 46 single-responsibility modules with zero public API changes: `main.go` -87%, `tools.go` -90%, `scanner.go` -86%, `native.go` -41%, `support.go` -53%
- **ZeroClaw (L3b) removal** — deleted ~3,400 lines of external binary sidecar that violated INV-5 (MCP tools = single source of truth); L3a Go Agent, patrol, and OOM self-healing fully preserved
- **Scenario system refactor** — fixed design violations in apply flow, added `scenario.show` tool, enforced startup ordering with readiness checks
- **Deployment port allocation** — refactored around startup specs with edge case coverage for port conflicts
- **OpenClaw request patches** — moved from hardcoded Go logic into catalog YAML with tightened sync migration

### Fixed

- **OpenClaw** — 6 end-to-end bugs in openclaw-multi pipeline, `plugins.allow` drift auto-fix in SyncLoop, YAML-driven `chat_provider` to prevent VLM overriding LLM provider, ASR auth provider + TTS proxy `response_format` passthrough, deployment context window propagation to OpenClaw config, managed ownership flow hardening
- **Deploy** — undeploy hardening with local agent guardrails, local model reuse and runtime readiness tightening, lifecycle status visibility fix, recent delete suppression persistence, container model preflight compatibility check
- **Runtime** — knowledge-driven delivery flow restoration, native process identity and failure detail preservation, engine and model delivery recovery, runtime planning alignment with no-pull semantics
- **Knowledge** — GPU-count-aware variant selection enforcement, engine profile overlay staleness tracking, engine asset rebuild after profile overlay changes
- **UI** — settings extras validation and patrol idle gaps, fleet device ordering stability, local fallback restoration, dashboard panel regrouping, default serve entry stabilization
- **Code quality** — 21-file audit fixing bugs and catalog hygiene, cross-reference errors in MCP tool count unified across docs.

### Infrastructure

101 MCP tools, 3 runtimes (K3S/Docker/Native), 11 hardware profiles, 27 engine YAMLs, 25 model YAMLs, 3 deployment scenarios. (Consolidated to 61 tools in v0.4.0.)

## [v0.2.0] - 2026-03-25 — "Connect the Dots"

36 commits, 108 files changed, 22468 insertions, 1047 deletions since v0.0.1.

### Added

- **Support Service Integration** — `internal/support/` standalone component with self-register, polling, task lifecycle, prompt/notify callbacks, and recovery code flow
- **askforhelp CLI** — interactive terminal UX with invite/worker/recovery code prompts, budget display (USD + task count), referral codes, and foreground wait mode
- **askforhelp MCP tool** — `support.askforhelp` wired via `ToolDeps.SupportAskForHelp`
- **Web UI redesign** — Apple-aesthetic embedded SPA with light/dark mode toggle
- **OpenClaw provider plugin** — LLM/ASR/TTS/image_gen backend integration with reverse proxy discovery
- **Embedded AIMA skills** — multimodal agent tool definitions for OpenClaw
- **Deployment scenarios** — `catalog/scenarios/` asset kind for multi-model deployment recipes (e.g. `openclaw-multi`)
- **Blackwell CUDA TTS engine** — GPU-accelerated TTS for GB10/Blackwell
- **Z-Image model + diffusers engine** — text-to-image support via diffusers backend
- **qwen3.5-9b model asset** — 9B dense model with native multimodal support
- **Hardware ID candidates** — robust device dedup using board serial, product serial, IOPlatformSerialNumber, MAC address
- **In-memory message log** — fixes lost notifications in UI polling

### Changed

- **Support endpoint** — migrated from `http://121.37.119.185/platform` to `https://aimaserver.com/platform`
- **Support wire format** — aligned with latest server API: budget USD fields, bound status, referral count, display language, hardware_id_candidates
- **Support wiring simplified** — 13-line closure in main.go replaced by single `supportSvc.AskForHelpJSON` call
- **Model path resolution** — fixed mismatch between root systemd service and regular user paths

### Fixed

- TTS format mismatch and image understanding config in OpenClaw
- Missing `http://` scheme in backend addresses for reverse proxy
- Agent pipeline: 4 bugs found during live GLM-4.7-Flash validation
- Orphaned explore runs and null-slice JSON responses
- Data races in proxy server and native runtime
- 4 data-integrity issues in knowledge sync/import/export and hardware identity
- Exact engine `metadata.name` preference when resolving variants

### Infrastructure

- 80 MCP tools (unchanged count, improved wiring)
- 3 runtimes: K3S, Docker, Native
- 9 hardware profiles, 22+ engine YAMLs, 16+ model YAMLs, 1 deployment scenario
- Supported platforms: darwin-arm64, linux-arm64, linux-amd64, windows-amd64

## [v0.0.1] - 2026-03-06

Initial tagged release. Foundation layer with hardware detection (8 GPU vendors), multi-runtime deployment, knowledge-driven config resolution, 80 MCP tools, central knowledge server, TUI dashboard, benchmark runner, and exploration runner.

[v0.4.0]: https://github.com/Approaching-AI/AIMA/compare/v0.3.3...v0.4.0
[v0.3.3]: https://github.com/Approaching-AI/AIMA/compare/v0.3.0...v0.3.3
[v0.3.0]: https://github.com/Approaching-AI/AIMA/compare/v0.2.0...v0.3.0
[v0.2.0]: https://github.com/Approaching-AI/AIMA/compare/v0.0.1...v0.2.0
[v0.0.1]: https://github.com/Approaching-AI/AIMA/releases/tag/v0.0.1
