# AIMA Full Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:dispatching-parallel-agents to implement this plan with parallel agent teams.

**Goal:** Implement the complete AIMA Go binary — hardware detection, knowledge system, K3S orchestration, MCP server, Agent loop, and CLI — from the existing PRD and architecture design.

**Architecture:** 11 internal Go packages + catalog YAML assets, compiled into a single cross-platform binary. Knowledge-driven design: YAML assets define engine/model behavior, Go code is glue. MCP tools are the single source of truth; CLI wraps them.

**Tech Stack:** Go 1.23+, modernc.org/sqlite, spf13/cobra, gopkg.in/yaml.v3, hashicorp/mdns, log/slog, net/http, text/template

---

## Execution Strategy: 4-Phase Parallel Teams

```
Phase 0: Foundation (sequential, main agent)
    │
    ├─── go.mod + dependencies
    ├─── Directory structure + all package stubs
    ├─── Catalog YAML sample files + embed.go
    └─── Shared domain types per package
    │
Phase 1: Core Modules (5 parallel agents)
    │
    ├─── Team A: internal/hal/ (hardware detection, cross-platform)
    ├─── Team B: internal/state/ (SQLite CRUD)
    ├─── Team C: internal/knowledge/ (loader + resolver + podgen)
    ├─── Team D: internal/k3s/ (K3S client wrapper)
    └─── Team E: internal/proxy/ (OpenAI-compatible HTTP proxy)
    │
Phase 2: Integration Modules (3 parallel agents)
    │
    ├─── Team F: internal/model/ + internal/engine/ (lifecycle management)
    ├─── Team G: internal/mcp/ (MCP server + 26 tool implementations)
    └─── Team H: internal/agent/ + internal/zeroclaw/ (Agent system)
    │
Phase 3: CLI + Entry Point + Integration (2 parallel agents)
    │
    ├─── Team I: internal/cli/ (all Cobra commands) + cmd/aima/main.go
    └─── Team J: Code review against PRD + Architecture
```

---

## Phase 0: Foundation

### Task 0.1: Project Skeleton

**Files to create:**
- `go.mod`
- `cmd/aima/main.go` (minimal stub)
- `catalog/embed.go`
- All `internal/*/` directory stubs with doc.go

### Task 0.2: Catalog YAML Assets

**Files to create:**
- `catalog/hardware/nvidia-gb10-arm64.yaml`
- `catalog/hardware/nvidia-rtx4090-x86.yaml`
- `catalog/engines/vllm-blackwell.yaml`
- `catalog/engines/llamacpp-universal.yaml`
- `catalog/models/glm-4.7-flash.yaml`
- `catalog/models/qwen3-8b.yaml`
- `catalog/partitions/gb10-dual-model.yaml`
- `catalog/partitions/single-model-default.yaml`

---

## Phase 1: Core Modules (Parallel)

### Team A: internal/hal/ — Hardware Detection

**Files:** `detect.go`, `detect_test.go`, `metrics.go`, `metrics_test.go`

**Cross-platform strategy:**
- Linux: nvidia-smi + /proc/cpuinfo + /proc/meminfo + /sys/class/powercap
- macOS: system_profiler + sysctl
- Windows: nvidia-smi + wmic/PowerShell

**Public API:**
```go
type HardwareInfo struct {
    GPU     GPUInfo
    CPU     CPUInfo
    RAM     RAMInfo
    Platform string // linux/amd64, darwin/arm64, windows/amd64
}

func Detect(ctx context.Context) (*HardwareInfo, error)
func Metrics(ctx context.Context) (*HardwareMetrics, error)
```

**TDD sequence:**
1. Test Detect() with mock nvidia-smi output → parse GPU info
2. Test CPU detection with mock /proc/cpuinfo → parse CPU info
3. Test RAM detection with mock /proc/meminfo
4. Test cross-platform fallbacks (no nvidia-smi → CPU-only mode)
5. Test Metrics() with mock nvidia-smi query output

### Team B: internal/state/ — SQLite State Store

**Files:** `sqlite.go`, `sqlite_test.go`

**Schema:** Per architecture §9.2 — models, engines, knowledge_notes, config, audit_log tables.

**Public API:**
```go
type Store struct { db *sql.DB }

func Open(ctx context.Context, dbPath string) (*Store, error)
func (s *Store) Close() error
// Models CRUD
func (s *Store) InsertModel(ctx, Model) error
func (s *Store) ListModels(ctx) ([]Model, error)
func (s *Store) GetModel(ctx, id) (*Model, error)
func (s *Store) UpdateModelStatus(ctx, id, status) error
func (s *Store) DeleteModel(ctx, id) error
// Engines CRUD
func (s *Store) InsertEngine(ctx, Engine) error
func (s *Store) ListEngines(ctx) ([]Engine, error)
// Knowledge Notes CRUD
func (s *Store) InsertKnowledgeNote(ctx, KnowledgeNote) error
func (s *Store) SearchKnowledgeNotes(ctx, query) ([]KnowledgeNote, error)
// Config
func (s *Store) GetConfig(ctx, key) (string, error)
func (s *Store) SetConfig(ctx, key, value) error
// Audit
func (s *Store) LogAudit(ctx, AuditEntry) error
```

**TDD sequence:**
1. Test Open() creates DB + schema (migration)
2. Test InsertModel + ListModels round-trip
3. Test SearchKnowledgeNotes by hardware/model/engine
4. Test Config get/set
5. Test AuditLog insert + query

### Team C: internal/knowledge/ — Knowledge System

**Files:** `loader.go`, `loader_test.go`, `resolver.go`, `resolver_test.go`, `podgen.go`, `podgen_test.go`, `types.go`

**Public API:**
```go
// Types matching YAML schema (architecture §4.2-4.6)
type HardwareProfile struct { ... }
type PartitionStrategy struct { ... }
type EngineAsset struct { ... }
type ModelAsset struct { ... }
type KnowledgeNote struct { ... }

// Loader
func LoadCatalog(fs embed.FS) (*Catalog, error)

// Resolver (L0→L3 merge)
type ResolvedConfig struct {
    Config    map[string]any
    Sources   map[string]string // key → "L0"/"L1"/"L2"
    Engine    *EngineAsset
    Model     *ModelAsset
    Partition *PartitionStrategy
    Slot      string
}
func (c *Catalog) Resolve(hw *hal.HardwareInfo, modelName, engineType, slot string, overrides map[string]any) (*ResolvedConfig, error)

// Pod Generator
func GeneratePod(rc *ResolvedConfig, modelPath string) ([]byte, error)
```

**TDD sequence:**
1. Test LoadCatalog parses all YAML types correctly
2. Test Resolve L0 only (no knowledge notes, no overrides)
3. Test Resolve L1 override (user CLI params override L0)
4. Test Resolve L2 (knowledge note overrides L0)
5. Test Resolve multi-layer merge (L0+L1+L2)
6. Test GeneratePod renders valid K3S Pod YAML
7. Test GeneratePod includes HAMi resource annotations

### Team D: internal/k3s/ — K3S Client

**Files:** `client.go`, `client_test.go`

**Public API:**
```go
type Client struct { kubeconfigPath string }

func NewClient(kubeconfigPath string) *Client
func (c *Client) Apply(ctx, yamlBytes []byte) error
func (c *Client) Delete(ctx, name string) error
func (c *Client) GetPod(ctx, name string) (*PodStatus, error)
func (c *Client) ListPods(ctx, labelSelector string) ([]PodStatus, error)
func (c *Client) Logs(ctx, podName string, lines int) (string, error)
```

**TDD sequence:**
1. Test Apply executes kubectl apply with correct args
2. Test Delete executes kubectl delete
3. Test GetPod parses kubectl JSON output
4. Test ListPods with label selector
5. Test Logs retrieves container logs
6. Test error handling (kubectl not found, pod not found)

### Team E: internal/proxy/ — HTTP Inference Proxy

**Files:** `handler.go`, `handler_test.go`, `router.go`, `router_test.go`

**Public API:**
```go
type Router struct { ... }

func NewRouter() *Router
func (r *Router) Register(modelName string, backend string) // backend = "http://pod-ip:8000"
func (r *Router) Unregister(modelName string)
func (r *Router) Handler() http.Handler // mounts /v1/chat/completions, /v1/models, etc.
```

**TDD sequence:**
1. Test Router.Register + route resolution
2. Test /v1/models returns registered model list
3. Test /v1/chat/completions proxies to correct backend
4. Test /v1/embeddings proxy
5. Test /health endpoint
6. Test model not found → 404

---

## Phase 2: Integration Modules (Parallel)

### Team F: internal/model/ + internal/engine/

**model/ files:** `scanner.go`, `scanner_test.go`, `manager.go`, `manager_test.go`
**engine/ files:** `scanner.go`, `scanner_test.go`, `manager.go`, `manager_test.go`

**Model scanner:** Detect safetensors (config.json), GGUF (header magic), Ollama (manifest.json).
**Engine scanner:** Query containerd images, match against Engine Asset YAML.

**TDD sequence:**
1. Test model scanner identifies safetensors directory
2. Test model scanner identifies GGUF file (magic bytes)
3. Test model manager registers/lists/removes via Store
4. Test engine scanner parses crictl images output
5. Test engine manager registers/lists via Store
6. Test deploy pre-checks (model exists? engine available? hardware compatible?)

### Team G: internal/mcp/ — MCP Server + Tools

**Files:** `server.go`, `server_test.go`, `tools.go`, `tools_test.go`, `tools_hardware.go`, `tools_model.go`, `tools_engine.go`, `tools_deploy.go`, `tools_knowledge.go`, `tools_inference.go`, `tools_system.go`

**Implements all 26 MCP tools from architecture §7.7.**

MCP Server is JSON-RPC 2.0 over stdio. Each tool is a function:
```go
type ToolFunc func(ctx context.Context, params json.RawMessage) (any, error)
```

**TDD sequence:**
1. Test JSON-RPC 2.0 request/response parsing
2. Test tools/list returns all registered tools
3. Test hardware.detect tool calls hal.Detect
4. Test model.list tool calls store.ListModels
5. Test knowledge.resolve tool calls resolver
6. Test deploy.apply tool generates pod + calls k3s.Apply
7. Test error handling (invalid params, tool not found)

### Team H: internal/agent/ + internal/zeroclaw/

**agent/ files:** `agent.go`, `agent_test.go`, `dispatcher.go`, `dispatcher_test.go`
**zeroclaw/ files:** `manager.go`, `manager_test.go`

**Go Agent:** Simple tool-calling loop (max 30 rounds).
**Dispatcher:** Route to L3a/L3b based on complexity heuristics.
**ZeroClaw Manager:** Start/stop/health check of sidecar process.

**TDD sequence:**
1. Test Go Agent constructs correct LLM API request with tools
2. Test Go Agent executes tool_call and feeds result back
3. Test Go Agent stops after text response (no more tool calls)
4. Test Go Agent respects 30-round limit
5. Test Dispatcher routes simple queries to L3a
6. Test Dispatcher routes complex queries to L3b
7. Test ZeroClaw Manager start/stop lifecycle
8. Test ZeroClaw Manager health check

---

## Phase 3: CLI + Entry Point

### Team I: internal/cli/ + cmd/aima/main.go

**cli/ files:** `root.go`, `deploy.go`, `model.go`, `engine.go`, `knowledge.go`, `ask.go`, `agent.go`, `server.go`, `config.go`

Every CLI command is a thin wrapper: parse Cobra flags → call MCP tool function → format output.

**TDD sequence:**
1. Test `aima deploy <model>` calls deploy.apply MCP tool
2. Test `aima status` calls hardware.metrics + deploy.list
3. Test `aima model list` calls model.list tool
4. Test `aima ask` routes through Dispatcher
5. Test main.go wires all dependencies together
6. Cross-platform build test: `GOOS=linux GOARCH=amd64`, `GOOS=darwin GOARCH=arm64`, `GOOS=windows GOARCH=amd64`, `GOOS=linux GOARCH=arm64`

---

## Phase 4: Code Review

### Team J: Full Review

Review all code against:
1. PRD requirements mapping (architecture §15)
2. Architecture invariants INV-1 through INV-8 (§14)
3. Go conventions from CLAUDE.md
4. Cross-platform correctness
5. Test coverage adequacy
