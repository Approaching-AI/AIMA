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
| dev-win | **local** (Light-Salt) | Windows 11 | x86_64 | i9-13980HX + RTX 4060 8GB (Driver 566, CUDA) | 32 GB | 551 GB | no | local | Dev machine, `go build/test` runs here directly |
| mac-m4 | `guanjiawei@100.125.202.50` (Tailscale) / `guanjiawei@192.168.108.250` (LAN) | macOS 26.2 | arm64 | Apple M4 | 16 GB | 393 GB | no | key | Apple Silicon validation |
| gb10 | `qujing@100.105.58.16` | Ubuntu 24.04 | aarch64 | NVIDIA GB10 (CUDA 13.0, Driver 580) | 120 GB unified | 149 GB | K3S v1.31.4 + Docker 28.5 | key | GPU inference + K3S full-stack validation |
| linux-1 | `cjwx@100.121.255.97` (Tailscale) / `cjwx@192.168.109.23` (LAN) | Ubuntu 22.04 | x86_64 | 2× NVIDIA RTX 4090 48GB (Driver 580, CUDA 13.0) | 503 GB | 72 GB | Docker | key | Dual-GPU inference validation |
| amd395 | `quings@100.71.145.56` (Tailscale) | Ubuntu 24.04 | x86_64 | AMD Ryzen AI MAX+ 395 + Radeon 8060S (no NVIDIA) | 62 GB | 57 GB | Docker 28.2 | key | AMD/APU inference validation |

> **Maintaining this table:** After first SSH to a new machine, run the device probe and update this table.
> Password: never store passwords here. Use SSH key auth. For initial key setup: `ssh-copy-id <user@host>`.

### Test Loop Workflow — ALL COLLECT, THEN ANALYZE

> **核心原则：先全量采集，再统一分析，最后一次性修改。**
> 绝对不要看一台改一台。逐台修复会制造"按下葫芦浮起瓢"的兼容性问题。
> 每一轮修改必须基于所有设备的完整结果矩阵。

```
 [1] Develop locally (edit Go / YAML)
      │
 [2] Build: 一次性交叉编译所有目标
      │  go build -o build/aima.exe ./cmd/aima                                       # dev-win
      │  GOOS=darwin  GOARCH=arm64 go build -o build/aima-darwin-arm64  ./cmd/aima   # mac-m4
      │  GOOS=linux   GOARCH=arm64 go build -o build/aima-linux-arm64   ./cmd/aima   # gb10
      │  GOOS=linux   GOARCH=amd64 go build -o build/aima-linux-amd64   ./cmd/aima   # linux-1
      │
 [3] Distribute: 同步到所有远程机器
      │  scp build/aima-darwin-arm64 guanjiawei@100.125.202.50:~/aima
      │  scp build/aima-linux-arm64  qujing@100.105.58.16:~/aima
      │  scp build/aima-linux-amd64  cjwx@100.121.255.97:~/aima
      │
 [4] Execute: 对所有设备（含本机）并行执行同一组测试命令
      │  本机:  build/aima.exe hal detect
      │  SSH:   ssh guanjiawei@100.125.202.50 './aima hal detect'
      │  SSH:   ssh qujing@100.105.58.16     './aima hal detect'
      │  SSH:   ssh cjwx@100.121.255.97      './aima hal detect'
      │
      ╔══════════════════════════════════════════════════════════╗
      ║  ⚠ BARRIER: 等待所有设备返回结果，一台都不能少。       ║
      ║  如果某台超时/不可达，记录为 UNREACHABLE，不要跳过。    ║
      ╚══════════════════════════════════════════════════════════╝
      │
 [5] Collect: 将所有结果汇总为对比矩阵
      │
      │  ┌──────────┬──────────────┬──────────┬──────────────┐
      │  │ 测试项    │ dev-win      │ mac-m4   │ gb10   │ ... │
      │  ├──────────┼──────────────┼──────────┼──────────────┤
      │  │ hal detect│ ✅ RTX 4060 │ ✅ no-gpu│ ❌ N/A parse│
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
cmd/aima/main.go              # Entry point
internal/
  hal/                        # Hardware detection (nvidia-smi, /proc)
  k3s/                        # K3S client (kubectl wrapper)
  proxy/                      # HTTP inference proxy (OpenAI-compatible)
  knowledge/                  # go:embed YAML + SQLite relational loader + L0-L3 resolver
                              #   + query engine (query.go) + vector similarity (similarity.go)
                              #   + Pod YAML generator (dynamic GPU resource names)
  runtime/                    # Multi-Runtime: K3S (Pod) + Native (exec + warmup)
  state/                      # SQLite (modernc.org/sqlite, zero CGO) — v2: 16 tables
  model/                      # Model scan/download/import
  engine/                     # Engine image scan/pull/import + native binary manager
  stack/                      # Infrastructure stack installer (K3S + HAMi, airgap, parallel downloads)
  mcp/                        # MCP server + 56 tool implementations
  agent/                      # Go Agent loop (L3a) + Dispatcher
  zeroclaw/                   # ZeroClaw lifecycle manager (optional L3b sidecar)
  cli/                        # Cobra commands (thin wrappers over MCP tools)
catalog/                      # Knowledge assets (go:embed, 编译时嵌入)
  embed.go
  hardware/                   # Hardware Profile YAML (incl. gpu.resource_name)
  engines/                    # Engine Asset YAML (incl. source, warmup)
  models/                     # Model Asset YAML
  partitions/                 # Partition Strategy YAML
  stack/                      # Stack Component YAML (K3S, HAMi — install config + airgap sources)
# Runtime overlay: ~/.aima/catalog/{hardware,engines,models,partitions,stack}/*.yaml
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
// L3b unavailable → fall back to L3a → fall back to L2 → fall back to L0
func (d *Dispatcher) Ask(ctx context.Context, query string) (string, error) {
    if d.zeroclaw.Available() && d.isComplex(query) {
        return d.zeroclaw.Ask(ctx, query)
    }
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
| L0/L1/L2/L3a/L3b | Progressive intelligence levels: defaults → human CLI → knowledge → Go Agent → ZeroClaw |
| ConfigResolver | Merges L0-L3 configs, higher layer overrides lower |
| Store | Knowledge query engine wrapping *sql.DB (Search/Compare/Gaps/Similar/Lineage/Aggregate) |
| MCP Tool | JSON-RPC function exposed to Agents (deploy.apply, model.scan, etc) |
