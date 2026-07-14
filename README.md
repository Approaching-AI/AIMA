# AIMA

This repository contains the AIMA application package for Moore Threads AIBook.

AI Infrastructure, managed by AI.

Ollama gave you Ollama-level TCO. AIMA hits SOTA inference performance at the same TCO, by putting an AI agent in the loop.

AIMA is a single Go binary that runs inference on your hardware. It detects the accelerator, picks a matching engine from a YAML knowledge base, deploys the model, runs benchmarks, and writes the winning config back. The loop is driven by a built-in agent. AIMA is also an MCP server, so any external agent (OpenClaw, for example) can operate it directly.

[中文](README_zh.md) · [Why AIMA](#why-aima) · [Quick Start](#quick-start) · [Battle-tested](#battle-tested)

---

## Why AIMA

Private-LLM stacks today usually sit in one of two corners.

Ollama and LM Studio take the simple path: one binary, one engine (llama.cpp / GGUF), defaults that work out of the box. Throughput ends up capped by the engine's ceiling.

Raw vLLM, SGLang, and TensorRT-LLM take the performance path: good numbers on paper, but you own flag tuning, quantization choice, deployment wiring, and every per-vendor quirk. Each new chip is basically a redo.

AIMA takes a different bet: the operator is an agent.

|  | Ollama / LM Studio | Raw vLLM / SGLang | AIMA |
|---|---|---|---|
| One-line install | ✅ | — | ✅ |
| OpenAI-compatible API | ✅ | ✅ | ✅ |
| Inference backend | llama.cpp | vLLM / SGLang | vLLM · SGLang · llama.cpp (auto-picked per hardware) |
| SOTA throughput on discrete GPUs | ❌ | ✅ (if you tune it) | ✅ (agent tunes it) |
| NVIDIA / AMD / Apple | ✅ | partial | ✅ |
| Huawei Ascend / Hygon DCU / Moore Threads / MetaX | ❌ | DIY | ✅ (validated on silicon) |
| MCP server out of the box | ❌ | ❌ | ✅ |
| Self-tuning loop (plan → deploy → benchmark → learn) | ❌ | ❌ | ✅ |
| LAN fleet / multi-node | ❌ | DIY | ✅ (mDNS auto-discovery) |
| Offline / airgap | partial | DIY | ✅ (airgap images preloaded) |

Ollama and LM Studio keep TCO low by only shipping one engine. Raw vLLM and SGLang get the best numbers, but the operator is you. With AIMA, the operator is an agent, and "what runs fastest on this silicon" accumulates in a YAML knowledge base instead of a consultant's head.

---

## Agent-native

AIMA is an MCP server.

### External agents drive AIMA

Point any MCP-compatible runtime at AIMA's port and it has the full operational surface: hardware detection, model scan, engine selection, deployment, benchmark, fleet discovery, knowledge sync. There is no REST wrapper to write and no official SDK to wait for.

AIMA is currently used in production as the inference backend behind OpenClaw, an active open-source multimodal agent framework. It covers LLM, ASR, TTS, image generation, and VLM. Other MCP-speaking runtimes plug in the same way.

```jsonc
// Point any MCP client at AIMA's HTTP endpoint. That's the integration.
{
  "mcpServers": {
    "aima": { "type": "http", "url": "http://<aima-host>:6188/mcp" }
  }
}
```

### AIMA runs its own agent internally

AIMA consumes MCP as well as serving it. A built-in PDCA agent (Explorer) plans benchmarks, deploys configs, samples throughput and TTFT, and promotes winners to a shared knowledge base. When a new chip arrives, the agent runs the tuning matrix itself, so a single binary can reach vLLM-level throughput without exposing vLLM's flag soup to you.

---

## Quick Start

### 1. Get the binary

One-line install:

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/Approaching-AI/AIMA/master/install.sh | sh

# Windows PowerShell
irm https://raw.githubusercontent.com/Approaching-AI/AIMA/master/install.ps1 | iex
```

Or grab a pre-built binary from the [Releases](https://github.com/Approaching-AI/AIMA/releases) page (macOS arm64, Linux amd64/arm64, Windows amd64).

Or build from source: `git clone https://github.com/Approaching-AI/AIMA && cd AIMA && make build`.

### 2. See what hardware AIMA finds

```bash
aima hal detect
```

Prints the detected GPU/NPU (NVIDIA, AMD, Ascend, DCU, Apple, Moore Threads, MetaX, or CPU-only), driver versions, and RAM. A quick way to sanity-check that the binary runs on this host.

### 3. Run the first-run guide

```bash
aima onboarding
```

The guide runs status, scan, and model recommendation checks, then prints the next concrete command. It is read-only by default and does not install system services or deploy a model without an explicit follow-up command.

### 4. Run a safe starter model

```bash
aima run qwen3-4b
```

`run` resolves the model, picks the engine and config for this host, pulls missing assets, deploys the model, and waits for readiness. You can replace `qwen3-4b` with a model from `aima onboarding recommend`.

To keep the OpenAI-compatible API and Web UI open in this terminal:

```bash
aima serve
```

### 5. Linux shared server path

For a Linux workstation or server that should serve other machines, initialize the infrastructure stack first:

```bash
sudo aima init
aima deploy qwen3-4b
aima serve
```

`init` installs the local infrastructure services AIMA needs on Linux. macOS and Windows users can skip it for local-only native use.

### 6. Call the OpenAI-compatible API

```bash
curl http://127.0.0.1:6188/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen3-4b","messages":[{"role":"user","content":"hello"}]}'
```

Any OpenAI SDK client works against `http://<server-ip>:6188/v1`.

### Extras

- For multi-host fleets, run `aima fleet devices` to list mDNS-discovered AIMA peers, then `aima fleet exec <id> hal.detect` to drive them remotely.
- AIMA is an MCP server, so any MCP-compatible agent runtime can drive it. See [Agent-native](#agent-native) above.
- Enable API-key auth with `aima config set api_key <key>` (hot-reloads, see [Security](#security)).

---

## AIBook source deployment

Use this flow when you have changed the source tree and need a fresh AIBook `.deb` for deployment.

### 1. Build and smoke-test locally

Run this on Linux or WSL because the packaging step depends on `bash` and `dpkg-deb`.

```bash
cd AIMA-aibook-source
go test ./internal/onboarding ./internal/cli ./internal/ui ./internal/mcp ./cmd/aima -count=1
make aibook-deb
```

`make aibook-deb` builds the arm64 binary with the repository `-ldflags "-s -w ..."` settings, so debug information is stripped before packaging. Do not use a plain `go build` output for the AIBook package, because it can include debug sections and make the `.deb` much larger.

The registered AIBook product version is `v0.4.0`. The Debian package version appends the build date so builds remain identifiable, while the application name, package name, and process name remain `AIMA`, `aima-aibook`, and `aima`. The package installs the orange AIMA icon registered for this edition.

### 2. Build the AIBook `.deb`

The package will be written to `build/release/` with a name like:

```text
aima-aibook_0.4.0+aibookYYYYMMDD.1_arm64.deb
```

If you need to pin the version explicitly, first run `make aibook-deb` to produce the stripped `build/aima-linux-arm64`, then call the packaging script:

```bash
bash scripts/package-aibook-deb.sh build/aima-linux-arm64 0.4.0+aibookYYYYMMDD.1
```

### 3. Deploy to the target AIBook

```bash
AIMA_DEB=$(ls -1t build/release/aima-aibook_*_arm64.deb | head -n 1)
scp "$AIMA_DEB" aibook@192.168.109.142:/tmp/aima-aibook_arm64.deb
ssh aibook@192.168.109.142
sudo dpkg -i /tmp/aima-aibook_arm64.deb
```

### 4. Verify the install

```bash
dpkg -s aima-aibook | grep Version
systemctl status aima.service --no-pager
ss -lntp | grep 6188
curl http://127.0.0.1:6188/health
curl -s http://127.0.0.1:6188/ui/api/onboarding-manifest | jq '.default_locale'
```

Expected results:

- the package version matches the new build
- `aima.service` is `active (running)`
- port `6188` is listening
- `/health` returns `status: "ok"`
- onboarding manifest returns `default_locale` as `zh`

### 5. Upgrade behavior on AIBook

The current AIBook package supports in-place upgrades:

```bash
sudo dpkg -i ./aima-aibook_*_arm64.deb
```

During upgrade it will:

- stop the old `aima.service`
- remove the legacy `aima-serve.service`
- replace `/usr/local/bin/aima`
- rewrite `/etc/aima/aima.env`
- rewrite `/etc/aima/data-dir`
- clear stale UI cache directories
- reload systemd and restart `aima.service`

It intentionally keeps persistent data under `/var/lib/aima`, including:

- `/var/lib/aima/aima.db`
- model state
- device identity and other persisted settings

If the browser still shows old language or old UI after an upgrade, check browser-side site data first, especially `localStorage` values such as `aima_lang`.

---

## AIBook clean uninstall

If you only want to remove the package but keep data:

```bash
sudo dpkg -r aima-aibook
```

That removes the package and service entrypoints, but keeps `/var/lib/aima`.

For a real clean uninstall that also removes config, cache, database, and logs:

```bash
sudo systemctl stop aima.service || true
sudo systemctl disable aima.service || true
sudo dpkg -P aima-aibook
sudo rm -rf /var/lib/aima /var/log/aima /etc/aima
sudo rm -f /etc/systemd/system/aima-serve.service
sudo systemctl daemon-reload
sudo systemctl reset-failed
sudo update-desktop-database /usr/share/applications || true
sudo gtk-update-icon-cache -q /usr/share/icons/hicolor || true
```

That removes:

- the AIMA package and desktop entry
- `/etc/aima` environment files
- `/var/lib/aima` database, model state, device identity, and cache
- `/var/log/aima` logs
- any leftover legacy `aima-serve.service`

If you want a factory-reset style reinstall without losing device identity or database state, back up `/var/lib/aima` first and restore only what you need later.

---

## Battle-tested

Every release tag goes through a UAT matrix first. The same single binary has to run on real hardware we own, across every vendor listed below, before the tag ships.

### Hardware coverage validated per release

Seven GPU/NPU vendors: NVIDIA, AMD, Huawei Ascend, Hygon DCU, Apple Silicon, Moore Threads, MetaX. Intel CPU-only is also covered.

Five OS families: Ubuntu, Windows 11, macOS, EulerOS, Kylin V10. Two CPU architectures: x86_64 and aarch64. Four cross-compile targets per build: `windows/amd64`, `darwin/arm64`, `linux/amd64`, `linux/arm64`.

### v0.4.0 release-gate scorecard

| Metric | Number |
|---|---|
| UAT items on the release gate | 16 (5 P0, 7 P1, 4 P2) |
| UAT PASS + tracked-known-issue | 11 PASS, 5 tracked |
| Evidence folders under `artifacts/uat/v0.4/` | 20 |
| Raw evidence files (logs, DB snapshots, status dumps, JSON) | 1,200+ across 86 sub-folders |
| Explorer end-to-end fix-and-rerun rounds before cut | 7 rounds (2026-03-XX → 2026-04-17) |
| v0.3.0 → v0.4.0 release cycle | 18 days, 176 commits, real-silicon smoke on every tag |
| Cumulative on-silicon runtime across the matrix | ~1,000 hours logged in artifacts |

### How we run UAT

The rule is "ALL COLLECT, THEN ANALYZE": every device in the matrix runs the same binary and the same commands before we start fixing anything. We don't ship fixes that only hold on one device. A green tag means the fix held across every vendor above in the same round.

Evidence:
- [`docs/uat/v0.4-release-uat.md`](docs/uat/v0.4-release-uat.md)
- [`artifacts/uat/v0.4/`](artifacts/uat/v0.4/)
- [`CHANGELOG.md`](CHANGELOG.md)

A YAML-first architecture is worth something only if it works on silicon you don't control. Every vendor in the list above came in as a clean YAML PR. The Go source has no `if engine == "vllm-ascend"` branch anywhere. If your fleet has a chip we haven't tested yet, adding it is a YAML PR, not a fork.

---

## How it works

Full architecture doc: [`design/ARCHITECTURE.md`](design/ARCHITECTURE.md). Four invariants:

1. No code branches per engine or model type. Engine behavior is YAML. Model metadata is YAML. A new engine or model is a YAML change, not a Go change.
2. No container lifecycle management. K3S and Docker handle that. AIMA only issues `apply`, `status`, `delete`, `logs`.
3. MCP tools are the single source of truth. CLI, Web UI, and the internal agent all go through the same tool API.
4. Offline first. All core features work with zero network. Network is an enhancement, not a requirement.

Progressive intelligence, L0 through L3, with graceful fallback:

- L0: YAML knowledge-base defaults. Always available, offline-safe.
- L1: human CLI overrides.
- L2: golden configs promoted from past benchmarks.
- L3: Explorer agent (plans, deploys, measures, learns).

Three runtimes: K3S (Pod) for servers and clusters, Docker for single-host, Native (exec) for bare-metal edge devices.

---

## Supported hardware

| Vendor | SDK | Notes |
|---|---|---|
| NVIDIA | CUDA | Includes GB10 (Grace Blackwell) |
| AMD | ROCm / Vulkan | Includes W7900D (RDNA3, 8-GPU server) and Ryzen AI MAX+ 395 APU |
| Huawei | CANN | Ascend 910B1 (aarch64 / Kunpeng) |
| Hygon | DCU | BW150 (HBM) |
| Apple | Metal | Apple Silicon (M-series) |
| Moore Threads | MUSA | M1000 discrete and SoC (GPU + NPU) |
| MetaX | MACA | N260 |
| Intel | — | CPU-only inference |

## Supported engines

| Engine | Accelerators | Format |
|---|---|---|
| vLLM | NVIDIA CUDA · AMD ROCm · Hygon DCU · MetaX MACA · Moore Threads MUSA | Safetensors |
| SGLang | NVIDIA CUDA · Huawei Ascend (CANN) | Safetensors |
| llama.cpp | NVIDIA CUDA · AMD Vulkan · Apple Metal · CPU | GGUF |

Engine routing is picked by the agent from hardware and model profile. You can also pin engines via CLI or MCP when you need to.

---

## Security

`aima init` starts without authentication (LAN trust model). To enable API keys:

```bash
aima config set api_key <your-key>       # hot-reloads, no restart needed
aima fleet devices --api-key <your-key>  # remote fleet calls
# Web UI and MCP then require Authorization: Bearer <your-key>
```

## Project structure

```
cmd/aima/          # Edge binary entry point
internal/
  hal/             # Hardware detection
  knowledge/       # YAML knowledge base + SQLite resolver
  runtime/         # K3S (Pod) + Docker + Native runtimes
  mcp/             # MCP server + tool implementations
  agent/           # Explorer PDCA agent (L3) + dispatcher
  cli/             # Thin CLI wrappers over MCP tools
  ui/              # Embedded Web UI (Alpine.js SPA)
  proxy/           # OpenAI-compatible HTTP proxy
  fleet/           # mDNS fleet discovery + remote execution
catalog/           # YAML knowledge: hardware / engines / models / partitions / stack / scenarios
```

## Building

```bash
make build                  # local build
make all                    # cross-compile windows / darwin-arm64 / linux-{amd64,arm64}
make release-assets         # package release artifacts + checksums.txt
make publish-release-assets # upload via gh to the matching GitHub release
go test ./...               # run tests
```

Annotated SemVer tags (for example `v0.4.0`) trigger `.github/workflows/release.yml`, which builds and publishes the same artifacts.

## License

Apache License 2.0. See [LICENSE](LICENSE).
