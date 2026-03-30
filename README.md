# AIMA

[中文](README_zh.md)

**AI Inference Managed by AI** — A single Go binary that detects hardware, resolves optimal configs from a YAML knowledge base, deploys inference engines via K3S, and exposes 56 MCP tools for AI Agents to operate everything.

## Features

- **Zero-config hardware detection** — automatically discovers GPUs (NVIDIA, AMD, Huawei Ascend, Hygon DCU, Apple Silicon), CPU, and RAM
- **Knowledge-driven deployment** — YAML catalog of hardware profiles, engines, models, and partition strategies; no engine-specific code branches
- **Multi-runtime** — K3S (Pod) for clusters, Docker for single-node containers, Native (exec) for bare-metal inference
- **56 MCP tools** — full programmatic control for AI Agents over hardware, models, engines, deployments, fleet, and more
- **Fleet management** — mDNS-based auto-discovery of LAN peers; remote tool execution across heterogeneous devices
- **Offline-first** — all core functions work with zero network; network is enhancement, not requirement
- **Single binary, zero CGO** — cross-compiles to Windows, macOS, Linux (amd64/arm64) with no C dependencies

## Quick Start

### Download

Grab a pre-built binary from the [Releases](https://github.com/jguan/aima/releases) page, or build from source:

```bash
git clone https://github.com/jguan/aima.git
cd aima
make build
```

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

## Supported Hardware

| Vendor | Tested Devices | SDK |
|--------|---------------|-----|
| NVIDIA | RTX 4060, RTX 4090, GB10 (Grace Blackwell) | CUDA |
| AMD | Radeon 8060S (RDNA 3.5), Ryzen AI MAX+ 395 | ROCm / Vulkan |
| Huawei | Ascend 910B1 (8× 64GB HBM, Kunpeng-920 aarch64) | CANN |
| Hygon | BW150 DCU (8× 64GB HBM) | DCU |
| Apple | M4 | Metal |
| Intel | CPU-only | — |

## Supported Engines

| Engine | GPU Support | Format |
|--------|------------|--------|
| vLLM | NVIDIA CUDA, AMD ROCm, Hygon DCU | Safetensors |
| llama.cpp | NVIDIA CUDA, AMD Vulkan, Apple Metal, CPU | GGUF |
| SGLang | NVIDIA CUDA, Huawei Ascend (CANN) | Safetensors |
| Ollama | All (via llama.cpp) | GGUF |

## Architecture

AIMA follows a layered intelligence architecture (L0-L3):

- **L0** — YAML knowledge base defaults
- **L1** — Human CLI overrides
- **L2** — Golden configs from benchmark history
- **L3a** — Go Agent loop (tool-calling LLM)

The system is built around four invariants: no code branches for engine/model types (YAML-driven), no container lifecycle management (K3S handles it), MCP tools as the single source of truth, and offline-first operation.

See [design/ARCHITECTURE.md](design/ARCHITECTURE.md) for the full architecture document.

## Project Structure

```
cmd/aima/          Entry point
internal/
  hal/             Hardware detection
  knowledge/       YAML knowledge base + SQLite resolver
  runtime/         K3S (Pod) + Docker (container) + Native (exec) runtimes
  mcp/             56 MCP tool implementations
  agent/           Go Agent loop (L3a)
  cli/             Cobra CLI (thin wrappers over MCP tools)
  ui/              Embedded Web UI (Alpine.js SPA)
  proxy/           OpenAI-compatible HTTP proxy
  fleet/           mDNS fleet discovery + remote execution
  state/           SQLite state store (modernc.org/sqlite, zero CGO)
  model/           Model scan/download/import
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

### Run tests

```bash
go test ./...
```

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
