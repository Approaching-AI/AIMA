# AIMA — Development Guide

## What Is This

AIMA (AI-Inference-Managed-by-AI): a Go binary that manages AI inference on edge devices.
It detects hardware, resolves optimal configs from a YAML knowledge base, generates K3S Pod YAML,
and exposes 79 MCP tools for AI Agents to operate everything. **This project is 100% developed by Claude Code.**

Tech: Go (no CGO), K3S, HAMi, SQLite (modernc.org/sqlite), MCP (JSON-RPC 2.0), Cobra CLI, log/slog.
Design docs: `design/ARCHITECTURE.md` (system architecture), `design/PRD.md`, `design/MRD.md`.

## Git Flow & Version Management

This project uses **Git Flow** branching model. Current version: **v0.0.x** (pre-release).

```
master ──●──── tag v0.0.1 ────────────────── tag v0.0.2 ──
          \                                  /
develop ───●──●──●──●──feature──●──●──●──●──●
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

- **0.0.x** — Current phase: foundational features, API not stable
- **0.1.0** — First feature-complete milestone (all core MCP tools working)
- **1.0.0** — Production-ready, stable API contract

### Daily workflow

```bash
# Start a new feature
git checkout develop
git pull origin develop
git checkout -b feat/my-feature

# ... develop, commit ...

# Push and create PR to develop
git push -u origin feat/my-feature
# Create PR: feat/my-feature → develop

# After PR merged, clean up
git checkout develop
git pull origin develop
git branch -d feat/my-feature
```

### Release workflow

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

### Build with version info

```bash
VERSION=$(git describe --tags --always)
COMMIT=$(git rev-parse --short HEAD)
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS="-X github.com/jguan/aima/internal/cli.Version=$VERSION \
         -X github.com/jguan/aima/internal/cli.GitCommit=$COMMIT \
         -X github.com/jguan/aima/internal/cli.BuildTime=$BUILD_TIME"

go build -ldflags "$LDFLAGS" -o build/aima ./cmd/aima
```

### Rules for AI Agents

- **Never commit directly to master.** Always branch from `develop`.
- **Never force-push to master or develop.** These are protected branches.
- **Feature branches merge to develop only.** Only release/hotfix branches touch master.
- **Tag every master merge** with the version number.

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
  runtime/                    # Multi-Runtime: K3S (Pod) + Docker (container) + Native (exec + warmup)
  state/                      # SQLite (modernc.org/sqlite, zero CGO) — v2: 16 tables
  model/                      # Model scan/download/import
  engine/                     # Engine image scan/pull/import + native binary manager
  stack/                      # Tiered stack installer (Docker/CTK/K3S/HAMi, archive/binary/helm, airgap)
  benchmark/                  # Live benchmark runner (SSE streaming, concurrency, percentile stats)
  mcp/                        # MCP server + 61 tool implementations
  agent/                      # Go Agent loop (L3a) + Dispatcher
  zeroclaw/                   # ZeroClaw lifecycle manager (optional L3b sidecar)
  cli/                        # Cobra commands (thin wrappers over MCP tools)
  ui/                         # Embedded Web UI (go:embed, Alpine.js SPA on :6188/ui/)
catalog/                      # Knowledge assets (go:embed, compiled in)
  embed.go
  hardware/                   # Hardware Profile YAML (incl. gpu.resource_name)
  engines/                    # Engine Asset YAML (incl. source, warmup)
  models/                     # Model Asset YAML
  partitions/                 # Partition Strategy YAML
  stack/                      # Stack Component YAML (K3S, HAMi — install config + airgap sources)
# Runtime overlay: ~/.aima/catalog/{hardware,engines,models,partitions,stack}/*.yaml
#   Same metadata.name overrides go:embed, new names append. No recompilation needed.
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
Every CLI command is a thin wrapper: parse flags -> call MCP tool function -> format output.
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
// L3b unavailable -> fall back to L3a -> fall back to L2 -> fall back to L0
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
6. **Branch from develop.** Never commit directly to master. Feature branches merge to develop via PR.

## Domain Terminology

| Term | Meaning |
|------|---------|
| Engine Asset | YAML describing an inference engine (vLLM, llama.cpp, etc) on specific hardware |
| Model Asset | YAML describing a model's variants across hardware/engine combos |
| Hardware Profile | YAML describing a device's GPU/CPU/RAM capability vector |
| Partition Strategy | YAML describing how to split resources across multiple workloads |
| Knowledge Note | Structured record of Agent exploration results (trials + recommendation) |
| Configuration | A tested Hardware x Engine x Model x Config instance with derivation chain |
| BenchmarkResult | Multi-dimensional performance data for a Configuration under specific load |
| PerfVector | 6-dimensional normalized performance vector for similarity search |
| L0/L1/L2/L3a/L3b | Progressive intelligence levels: defaults -> human CLI -> knowledge -> Go Agent -> ZeroClaw |
| ConfigResolver | Merges L0-L3 configs, higher layer overrides lower |
| Store | Knowledge query engine wrapping *sql.DB (Search/Compare/Gaps/Similar/Lineage/Aggregate) |
| MCP Tool | JSON-RPC function exposed to Agents (deploy.apply, model.scan, etc) |

## MCP Tools for Benchmarking & Knowledge Transfer

### Benchmark Workflow (Agent Guidance)

After deploying a model, establish performance baselines using the benchmark tools:

```
1. benchmark.run   — Single benchmark run against a deployed model
                     Auto-detects endpoint from proxy; measures TTFT/TPOT/TPS
                     Results auto-saved to DB when hardware + engine provided

2. benchmark.matrix — Test matrix: concurrency × input_len × output_len
                      Runs benchmark.run for each combination sequentially
                      Use for comprehensive performance characterization

3. benchmark.list   — Query historical benchmark results
                      Filter by model, hardware, engine, or config ID

4. benchmark.record — Manually record external benchmark measurements
```

### Knowledge Export/Import Workflow (Agent Guidance)

Share tuning knowledge across devices using export/import:

```
1. knowledge.export — Export configs + benchmarks + notes to JSON
                      Filter by --hardware, --model, --engine
                      No filter = full DB dump
                      Output to file (--output) or stdout

2. knowledge.import — Import from JSON file
                      Conflict: skip (default) | overwrite
                      Supports --dry-run preview
                      Atomic transaction (all-or-nothing)
```

Typical cross-device flow:
```bash
# On device A (has benchmark data)
aima knowledge export --hardware nvidia-gb10-arm64 -o gb10.json

# Transfer file to device B
scp gb10.json user@device-b:/tmp/

# On device B (import knowledge)
aima knowledge import -i /tmp/gb10.json --dry-run   # preview first
aima knowledge import -i /tmp/gb10.json              # commit
```

Export JSON format (schema_version: 1):
```json
{
  "schema_version": 1,
  "data": {
    "configurations": [...],    // Hardware×Engine×Model configs
    "benchmark_results": [...], // Performance measurements (FK → configurations)
    "knowledge_notes": [...]    // Agent exploration notes
  },
  "stats": { "configurations": N, "benchmark_results": N, "knowledge_notes": N }
}
```
