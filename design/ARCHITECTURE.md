# AIMA — 系统架构文档

> AI-Inference-Managed-by-AI
> 有限硬件资源上的 AI 推理优化与调度系统

---

## 1. 设计原则

### P1: 基础设施 + 轻量 Agent 便利层

Go 二进制是**薄基础设施层**——硬件检测、知识解析、Pod 生成、请求代理。
简单一次性查询可使用**内置 Go Agent (L3a)** 以最小延迟响应。
复杂智能逻辑由**外部/Sidecar AI Agent (L3b)** 通过 MCP 工具实现。

策略 = Agent 的事。基础设施 = Go 的事。
Go 代码不包含 if-else 决策树、规则引擎、或任何 "策略" 代码。

### P2: 知识胜于代码

能力扩展的主要方式是写 YAML 知识文件，而不是写新代码。

- 支持新引擎 → 写 Engine Asset YAML
- 支持新模型 → 写 Model Asset YAML
- 支持新硬件 → 写 Hardware Profile YAML（可能加少量检测代码）
- 沉淀调优经验 → Knowledge Note (YAML / SQLite)

80% 的能力扩展不需要重新编译。

### P3: 声明式优先

用 K3S Pod YAML 描述期望状态，系统自动收敛。

- 容器的启动、重试、健康检查、生命周期 —— K3S 管
- GPU 显存和算力的切分与隔离 —— HAMi 管
- AIMA 只做：(1) 从知识生成 Pod YAML, (2) kubectl apply, (3) 查询状态

### P4: 成熟工具组合

| 职责 | 选型 | 替代方案 |
|------|------|---------|
| 容器编排 | K3S | 标准 K8s, MicroK8s |
| GPU 虚拟化 | HAMi | MIG, MPS, Flex:ai |
| 状态存储 | SQLite | PostgreSQL, etcd |
| Agent 接口 | MCP (JSON-RPC) | gRPC, REST |
| 服务发现 | mDNS | Consul, etcd |
| Sidecar Agent | ZeroClaw (可选) | 任何 MCP Client |

每个工具可独立替换，彼此解耦。

### P5: 渐进智能 L0 → L3b

```
L3b: ZeroClaw ── 完整 AgentLoop + 持久记忆 + Identity ── 最优解 (动态+持久)
  ^ 升级
L3a: Go Agent ── 简单工具调用循环 + 会话级记忆 ──────── 良好解 (动态+进程内)
  ^ override
L2:  知识库 ──── 确定性匹配 ────────────────────────── 已知好解 (静态)
  ^ override
L1:  人类 CLI ── 手动指定参数 ──────────────────────── 指定解
  ^ override
L0:  默认值 ──── YAML 知识 (go:embed + ~/.aima/catalog/ overlay 合并) ── 可用解 (always)
```

每层独立可用。无 Agent、无网络、无知识库 → L0 仍能启动推理服务。

### P6: 探索即知识

Agent 每次探索（调优、排障、部署尝试）产出结构化的 Knowledge Note。
其他设备的 Agent 复用这些知识，跳过已知失败、从最优起点开始。

### P7: 本地优先，偶尔联网

AIMA 的所有核心功能**必须在完全离线下可用**。网络连接是"锦上添花"。

### P8: 设备形态中立

AIMA 管理的"设备"不限于本机——架构预留远程设备扩展点。
远程设备（如手机、IoT 节点）通过 `device.register` 注册能力向量和网络地址，
AIMA 通过 Remote Runtime 将推理请求代理到远程设备。
当前版本仅支持本机设备；远程设备支持计划在 v3.0 实现。

---

## 2. 系统全景

### 四层 + 双脑架构

```
┌─────────────────────────────────────────────────────────────┐
│   External Agent (远程/强力) — 可选                            │
│   Claude Code / GPT / 自定义 MCP Client                       │
├───────────────────────────────────────────────────────────────┤
│   Agent Layer (双皮层 Dual Cortex)                             │
│   L3a: Go Agent (内置轻量) │ L3b: ZeroClaw (Sidecar)         │
├───────────────────────────────────────────────────────────────┤
│   Knowledge Layer (知识层)                                     │
│   5 种知识资产 (go:embed YAML + 磁盘 overlay) + SQLite 查询    │
├───────────────────────────────────────────────────────────────┤
│   Orchestration Layer (编排层)                                 │
│   K3S (轻量 Kubernetes) + HAMi (GPU 虚拟化中间件)              │
├───────────────────────────────────────────────────────────────┤
│   Infrastructure Layer (基础设施层) — AIMA Go 二进制            │
│   54 MCP 工具 · LAN 推理代理 (:6188) · Fleet REST API          │
│   mDNS 多网卡发现 · 硬件检测 · Native Runtime · 审计+回滚       │
└─────────────────────────────────────────────────────────────┘
```

---

## 3. 按领域组织的详细文档

| 领域 | 文档 | 主要内容 |
|------|------|----------|
| 核心原则 | 本文档 | 设计原则、架构全景 |
| Model | [docs/model.md](../docs/model.md) | 模型扫描、导入、删除、元数据 |
| Engine | [docs/engine.md](../docs/engine.md) | 引擎镜像、拉取、导入、Native 二进制 |
| Runtime | [docs/runtime.md](../docs/runtime.md) | K3S Runtime、Native Runtime、Multi-Runtime 抽象 |
| Knowledge | [docs/knowledge.md](../docs/knowledge.md) | 知识库、配置解析、Pod 生成 |
| HAL | [docs/hal.md](../docs/hal.md) | 硬件检测、能力向量 |
| K3S | [docs/k3s.md](../docs/k3s.md) | K8s 集成、Pod 管理、HAMi |
| Stack | [docs/stack.md](../docs/stack.md) | 基础设施（K3S, HAMi, aima-serve） |
| MCP | [docs/mcp.md](../docs/mcp.md) | MCP 服务器、工具定义 |
| CLI | [docs/cli.md](../docs/cli.md) | 命令行接口 |
| Agent | [docs/agent.md](../docs/agent.md) | Go Agent、ZeroClaw Sidecar |

---

## 4. 硬件感知配置解析

ConfigResolver 不仅用 `gpu_arch` 匹配 variant，还根据硬件规格和运行时状态做两层过滤：

**静态层（部署前校验）**

在 `findModelVariant()` 和 `InferEngineType()` 中，利用 YAML 中定义的 `vram_min_mib` 和 `unified_memory`
字段，跳过当前硬件无法承载的 variant。例如 RTX 4060 (8GB, Ada) 部署 qwen3-8b 时，
跳过要求 16384 MiB 的 vllm-Ada variant，自动落到 llamacpp wildcard variant。

过滤阈值全部在 YAML 中定义（Model Asset 的 variant.hardware 字段），Go 代码仅做数值比较。
当 `HardwareInfo` 的显存/统一显存字段为零值时，跳过所有检查（graceful degradation，兼容旧调用方）。

**动态层（部署前适配）**

`CheckFit()` 在 resolve 之后、部署之前运行，根据 `hal.CollectMetrics()` 采集的实时 GPU 显存占用，
自动调低 `gpu_memory_utilization` 以避免 OOM。512 MiB 安全余量，最低阈值 0.1。
采集失败时不阻止部署（graceful degradation）。

```
HAL Detect (静态规格)  ──→  HardwareInfo  ──→  findModelVariant (VRAM/统一显存过滤)
HAL Metrics (动态状态)  ──→  HardwareInfo  ──→  CheckFit (gpu_memory_utilization 自动调整)
Hardware Profile YAML  ──→  ContainerAccess ──→  GeneratePod (厂商无关容器配置)
```

**容器访问配置**

Hardware Profile YAML 的 `container` 字段描述该硬件在 K3S 容器中运行推理时需要的厂商特定配置：
- `devices`: 需挂载的宿主机设备（如 AMD ROCm 的 `/dev/kfd`, `/dev/dri`）
- `env`: 注入到容器的环境变量（如 NVIDIA 的 `LD_LIBRARY_PATH`, AMD 的 `LD_PRELOAD`）
- `volumes`: 额外的 hostPath 挂载
- `security`: securityContext 配置（如 `supplemental_groups` 用于 video/render 组权限）

ConfigResolver 在 `Resolve()` 中通过 `findContainerAccess()` 匹配当前硬件的 container 配置，
传入 `ResolvedConfig.Container`，最终由 `GeneratePod()` 通用渲染。
Env 合并规则：hardware container env（基础层）+ engine env（覆盖层），引擎 env 在冲突时优先。

---

## 5. 架构不变量

不可违反的架构约束：

**INV-1: 不为引擎类型写代码。** 引擎行为定义在 YAML。添加新引擎 = 写 YAML。
引擎支持的模型格式通过 `metadata.supported_formats` 声明，运行时 `Catalog.FormatToEngine()` 动态映射。
默认引擎通过 `metadata.default: true` 标记，运行时 `Catalog.DefaultEngine()` 动态读取。

**INV-2: 不为模型/硬件类型写代码。** 模型元数据在 YAML。模型类型是知识，不是代码分支。
硬件约束（`vram_min_mib`、`unified_memory`）同样在 YAML 中定义，Go 代码仅做数值比较。
厂商特定的容器访问配置（设备挂载、环境变量、安全上下文）定义在 Hardware Profile YAML 的 `container` 字段中，
Pod 生成器通用渲染，不含 NVIDIA/AMD/Intel 等厂商分支。
HAL 层的 GPU enrichment 使用表驱动 map（`gpuEnrichers`），新增厂商 = 添加 map 条目。

**INV-3: 最小化运行时管理。** K3S 管 Pod 的创建、监控、重启、销毁。
Native runtime 只做极简进程管理（start/stop/logs）。

**INV-4: 职责分离的状态存储。** AIMA 系统状态在 `aima.db`，Agent 记忆在 `zeroclaw.db`。

**INV-5: MCP 工具即真相。** CLI 是 MCP 工具的包装。CLI 永不实现 MCP 工具之外的逻辑。
所有 CLI 命令（含 `ask`, `agent install/status`, `status`, `knowledge list`, `config`, `fleet`）均通过 ToolDeps 调用 MCP 工具。
Fleet CLI 的 mDNS 发现逻辑也在 ToolDeps 层实现（`fleet.list_devices` 每次自动扫描，其余 fleet 工具懒发现），CLI 和 MCP Agent 走完全相同的代码路径。
当前共 54 个 MCP 工具覆盖所有功能领域 (Hardware 2 + Model 6 + Engine 6 + Deploy 6 + Knowledge 15 + Benchmark 1 + Stack 3 + Catalog 2 + System 3 + Discovery 1 + Agent 5 + Fleet 4)。

**INV-6: 探索即知识。** Agent 每次探索必须产出 Knowledge Note。

**INV-7: 知识对齐优化链路。** 知识资产严格对齐 PRD 优化链路。

**INV-8: 离线可用。** 所有核心功能必须在完全离线下可用。

---

## 6. LAN 推理代理

### 架构

`aima serve` 启动 OpenAI 兼容的 HTTP 推理代理（默认端口 `6188`，定义在 `proxy.DefaultPort`），
同时提供本地部署路由和远程服务自动发现：

```
开发者机器 (无 GPU):                          GPU 服务器:
┌──────────────────────┐                  ┌─────────────────────┐
│ aima serve           │                  │ aima serve (systemd) │
│   :6188              │                  │   :6188              │
│                      │   mDNS           │                      │
│ ┌──────────────────┐ │  _llm._tcp      │ ┌──────────────────┐ │
│ │ Remote Discovery │←├──────────────────┤→│ mDNS Advertiser  │ │
│ │ (10s interval)   │ │                  │ │ (lanIPs filter)  │ │
│ └────────┬─────────┘ │                  │ └──────────────────┘ │
│          │           │                  │                      │
│ ┌────────▼─────────┐ │  HTTP proxy      │ ┌──────────────────┐ │
│ │ Route Table      │─├──────────────────┤→│ vLLM / llamacpp  │ │
│ │ model → backend  │ │                  │ │ pod / process    │ │
│ └──────────────────┘ │                  │ └──────────────────┘ │
└──────────────────────┘                  └─────────────────────┘
```

### 路由规则

1. **本地优先**：本地部署的 backend（`Remote=false`）始终优先于远程同名模型
2. **自动发现**：`--discover` 开启 mDNS 扫描，每 10s 查询远程 `/v1/models` 并注册
3. **防自发现**：`isLocalIP()` + 端口匹配过滤自身 mDNS 广播，避免路由回环
4. **Stale 清理**：每轮发现后，移除不再存活的远程 backend

### mDNS 广播

- 服务类型：`_llm._tcp`
- IP 筛选：`lanIPs()` 排除 loopback、Docker bridge (172.16-31.x)、K3S overlay (10.x)
- TXT 记录：`aima=1`, `models=a,b,c`

### systemd 持久化

`aima init` 通过 Stack Component YAML (`catalog/stack/aima-serve.yaml`) 安装 systemd 服务：

- `Type=simple`（非 sd_notify）
- `Environment=HOME=/root`（systemd 不设 HOME）
- env 文件：`/etc/aima/aima-serve.env`（非 K3S 组件使用独立目录）
- 子命令/服务类型可配置（`StackInstall.Subcommand` / `ServiceType`），向后兼容 K3S

### Config 模板扩展

Pod 生成支持 `{{.ModelName}}` 和 `{{.ModelPath}}` 模板变量用于 string 类型的 config 值。
vLLM engine YAML 通过 `served_model_name: "{{.ModelName}}"` 确保 vLLM 的对外模型名与 AIMA 一致。

---

## 7. Fleet 多设备管理

### 架构

Fleet 子系统让局域网内多台 AIMA 设备协同工作——自动发现、状态汇聚、远程工具执行。

```
设备 A (aima serve)                    设备 B (aima serve)
┌──────────────────────┐            ┌──────────────────────┐
│ Fleet Registry       │   mDNS    │ Fleet Registry       │
│  ├─ local device     │←─────────→│  ├─ local device     │
│  └─ discovered peers │  _llm._tcp│  └─ discovered peers │
│                      │            │                      │
│ REST API (:6188)     │   HTTP    │ REST API (:6188)     │
│  /api/v1/devices/*   │←─────────→│  /api/v1/devices/*   │
└──────────────────────┘            └──────────────────────┘
```

### REST 端点 (7 个)

| 端点 | 方法 | 用途 |
|------|------|------|
| `/api/v1/device` | GET | 本设备状态 |
| `/api/v1/tools` | GET | 本设备 MCP 工具列表 |
| `/api/v1/tools/{name}` | POST | 在本设备执行 MCP 工具 |
| `/api/v1/devices` | GET | 所有发现的设备列表 |
| `/api/v1/devices/{id}` | GET | 远程设备详情 |
| `/api/v1/devices/{id}/tools` | GET | 远程设备工具列表 |
| `/api/v1/devices/{id}/tools/{name}` | POST | 在远程设备执行 MCP 工具 |

### MCP 工具 (4 个)

`fleet.list_devices`, `fleet.device_info`, `fleet.device_tools`, `fleet.exec_tool`

### 多网卡 mDNS

Server 端为每个 LAN 接口创建独立的 mdns.Server 实例，Client 端并行查询所有接口并按 Name 去重。
解决 WiFi↔有线切换后 mDNS 单接口绑定断连的问题。

### 安全

- **Fleet 工具拦截**: `fleet.exec_tool` 在远程执行时屏蔽破坏性工具 (`model.remove`, `engine.remove`, `deploy.delete`, `agent.install`, `stack.init`, `agent.rollback`, `shell.exec`)，防止 Agent 通过远程 Fleet 调用绕过本地安全护栏。
- **API Key 热更新**: `system.config set api_key <KEY>` 立即传播到 Proxy、MCP Server、Fleet Client 三条认证路径，无需重启。
- **LLM Config 热更新**: `system.config set llm.endpoint/llm.model/llm.api_key` 立即热替换 OpenAIClient，无需重启。LLM 配置持久化在 SQLite，优先级: env var > SQLite > default。
- **Timing-safe 比较**: 所有 Bearer token 校验使用 `crypto/subtle.ConstantTimeCompare`，防止侧信道攻击。
- **Fleet Client 并发安全**: `fleet.Client.SetAPIKey()` 使用 `sync.RWMutex` 保护，支持运行时热更新。
- **敏感值脱敏**: `system.config` 读写 `api_key` 和 `llm.api_key` 时响应中显示 `***`，不回显明文。CLI `aima config get/set` 同样脱敏。
- **Fleet 自动发现**: Fleet MCP 工具自带 mDNS 发现能力，`fleet.list_devices` 每次调用都执行扫描，其余 fleet 工具在 registry 为空时自动触发发现。云端 Agent 通过 MCP 即可直接管理 LAN 设备，无需 CLI 或 `serve --discover`。

---

## 8. Agent 安全护栏

### 审计日志

所有 MCP 工具调用记录到 SQLite `audit_log` 表，包含时间戳、工具名、参数、结果、调用来源。

### 回滚快照

破坏性操作 (`model.remove`, `engine.remove`, `deploy.delete`) 执行前自动保存快照到 `rollback_snapshots` 表。
`agent.rollback_list` 查看可回滚操作，`agent.rollback` 一键恢复。

### 破坏性操作拦截

Agent (L3a/L3b) 调用以下工具时被 blockedAgentTools 拦截，需人类确认:
- `model.remove` — 删除模型记录
- `engine.remove` — 删除引擎镜像
- `deploy.delete` — 停止部署
- `agent.install` — 安装 ZeroClaw sidecar

### 工具调用上限

Agent 单次决策循环限制 ≤ 30 轮工具调用 (可配置)，防止无限循环。

---

*最后更新：2026-03-02 (LLM config persistence + fleet MCP auto-discovery + INV-5 parity)*
