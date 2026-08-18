# AIMA README v2 — Draft Spec

**Purpose**: HKUDS-inspired structural rewrite of the repo README to build visual hierarchy, surface hard evidence (UAT matrix, hardware coverage), and land traffic hooks (Typing SVG, theatrical section titles) while respecting AIMA's enterprise-grade brand system.

**Scope**: README.md + README_zh.md, plus asset plan for Banner / Typing SVG / Hero GIF / Architecture diagram / Star History / Contributor wall.

**Non-goals**: Not touching product identity (tagline / product name / feature names stay frozen), not adding emoji decoration, not adopting HKUDS neon-cyan palette.

---

## 1. Design decisions (locked)

| Decision | Value |
|---|---|
| Tagline | `AI Infrastructure, managed by AI` — unchanged |
| Brand primary / accent | `#5E35B1` 趋境紫 / `#42A5F5` 科技蓝 |
| Emoji in section titles | None |
| Feature命名噱头化 | Off (tagline/feature names frozen, only section titles get drama) |
| Theatrical section titles | 4 — `60-Second Genesis` / `The Forge` / `The L0→L3 Intelligence Ladder` / `Eight Silicon Ecosystems` |
| Typing SVG | On — brand-colored, English + Chinese variants |
| Bilingual switcher | Top-of-file badge pair |
| Badge wall | stars / release / license / Go version / platforms (Discord/WeChat deferred — no community yet) |
| Star History | Removed from scope (per PMM, 2026-04-23) |

## 2. Asset inventory

| Asset | Status | Source |
|---|---|---|
| Banner SVG | ✅ Ready | `docs/assets/banner.svg` (existing, on-brand purple+blue) |
| Product logo | ✅ Ready | `internal/ui/static/logo-dark-wordmark.svg` |
| Architecture diagram | ✅ Ready | `docs/assets/architecture-ladder.png` (1.3MB, 1672×941, neon/layered visual generated 2026-04-24) |
| Eight Silicon Ecosystems visual | ✅ Ready | `docs/assets/eight-silicon-ecosystems.png` (1.4MB, 1672×941, 8-vendor grid — logo + tested devices + SDK per card, bottom line "Unified framework. Broad silicon compatibility.") |
| Supported Engines visual | 🔄 Regen pending | `docs/assets/supported-engines.png` — drop Ollama per skyguan92 review (catalog/engines/ has no ollama.yaml). Target: 3-engine 1×3 row (vLLM / llama.cpp / SGLang). |
| Hero Terminal GIF | 🚫 Out of scope | Per PMM 2026-04-23, not adopting (path C — WebUI GIF carries the install→deploy story). |
| Onboarding WebUI GIF | ✅ Ready | `docs/assets/onboarding-webui.gif` (2.4MB, 18s, β narrative — natural-language chat → Kimi routing → hardware.detect MCP tool → expanded JSON result) |
| Discord / WeChat QR | 🚫 Deferred | Per PMM 2026-04-23, skip until community strategy is decided |
| Star History | 🚫 Out of scope | Per PMM 2026-04-23, not adopting |

## 3. Known data inconsistencies to resolve before merging

| Issue | Current state | Action |
|---|---|---|
| MCP tool count | EN=56, ZH=94, CLAUDE.md=61 (post-v0.4 consolidation 101→61) | **Decision (PMM 2026-04-23)**: always take the number from the current-version CLAUDE.md → use **61** for v0.4; every release PR must sync README EN + ZH to CLAUDE.md |
| Hardware vendor count | README table=6; CLAUDE.md Remote Test Lab adds MetaX (N260) + Moore Threads (M1000, AIBook) as validated | **Decision (PMM 2026-04-23)**: expand to **8 vendors** now. Section title = `Eight Silicon Ecosystems`. Keep the table open-ended — future vendors append rows and the number in the section title gets bumped accordingly |

---

## 4. Draft — `README.md` (English)

> Paste block below into `README.md` at the repo root. Comments `<!-- -->` are for reviewer context, remove before merge.

````markdown
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
<!-- GitHub Stars badge — hidden until stars ≥ 500. Re-enable:
[![GitHub Stars](https://img.shields.io/github/stars/Approaching-AI/AIMA?style=for-the-badge&color=42A5F5&labelColor=1a1a2e)](https://github.com/Approaching-AI/AIMA/stargazers) -->
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
cd AIMA
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
#    --tier k3s pulls in K3S + HAMi on top of the docker baseline.
#    Requires root for systemd service installation.
sudo aima onboarding init --tier k3s --yes

# 3. Deploy a model (auto-resolves engine + config for your hardware)
aima deploy qwen3.5-35b-a3b
```

After `aima onboarding init --tier k3s`, three components are running as systemd services:

| Component | What it does |
|-----------|-------------|
| K3S | Container orchestration (containerd, airgap images pre-loaded) |
| HAMi | GPU virtualization for multi-model sharing (skipped on unsupported hardware) |
| aima-serve | API server on `0.0.0.0:6188` with mDNS broadcast |

The server is now discoverable on the LAN and ready to serve inference requests.

### Client Usage (Any Platform)

On another device with the AIMA binary — no `onboarding init` or `serve` needed:

```bash
# List AIMA devices auto-discovered on the LAN via mDNS (no IP needed)
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

To discover the server IP first: `aima fleet devices`.

To get a Fleet dashboard that auto-discovers all LAN peers, run `aima serve --discover` on your own device and open `http://localhost:6188/ui/`.

<!-- ======== Web UI Demo GIF ======== -->
<div align="center">
  <img src="docs/assets/onboarding-webui.gif" alt="AIMA Web UI — natural-language query resolves through L3a Agent to a hardware.detect MCP tool call" width="1000"/>
</div>

### Security

`aima onboarding init` starts the server **without authentication** (LAN trust model). To enable API key authentication:

```bash
# Set API key (hot-reloads, no restart needed)
aima config set api_key <your-key>

# All API/MCP/Fleet requests now require: Authorization: Bearer <your-key>
# Web UI will prompt for the key automatically.

# Remote fleet commands with authentication
aima fleet devices --api-key <your-key>
```

## Eight Silicon Ecosystems

<!-- section title: replaces "Supported Hardware" — hardware matrix as a piece of lore.
     Number in the title tracks the table row count; bump when new vendors are validated. -->

AIMA has been validated end-to-end across eight GPU/NPU ecosystems — NVIDIA (CUDA), AMD (ROCm / Vulkan), Huawei Ascend (CANN), Hygon (DCU), MetaX (MACA), Moore Threads (MUSA), Apple (Metal), and Intel (CPU-only):

<div align="center">
  <img src="docs/assets/eight-silicon-ecosystems.png" alt="Eight Silicon Ecosystems — AIMA runs end-to-end across NVIDIA RTX 4060/4090/GB10, AMD Radeon 8060S/Ryzen AI MAX+/W7900D×8, Huawei Ascend 910B1, Hygon BW150 DCU, MetaX N260, Moore Threads M1000/AIBook M1000 SoC, Apple M4, and Intel CPU-only" width="1000"/>
</div>

## Supported Engines

AIMA orchestrates three inference runtimes — vLLM (Safetensors), llama.cpp (GGUF), and SGLang (Safetensors) — across every supported backend:

<div align="center">
  <img src="docs/assets/supported-engines.png" alt="Supported Engines — vLLM on NVIDIA CUDA, AMD ROCm, Hygon DCU; llama.cpp on NVIDIA CUDA, AMD Vulkan, Apple Metal, CPU; SGLang on NVIDIA CUDA and Huawei Ascend (CANN)" width="1000"/>
</div>

## The L0→L3 Intelligence Ladder

<!-- section title: replaces "Architecture" — preserves the L0-L3 feature name, wraps it in ladder narrative -->

AIMA resolves every deployment decision by climbing four layers of intelligence. Each layer can override the layer below it, and every layer fails gracefully to the one underneath.

<div align="center">
  <img src="docs/assets/architecture-ladder.png" alt="AIMA L0→L3 Intelligence Ladder — four stacked layers (L0 Defaults YAML knowledge bedrock, L1 Human CLI manual parameters, L2 Knowledge Base deterministic matching, L3a Go Agent tool-calling loop); each higher layer overrides the one below, every layer guarantees graceful fallback" width="1000"/>
</div>

The system is built around four invariants: no code branches for engine/model types (YAML-driven), no container lifecycle management (K3S handles it), MCP tools as the single source of truth, and offline-first operation.

See [design/ARCHITECTURE.md](design/ARCHITECTURE.md) for the full architecture document.

## The Forge

<!-- NEW section: surfaces the UAT evidence that's currently buried. Replaces/absorbs "Battle-tested" from HKUDS-inspired template. -->

Every AIMA release passes through The Forge — an end-to-end UAT matrix run on real hardware.

<div align="center">

![8 Vendors](https://img.shields.io/badge/8-VENDORS-5E35B1?style=for-the-badge&labelColor=1a1a2e)
![3 Runtimes](https://img.shields.io/badge/3-RUNTIMES-42A5F5?style=for-the-badge&labelColor=1a1a2e)
![16 UAT](https://img.shields.io/badge/16-UAT%20ITEMS-5E35B1?style=for-the-badge&labelColor=1a1a2e)
![1200+ Evidence Files](https://img.shields.io/badge/1%2C200+-EVIDENCE%20FILES-42A5F5?style=for-the-badge&labelColor=1a1a2e)
![1000+ Runtime Hours](https://img.shields.io/badge/~1%2C000-RUNTIME%20HOURS-5E35B1?style=for-the-badge&labelColor=1a1a2e)

</div>

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

<details>
<summary><strong>Browse the MCP tool surface by domain</strong></summary>

<br>

| Domain | Representative tools | Purpose |
|---|---|---|
| **hardware** | `hardware.detect` · `hardware.metrics` | GPU/CPU/RAM inventory + live telemetry |
| **model** | `model.list` · `model.scan` · `model.pull` · `model.import` · `model.info` · `model.remove` | Local model catalog + download + import |
| **engine** | `engine.list` · `engine.pull` · `engine.scan` · `engine.import` · `engine.info` | Inference engine lifecycle |
| **deploy** | `deploy.apply` · `deploy.dry_run` · `deploy.list` · `deploy.logs` · `deploy.delete` · `deploy.approve` · `deploy.status` | Start / monitor / stop model serving |
| **fleet** | `fleet.info` · `fleet.exec` | LAN auto-discovery + remote tool execution |
| **knowledge** | `knowledge.resolve` · `knowledge.search` · `knowledge.promote` · `knowledge.evaluate` · `knowledge.save` · `knowledge.analytics` | YAML catalog + golden-config lifecycle |
| **agent** | `agent.ask` · `agent.status` · `agent.rollback` | L3a Agent invocation + decision trace |
| **device** | `device.register` · `device.status` · `device.renew` · `device.reset` | aima-service identity lifecycle |
| **benchmark** | `benchmark.run` · `benchmark.matrix` · `benchmark.record` · `benchmark.list` | Reproducible performance testing |
| **central** | `central.advise` · `central.scenario` · `central.sync` | Central knowledge server + advisory feedback |
| **system** | `system.config` · `system.status` | Hot-reload config + overall health |

See [`internal/mcp/`](internal/mcp/) for the complete registry.

</details>

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
````

---

## 5. Draft — `README_zh.md` delta

Structure mirrors the English draft exactly. Only the following blocks differ — everything else is a direct translation of the English sections above.

### 5.1 Bilingual switcher (reversed highlight)

```markdown
<div align="center">
  <a href="README.md"><img src="https://img.shields.io/badge/English-42A5F5?style=for-the-badge&labelColor=1a1a2e" alt="English"></a>
  <a href="README_zh.md"><img src="https://img.shields.io/badge/中文-5E35B1?style=for-the-badge&labelColor=1a1a2e" alt="中文"></a>
</div>
```

### 5.2 Typing SVG (Chinese)

```markdown
<div align="center">
  <img src="https://readme-typing-svg.herokuapp.com?font=Noto+Sans+SC&size=22&duration=3200&pause=900&color=5E35B1&center=true&vCenter=true&width=720&lines=AI+%E7%AE%A1%E7%90%86+AI+%E7%9A%84%E7%AE%97%E5%8A%9B%E5%9F%BA%E7%A1%80%E8%AE%BE%E6%96%BD;%E4%B8%80%E6%9D%A1%E5%91%BD%E4%BB%A4+%E5%85%AB%E5%AE%B6%E7%A1%AC%E4%BB%B6+%E9%9B%B6%E9%85%8D%E7%BD%AE;60+%E7%A7%92%E4%BB%8E%E5%BC%80%E7%AE%B1%E5%88%B0%E7%AC%AC%E4%B8%80%E4%B8%AA+Token;%E5%88%AB%E5%86%8D%E9%85%8D%E7%8E%AF%E5%A2%83%E4%BA%86%EF%BC%8C%E7%9B%B4%E6%8E%A5%E6%8E%A8%E7%90%86" alt="Typing SVG"/>
</div>
```

(字幕内容：`AI 管理 AI 的算力基础设施` · `一条命令 八家硬件 零配置` · `60 秒从开箱到第一个 Token` · `别再配环境了，直接推理`)

### 5.3 Section title translations (4 theatrical)

| English | 中文 |
|---|---|
| 60-Second Genesis | 60 秒创世 |
| Eight Silicon Ecosystems | 八大硅基生态 |
| The L0→L3 Intelligence Ladder | L0→L3 智能阶梯 |
| The Forge | 熔炉 — 1200 次真机验证 |

### 5.4 News 中文版

```markdown
## 动态

- **2026-04** — v0.4.0 发布：Explorer Agent Planner（PDCA）、Central Advisor + Analyzer、MCP 工具从 101 精简至 61、aima-service 设备身份 Phase 1、Onboarding 冷启动向导（五维 0-100 打分）、多模态 Benchmark（chat/TTS/ASR/T2I/T2V）。
- **2026-03** — v0.3.x：OpenClaw 全栈集成、智能 Agent 路由、带 SGLang-KT 的 Engine Profile 体系、AMD RDNA3（W7900D）8 卡已验证。
- **2026-02** — v0.2.0：Support 服务、Web UI 重构、OpenClaw 集成。
- **2026-01** — v0.0.1：首个基础版本（硬件检测、多运行时）。
```

### 5.5 The Forge 中文

```markdown
## 熔炉 — 1200 次真机验证

每次 AIMA 发版都会走一遍熔炉——一套端到端 UAT 矩阵，全部在真机上跑。

- **八家厂商** × 多款设备（NVIDIA GB10 / RTX 4090、AMD W7900D × 8、华为 Ascend 910B × 8、海光 BW150 DCU × 8、沐曦 N260 × 2、摩尔线程 M1000、Apple M4、Intel CPU）
- 每个版本验证 **三种运行时**：K3S Pod、Docker 容器、Native exec
- 每轮 **16 个 UAT 项**：安装 / 硬件识别 / 模型部署 / API / MCP / 集群 / Onboarding 向导 / 故障转移
- 累计 **1200+ 证据文件**，全集群 ~1000 小时运行日志

完整的测试机名册、逐台 UAT 结果和复现命令，见 `CLAUDE.md` 的 *Remote Test Lab* 部分。
```

---

## 6. Implementation plan

| Step | Action | Owner | Blocking |
|---|---|---|---|
| 1 | Align MCP tool count to **61** (source-of-truth = current-version CLAUDE.md) across EN README + ZH README | dev | — |
| 2 | Expand hardware table to 8 vendors (already reflected in §4 draft, §5.5 中文版) | dev | — |
| 3 | Copy draft §4 into `README.md`, §5 into `README_zh.md` | PMM | Step 1, 2 |
| 4 | ~~Record Onboarding WebUI GIF via Playwright~~ — ✅ done 2026-04-24, at `docs/assets/onboarding-webui.gif` | Claude | — |
| 5 | Hero Terminal GIF — dropped per PMM 2026-04-23 (path C) | — | — |
| 6 | Export architecture diagram from `design/ARCHITECTURE.md` §system to `docs/assets/architecture.svg` | dev | — |

## 7. Decisions from PMM 2026-04-23

| # | Question | Decision |
|---|---|---|
| 1 | Vendor scope | **Expand to 8**. Section title = `Eight Silicon Ecosystems`. Open to further vendors. |
| 2 | MCP tool count | **Source-of-truth = current-version CLAUDE.md.** For v0.4 = 61. |
| 3 | Star History | **Remove from scope.** |
| 4 | Discord / WeChat badges | **Defer.** Do not publish placeholders. |
| 5 | Hero Terminal GIF + Onboarding WebUI GIF | **Claude records directly**, target = real env `http://192.168.110.71:6188/ui/` |
| 6 | Typing SVG font | **Orbitron** — picked for memorability / brand-moment over ToB-developer legitimacy |
| 7 | Terminal Hero GIF | **Skip for now** (path C). WebUI onboarding GIF alone covers the "install → deploy" visual. Revisit if feedback says it's needed. |
| 8 | WebUI onboarding GIF exploration | **Claude to self-explore** the live env and come back with a storyboard before actually recording |

## 8. Open — font comparison for Typing SVG

Two finalists, same text + color + speed. Click the raw URLs to preview in browser:

**Option A — `Orbitron`** (futuristic / HKUDS-match):
```
https://readme-typing-svg.herokuapp.com?font=Orbitron&size=22&duration=3200&pause=900&color=5E35B1&center=true&vCenter=true&width=720&lines=AI+Infrastructure%2C+managed+by+AI;One+Command.+Eight+GPU+Vendors.+Zero+Config;From+Zero+to+First+Token+in+60+Seconds;Stop+Configuring.+Start+Inferring
```

**Option B — `JetBrains Mono`** (developer-native / ToB):
```
https://readme-typing-svg.herokuapp.com?font=JetBrains+Mono&size=22&duration=3200&pause=900&color=5E35B1&center=true&vCenter=true&width=720&lines=AI+Infrastructure%2C+managed+by+AI;One+Command.+Eight+GPU+Vendors.+Zero+Config;From+Zero+to+First+Token+in+60+Seconds;Stop+Configuring.+Start+Inferring
```

Character of each:
- **Orbitron**: geometric sans, wide letters, all-caps personality, sci-fi vibe. Strong at "futuristic promise / brand-scale moment". Weaker for code-mentioning lines (monowidth expectation broken).
- **JetBrains Mono**: monospace, ligature-aware, made for developer eyes. Feels like "this thing is real infrastructure, not a landing page." Pairs naturally with the CLI blocks that follow in Quick Start. Weaker at hero-grade drama.

**PMM decision (2026-04-23): Orbitron.** Picked for memorability / brand-moment weight. Rationale: hero slot is the one place in the repo where a distinctive typography signature is worth more than developer-native legibility; JetBrains Mono can appear later in product screenshots / video title cards if needed.

---

## 9. Post-v1 follow-ups (TODO after first README merge)

Items explicitly deferred from the first ship — not blockers for merging the v1 README, but should be picked up once the baseline is live.

| # | Item | Why deferred | Trigger to pick up |
|---|---|---|---|
| F1 | **Richer GIF demo scenario** — β v1 asks "What hardware do I have?" which is a single-tool (`hardware.detect`) one-shot demo. A stronger story would show multi-tool orchestration (e.g. "Can I run GLM-4.5-Air on this machine?" → triggers `hardware.detect` + `model.list` + reasoning about fit) or multi-turn (user asks a follow-up that references the prior result). Requires: choosing a scenario that reliably returns in <10s on production network, verifying it doesn't expose internal model configs, re-recording β. | PMM 2026-04-24: single-tool demo ships v1; richer scenario is a v1.1 polish once feedback arrives. | First round of README feedback / star growth review. Target: pick up 1-2 weeks after README v1 merge. |
| F2 | Record an alt version without the Onboarding drawer beat (since β shows chat instead, the drawer context isn't essential) — decide whether that's a better fit for the slot. | Bundled into the same "richer GIF" iteration. | Same trigger as F1. |
| F3 | Architecture diagram SVG export (specs plan step 6) — still pending. | Lower priority than GIF; README can ship without it, just means the "L0→L3 Intelligence Ladder" section stays text-only. | When someone has 1-2h to do the SVG. |
| F4 | **Standing rule — version-number synchronization across marketing assets.** Every release PR must sync these four surfaces to the same numbers before merge: (1) `CLAUDE.md` (source of truth — under "Current State"), (2) `README.md` Features + Agent-native sections, (3) `README_zh.md` 同位置, (4) `docs/assets/banner.svg` (stats row). Fields that move version-to-version: MCP tool count, hardware-platform count, validated engine list, number of active fleet devices shown as examples. 2026-04-23 v1 README caught two drift cases (banner said `94 tools / 6 vendors`, README said `56 tools / 6 vendors`, CLAUDE.md said `61 tools / 8 validated`) — all unified at 61 / 8. | Adopt as release-PR checklist item starting v0.5; add a grep-based sanity check to the release workflow if it recurs. |
