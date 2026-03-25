# Changelog

All notable changes to AIMA are documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/). Versioning follows [SemVer](https://semver.org/).

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

[v0.2.0]: https://github.com/Approaching-AI/AIMA/compare/v0.0.1...v0.2.0
[v0.0.1]: https://github.com/Approaching-AI/AIMA/releases/tag/v0.0.1
