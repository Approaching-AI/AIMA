# AIMA Development Lessons

> Distilled from 7 memory files + 6 months of multi-device development. Cross-cutting Go/YAML engineering patterns.

## 1. YAML-Driven Architecture (INV-1/2 Core)

### Rule: 80% of capability expansion = writing YAML, not Go code

**Anti-pattern: Engine/model-specific code branches**
```go
// WRONG: Go code that branches on engine type
if engineType == "vllm" { ... } else if engineType == "sglang" { ... }
```
**Correct: Knowledge-driven resolution**
```go
// CORRECT: Engine behavior comes from YAML
engineAsset, _ := knowledge.FindEngine(engineType, gpuArch)
pod := podgen.Render(engineAsset, modelAsset, partitionSlot)
```

**Real examples of YAML solving code problems:**
- vLLM `served_model_name` issue → YAML template `"{{.ModelName}}"` + podgen expansion (not Go code)
- SGLang CuDNN check → engine YAML `startup.env: SGLANG_DISABLE_CUDNN_CHECK=1` (not Go code)
- CUDA Error 803 → Hardware Profile YAML `container.env` (not Go code)
- Docker mirror proxies → Stack YAML `mirror` field (not Go code)
- Tool call parser selection → Engine YAML `startup.default_args.tool_call_parser` (not Go code)

### Pattern: Vendor-neutral GPU abstraction
- Hardware Profile YAML `container` field drives GPU container access
- `ComputeCapability` → `ComputeID`, `CUDACores` → `ComputeUnits`, `CUDAVersion` → `SDKVersion`
- podgen.go has zero NVIDIA-specific logic; env merge = hardware env (base) + engine env (override)
- Empty GPU resources = no GPU request (not a fallback "all GPUs")

### Pattern: Model variant engine matching
- `variant.engine` matches `engine.metadata.type` (e.g. "sglang"), NOT `engine.metadata.name` (e.g. "sglang-universal")
- This is a frequent mistake — always verify which field is used for matching

## 2. Config Layer System (L0-L3)

### Layer precedence (lowest to highest):
```
L0: YAML defaults (engine/model asset startup.default_args)
L2: Golden configs from DB (queryGoldenOverrides, keyed by GPUArch)
L1: User overrides (CLI --config flags, human intent always wins)
L3: Agent-injected config (via MCP tools)
```

### Critical bug pattern: zero-value propagation
**P0 Bug**: `HardwareInfo.HardwareProfile` was never set by `buildHardwareInfo()` → golden config query with empty string → no hardware filtering → cross-device config injection (GB10 config on RTX 4090 → OOM).

**Lesson**: For every struct field, trace the assignment chain:
1. Where is it declared?
2. Who assigns it?
3. Is it populated at call-time?
4. What does zero-value mean? (empty string = "match all" is dangerous)

### Golden config merge timing: after resolve, not before
```
WRONG: merge golden + overrides → resolveWithFallback(merged)
RIGHT: resolveWithFallback(originalOverrides) → get canonicalName → query golden → write directly
```
Reason: need canonicalName (user may pass `Qwen3-8B`, catalog normalizes to `qwen3-8b`).

## 3. MCP Tool Design

### Thin CLI pattern (INV-5)
Every CLI command = parse flags → call MCP tool → format output. CLI never contains business logic.

**Fleet CLI trap**: CLI process is independent from `aima serve` — `fleet.Registry` is empty in CLI process. Must call localhost REST API, not in-process state.

### Agent safety guardrails
- **Destructive tools blocklist**: model.remove, engine.remove, deploy.delete
- **Parameter-level blocking**: `system.config` allows `value == nil` (read) but blocks `value != nil` (write)
- **Shell whitelist**: shell.exec only allows nvidia-smi, df, free, uname, cat /proc/cpuinfo, kubectl get/describe/logs/top/version
- **Fleet remote blocking**: fleet.exec_tool blocks same 7 destructive tools on remote execution

**Checklist for new MCP tools:**
1. Can Agent auto-call this tool safely? No → add to `blockedAgentTools`
2. Does it modify system state? Yes → require human approval
3. Does it expose remote execution? Yes → add to `fleetBlockedTools`
4. Are empty/malformed parameters safe? No → block as default (whitelist pattern)

### Tool signature evolution
- `StackPreflight/StackInit` gained `tier` parameter — default "docker" (backward-compatible)
- Always provide sensible defaults so existing callers don't break

## 4. Code Audit Methodology

### Reusable checklist (from L2 golden config audit + design principles audit):
1. **Zero-value safety**: Every new field — who assigns it? Zero-value semantic safe?
2. **Agent tool blocking**: Whitelist pattern? Empty params fallthrough safe?
3. **INV-1/2**: No engine/model type branches in Go?
4. **INV-5**: New logic → MCP tool first, CLI wraps it?
5. **Prime Directive**: Can this be YAML instead of Go?
6. **Graceful Degradation**: Dependency absent → safe fallback?
7. **Parameter count**: >5 params → consider struct (tech debt marker)

### Documentation sync
When changing ports, protocols, tool signatures, or API schemas:
- Search all `docs/*.md`, `design/*.md`, `CLAUDE.md`
- Update MEMORY.md index if the change affects cross-session context
- Run `go vet ./...` + affected package tests before commit

## 5. Runtime Selection Architecture

### Two-level selection (post-refactor):
1. **Default runtime** (global): K3S > Docker > Native
2. **Per-deployment** (engine YAML `RuntimeRecommendation`):
   - `"native"` → NativeRuntime (always available)
   - `"docker"` → DockerRuntime > NativeRuntime
   - `"k3s"` → K3SRuntime (required, error if absent)
   - `"container"` → K3S > Docker (partition needs K3S)
   - `"auto"` / `""` → default runtime

### Tiered stack init
- Tier 1 (`aima init`): Docker + nvidia-ctk + aima-serve
- Tier 2 (`aima init --k3s`): Tier 1 + K3S + HAMi
- `FilterByTier("docker")` → only tier=docker components
- `FilterByTier("k3s")` → tier=docker AND tier=k3s (superset)

## 6. Go-Specific Patterns

### Consumer-side interfaces (avoid circular deps)
```go
// fleet.handler.go defines the interface it needs
type MCPExecutor interface {
    ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error)
}
// main.go provides the adapter
```
Use `json.RawMessage` as the universal message type — saves ~30 lines of type conversion.

### Config hot-swap via RWMutex
```go
// OpenAIClient: SetEndpoint/SetModel protected by RWMutex
// Takes effect immediately — no process restart needed
```
Pattern: SQLite persistence + in-memory RWMutex hot-swap + REST endpoint for updates.

### resolveDeployment parameter bloat (Tech Debt)
13 parameters → candidate for `type deployContext struct {}`. Rule: mark as tech debt, refactor at 3+ call sites.

### Variadic params for optional behavior extension
When a function needs to support new optional behavior without breaking callers:
```go
// BEFORE: caller must inject variant logic externally (violates INV-5)
synth := cat.BuildSyntheticModelAsset(name, type, family, params, format)
// + 16 lines of variant injection in CLI layer

// AFTER: variadic param keeps backward-compat, logic stays in knowledge package
synth := cat.BuildSyntheticModelAsset(name, type, family, params, format, engineType)
```
Rule: If you're adding logic to CLI that should be in a library, check if a variadic param can absorb it.

### Boolean config flag conversion
Docker/native runtimes convert config map to CLI flags. Boolean values need special handling:
```go
// WRONG: --enable-chunked-prefill true (not a valid flag format)
// RIGHT: --enable-chunked-prefill (flag present = true, absent = false)
```
Pattern: `if v == "true" { args = append(args, "--"+k) }` — skip `false` values entirely.

### Docker ENTRYPOINT duplication (commit ce664cc)
**Bug**: YAML `command: ["vllm", "serve", "{{.ModelPath}}"]` + image ENTRYPOINT `["vllm", "serve"]` → Docker concatenates both → `vllm serve vllm serve /models`.
**Fix**: When YAML defines a command, use `--entrypoint command[0]` to override image ENTRYPOINT, then pass `command[1:]...` as CMD. Matches K3S `command:` semantics where YAML command replaces ENTRYPOINT entirely.
```go
// With init commands: --entrypoint bash image -c "init && exec main"
// With command:       --entrypoint cmd[0] image cmd[1:]...
// No command:         image (use image default)
```

### Template substitution in configToFlags output
**Bug**: `configToFlags()` outputs raw strings — `{{.ModelName}}` and `{{.ModelPath}}` not expanded in Docker/Native runtimes (K3S podgen.go did expand them).
**Fix**: Apply `strings.ReplaceAll` for `{{.ModelName}}` and `{{.ModelPath}}` on configToFlags output in both `docker.go` and `native.go`, after the `configToFlags()` call.
**Lesson**: When template variables appear in YAML config values, every runtime that consumes those values must expand them. Trace the full code path from YAML → runtime CLI args.
