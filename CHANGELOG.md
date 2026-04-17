# Changelog

All notable changes to AIMA are documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/). Versioning follows [SemVer](https://semver.org/).

## [Unreleased]

### Added

- **aima-service Device Registry Integration (Phase 1)** — edge devices now obtain a unified cloud identity (`device_id` + `token` + `recovery_code`) from the `aima-service` device-registry on first boot, flowing that identity through every Central Knowledge Server call. Registration runs as a non-blocking background goroutine with exponential backoff, so offline edges continue to serve local traffic while waiting for network recovery.
  - `internal/cloud/device.go` — canonical identity surface (`device.*` config keys, `RequireRegistered`, `ReadIdentity`)
  - `internal/support/bootstrap.go` — `Service.Bootstrap` first-boot entry point, `StartRegistrationWorker` for startup, `RenewToken` / `ResetIdentity` for operator control
  - `internal/support/state.go::mirrorCanonical` — saveState mirrors identity into `device.*` keys so the rest of AIMA reads one source of truth
  - `internal/cli/serve.go` — registration worker launched alongside existing support supervisor
  - `internal/cli/root.go` — `--invite-code` persistent flag; `AIMA_INVITE_CODE` env var takes priority
  - `internal/cli/device.go` + `internal/mcp/tools_device.go` — `aima device register/status/renew/reset` CLI + 4 new MCP tools (bringing the total to 60)
  - `cmd/aima/tooldeps_integration.go` — all 10 outbound Central closures gate on `cloud.RequireRegistered`; URLs carry `?device_id=` query param
- **Model `metadata.aliases` in catalog YAML** — `ModelAsset` now honors a `metadata.aliases` list so scan-name → canonical-name matching is catalog-driven, not hardcoded. `qwen3-emb-0.6b.yaml` gains `Qwen3-Embedding-0.6B` / `qwen3-embedding-0.6b`; `qwen3-8b.yaml` gains `Qwen3-8B-junhowie` / `gptq-Qwen3-8B-junhowie`. Adding a new alias is now a YAML-only change (honors INV-1/2).

### Changed

- **Central Knowledge Server strict mode** — every scoped endpoint now requires `device_id` query parameter, returning 400 when missing; `/healthz` and `/api/v1/stats` remain exempt. Edge is expected to have completed aima-service registration before issuing any Central request. See `aima-central-knowledge` commit history for the server-side implementation.
- **Onboarding engine selection hardened (#36)** — `FormatToEngine` prefers general-purpose LLM engines over specialized ones (safetensors → vllm instead of mooer-asr); `InferEngineType` enforces format compatibility; blocked engines (status: blocked) now fail fast instead of hanging a 15-minute docker pull. Three `vllm-nightly-*` assets marked blocked where the referenced image `qwen3_5-cu130` does not exist.
- **Onboarding wizard UX (#36)** — GPU occupancy panel with Stop buttons for non-AIMA containers, clickable phase dots for back-nav, scan-complete summary + Back/Continue buttons, actionable engine-blocked / GPU-busy error copy, and a "Skip for now" button on the support page.
- **Knowledge resolver scan-name resolution (#39, refactored)** — `resolveCatalogModelName` matches scan inputs (`Qwen3-Embedding-0.6B`, `gptq-Qwen3-8B-junhowie`) against catalog via the new `Aliases` field. Synthetic fallback no longer auto-selects the ASR-only `mooer` engine for `safetensors` llm/embedding models; redundant guard checks in `BuildSyntheticModelAsset` collapsed into a single `substituteDisallowedMooer` helper.

### Fixed

- **`aima init` systemd unit ExecStart path (#38)** — the stable installer was writing a user-dir binary path (e.g. `/home/qujing/aima`) into `aima-serve.service`, causing `203/EXEC` startup failure even after docker/k3s installed successfully. Bare command names are now resolved via `exec.LookPath`; absolute paths pass through unchanged.

## [v0.3.4] - 2026-04-09

### Added

- **Explorer Agent Planner** — replaced single-shot JSON LLM planner with document-driven PDCA agent workflow (`ExplorerAgentPlanner`). LLM operates as a research agent reading/writing documents in `~/.aima/explorer/` workspace via 7 bash-like tools (cat/ls/write/append/grep/query/done), with three-phase Chinese system prompts (Plan/Check/Act)
- **Explorer Workspace** — `ExplorerWorkspace` manages fact documents (device-profile.md, available-combos.md, knowledge-base.md), analysis documents (plan.md, summary.md), and experiment results (experiments/*.md) with read-only guards and path safety
- **Knowledge query tool** — `query` tool wired to SQLite knowledge store (search/compare/gaps/aggregate), enabling LLM to query historical benchmark data during planning
- **Enriched experiment results** — benchmark entries now include concurrency, input/max tokens, and latency metrics from `HarvestResult`, giving the Check phase richer data for analysis

### Changed

- **Engine discovery decoupled** — removed `installedEnginesContainResolvedAsset` gate from `WithGatherLocalEngines`; all locally installed engines are now visible to Explorer regardless of catalog match. Catalog YAML only enriches metadata (Features, TunableParams)
- **AnalyzablePlanner interface** — extends existing `Planner` interface with `Analyze()` method for PDCA Check+Act phases without breaking `RulePlanner`

### Removed

- **LLMPlanner** — deleted `explorer_llmplanner.go` and `explorer_llmplanner_test.go` (replaced by `ExplorerAgentPlanner`)
- **Dead code** — removed unused `installedEnginesContainResolvedAsset` from `engine_match.go`

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
- **Code quality** — 21-file audit fixing bugs and catalog hygiene, cross-reference errors unified (MCP tool count was 56/79/80/94 in different docs, now consistently 94)

### Infrastructure

94 MCP tools, 3 runtimes (K3S/Docker/Native), 11 hardware profiles, 27 engine YAMLs, 25 model YAMLs, 3 deployment scenarios.

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

[v0.3.0]: https://github.com/Approaching-AI/AIMA/compare/v0.2.0...v0.3.0
[v0.2.0]: https://github.com/Approaching-AI/AIMA/compare/v0.0.1...v0.2.0
[v0.0.1]: https://github.com/Approaching-AI/AIMA/releases/tag/v0.0.1
