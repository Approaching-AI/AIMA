<!-- ======== Banner ======== -->
<div align="center">
  <img src="docs/assets/banner.svg" alt="AIMA — AI Infrastructure, managed by AI" width="100%"/>
</div>

<!-- ======== Bilingual switcher ======== -->
<div align="center">
  <a href="README.md"><img src="https://img.shields.io/badge/English-5E35B1?style=for-the-badge&labelColor=1a1a2e" alt="English"></a>
  <a href="README_zh.md"><img src="https://img.shields.io/badge/中文-42A5F5?style=for-the-badge&labelColor=1a1a2e" alt="中文"></a>
</div>

<!-- ======== Typing SVG (brand purple) ======== -->
<div align="center">
  <img src="https://readme-typing-svg.herokuapp.com?font=Orbitron&size=22&duration=3200&pause=900&color=5E35B1&center=true&vCenter=true&width=720&lines=AI+Infrastructure%2C+managed+by+AI;One+Command.+Eight+GPU+Vendors.+Zero+Config;From+Zero+to+First+Token+in+60+Seconds;Stop+Configuring.+Start+Inferring" alt="Typing SVG"/>
</div>

<!-- ======== Badge wall ======== -->
<div align="center">

[![GitHub Release](https://img.shields.io/github/v/release/Approaching-AI/AIMA?style=for-the-badge&color=5E35B1&labelColor=1a1a2e)](https://github.com/Approaching-AI/AIMA/releases)
<!-- GitHub Stars badge hidden until stars ≥ 500 -->
[![License](https://img.shields.io/github/license/Approaching-AI/AIMA?style=for-the-badge&color=4ecdc4&labelColor=1a1a2e)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/Approaching-AI/AIMA?style=for-the-badge&logo=go&logoColor=white&labelColor=1a1a2e&color=5E35B1)](go.mod)
[![Platforms](https://img.shields.io/badge/Platforms-Linux_%7C_macOS_%7C_Windows-42A5F5?style=for-the-badge&labelColor=1a1a2e)](#60-second-genesis)

</div>

---

**AI Inference Managed by AI** — A single Go binary that detects hardware, resolves optimal configs from a YAML knowledge base, deploys inference engines via K3S, and exposes 61 MCP tools for AI Agents to operate everything.

<!-- ======== Hero GIF (placeholder) ======== -->
<!-- TODO: drop terminal cast here once recorded
<div align="center">
  <img src="docs/assets/hero-terminal.gif" alt="AIMA 60-second install demo" width="820"/>
</div>
-->

## News

- **2026-04** — v0.4.0 ships: Explorer Agent Planner (PDCA), Central Advisor + Analyzer, MCP consolidation 101→61, aima-service device identity Phase 1, onboarding wizard with 5-dimension 0–100 scoring, multi-modal benchmark (chat/TTS/ASR/T2I/T2V).
- **2026-03** — v0.3.x: OpenClaw full-stack integration, smart agent routing, Engine Profile system with SGLang-KT, AMD RDNA3 (W7900D) 8-GPU validated.
- **2026-02** — v0.2.0: Support service, Web UI redesign, OpenClaw integration.
- **2026-01** — v0.0.1: Initial foundation release (hardware detection, multi-runtime).

## Features

- **Zero-config hardware detection** — automatically discovers GPUs (NVIDIA, AMD, Huawei Ascend, Hygon DCU, Apple Silicon), CPU, and RAM.
- **Knowledge-driven deployment** — YAML catalog of hardware profiles, engines, models, and partition strategies; no engine-specific code branches.
- **Multi-runtime** — K3S (Pod) for clusters, Docker for single-node containers, Native (exec) for bare-metal inference.
- **61 MCP tools** — full programmatic control for AI Agents over hardware, models, engines, deployments, fleet, and more.
- **Fleet management** — mDNS-based auto-discovery of LAN peers; remote tool execution across heterogeneous devices.
- **Offline-first** — all core functions work with zero network; network is enhancement, not requirement.
- **Single binary, zero CGO** — cross-compiles to Windows, macOS, Linux (amd64/arm64) with no C dependencies.

## 60-Second Genesis

<!-- section title: theatrical drama for "Quick Start" — the name lives as a standalone slogan -->

### Download

Grab a pre-built binary from the [Releases](https://github.com/Approaching-AI/AIMA/releases) page, or build from source:

```bash
git clone https://github.com/Approaching-AI/AIMA.git
cd aima
make build
```

For published product releases, the binary installer can be one line:

```bash
curl -fsSL https://raw.githubusercontent.com/Approaching-AI/AIMA/master/install.sh | sh
```

On Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/Approaching-AI/AIMA/master/install.ps1 | iex
```

Notes:
- The installer resolves the latest installable `vX.Y.Z` product release instead of GitHub's `latest` release, because bundle tags such as `bundle/stack/2026-02-26` are not product binaries.
- If tags are ahead of published binaries, the installer warns and stays on the latest installable release until the new assets are uploaded.
- Override the source repo for forks with `AIMA_REPO=<owner>/<repo>`.
- Pin a release with `AIMA_VERSION=v0.2.0`.
- Windows installer currently targets `windows/amd64` and installs to `%LOCALAPPDATA%\Programs\AIMA`.

### Server Setup (Linux)

```bash
# 1. Detect your hardware
aima hal detect

# 2. Initialize infrastructure (installs K3S + HAMi + aima-serve daemon)
#    Downloads airgap images for offline container startup.
#    Requires root for systemd service installation.
sudo aima init

# 3. Deploy a model (auto-resolves engine + config for your hardware)
aima deploy apply --model qwen3.5-35b-a3b
```

After `aima init`, three components are running as systemd services:

| Component | What it does |
|-----------|-------------|
| K3S | Container orchestration (containerd, airgap images pre-loaded) |
| HAMi | GPU virtualization for multi-model sharing (skipped on unsupported hardware) |
| aima-serve | API server on `0.0.0.0:6188` with mDNS broadcast |

The server is now discoverable on the LAN and ready to serve inference requests.

### Client Usage (Any Platform)

On another device with the AIMA binary — no `init` or `serve` needed:

```bash
# Discover servers on the LAN via mDNS (no IP needed)
aima discover

# List all discovered AIMA devices
aima fleet devices

# Query a remote device
aima fleet exec <device-id> hardware.detect
aima fleet exec <device-id> deploy.list

# Call the OpenAI-compatible API directly
curl http://<server-ip>:6188/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen3.5-35b-a3b","messages":[{"role":"user","content":"hello"}]}'
```

### Web UI

Every AIMA server hosts a built-in Web UI at `http://<server-ip>:6188/ui/`.

To discover the server IP first: `aima discover`.

To get a Fleet dashboard that auto-discovers all LAN peers, run `aima serve --discover` on your own device and open `http://localhost:6188/ui/`.

<!-- ======== Web UI Demo GIF ======== -->
<div align="center">
  <img src="docs/assets/onboarding-webui.gif" alt="AIMA Web UI — natural-language query resolves through L3a Agent to a hardware.detect MCP tool call" width="1000"/>
</div>

### Security

`aima init` starts the server **without authentication** (LAN trust model). To enable API key authentication:

```bash
# Set API key (hot-reloads, no restart needed)
aima config set api_key <your-key>

# All API/MCP/Fleet requests now require: Authorization: Bearer <your-key>
# Web UI will prompt for the key automatically.

# Remote fleet commands with authentication
aima fleet devices --api-key <your-key>
```

## Eight Silicon Kingdoms

<!-- section title: replaces "Supported Hardware" — hardware matrix as a piece of lore.
     Number in the title tracks the table row count; bump when new vendors are validated. -->

AIMA has been validated end-to-end across eight GPU/NPU ecosystems:

| Vendor | Tested Devices | SDK |
|--------|---------------|-----|
| NVIDIA | RTX 4060, RTX 4090, GB10 (Grace Blackwell) | CUDA |
| AMD | Radeon 8060S (RDNA 3.5), Ryzen AI MAX+ 395, W7900D × 8 (RDNA 3) | ROCm / Vulkan |
| Huawei | Ascend 910B1 (8× 64GB HBM, Kunpeng-920 aarch64) | CANN |
| Hygon | BW150 DCU (8× 64GB HBM) | DCU |
| MetaX | N260 (64GB HBM2e, MACA 3.1) | MACA |
| Moore Threads | M1000 (MUSA 3.1), AIBook M1000 SoC | MUSA |
| Apple | M4 | Metal |
| Intel | CPU-only | — |

## Supported Engines

| Engine | GPU Support | Format |
|--------|------------|--------|
| vLLM | NVIDIA CUDA, AMD ROCm, Hygon DCU | Safetensors |
| llama.cpp | NVIDIA CUDA, AMD Vulkan, Apple Metal, CPU | GGUF |
| SGLang | NVIDIA CUDA, Huawei Ascend (CANN) | Safetensors |
| Ollama | All (via llama.cpp) | GGUF |

## The L0→L3 Intelligence Ladder

<!-- section title: replaces "Architecture" — preserves the L0-L3 feature name, wraps it in ladder narrative -->

AIMA resolves every deployment decision by climbing four layers of intelligence. Each layer can override the layer below it, and every layer fails gracefully to the one underneath.

- **L0** — YAML knowledge base defaults
- **L1** — Human CLI overrides
- **L2** — Golden configs from benchmark history
- **L3a** — Go Agent loop (tool-calling LLM)

The system is built around four invariants: no code branches for engine/model types (YAML-driven), no container lifecycle management (K3S handles it), MCP tools as the single source of truth, and offline-first operation.

<!-- ======== Architecture diagram (placeholder) ======== -->
<!-- TODO: export ARCHITECTURE.md §system diagram to SVG
<div align="center">
  <img src="docs/assets/architecture.svg" alt="AIMA L0-L3 intelligence ladder" width="820"/>
</div>
-->

See [design/ARCHITECTURE.md](design/ARCHITECTURE.md) for the full architecture document.

## The Forge

<!-- NEW section: surfaces the UAT evidence that's currently buried. Replaces/absorbs "Battle-tested" from HKUDS-inspired template. -->

Every AIMA release passes through The Forge — an end-to-end UAT matrix run on real hardware.

- **Eight vendors** × multiple devices (NVIDIA GB10, RTX 4090, AMD W7900D × 8, Huawei Ascend 910B × 8, Hygon BW150 DCU × 8, MetaX N260 × 2, Moore Threads M1000, Apple M4, Intel CPU)
- **Three runtimes** validated per release: K3S Pod, Docker container, Native exec
- **16 UAT items** per release covering install / hardware detect / model deploy / API / MCP / fleet / onboarding wizard / failover
- **1,200+ evidence files** and ~1,000 hours of logged runtime across the fleet

The live registry of test machines, per-device UAT results, and reproducer commands live in `CLAUDE.md` under *Remote Test Lab*.

## Agent-native

AIMA is built to be driven by AI agents first, humans second.

- **61 MCP tools** expose hardware / model / engine / deploy / fleet / knowledge / agent / device identity as JSON-RPC 2.0 functions
- **The Dispatcher** routes any request through L0→L3 with automatic fallback — agents get the same API whether the local LLM is loaded or not
- **Explorer Agent Planner** runs document-driven PDCA cycles with a SQLite-backed workspace and seven bash-like tools; every decision leaves a structured trace

Connect any MCP-compatible client (Claude Desktop, Cursor, custom agent) and AIMA becomes your AI-inference control plane.

## Project Structure

```
cmd/aima/          Entry point + dependency wiring split by domain
internal/
  hal/             Hardware detection
  knowledge/       YAML knowledge base + SQLite resolver
  runtime/         K3S (Pod) + Docker (container) + Native (exec) runtimes
  mcp/             MCP server + 61 MCP tool registrations/implementations
  agent/           Go Agent loop (L3a)
  cli/             Cobra CLI (thin wrappers over MCP tools)
  ui/              Embedded Web UI (Alpine.js SPA)
  proxy/           OpenAI-compatible HTTP proxy
  fleet/           mDNS fleet discovery + remote execution
  sqlite.go        SQLite state store (`package state`, modernc.org/sqlite, zero CGO)
  model/           Model scan/download/import + metadata detection
  engine/          Engine image management
  stack/           K3S + HAMi infrastructure installer
catalog/
  hardware/        Hardware profile YAML
  engines/         Engine asset YAML
  models/          Model asset YAML
  partitions/      Partition strategy YAML
  stack/           Stack component YAML
```

## Building

### Local build

```bash
make build
# Output: build/aima (or build/aima.exe on Windows)
```

### Cross-compile all platforms

```bash
make all
# Output:
#   build/aima.exe          (windows/amd64)
#   build/aima-darwin-arm64 (macOS/arm64)
#   build/aima-linux-arm64  (linux/arm64)
#   build/aima-linux-amd64  (linux/amd64)
```

### Package GitHub release assets

```bash
make release-assets
# Output:
#   build/release/<version>/aima-darwin-arm64
#   build/release/<version>/aima-linux-amd64
#   build/release/<version>/aima-linux-arm64
#   build/release/<version>/aima-windows-amd64.exe
#   build/release/<version>/checksums.txt
```

To upload those assets to the matching GitHub release with `gh`:

```bash
make publish-release-assets
```

Annotated SemVer tag pushes such as `v0.2.1` also trigger `.github/workflows/release.yml`, which builds the same assets and uploads them automatically.

### Run tests

```bash
go test ./...
```

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.