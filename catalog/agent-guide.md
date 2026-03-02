# AIMA Agent Usage Guide

> This document is the structured reference for AI Agents operating AIMA.
> Every capability maps to an MCP tool name. Use tool names directly in JSON-RPC calls.

## Quick Start

```
# 1. Start AIMA service (enables all APIs)
aima serve --api-key <KEY> --mdns --discover

# 2. Agent connects via MCP (JSON-RPC 2.0)
POST http://localhost:9090/mcp
Authorization: Bearer <KEY>

# 3. Or use OpenAI-compatible proxy
POST http://localhost:6188/v1/chat/completions
Authorization: Bearer <KEY>
```

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│                    AIMA Binary                          │
│                                                         │
│  ┌──────────┐  ┌──────────┐  ┌──────────────────────┐  │
│  │ CLI      │  │ MCP      │  │ HTTP Proxy (:6188)   │  │
│  │ (human)  │──│ Server   │──│ OpenAI-compatible    │  │
│  │          │  │ (:9090)  │  │ /v1/chat/completions │  │
│  └──────────┘  └──────────┘  │ /v1/models           │  │
│       │              │       │ /v1/embeddings        │  │
│       └──────┬───────┘       └──────────────────────┘  │
│              │                        │                 │
│       ┌──────▼───────┐        ┌───────▼──────┐         │
│       │  56 MCP Tools │        │  Backends    │         │
│       │  (single      │        │  local K3S   │         │
│       │   source of   │        │  local native│         │
│       │   truth)      │        │  remote mDNS │         │
│       └──────┬───────┘        └──────────────┘         │
│              │                                          │
│  ┌───────────┼───────────────────────────────────┐     │
│  │           │                                   │     │
│  ▼           ▼              ▼            ▼       │     │
│ SQLite    Knowledge      Runtime       Fleet     │     │
│ (state)   (YAML catalog) (K3S/native)  (LAN)    │     │
└──────────────────────────────────────────────────┘     │
└─────────────────────────────────────────────────────────┘
```

**Key principle**: CLI and Agent use the exact same MCP tools. No hidden logic in either path.

---

## Running Modes

### CLI Mode (one-shot commands)

Run a single command and exit. No server needed.

```bash
aima hal detect          # detect hardware
aima model list          # list models
aima deploy qwen3-0.6b   # deploy a model
```

### Serve Mode (long-running service)

Start the full service stack. Required for Agent access and LAN features.

```bash
aima serve [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` | `:6188` | HTTP proxy listen address |
| `--mcp` | `false` | Enable MCP server over HTTP |
| `--mcp-addr` | `:9090` | MCP server listen address |
| `--api-key` | (none) | Bearer token for all APIs |
| `--mdns` | `false` | Broadcast service via mDNS |
| `--discover` | `false` | Discover remote AIMA instances |
| `--allow-insecure-no-auth` | `false` | Allow non-loopback without auth |

**Typical Agent setup**:
```bash
export AIMA_API_KEY="my-secret-key"
aima serve --mcp --mdns --discover
```

This starts:
- Inference proxy on `:6188` (OpenAI-compatible)
- MCP server on `:9090` (JSON-RPC 2.0)
- mDNS advertisement (`_llm._tcp`)
- Remote device discovery (10s interval)
- Local deployment sync (5s interval)

---

## Authentication

### API Key

A single shared secret protects all APIs.

| Method | Example |
|--------|---------|
| Environment variable | `export AIMA_API_KEY=sk-my-secret` |
| CLI flag | `aima serve --api-key sk-my-secret` |

The key is a plain string. No generation tool, no expiry.

### Hot-reload

The API key can be changed at runtime without restarting the server:

```
system.config set api_key NEW_KEY
```

This propagates immediately to all three auth paths (proxy, MCP, fleet client).
The `system.config` tool masks `api_key` and `llm.api_key` values in both get and set responses.

### LLM Config Persistence

The Go Agent's LLM endpoint, model, and API key persist in SQLite and can be hot-swapped:

```
system.config set llm.endpoint http://remote-gpu:6188/v1
system.config set llm.model qwen3-8b
system.config set llm.api_key sk-remote-key
```

**Precedence**: environment variable > SQLite config > default (`localhost:6188/v1`).
Changes take effect immediately — the OpenAIClient is updated without restart.
CLI equivalent: `aima config set llm.endpoint http://remote-gpu:6188/v1`.

### Where the key is enforced

| Endpoint | Auth required? |
|----------|---------------|
| `GET /health` | No (load balancer probe) |
| `/v1/*` (proxy) | Yes, if key is set |
| `/mcp` (MCP server) | Yes, if key is set |
| `/api/v1/*` (fleet) | Yes, if key is set |
| CLI commands | No (local only) |

### HTTP header format

```
Authorization: Bearer <API_KEY>
```

### Security rules

- Non-loopback bind address (`0.0.0.0`, LAN IP) **requires** `--api-key` or `--allow-insecure-no-auth`
- Loopback (`127.0.0.1`, `localhost`) works without auth
- All auth endpoints use timing-safe comparison (`crypto/subtle`)

---

## MCP Tools Reference

All tools are called via JSON-RPC 2.0. Group names use dot notation.

### hardware — Device Detection

| Tool | Parameters | Returns | Description |
|------|-----------|---------|-------------|
| `hardware.detect` | (none) | GPU, CPU, RAM, NPU info | Detect all hardware capabilities |
| `hardware.metrics` | (none) | utilization, memory, temp | Real-time hardware metrics |

### model — Model Management

| Tool | Parameters | Returns | Description |
|------|-----------|---------|-------------|
| `model.scan` | (none) | list of found models | Scan filesystem for model files |
| `model.list` | (none) | list of registered models | List models in database |
| `model.pull` | `name` | download progress | Download model by name |
| `model.import` | `path` | model record | Import model from local path |
| `model.info` | `name` | model details | Model metadata and variants |
| `model.remove` | `name`, `delete_files?` | success | Remove model from database |

**Model statuses**: `registered` → `downloading` → `imported` | `failed`

> **Tip**: `model.list` = what's in the local database (downloaded/imported). `model.scan` = rescan the filesystem for new files. `knowledge.list_models` = browse the YAML catalog of all supported models. If the user asks "what models can I run", start with `model.list`; if they ask "what models does AIMA support", use `knowledge.list_models`.

### engine — Engine Management

| Tool | Parameters | Returns | Description |
|------|-----------|---------|-------------|
| `engine.scan` | `runtime?` (auto/container/native) | list of engines | Scan for engine images/binaries |
| `engine.list` | (none) | list of registered engines | List engines in database |
| `engine.pull` | `name?` | pull progress | Download engine image |
| `engine.import` | `path` | engine record | Import engine from tar file |
| `engine.info` | `name` | engine details + availability | Engine metadata with live status |
| `engine.remove` | `name` | success | Remove engine |

**Engine runtime types**: `container` (K3S/Docker image) | `native` (local binary)

> **Tip**: `engine.list` = engines registered locally. `engine.scan` = rescan for new container images/binaries. `knowledge.list_engines` = browse the YAML catalog of all supported engines. Same pattern as the model tools above.

### deploy — Deployment Lifecycle

| Tool | Parameters | Returns | Description |
|------|-----------|---------|-------------|
| `deploy.apply` | `model`, `engine?`, `slot?`, `config?`, `dry_run?` | `{name, model, engine, slot, status, runtime}` | Deploy model for inference. `name` is the sanitized pod/process name. **Agent calls require approval** — returns plan + approval ID |
| `deploy.approve` | `id` | deployment status | Approve and execute a pending deployment by approval ID |
| `deploy.dry_run` | `model`, `engine?`, `slot?`, `config?` | fitness report + warnings | Preview deployment without executing |
| `deploy.delete` | `name` | success | Remove a deployment |
| `deploy.status` | `name` | pod/process status (phase/ready/restarts/exit_code) | Check deployment health. Accepts pod name or model name |
| `deploy.list` | (none) | all deployments | List all active deployments |
| `deploy.logs` | `name`, `tail_lines?` | log text | Get deployment logs. Accepts pod name or model name |

> **Tip**: `deploy.apply` = actually deploy (requires approval). `deploy.dry_run` = preview config and fitness report without executing. Always use `deploy.dry_run` first if you want to check compatibility before deploying.

**Deployment flow**:
```
deploy.apply("qwen3-0.6b")
  → hardware.detect
  → knowledge.resolve (find best engine + config)
  → runtime.Deploy (K3S Pod or native process)
  → proxy registers backend automatically (5s sync)
  → model available at /v1/chat/completions
```

**Config overrides** (passed as `config` map):
```json
{
  "config": {
    "gpu_memory_utilization": 0.9,
    "max_model_len": 131072,
    "tensor_parallel_size": 2
  }
}
```

### knowledge — Knowledge Base

| Tool | Parameters | Returns | Description |
|------|-----------|---------|-------------|
| `knowledge.resolve` | `model`, `engine?`, `overrides?` | ResolvedConfig | Find optimal config for model |
| `knowledge.search` | `hardware?`, `model?`, `engine?` | matching notes | Search knowledge notes |
| `knowledge.save` | `title`, `content`, `tags`, etc. | note record | Store knowledge note |
| `knowledge.generate_pod` | (resolved config fields) | Pod YAML | Generate K3S Pod manifest |
| `knowledge.list_profiles` | (none) | hardware profiles | List all hardware profiles |
| `knowledge.list_engines` | (none) | engine assets | List all engine definitions |
| `knowledge.list_models` | (none) | model assets | List all model definitions |

> **Tip**: `knowledge.search` = search Agent exploration notes (free-text with hardware/model/engine filter). `knowledge.search_configs` = query tested configurations with performance data (SQL multi-dimensional). Use `search` for "what has the agent tried before?", use `search_configs` for "what config gave the best throughput?".

### knowledge (advanced) — Analytics & Comparison

| Tool | Parameters | Returns | Description |
|------|-----------|---------|-------------|
| `knowledge.search_configs` | filters (hardware, model, engine, status) | configurations | Multi-dimensional config search |
| `knowledge.compare` | `config_ids` | side-by-side | Compare configurations |
| `knowledge.similar` | `config_id` | similar configs | Vector similarity (cross-hardware) |
| `knowledge.lineage` | `config_id` | parent chain | Performance evolution history |
| `knowledge.gaps` | (none) | untested combos | Identify benchmark coverage gaps |
| `knowledge.aggregate` | `group_by`, `metric` | grouped stats | Aggregate benchmark statistics |

### benchmark — Performance Recording

| Tool | Parameters | Returns | Description |
|------|-----------|---------|-------------|
| `benchmark.record` | `hardware`, `engine`, `model`, `throughput`, ... | record | Store benchmark result |

**Required fields**: `hardware`, `engine`, `model`, `throughput`
**Optional fields**: `ttft_p50`, `tpot_p50`, `vram`, `input_bucket`, `samples`, `notes`

### system — System Operations

| Tool | Parameters | Returns | Description |
|------|-----------|---------|-------------|
| `system.status` | (none) | full system state | Hardware + deployments + models + engines |
| `system.exec` | `command` | output | Execute whitelisted shell command |
| `system.config` (get) | `key` | value | Read persistent config (`api_key`/`llm.api_key` masked) |
| `system.config` (set) | `key`, `value` | success | Write persistent config (`api_key` hot-reloads auth; `llm.*` hot-swaps Agent LLM client) |
| `catalog.override` | `type`, `name`, `content` | success | Override YAML asset at runtime |

> **Tip**: `system.status` = combined overview of everything (hardware + deployments + models + engines). `hardware.detect` = detailed hardware capability vector only. `hardware.metrics` = real-time GPU utilization and temperature. For a quick "what's going on?" question, use `system.status`. For deployment decisions, use `hardware.detect`. For monitoring GPU load, use `hardware.metrics`.

**Whitelisted commands** for `system.exec`:
`nvidia-smi`, `df`, `free`, `uname`, `kubectl get/describe/logs/top/version`

### stack — Infrastructure

| Tool | Parameters | Returns | Description |
|------|-----------|---------|-------------|
| `stack.preflight` | (none) | download checklist | Check what needs downloading |
| `stack.init` | `components` | install progress | Install K3S + HAMi |

### discovery — LAN Service Discovery

| Tool | Parameters | Returns | Description |
|------|-----------|---------|-------------|
| `discovery.lan` | `timeout?` | found services | Scan LAN for AIMA instances via mDNS |

> **Tip**: `discovery.lan` = low-level mDNS scan returning raw service records. `fleet.list_devices` = higher-level tool that auto-discovers via mDNS and returns structured device list. Prefer `fleet.list_devices` for most use cases.

### fleet — Multi-Device Management

| Tool | Parameters | Returns | Description |
|------|-----------|---------|-------------|
| `fleet.list_devices` | (none) | device list | All discovered AIMA devices |
| `fleet.device_info` | `id` | device details | Hardware + models of remote device |
| `fleet.device_tools` | `id` | tool list | Available MCP tools on remote device |
| `fleet.exec_tool` | `id`, `tool`, `params` | tool result | Execute MCP tool on remote device |

**Fleet enables**: one Agent managing multiple edge devices, each running AIMA.
`fleet.exec_tool` applies the same agent safety guardrails (blocked/confirmable) to the inner `tool_name` as local calls — the Agent cannot bypass restrictions by routing through fleet.

### agent — AI Agent

| Tool | Parameters | Returns | Description |
|------|-----------|---------|-------------|
| `agent.ask` | `query`, `local?`, `deep?`, `dangerously_skip_permissions?`, `session?` | response | Route query to L3a/L3b agent |
| `agent.install` | (none) | status | Install ZeroClaw sidecar (L3b) |
| `agent.status` | (none) | availability | Check agent layer availability |
| `agent.guide` | (none) | full reference | Get complete agent usage guide |

**Intelligence levels**:
| Level | Name | What it does |
|-------|------|-------------|
| L0 | Defaults | Engine/model YAML defaults |
| L1 | Human CLI | User-provided config overrides |
| L2 | Knowledge | Deterministic YAML resolution |
| L3a | Go Agent | LLM-powered multi-turn tool use |
| L3b | ZeroClaw | Persistent memory, deep reasoning |

### rollback — Safety

| Tool | Parameters | Returns | Description |
|------|-----------|---------|-------------|
| `rollback.list` | (none) | snapshots | List recovery snapshots |
| `rollback.restore` | `id` | restored data | Restore from snapshot |

---

## HTTP Proxy API (OpenAI-Compatible)

Base URL: `http://<host>:6188`

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Health check (no auth) |
| GET | `/status` | Backend status |
| GET | `/v1/models` | List available models |
| POST | `/v1/chat/completions` | Chat inference |
| POST | `/v1/completions` | Text completion |
| POST | `/v1/embeddings` | Embeddings |

### Chat completion example

```bash
curl http://localhost:6188/v1/chat/completions \
  -H "Authorization: Bearer $AIMA_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen3-0.6b",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

### Model routing

The proxy routes by model name to the best available backend:

1. **Local deployment** (K3S Pod or native process) — always preferred
2. **Remote mDNS** (discovered from LAN) — fallback

If the same model is available locally and remotely, local always wins.

---

## Fleet REST API

Base URL: `http://<host>:6188/api/v1`

### Local device endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/device` | This device's info |
| GET | `/api/v1/tools` | Available MCP tools |
| POST | `/api/v1/tools/{name}` | Execute tool locally |

### Fleet manager endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/devices` | All discovered devices |
| GET | `/api/v1/devices/{id}` | Remote device info |
| GET | `/api/v1/devices/{id}/tools` | Remote device tools |
| POST | `/api/v1/devices/{id}/tools/{name}` | Execute tool on remote device |

### Execute remote tool example

```bash
# List models on remote device "gb10"
curl -X POST http://localhost:6188/api/v1/devices/gb10/tools/model.list \
  -H "Authorization: Bearer $AIMA_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{}'

# Deploy model on remote device
curl -X POST http://localhost:6188/api/v1/devices/gb10/tools/deploy.apply \
  -H "Authorization: Bearer $AIMA_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "qwen3-0.6b"}'
```

---

## Data Storage

### SQLite Database (`~/.aima/aima.db`)

| Table | Content | Agent-writable? |
|-------|---------|----------------|
| `models` | Scanned model metadata | Yes (via model.scan/import) |
| `engines` | Scanned engine metadata | Yes (via engine.scan/import) |
| `knowledge_notes` | Agent exploration notes | Yes (via knowledge.save) |
| `configurations` | Tested hardware×engine×model configs | Yes (via knowledge tools) |
| `benchmark_results` | Performance measurements | Yes (via benchmark.record) |
| `config` | Key-value settings | Yes (via config.set) |
| `audit_log` | Agent action history | Auto (every tool call logged) |
| `rollback_snapshots` | Recovery points | Auto (on destructive ops) |
| `hardware_profiles` | From YAML (rebuilt on start) | Read-only |
| `engine_assets` | From YAML (rebuilt on start) | Read-only |
| `model_assets` | From YAML (rebuilt on start) | Read-only |
| `partition_strategies` | From YAML (rebuilt on start) | Read-only |

### YAML Knowledge Base (`catalog/`)

Embedded at compile time. Runtime overlay at `~/.aima/catalog/`.

```
catalog/
├── hardware/     # GPU/CPU profiles (container access, resource names)
├── engines/      # Engine images, configs, features
├── models/       # Model metadata, variants, sources
├── partitions/   # Multi-slot resource allocation
└── stack/        # Infrastructure installers
```

**Override mechanism**: Place YAML files in `~/.aima/catalog/{type}/` with same `metadata.name` to override compiled-in assets. New names are appended.

---

## Environment Variables

| Variable | Purpose | Default |
|----------|---------|---------|
| `AIMA_DATA_DIR` | Database + logs + models directory | `~/.aima` |
| `AIMA_API_KEY` | Bearer token for all APIs | (none, auth disabled) |
| `AIMA_LLM_ENDPOINT` | LLM API for Go Agent (L3a) | `http://localhost:6188/v1` |
| `AIMA_LLM_MODEL` | Model name for Agent reasoning | (auto-discover) |

**Precedence**: env var > SQLite (`aima config set llm.*`) > default. Environment variables take effect at startup; `system.config set llm.*` hot-swaps at runtime.

---

## Common Agent Workflows

### Workflow 1: Detect hardware and deploy best model

```
1. hardware.detect           → get GPU arch, VRAM, runtime
2. model.list                → see available models
3. knowledge.resolve(model)  → get optimal engine + config
4. deploy.dry_run(model)     → preview fitness report
5. deploy.apply(model)       → deploy
6. deploy.status(name)       → wait for ready
7. POST /v1/chat/completions → use the model
```

### Workflow 2: Benchmark and record performance

```
1. deploy.apply(model)       → deploy model
2. POST /v1/chat/completions → run inference requests
3. benchmark.record(...)     → store results
4. knowledge.search_configs  → compare with other configs
5. knowledge.similar(id)     → find similar setups
```

### Workflow 3: Fleet-wide model deployment

Fleet MCP tools include built-in mDNS discovery — no need for `serve --discover` or CLI pre-scan.
A cloud-based Agent can manage LAN devices directly via MCP.

```
1. fleet.list_devices        → mDNS scan + list all devices (always fresh)
2. fleet.device_info(id)     → check each device's hardware
3. fleet.exec_tool(id, "knowledge.resolve", {model})  → resolve per-device
4. fleet.exec_tool(id, "deploy.apply", {model})        → deploy per-device
5. fleet.exec_tool(id, "deploy.status", {name})        → verify each
```

### Workflow 4: Find and fix performance gaps

```
1. knowledge.gaps            → identify untested combos
2. knowledge.aggregate(...)  → summarize existing data
3. deploy + benchmark        → fill the gaps
4. knowledge.compare(ids)    → compare results
5. knowledge.save(...)       → record findings
```

### Workflow 5: LAN inference routing

```
# On each edge device:
aima serve --api-key KEY --mdns

# On coordinator device:
aima serve --api-key KEY --mdns --discover

# Agent sends requests to coordinator:
POST http://coordinator:6188/v1/chat/completions
  → routes to best available backend across LAN
```

---

## Safety Mechanisms

| Mechanism | Description |
|-----------|-------------|
| **Audit log** | Every MCP tool call logged with args + result |
| **Blocked tools** | `model.remove`, `engine.remove`, `deploy.delete` completely blocked for agents |
| **Deployment approval** | `deploy.apply` requires user approval — returns plan + approval ID; call `deploy.approve(id)` after user confirms. Use `dangerously_skip_permissions` to bypass |
| **Rollback snapshots** | Auto-snapshot before destructive operations |
| **Whitelisted shell** | `system.exec` only allows safe commands |
| **Auth middleware** | Bearer token on all non-health endpoints |
| **Dry-run** | `deploy.dry_run` previews without side effects |
| **Graceful degradation** | Missing GPU/K3S/network → fallback, not crash |

---

## Glossary

| Term | Meaning |
|------|---------|
| **Engine** | Inference runtime (vLLM, llama.cpp, etc.) defined in YAML |
| **Model** | AI model files (SafeTensors, GGUF, etc.) |
| **Hardware Profile** | YAML describing device capabilities |
| **Deployment** | Running inference service (K3S Pod or native process) |
| **Backend** | Proxy route target (local or remote) |
| **Knowledge Note** | Agent exploration record |
| **Configuration** | Tested hardware×engine×model×config combo |
| **Partition** | Resource allocation strategy for multi-model |
| **MCP** | Model Context Protocol (JSON-RPC 2.0) |
| **Fleet** | Collection of AIMA devices on the LAN |
| **mDNS** | Multicast DNS for zero-config service discovery |
