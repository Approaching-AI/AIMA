# AIMA Operations Lessons

> Distilled from LAN proxy, mDNS, Fleet, systemd, container runtime, and China deployment experience.

## 1. mDNS & Service Discovery

### Platform-specific mDNS behavior
| Platform | Library | Behavior |
|----------|---------|----------|
| Linux | hashicorp/mdns | Works, but single-interface by default |
| macOS | dns-sd subprocess | Required — hashicorp/mdns silently fails (mDNSResponder owns port 5353) |
| Windows | N/A | Not supported (no multicast in typical environments) |

### hashicorp/mdns single-interface trap
**Problem**: `net.ListenMulticastUDP("udp4", nil, addr)` — `nil` = system default interface only.
WiFi ↔ wired switching → other subnet's devices become invisible.

**Fix (commit 5f0d210)**:
- `lanInterfaces()`: enumerate all up + FlagMulticast + has LAN IP interfaces
- Server: one `mdns.Server` per LAN interface (shared Zone/Service)
- Client: parallel query all LAN interfaces, deduplicate by `svc.Name`
- Fallback: `lanInterfaces()` empty → use nil (system default), compatible with Docker/CI

### macOS dns-sd pitfalls
- Instance names with `.` are escaped as `\.` → need `normalizeID()` to unescape
- Browse (dns-sd -B) → Lookup (dns-sd -L) → GetAddr (dns-sd -G): three-step, each instance parallel
- Shutdown: `cmd.Process.Kill()` to terminate dns-sd subprocess

### mDNS IP selection: exclude non-LAN addresses
`isLANAddr(ip4)` filter: exclude loopback + 10.0.0.0/8 (K3S overlay) + 172.16.0.0/12 (Docker bridge).
Tailscale 100.x is included (not in filter), but harmless — Tailscale doesn't support multicast.

### Cross-subnet mDNS: discovery works, TCP may not
mDNS multicast can cross VLANs (switch-dependent), but TCP connections to discovered IPs may fail.
**Fix**: `Registry.healthCheck()` does TCP DialTimeout (2s) after each mDNS round; unreachable → `online=false`.

## 2. LAN Proxy Routing

### "Exactly 1 backend" fallback = silent misrouting
**Bug**: When only 1 backend registered, any model name routes to it. User asks for 35B model → gets 0.6B response.
**Fix**: Delete the fallback. No match → 404. Explicit error > silent wrong answer.

**Lesson**: "Smart fallbacks" in multi-model/multi-tenant routing are traps. Precision over convenience.

### Self-discovery loop
**Bug**: Proxy discovers its own mDNS broadcast → registers self as remote → infinite request loop.
**Fix**: `isLocalIP(addr) && svc.Port == localPort` → skip.

### Stale backend cleanup
After each mDNS round, backends not in current `alive` set are removed.
Risk: brief network interruption → healthy backend gets cleaned → needs next discovery round to re-register.
Current: acceptable (10s recovery). Future: consider grace period (N consecutive failures before removal).

## 3. systemd & Service Management

### systemd doesn't set HOME
**Bug**: `os.UserHomeDir()` panics under systemd.
**Fix**: `Environment=HOME=/root` in unit file.

### Multi-unit daemon components (Archive method)
Docker installs as archive with dependency chain:
```
containerd.service (Type=notify) → docker.service (Type=notify, After=containerd.service)
```
- PreCheck: iterate ALL systemd_units (not just component name)
- checkComponent: iterate ALL units, any inactive → "not running"
- installArchive: daemon-reload → enable --now each unit in order

### Post-install commands
- `post_install` commands support `{{.DistDir}}` template variable
- Non-fatal: failure logged as WARN, doesn't abort installation
- Use case: nvidia-ctk dpkg install from deb tarball, CDI spec generation

### ServiceType/Subcommand generalization
K3S: `ExecStart=k3s server` (Type=notify). aima-serve: `ExecStart=aima serve` (Type=simple).
→ YAML fields `subcommand` and `service_type` with K3S-compatible defaults.

## 4. Container Runtime Operations

### Docker CDI vs legacy GPU access
```
CDI (preferred):    --device nvidia.com/gpu=all    # needs /etc/cdi/nvidia.yaml
Legacy (fallback):  --gpus all                      # needs nvidia-container-toolkit
```
Detection: `os.Stat("/etc/cdi/nvidia.yaml")` — CDI spec exists → use CDI.

### Docker → K3S containerd image import
```bash
docker save <image> | sudo k3s ctr images import -
```
- `aima engine scan` with `--auto-import` automates this
- Only triggered during `aima init --k3s` (not default Docker tier)
- containerd adds `docker.io/` prefix; Docker doesn't — causes reference format mismatch in engine scan
- **Requires root**: `k3s ctr` needs root; non-root → pipe deadlock (docker save writes to dead pipe)
- **Fix (commit a0432c2)**: `os.Getuid() != 0` check → skip import, print sudo fix command

### Pipe deadlock: sender blocks on dead receiver
**Bug**: `docker save | k3s ctr import` — receiver dies (no root), sender blocks writing forever.
Sequential `fromCmd.Wait()` then `toCmd.Wait()` — `fromCmd.Wait()` never returns because pipe buffer is full and receiver is dead.

**Fix**: Concurrent wait with receiver-first error detection:
```go
toErr := make(chan error, 1)
go func() { toErr <- toCmd.Wait() }()
fromErr := fromCmd.Wait()
tErr := <-toErr
if tErr != nil {
    _ = fromCmd.Process.Kill()
    return fmt.Errorf("%s: %w", to[0], tErr)
}
```
**Lesson**: In shell pipes, always wait for receiver concurrently with sender. Receiver death should kill the sender, not leave it blocking.

### Docker-only deployment: proxy sync discovers Docker containers
**Scenario**: K3S removed, Docker is the only runtime. `aima-serve` started when K3S was available → sync loop uses K3S → `"list pods: exit status 1"` every 5s → proxy `/v1/models` returns empty.
**Root cause**: Runtime selection happens at startup (`selectDefaultRuntime`: K3S > Docker > Native). Old binary started with K3S available → chose K3S runtime. After K3S removal, the running process still tries K3S.
**Fix**: Update binary + restart `aima-serve`. New binary detects `K3SAvailable=false, DockerAvailable=true` → selects Docker runtime → sync loop uses `docker ps --filter label=aima.dev/engine` → discovers containers correctly.
**Lesson**: After removing K3S from a machine, always restart `aima-serve` to re-evaluate runtime selection.

### Engine scan pattern merge bug
```go
// WRONG: overwrites previous patterns for same type
assetPatterns[type] = patterns
// RIGHT: appends to existing patterns
assetPatterns[type] = append(assetPatterns[type], patterns...)
```

### K3S DirectoryOrCreate volume trap
K3S creates host directories as root when they don't exist. Later non-root writes → permission denied.
Fix: `chown` after K3S creates the directory, or pre-create with correct ownership.

## 5. China Network Deployment

### Mirror strategy (YAML-driven, zero Go code changes)
```yaml
mirror:
  linux/amd64:
    - "https://mirrors.tuna.tsinghua.edu.cn/..."   # fastest
    - "https://mirrors.aliyun.com/..."               # fallback
```
Download order: mirror list first (domestic fast) → primary URL last. Retry sequentially.

### GitHub proxy servers (reliability order)
1. `ghfast.top`
2. `cf.ghproxy.cc`
3. `gh-proxy.com`

### Container image mirrors
- docker.io: daocloud > 1ms.run > rat.dev > vvvv.ee > dockerproxy.net
- ghcr.io: `ghcr.m.daocloud.io` (DaoCloud)
- Aliyun: `registry.cn-hangzhou.aliyuncs.com/aima/*`

### HuggingFace mirror
```bash
HF_ENDPOINT=https://hf-mirror.com
```
Direct huggingface.co unreachable from China. model.pull should eventually support this natively.

### Adding/removing mirrors = YAML change only
INV-1 + Prime Directive: proxy list changes = edit YAML, zero Go code. New mirrors are picked up at next build.

### Container image pull: mirror reliability tiers (China)
| Mirror | docker.io 热门镜像 | docker.io 冷门镜像 | nvcr.io | ghcr.io |
|--------|-------------------|-------------------|---------|---------|
| daemon registry-mirrors | ✅ 自动 | ✅ 自动 | ❌ | ❌ |
| docker.1ms.run | ✅ | ✅ (可能限速) | ❌ | ❌ |
| docker.xuanyuan.me | ✅ | ⚠️ 免费限速 | ❌ | ❌ |
| docker.m.daocloud.io | ✅ 白名单内 | ❌ 白名单外 | ❌ | ✅ ghcr.m.daocloud.io |
| Aliyun aima/* | ✅ 自建推送 | ❌ 需自建 | ❌ | ❌ |
| nvcr.io 直连 | ❌ | ❌ | ✅ 直连 | ❌ |

**教训**:
- nvcr.io (NGC) 在国内可直连，不需要镜像
- docker.io 小众镜像 (如 scitrera) 多数镜像站不缓存或限速，`1ms.run` 最可靠但大镜像 ~2MB/s
- 拉取前先检查 `docker images` 避免重复下载
- 大镜像 (>20GB) 在 arm64 上解压可能需要数小时

### Engine YAML 镜像名必须与实际可拉取的镜像一致
**Bug**: `vllm-spark.yaml` 写 `vllm-spark-tf5:latest` (本地构建名), 实际不存在于任何 registry。
**Fix**: 改为 `scitrera/dgx-spark-vllm:0.14.1-t5` (Docker Hub 真实镜像名)。

**规则**: Engine YAML `image.name:tag` 必须是可通过 `docker pull` 拉取的完整镜像引用。
社区/自定义构建也要用其 Docker Hub 的真实名称，不要用本地 tag 别名。
`registries` 列表 + `source.mirror` 字段提供回退拉取路径。

### scitrera/dgx-spark-vllm — GB10 社区 vLLM 构建
- **来源**: [NVIDIA Forum](https://forums.developer.nvidia.com/t/new-pre-built-vllm-docker-images-for-nvidia-dgx-spark/357832)
- **Tag 语义**: `-t4` = Transformers 4.x, `-t5` = Transformers 5.x
- **用途**: Qwen3-Coder-Next (FLASH_ATTN, 2x prefill vs 官方 nightly), GLM-4.7-Flash (需 Transformers 5)
- **版本**: 0.14.1-t5 = vLLM 0.14.1 + CUDA 13.1 + FlashInfer + Transformers 5.0
- **拉取**: 国内用 `docker.1ms.run/scitrera/dgx-spark-vllm:0.14.1-t5`，约 25GB

## 6. Fleet REST API Architecture

### Consumer-side interface pattern
```go
// fleet package defines what it needs (no import of mcp package)
type MCPExecutor interface {
    ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error)
}
// main.go provides adapter: mcp.Server → fleet.MCPExecutor
```
Use `json.RawMessage` everywhere to avoid mirror types and conversion boilerplate.

### Fleet CLI must use REST API, not in-process state
CLI is a separate process from `aima serve`. Fleet Registry is empty in CLI.
**Correct**: CLI → HTTP localhost:6188 → REST handler → MCP tool.
Error message: `"cannot connect to local aima serve (is 'aima serve --mdns --discover' running?)"`.

### Fleet security: remote tool execution blocking
`fleet.exec_tool` can remotely invoke any MCP tool, potentially bypassing local guardrails.
**Must** add dangerous tools to `fleetBlockedTools`: model.remove, engine.remove, deploy.delete, stack.init, agent.rollback, shell.exec.

## 7. API Key & Security

### Timing-safe token comparison
All Bearer token checks use `crypto/subtle.ConstantTimeCompare`. Never use `==` for secrets.

### API key hot-update propagation
`system.config set api_key <KEY>` → three-way propagation:
1. Proxy server (request authentication)
2. MCP server (tool authentication)
3. Fleet client (remote API calls)
No restart needed. SQLite persistence for cross-restart survival.

### Credential redaction
`system.config` and CLI reading `api_key` or `llm.api_key` → response returns `***`.
Never log or return actual key values.

## 8. Tiered Stack Installation

### Upgrade path (Docker → K3S)
```
1. aima init --k3s (already has Docker)
2. Docker + nvidia-ctk → Ready (skip, idempotent)
3. K3S installs (own containerd, coexists with Docker)
4. HAMi installs into K3S
5. Auto-import: Docker images → K3S containerd
6. Next aima serve restart → auto-selects K3S as default runtime
```

### Component priority order
| Priority | Component | Tier | Method |
|----------|-----------|------|--------|
| 5 | docker | docker | archive |
| 6 | nvidia-ctk | docker | archive (post_install dpkg) |
| 10 | k3s | k3s | binary |
| 20 | hami | k3s | helm |
| 30 | aima-serve | docker | binary |

### Archive install method
For tar.gz containing multiple binaries (Docker):
1. Extract specified paths to /usr/local/bin/ (0755)
2. Write systemd units from `systemd_units` list
3. daemon-reload + enable --now each unit

For deb tarballs (nvidia-ctk):
- `extract_binaries` intentionally empty
- `post_install` handles: tar xzf → dpkg -i → cleanup → CDI generate
