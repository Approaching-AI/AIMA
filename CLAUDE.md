# AIMA — Claude Code Development Guide

## What Is This

AIMA (AI-Inference-Managed-by-AI): a Go binary that manages AI inference on edge devices.
It detects hardware, resolves optimal configs from a YAML knowledge base, generates K3S Pod YAML,
and exposes 56 MCP tools for AI Agents to operate everything. **This project is 100% developed by Claude Code.**

Tech: Go (no CGO), K3S, HAMi, SQLite (modernc.org/sqlite), MCP (JSON-RPC 2.0), Cobra CLI, log/slog.
Design docs: `design/ARCHITECTURE.md` (system architecture), `design/PRD.md`, `design/MRD.md`.

## ====== Remote Test Lab (Heterogeneous Hardware) ======

**This is a live, SSH-driven test environment for real-device validation.**
Claude Code SSHes into each machine, runs AIMA, collects results, and feeds them back into development.

### Machine Registry

| ID | User@Host | OS | Arch | Chip/GPU | RAM | Disk Free | K3S/Docker | SSH Auth | Role |
|----|-----------|-----|------|----------|-----|-----------|------------|----------|------|
| dev-mac | **local** | macOS 15.3 | arm64 | Apple M4 | 16 GB | 393 GB | no | local | Dev machine, `go build/test` runs here directly |
| test-win | `jguan@100.114.25.35` (Tailscale: light-salt) | Windows 11 | x86_64 | i9-13980HX + RTX 4060 8GB (Driver 566, CUDA) | 32 GB | 551 GB | no | key | Test machine, Windows + NVIDIA GPU validation |
| gb10 | `qujing@100.105.58.16` | Ubuntu 24.04 | aarch64 | NVIDIA GB10 (CUDA 13.0, Driver 580) | 120 GB unified | 149 GB | K3S v1.31.4 + Docker 28.5 | key | GPU inference + K3S full-stack validation |
| linux-1 | `cjwx@100.121.255.97` (Tailscale) / `cjwx@192.168.109.23` (LAN) | Ubuntu 22.04 | x86_64 | 2× NVIDIA RTX 4090 48GB (Driver 580, CUDA 13.0) | 503 GB | 72 GB | Docker | key | Dual-GPU inference validation |
| amd395 | `quings@100.71.145.56` (Tailscale) | Ubuntu 24.04 | x86_64 | AMD Ryzen AI MAX+ 395 + Radeon 8060S (no NVIDIA) | 62 GB | 57 GB | Docker 28.2 | key | AMD/APU inference validation |
| hygon | `qujing@100.113.47.73` (Tailscale) / `qujing@192.168.110.24` (LAN) | Ubuntu 22.04 | x86_64 | 2× Hygon C86-4G 48C + 8× Hygon BW150 DCU 64GB | 751 GB | 265 GB + 564 GB NVMe | K3S + Docker 28.0 | key | DCU inference validation |
| qjq2 | `root@192.168.0.22` (via qjq0 `116.204.103.3`) | EulerOS 2.0 | aarch64 | 8× Ascend 910B1 64GB HBM (Driver 25.3, CANN 8.3) | 1.5 TiB | 99 GB | Docker 18.09 | key (ProxyCommand) | Ascend NPU inference validation |
| m1000 | `dev@100.123.212.6` (Tailscale) / `dev@192.168.108.188` (LAN) | Ubuntu 22.04 | aarch64 | Moore Threads M1000 MUSA GPU (MUSA 3.1.3-AB100, SDK 4.1.4) | 62 GB | 365 GB | Docker | key | Moore Threads MUSA GPU inference validation |
| metax-n260 | `kylin@100.94.119.128` (Tailscale) / `kylin@192.168.110.66` (LAN) | Kylin V10 (Sword) | x86_64 | 2× Hygon C86-4G 32C + 2× MetaX N260 64GB HBM2e (MACA 3.1.0.14, Driver 3.0.11) | 124 GB | 230 GB | Docker 18.09 | key | MetaX MACA GPU inference validation |
| aibook | `aibook@100.106.164.54` (Tailscale) | Ubuntu 22.04 | aarch64 | Moore Threads M1000 SoC (CPU 12C A78 + GPU MUSA + 2×NPU 50TOPS) | 32 GB unified LPDDR5X | 711 GB | Docker 24.0.7 | key | AIBook 笔记本，M1000 SoC GPU+NPU 推理验证 |
| w7900d | `root@36.151.243.68 -p 21985` | Ubuntu 24.04 | x86_64 | 2× EPYC 9334 128T + 8× AMD Radeon Pro W7900D 48GB (RDNA3, Navi 31, ROCm 5.7) | 1 TiB | 3.0 TB NVMe (`/disk/ssd1`) | Docker 29.0 | key | AMD RDNA3 8-GPU 推理验证, Ollama 0.13.5 预装 |
| gb10-4T | `qujing@100.91.39.109` (Tailscale) / `qujing@192.168.108.131` (LAN) | Ubuntu 24.04 | aarch64 | NVIDIA GB10 (CUDA 13.0, Driver 580) | 120 GB unified | 2.0 TB (3.7 TB NVMe) | Docker 28.5 | key | DGX Spark GB10, 大容量存储, GPU 推理验证 |

> **Maintaining this table:** After first SSH to a new machine, run the device probe and update this table.
> Password: never store passwords here. Use SSH key auth. For initial key setup: `ssh-copy-id <user@host>`.

### Hardware Reference Docs

Vendor-specific hardware reference documents are stored in `hardware-reference/`.

| File | Description |
|------|-------------|
| `hardware-reference/README.md` | Index and quick reference for M1000 |
| `hardware-reference/mt-ai-developer-kit-guide.md` | M1000 Developer Kit: hardware, connectors, setup, serial/SSH |
| `hardware-reference/vllm-musa-m1000-guide.md` | vLLM-MUSA on M1000: model download, startup params, troubleshooting |
| `hardware-reference/MT_AI_Developer_Kit_User_Guide_v1.0.1.pdf` | Original PDF with photos (14 pages) |
| `hardware-reference/metax-n260-vllm-guide.md` | MetaX N260: hardware specs, mx-smi, vLLM-MetaX Docker deployment |
| `hardware-reference/aibook-m1000-guide.md` | AIBook M1000 SoC: hardware specs, vLLM-MUSA, NPU/MTNN, pre-loaded models |

External (Tencent Docs, not downloadable):
- E300 AI Module Spec: https://docs.qq.com/pdf/DS0NwaVZoQ2ZSTkZH
- Qwen3-30B-A3B-GPTQ-Int4 Deploy Guide: https://docs.qq.com/doc/DS25rT2RPamNadUNp

### Test Loop Workflow — ALL COLLECT, THEN ANALYZE

> **核心原则：先全量采集，再统一分析，最后一次性修改。**
> 绝对不要看一台改一台。逐台修复会制造"按下葫芦浮起瓢"的兼容性问题。
> 每一轮修改必须基于所有设备的完整结果矩阵。

```
 [1] Develop locally (edit Go / YAML)
      │
 [2] Build: 一次性交叉编译所有目标
      │  go build -o build/aima-darwin-arm64 ./cmd/aima                               # dev-mac (local)
      │  GOOS=windows GOARCH=amd64 go build -o build/aima.exe          ./cmd/aima    # test-win
      │  GOOS=linux   GOARCH=arm64 go build -o build/aima-linux-arm64  ./cmd/aima    # gb10
      │  GOOS=linux   GOARCH=amd64 go build -o build/aima-linux-amd64  ./cmd/aima    # linux-1, amd395, hygon
      │
 [3] Distribute: 同步到所有远程机器
      │  scp build/aima.exe          jguan@100.114.25.35:~/aima.exe        # test-win
      │  scp build/aima-linux-arm64  qujing@100.105.58.16:~/aima
      │  scp build/aima-linux-amd64  cjwx@100.121.255.97:~/aima
      │  scp build/aima-linux-amd64  quings@100.71.145.56:~/aima
      │  scp build/aima-linux-amd64  qujing@100.113.47.73:~/aima
      │  scp build/aima-linux-arm64  qjq2:~/aima                          # qjq2 (reuses gb10's arm64 binary)
      │  scp build/aima-linux-arm64  dev@100.123.212.6:~/aima              # m1000 (arm64)
      │  scp build/aima-linux-amd64  kylin@100.94.119.128:~/aima          # metax-n260 (amd64)
      │  scp build/aima-linux-arm64  aibook@100.106.164.54:~/aima         # aibook (arm64)
      │  scp -P 21985 build/aima-linux-amd64 root@36.151.243.68:~/aima   # w7900d (amd64)
      │  scp build/aima-linux-arm64  qujing@100.91.39.109:~/aima           # gb10-4T (arm64)
      │
 [4] Execute: 对所有设备（含本机）并行执行同一组测试命令
      │  本机:  build/aima-darwin-arm64 hal detect
      │  SSH:   ssh jguan@100.114.25.35      'aima.exe hal detect'         # test-win
      │  SSH:   ssh qujing@100.105.58.16     './aima hal detect'
      │  SSH:   ssh cjwx@100.121.255.97      './aima hal detect'
      │  SSH:   ssh quings@100.71.145.56     './aima hal detect'
      │  SSH:   ssh qujing@100.113.47.73     './aima hal detect'
      │  SSH:   ssh qjq2                        './aima hal detect'
      │  SSH:   ssh dev@100.123.212.6          './aima hal detect'
      │  SSH:   ssh kylin@100.94.119.128       './aima hal detect'          # metax-n260
      │  SSH:   ssh aibook@100.106.164.54      './aima hal detect'          # aibook
      │  SSH:   ssh -p 21985 root@36.151.243.68 './aima hal detect'        # w7900d
      │  SSH:   ssh qujing@100.91.39.109       './aima hal detect'          # gb10-4T
      │
      ╔══════════════════════════════════════════════════════════╗
      ║  ⚠ BARRIER: 等待所有设备返回结果，一台都不能少。       ║
      ║  如果某台超时/不可达，记录为 UNREACHABLE，不要跳过。    ║
      ╚══════════════════════════════════════════════════════════╝
      │
 [5] Collect: 将所有结果汇总为对比矩阵
      │
      │  ┌──────────┬──────────────┬──────────┬──────────────┐
      │  │ 测试项    │ dev-mac      │ test-win │ gb10   │ ... │
      │  ├──────────┼──────────────┼──────────┼──────────────┤
      │  │ hal detect│ ✅ no-gpu   │ ✅ RTX4060│ ❌ N/A parse│
      │  │ engine ls │ ✅           │ ✅       │ ✅          │
      │  │ ...       │              │          │              │
      │  └──────────┴──────────────┴──────────┴──────────────┘
      │
 [6] Analyze: 基于完整矩阵统一分析
      │  - 哪些设备通过、哪些失败、失败模式是否相同
      │  - 是否存在仅在某一架构上出现的 edge case
      │  - 修复方案是否对所有设备都安全（不能只修一个平台）
      │
 [7] Fix: 一次性提交修改，修改必须覆盖所有已知平台
      │
 [8] Re-verify: 回到 [2]，再次全量验证，直到矩阵全绿
```

### Standard Test Commands

```bash
# --- Device probe (first time or hardware change) ---
ssh <user@host> 'uname -a && cat /etc/os-release 2>/dev/null; sw_vers 2>/dev/null; nvidia-smi 2>/dev/null || echo no-nvidia; free -h 2>/dev/null; df -h / | tail -1'

# --- Smoke test suite (run same commands on EVERY device) ---
./aima version                # or ssh <user@host> './aima version'
./aima hal detect
./aima engine list
./aima model list
./aima deploy list            # only meaningful on K3S-capable devices
```

### Adding a New Machine

1. Ensure SSH key auth works: `ssh-copy-id <user@host>`
2. SSH in and run the device probe command above
3. Update the Machine Registry table with the results
4. Determine the correct `GOOS/GOARCH` for cross-compilation
5. Add the machine to the sync & test scripts

### Conventions

- **Never store passwords in this file or any tracked file.** Use SSH keys only.
- **Cross-compile locally.** Don't install Go on remote machines — AIMA has zero CGO, so cross-compilation always works.
- **Test results are ephemeral.** Don't commit raw test outputs. Summarize findings in commit messages or design docs.
- **One binary per arch.** Build outputs go to `build/` (gitignored). Name pattern: `aima-{os}-{arch}`.

---

## Git Flow & Version Management

This project uses **Git Flow** branching model. Current version: **v0.3.x** (pre-release).

```
master ──●──── tag v0.0.1 ──────── tag v0.2.0 ──
          \                        /
develop ───●──●──●──●──feature──●──●
                   \           /
                    feat/xxx──●
```

| Branch | Purpose | Merges to |
|--------|---------|-----------|
| `master` | Production releases only. Every commit = a tagged release. | — |
| `develop` | Integration branch. Daily development lands here. | `master` (via release) |
| `feat/<name>` | New features. Branch from `develop`. | `develop` (via PR) |
| `fix/<name>` | Bug fixes for develop. Branch from `develop`. | `develop` (via PR) |
| `release/<ver>` | Release prep (version bump, final fixes). Branch from `develop`. | `master` + `develop` |
| `hotfix/<ver>` | Urgent fix for production. Branch from `master`. | `master` + `develop` |

### Version Numbering (SemVer)

- **0.0.1** — Initial foundation release (hardware detection, 94 MCP tools, multi-runtime)
- **0.2.0** — Support service, Web UI redesign, OpenClaw integration
- **0.3.0** — Edge Intelligence: OpenClaw full-stack, smart agent routing, RDNA3 support, major refactoring
- **0.4.0** — Next milestone: expanded catalog, agent orchestration maturity
- **1.0.0** — Production-ready, stable API contract

### Daily Workflow

```bash
# Start a new feature
git checkout develop && git pull origin develop
git checkout -b feat/my-feature

# ... develop, commit ...

# Push and create PR to develop
git push -u origin feat/my-feature
# Create PR: feat/my-feature → develop
```

### Release Workflow

```bash
# Prepare release
git checkout develop
git checkout -b release/v0.0.2

# Version bump, final fixes, then merge to master
git checkout master
git merge --no-ff release/v0.0.2
git tag -a v0.0.2 -m "Release v0.0.2"
git push origin master --tags

# Back-merge to develop
git checkout develop
git merge --no-ff release/v0.0.2
git branch -d release/v0.0.2
```

### Build with Version Info

```bash
VERSION=$(git describe --tags --always)
COMMIT=$(git rev-parse --short HEAD)
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS="-X github.com/jguan/aima/internal/cli.Version=$VERSION \
         -X github.com/jguan/aima/internal/cli.GitCommit=$COMMIT \
         -X github.com/jguan/aima/internal/cli.BuildTime=$BUILD_TIME"

go build -ldflags "$LDFLAGS" -o build/aima ./cmd/aima
```

### Rules for Claude Code

- **Never commit directly to master.** Always branch from `develop`.
- **Never force-push to master or develop.** These are protected branches.
- **Feature branches merge to develop only.** Only release/hotfix branches touch master.
- **Tag every master merge** with the version number.

---

## Roadmap & Gap Analysis

**Gap analysis document: `design/v1.0-gap-analysis.md`** — full PRD v1.0 vs current codebase comparison.
Before starting any v1.0-targeted work, read that doc first. Keep it updated as gaps are closed.
**Changelog: `CHANGELOG.md`** — release history with all changes per version.

### Current State (v0.3.0)

56 MCP tools, 3 runtimes (K3S/Docker/Native), 11 hardware profiles, 27 engine YAMLs, 25 model YAMLs, 3 deployment scenarios.
OpenClaw full-stack integration (local TTS cloning end-to-end). Smart Agent routing with model ranking.
Engine Profile system with SGLang-KT support. AMD RDNA3 (W7900D) 8-GPU validated.
God file refactor: `cmd/aima/main.go` split into 46 modules. ZeroClaw removal.
Embedded Web UI with per-card GPU metrics, collapsible panels, multi-socket CPU fix.
Central knowledge server (`cmd/central`). TUI dashboard (Bubble Tea). ResourceSlot abstraction.
Knowledge query engine complete (6 query types). Agent: patrol + self-healing with auto-diagnosis/recovery.
L2c golden config injection in resolve chain. Time constraint engine filtering.

### v0.3.0 Completed — "Edge Intelligence"

Focus: OpenClaw full-stack integration, smart agent routing, Engine Profile system, RDNA3 support, god file refactor, catalog expansion.

| Task | PRD IDs | Status | Key Files |
|------|---------|--------|-----------|
| Parse `startup_time_s` / `cold_start_time_s` in model variant loader | K4, D5 | **DONE** | `knowledge/loader.go` |
| Surface `cold_start_s` + time fields in `ResolvedConfig` | D5, A5 | **DONE** | `knowledge/resolver.go` |
| Power budget warning in `CheckFit()` (compare `tdp_watts` vs deployment) | S3, F4 | **DONE** | `knowledge/resolver.go` |
| L2c auto-promote: after benchmark, promote best config automatically | K5 | **DONE** | `cmd/aima/main.go`, `internal/sqlite.go` |
| Resource estimation in dry-run response (predicted VRAM/RAM cost) | S4 | **DONE** | `knowledge/resolver.go`, `cmd/aima/main.go` |
| Power monitoring endpoint (`GET /api/v1/power`) | F4 | **DONE** | `cmd/aima/main.go` |
| Patrol status/alerts/config MCP tools | A2 | **DONE** | `mcp/tools.go`, `cmd/aima/main.go` |
| Auto-tuning start/status/stop/results MCP tools | A3 | **DONE** | `mcp/tools.go`, `cli/tuning.go` |
| Self-healing patrol loop scaffolding | A4 | **DONE** | `cmd/aima/main.go` |
| Engine switch cost evaluation tool | A5, D5 | **DONE** | `mcp/tools.go`, `cmd/aima/main.go` |
| Performance reference in dry-run + perf overlay (K5) | K4, K5 | **DONE** | `cmd/aima/main.go` |
| Knowledge validation tool (predicted vs actual) | F5 | **DONE** | `mcp/tools.go`, `cmd/aima/main.go` |
| Power history tracking + MCP tool | F4 | **DONE** | `cmd/aima/main.go`, `sqlite.go` |
| TUI terminal dashboard (Bubble Tea) | F6 | **DONE** | `internal/tui/tui.go`, `cli/tui.go` |
| Central knowledge server (SQLite + REST) | K9 | **DONE** | `internal/central/server.go`, `cmd/central/main.go` |
| Knowledge sync push/pull/status MCP tools | K6 | **DONE** | `mcp/tools.go`, `cmd/aima/main.go` |
| Open questions resolution from YAML | I6 | **DONE** | `mcp/tools.go`, `cmd/aima/main.go` |
| App register/provision/list MCP tools | D4 | **DONE** | `mcp/tools.go`, `cli/app.go` |
| Power mode query MCP tool | S3 | **DONE** | `mcp/tools.go`, `cmd/aima/main.go` |
| ResourceSlot abstraction (4 backends) | S5 | **DONE** | `internal/runtime/slot.go` |
| Expand catalog: more model YAMLs for modalities | Product metrics | TODO | `catalog/models/` |

### v1.0 Gap Summary (for reference)

| Category | Complete | Partial | Missing |
|----------|----------|---------|---------|
| Supply (S1-S6) | S1, S2 | S3, S4, S5 | S6 |
| Demand (D1-D5) | D1, D2, D3, D5 | D4 | — |
| Knowledge (K1-K9) | K1, K2, K3, K5, K6, K7, K8, K9 | K4 | — |
| Control (A1-A5) | A1 | A2, A3, A4, A5 | — |
| Feedback (F1-F6) | F1, F2, F3 | F4, F5, F6 | — |
| Infrastructure (I1-I6) | I1, I2, I3, I4 | I5, I6 | — |

See `design/v1.0-gap-analysis.md` §5 for the full 3-tier implementation roadmap.

---

## The Prime Directive: Less Code

**Every line of Go code is a liability.** The goal is the smallest possible binary that glues
mature external tools (K3S, HAMi, containerd, SQLite) together with YAML knowledge.

- Before writing code, ask: "Can this be a YAML knowledge file instead?"
- Before adding a function, ask: "Does an existing tool/library already do this?"
- Before adding an abstraction, ask: "Do I have 3+ concrete uses, or am I guessing?"
- Before adding error handling, ask: "Can this actually happen, or am I being defensive?"
- 80% of capability expansion = writing YAML, not Go code.

## Architecture Invariants (Never Violate)

Read `design/ARCHITECTURE.md` §14 for full list. The critical ones:

1. **INV-1/2: No code branches for engine/model types.** Engine behavior = YAML. Model metadata = YAML.
   Adding a new engine or model = writing YAML, zero Go code.
2. **INV-3: Don't manage container lifecycle.** K3S does it. AIMA only does: apply / get / delete / logs.
3. **INV-5: MCP tools are the single source of truth.** CLI wraps MCP tools. CLI never has logic
   that MCP tools don't. Agent and human always walk the same code path.
4. **INV-8: Offline-first.** All core functions must work with zero network. Network = enhancement, not requirement.

## Project Structure

```
cmd/aima/main.go              # Edge binary entry point
cmd/central/main.go           # Central knowledge server entry point
internal/
  hal/                        # Hardware detection (nvidia-smi, /proc)
  k3s/                        # K3S client (kubectl wrapper)
  proxy/                      # HTTP inference proxy (OpenAI-compatible)
  knowledge/                  # go:embed YAML + SQLite relational loader + L0-L3 resolver
                              #   + query engine (query.go) + vector similarity (similarity.go)
                              #   + Pod YAML generator (dynamic GPU resource names)
  runtime/                    # Multi-Runtime: K3S (Pod) + Docker (container) + Native (exec + warmup)
  state/                      # SQLite (modernc.org/sqlite, zero CGO) — v2: 16 tables
  model/                      # Model scan/download/import
  engine/                     # Engine image scan/pull/import + native binary manager
  stack/                      # Tiered stack installer (Docker/CTK/K3S/HAMi, archive/binary/helm, airgap)
  benchmark/                  # Live benchmark runner (SSE streaming, concurrency, percentile stats)
  mcp/                        # MCP server + 56 tool implementations
  agent/                      # Go Agent loop (L3a) + Dispatcher
  cli/                        # Cobra commands (thin wrappers over MCP tools)
  ui/                         # Embedded Web UI (go:embed, Alpine.js SPA on :6188/ui/)
  tui/                        # Terminal dashboard (Bubble Tea, lipgloss)
  central/                    # Central knowledge aggregation server (SQLite + REST)
catalog/                      # Knowledge assets (go:embed, 编译时嵌入)
  embed.go
  hardware/                   # Hardware Profile YAML (incl. gpu.resource_name)
  engines/                    # Engine Asset YAML (incl. source, warmup)
  models/                     # Model Asset YAML
  partitions/                 # Partition Strategy YAML
  stack/                      # Stack Component YAML (K3S, HAMi — install config + airgap sources)
  scenarios/                  # Deployment Scenario YAML (multi-model deployment recipes)
# Runtime overlay: ~/.aima/catalog/{hardware,engines,models,partitions,stack,scenarios}/*.yaml
#   同名 metadata.name 覆盖 go:embed, 新名追加。无需重编译。
```

## Key Commands

```bash
go build ./cmd/aima               # Build
go test ./...                      # Test all
go test -race ./...                # Test with race detector
go vet ./...                       # Static analysis
```

## Go Conventions for This Project

- **Zero CGO.** SQLite via `modernc.org/sqlite`. No C dependencies, ever.
- **Standard library first.** `net/http` not gin/echo. `log/slog` not zap/logrus. `encoding/json` not jsoniter.
- **Errors wrap with context:** `fmt.Errorf("resolve config for %s: %w", model, err)`.
- **Context as first param.** Every function that does I/O takes `context.Context`.
- **Interfaces at consumer, not provider.** Define interfaces where they're used, not where they're implemented.
- **Functional options for config:** `NewServer(addr, WithTimeout(5*time.Second))`.
- **No init(), no global state.** Everything is dependency-injected via struct constructors.
- **Table-driven tests.** Use `testdata/` for fixtures.

## Design Patterns to Follow

### The "Thin CLI" Pattern
Every CLI command is a thin wrapper: parse flags → call MCP tool function → format output.
CLI never contains business logic. If you need new logic, add it as an MCP tool first.

```go
// CORRECT: CLI calls MCP tool
func runDeploy(cmd *cobra.Command, args []string) error {
    return mcpTools.DeployApply(ctx, engine, model, slot)
}

// WRONG: CLI contains logic
func runDeploy(cmd *cobra.Command, args []string) error {
    hw := hal.Detect()
    config := knowledge.Resolve(hw, model)
    pod := knowledge.GeneratePod(config)
    return k3s.Apply(pod)  // This logic belongs in deploy.apply MCP tool
}
```

### The "Knowledge-Driven" Pattern
Don't hardcode behaviors per engine/model. Load them from YAML:

```go
// CORRECT: Knowledge-driven
engineAsset, _ := knowledge.FindEngine(engineType, gpuArch)
pod := podgen.Render(engineAsset, modelAsset, partitionSlot)

// WRONG: Code-driven
if engineType == "vllm" {
    pod.Image = "vllm/vllm-openai:latest"
    pod.Command = []string{"vllm", "serve"}
} else if engineType == "llamacpp" {
    // ...more branches for each engine...
}
```

### The "Graceful Degradation" Pattern
Every feature must handle absence of its dependencies:

```go
// L3a unavailable → fall back to L2 → fall back to L0
func (d *Dispatcher) Ask(ctx context.Context, query string) (string, error) {
    if d.goAgent.Available() {
        return d.goAgent.Ask(ctx, query)
    }
    return d.knowledgeResolve(ctx, query)  // L2 deterministic
}
```

## What NOT to Do

- **Don't write strategy/policy code in Go.** That's the Agent's job via MCP tools.
- **Don't add engine-specific or model-specific if/switch branches.** Use YAML knowledge.
- **Don't manage container lifecycle.** K3S handles health checks, restarts, resource limits.
- **Don't create abstractions "for the future."** Three concrete uses before abstracting.
- **Don't add comments to code you didn't change.** Don't add docstrings unless the function is exported and non-obvious.
- **Don't create wrapper types around standard library types.** Use `*sql.DB` directly, not `type Database struct { db *sql.DB }` unless there's a real reason.
- **Don't add metrics/tracing/logging infrastructure preemptively.** `slog.Info()` is enough until proven otherwise.
- **Don't create separate files for single types or tiny functions.** Keep related code together.

## Workflow

1. **Read before writing.** Always read existing code before modifying. Understand the pattern first.
2. **Architecture doc is source of truth.** When in doubt, consult `design/ARCHITECTURE.md`.
3. **Test what matters.** Test business logic and edge cases. Don't test that Go's JSON marshaling works.
4. **One MCP tool = one function = one responsibility.** Keep tool implementations focused.
5. **Commit atomically.** Each commit should be a coherent, working unit.
6. **Branch from develop.** Never commit directly to master. Feature branches merge to develop via PR.

## Domain Terminology

| Term | Meaning |
|------|---------|
| Engine Asset | YAML describing an inference engine (vLLM, llama.cpp, etc) on specific hardware |
| Model Asset | YAML describing a model's variants across hardware/engine combos |
| Hardware Profile | YAML describing a device's GPU/CPU/RAM capability vector |
| Partition Strategy | YAML describing how to split resources across multiple workloads |
| Knowledge Note | Structured record of Agent exploration results (trials + recommendation) |
| Configuration | A tested Hardware×Engine×Model×Config instance with derivation chain |
| BenchmarkResult | Multi-dimensional performance data for a Configuration under specific load |
| PerfVector | 6-dimensional normalized performance vector for similarity search |
| L0/L1/L2/L3a | Progressive intelligence levels: defaults → human CLI → knowledge → Go Agent |
| ConfigResolver | Merges L0-L3 configs, higher layer overrides lower |
| Store | Knowledge query engine wrapping *sql.DB (Search/Compare/Gaps/Similar/Lineage/Aggregate) |
| MCP Tool | JSON-RPC function exposed to Agents (deploy.apply, model.scan, etc) |
