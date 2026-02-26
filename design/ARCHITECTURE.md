# AIMA — 系统架构文档

> AI-Inference-Managed-by-AI
> 有限硬件资源上的 AI 推理优化与调度系统

---

## 1. 设计原则

6 条架构原则，指导所有技术决策。

### P1: 基础设施 + 轻量 Agent 便利层

Go 二进制是**薄基础设施层**——硬件检测、知识解析、Pod 生成、请求代理。
简单一次性查询可使用**内置 Go Agent (L3a)** 以最小延迟响应。
复杂智能逻辑由**外部/Sidecar AI Agent (L3b)** 通过 MCP 工具实现。

策略 = Agent 的事。基础设施 = Go 的事。
Go 代码不包含 if-else 决策树、规则引擎、或任何 "策略" 代码。

**关键区分**: 内置 Go Agent 是"便利层"——极简的工具调用循环 (~400 行)，处理简单查询。
ZeroClaw Sidecar 是"嵌入式组件"——完整的持久记忆 + 多 LLM + Identity 能力，
处理复杂推理。两者都不是"框架依赖"——ZeroClaw 的极致轻量 (~8.8MB, <5MB RAM)
使其从框架依赖降级为嵌入式组件，与绑定 LangChain 等重量级框架完全不同的成本收益计算。

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

声明式的好处：幂等、可审计、可回滚、Agent 容易理解和操作。

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
选用外部成熟工具的原因：这些项目有数千贡献者、数年生产验证、持续安全更新。
AIMA 把精力集中在"知识 + 工具胶合"上，不在已有成熟方案的领域重复造轮。

### P5: 渐进智能 L0 → L3b

```
L3b: ZeroClaw ── 完整 AgentLoop + 持久记忆 + Identity ── 最优解 (动态+持久)
  ^ 升级
L3a: Go Agent ── 简单工具调用循环 ──────────────────── 良好解 (动态+无状态)
  ^ override
L2:  知识库 ──── 确定性匹配 ────────────────────────── 已知好解 (静态)
  ^ override
L1:  人类 CLI ── 手动指定参数 ──────────────────────── 指定解
  ^ override
L0:  默认值 ──── 硬编码保守配置 ────────────────────── 可用解 (always)
```

**每层独立可用。** 无 Agent、无网络、无知识库 → L0 仍能启动推理服务。
ZeroClaw 不可用 → L3a；L3a 不可用 → L2；全部不可用 → L0。
这是架构的生存性保证：任何上层组件失败都不导致系统完全不可用。

### P6: 探索即知识

Agent 每次探索（调优、排障、部署尝试）产出结构化的 Knowledge Note：
记录完整探索过程（每次尝试的参数和结果）、最终推荐配置、置信度、人类可读洞察。
其他设备的 Agent 复用这些知识，跳过已知失败、从最优起点开始。

知识是 Agent 留给世界的遗产——即使 Agent 离线，知识仍在。

### P7: 本地优先，偶尔联网

AIMA 面向的边缘设备场景，网络环境不可预期：
- 工厂车间、矿山、医院等场景可能完全离线或间歇联网
- 数据敏感环境（医疗、金融、政务）可能禁止外联
- 即便有网络，带宽和延迟也可能极不稳定

因此 AIMA 的所有核心功能**必须在完全离线下可用**。网络连接是"锦上添花"：
- 离线：L0 默认值 + go:embed 内嵌知识 → 可部署、可推理、可用
- 联网：知识同步 + 模型下载 + 云端 LLM Agent → 更优、更新、更智能

**具体设计约束：**
1. 所有模型文件和引擎镜像支持离线预加载（USB/局域网/预装）
2. 知识库通过 go:embed 编译时内嵌，离线可查询
3. Agent 优先使用本地 LLM，无需外部 API Key
4. 模型/镜像下载支持断点续传，适应不稳定网络
5. 社区知识同步为按需拉取，非强制，支持离线增量包导入

---

## 2. 系统全景

### 四层 + 双脑架构

```
┌─────────────────────────────────────────────────────────────┐
│                                                               │
│   External Agent (远程/强力) — 可选                            │
│   Claude Code / GPT / 自定义 MCP Client                       │
│   用于：最复杂的推理、架构级决策、人机交互式协作                 │
│                                                               │
├───────────────────────────────────────────────────────────────┤
│                                                               │
│   Agent Layer (双皮层 Dual Cortex)                             │
│                                                               │
│   ┌───────────────────────────────────────┐                   │
│   │        Agent Dispatcher               │                   │
│   │   (aima ask → 自动路由 or 手动指定)    │                   │
│   └───────┬─────────────────────┬─────────┘                   │
│           │                     │                              │
│   ┌───────▼───────┐   ┌────────▼──────────┐                  │
│   │ L3a: Go Agent │   │ L3b: ZeroClaw     │                  │
│   │ (内置轻量)     │   │ (Sidecar 进程)     │                  │
│   │               │   │                    │                  │
│   │ - 无状态/会话级│   │ - 持久记忆          │                  │
│   │ - 30轮工具循环 │   │ - 无限轮工具循环    │                  │
│   │ - 单 LLM 后端 │   │ - 22+ LLM Provider │                  │
│   │ - ~0 额外开销  │   │ - ~5MB RAM         │                  │
│   └───────┬───────┘   └────────┬──────────┘                  │
│           │                     │                              │
│           └──────────┬──────────┘                              │
│                      │  同一套 MCP 工具                         │
│                      ▼                                         │
├───────────────────────────────────────────────────────────────┤
│                                                               │
│   Knowledge Layer (知识层)                                     │
│                                                               │
│   5 种知识资产 (YAML): Hardware Profile · Partition Strategy   │
│     · Engine Asset · Model Asset · Knowledge Note             │
│   对齐 PRD 优化链: 需求 → 硬件+划分 → 引擎 → 模型 → 反馈     │
│   go:embed 内嵌 + SQLite 状态 + 社区同步 (git)                 │
│                                                               │
├───────────────────────────────────────────────────────────────┤
│                                                               │
│   Orchestration Layer (编排层)                                 │
│                                                               │
│   K3S (轻量 Kubernetes) + HAMi (GPU 虚拟化中间件)              │
│   声明式 Pod 管理 · 原生健康检查 · 资源限制 · 自动重启          │
│   HAMi: GPU 显存 MB 级切分 · 算力 % 级隔离 · 多厂商支持        │
│                                                               │
├───────────────────────────────────────────────────────────────┤
│                                                               │
│   Infrastructure Layer (基础设施层) — AIMA Go 二进制            │
│                                                               │
│   ~20 个 MCP 工具 · HTTP 推理代理 · 硬件检测                    │
│   模型生命周期管理 · 引擎镜像生命周期管理                        │
│   知识解析 (L0→L3) · Pod YAML 生成 · CLI · mDNS                │
│                                                               │
└─────────────────────────────────────────────────────────────┘
```

### 各层定位

| 层 | 本质 | 谁提供 | 谁维护 |
|----|------|--------|-------|
| External Agent | 远程强力 LLM + 决策循环 | Claude Code / GPT / 自定义 MCP Client | Agent 框架社区 |
| Agent Layer (L3a) | 内置轻量 Go Agent | 本项目 | AI coding agent |
| Agent Layer (L3b) | ZeroClaw Sidecar (可选) | ZeroClaw 项目 | ZeroClaw 社区 |
| Knowledge Layer | YAML 知识文件 + SQLite | AIMA go:embed + 社区贡献 | 社区 + Agent 自动产出 |
| Orchestration Layer | K3S + HAMi | Rancher / CNCF HAMi 项目 | 各自开源社区 |
| Infrastructure Layer | AIMA Go 二进制 | 本项目 | AI coding agent |

### 与 PRD 正交关注面的映射

PRD 定义了 Supply / Demand / Control / Feedback 四个正交关注面（见 PRD §4）。

```
PRD 关注面              架构映射
──────────              ──────────
Supply (供给面)     →   Infrastructure Layer (硬件检测, 模型扫描, 引擎镜像扫描)
                        + Orchestration Layer (K3S + HAMi 资源管理)
                        + Knowledge Layer (Hardware Profile, Partition Strategy)

Demand (需求面)     →   Knowledge Layer (Engine Asset, Model Asset)
                        + Infrastructure Layer (推理代理, Pod 生成)

Control (控制面)    →   Agent Layer (L3a/L3b 决策)
                        + Knowledge Layer (L2 知识匹配)
                        + Infrastructure Layer (L0 默认值, L1 CLI)

Feedback (反馈)     →   Infrastructure Layer (metrics 采集)
                        + Knowledge Layer (Knowledge Note 沉淀)
                        + Agent Layer (分析 + 优化)
```

---

## 3. 编排层 — Multi-Runtime 抽象

### 3.0 Runtime 抽象

AIMA 通过 `runtime.Runtime` 接口抽象部署执行，使知识层和 MCP 工具层无需关心底层运行时：

```
知识层 (YAML)     → 描述"跑什么"（引擎、参数、模型）    → 不知道 runtime 的存在
Resolver          → 选"怎么跑最优"（hardware × engine）  → 按平台过滤引擎
Runtime 接口      → 执行"实际部署"（K3S Pod 或 exec）    → 由 main.go 选择
MCP ToolDeps      → 签名不变，runtime 在闭包内隐藏       → CLI/Agent 无感知
```

**两种 Runtime：**

| Runtime | 适用场景 | 部署方式 | GPU 切分 | 平台 |
|---------|---------|---------|---------|------|
| **K3S** | Linux + K3S 已安装 | Pod YAML + kubectl apply | HAMi 细粒度切分 | Linux |
| **Native** | 跨平台 fallback | 直接 exec 引擎二进制 | 不支持（单进程独占） | 全平台 |

**选择逻辑（`selectRuntime()`）：**
- Linux + K3S 可用 → K3S Runtime（完整体验：GPU 切分、多模型并行、声明式健康检查）
- 否则 → Native Runtime（跨平台 fallback：单模型、进程管理）

**渐进降级（与 L0→L3 同构）：**
```
K3S + HAMi  → 多模型并行 + GPU 细粒度切分 + 声明式生命周期
     ↓ K3S 不可用
Native      → 单模型 + 直接 exec + 极简进程管理（start/stop/logs）
```

知识资产不增加 runtime 维度。Engine Asset 可选地声明 `source` 字段描述二进制来源，
有 `source` 的引擎可在 native runtime 运行（如 llama.cpp），没有的只能容器运行（如 vLLM）。

**Native 二进制自动获取（BinaryManager）：**

当 Native Runtime 需要引擎二进制时，按三级 fallback 链查找：

```
resolveBinary(engineAsset)
  ├── 1. distDir: ~/.aima/dist/{os}-{arch}/{binary}    → 预装/已下载的二进制
  ├── 2. PATH: which/where {binary}                     → 用户手动安装的二进制
  └── 3. 自动下载: BinaryManager.Resolve()
         ├── 从 engineAsset.source.download[platform] 获取 URL
         ├── 下载到 distDir (断点续传)
         ├── chmod +x (非 Windows)
         └── 返回路径
```

`BinaryManager` (`internal/engine/binary.go`) 管理 native 引擎二进制的下载和缓存。
通过 `BinaryResolveFunc` 函数类型注入到 `NativeRuntime`，避免 runtime 包对 engine 包的直接依赖。

**部署后预热（Warmup）：**

引擎冷启动后首次推理通常很慢（CUDA kernel JIT 编译、模型权重加载到 GPU 等）。
Engine Asset 可声明 `warmup` 配置，NativeRuntime 在 health check 通过后自动执行预热：

```
Deploy → 启动进程 → health check 轮询
  → health check 通过
  → warmup: POST /v1/chat/completions {"messages":[...], "max_tokens":1}
  → 预热完成 → 标记 ready
```

预热使用 dummy prompt 触发一次完整推理路径，将 CUDA kernel 编译和模型权重加载提前完成。

### 3.1 为什么选 K3S

K3S 是 Rancher 维护的轻量级 Kubernetes 发行版，单一二进制 (<70MB) 打包了
kube-apiserver、kube-scheduler、kube-controller-manager、kubelet、containerd 和 Flannel 网络。

| 特性 | K3S 实际数据 |
|------|-------------|
| 服务端最低要求 | 2 核 CPU, 2 GB RAM |
| Agent 节点最低 | 1 核 CPU, 512 MB RAM |
| 支持架构 | x86_64, arm64/aarch64, armhf |
| 默认数据库 | 嵌入式 SQLite (单节点); 嵌入 etcd (HA 多节点) |
| 容器运行时 | 内置 containerd，无需单独安装 Docker |
| 单节点实测 | 服务端 ~6% CPU + ~1.6 GB RAM (Intel 8375C, 含工作负载) |
| Agent 实测 | ~3% CPU + ~275 MB RAM (Intel 8375C) |

**可禁用组件**（降低资源消耗）:

```bash
k3s server \
  --disable=traefik \          # 不需要 Ingress Controller
  --disable=metrics-server \   # Agent 用 nvidia-smi 直接采集
  --disable=coredns \          # 单节点不需要集群 DNS
  --disable=servicelb \        # 不需要 LoadBalancer
  --disable=local-storage      # 用 hostPath 直接挂载模型目录
```

禁用这些组件后，单节点场景下 K3S 开销主要由 kube-apiserver + kubelet + containerd 构成。

**与直接管理 Docker 容器相比的核心优势**:

| 能力 | Docker 直接管理 | K3S |
|------|----------------|-----|
| 健康检查 | 需自己写轮询代码 | 原生 livenessProbe / readinessProbe |
| 重启策略 | 需自己写退避逻辑 | restartPolicy + 指数退避 (原生) |
| 资源限制 | docker --cpus, --memory | Pod resources.limits (声明式) |
| GPU 切分 | docker --gpus (全卡或 N 卡) | HAMi: 显存 MB + 算力 % 细粒度 |
| 多容器编排 | 自己管理依赖和顺序 | Pod / Deployment 声明式 |
| 状态查询 | docker inspect (自定义解析) | kubectl get pods (标准 K8s API) |
| 扩展到多节点 | 需额外方案 | K3S agent 加入即可 |

### 3.2 HAMi — GPU 虚拟化中间件

HAMi (Heterogeneous AI Computing Virtualization Middleware) 是 CNCF Sandbox 项目，
从 `k8s-vGPU-scheduler` 演进而来，用于在 Kubernetes 中实现异构 AI 加速器的细粒度切分和隔离。

**核心架构组件**:

| 组件 | 职责 |
|------|------|
| **hami-device-plugin** (DaemonSet) | 运行在每个 GPU 节点，发现 GPU 设备并注册为 K8s 扩展资源 |
| **hami-scheduler** (Deployment) | Scheduler Extender，在 Filter/Score/Bind 阶段让原生调度器"理解" vGPU 资源模型 |
| **MutatingWebhook** | 自动注入 libvgpu.so 到 Pod 容器 |
| **libvgpu.so** (容器内) | 通过 LD_PRELOAD 拦截 CUDA API，实现显存/算力的运行时隔离 |

**GPU 虚拟化原理 (libvgpu.so)**:

libvgpu.so 通过 `LD_PRELOAD` 机制在容器启动时注入。它拦截关键 CUDA API 调用:

- **显存管理**: `cuMemAlloc`, `cuMemAllocPitch`, `cudaMalloc` — 每次分配前检查是否超出配额
- **设备查询**: `cuDeviceTotalMem`, `cuDeviceGetAttribute` — 返回配额值而非物理总量
- **内核执行**: `cuLaunchKernel` — 当启用算力限制时修改内核参数约束 SM 使用率
- **上下文管理**: `cuCtxCreate`, `cuCtxGetCurrent` — 跟踪上下文相关资源消耗

显存隔离是**硬限制**: 超出配额时 `cuMemAlloc` 返回 `cudaErrorMemoryAllocation`。
算力隔离支持三种策略: `default`(仅共享时限制), `force`(始终限制), `disable`(调试用)。

容器状态持久化在 `/usr/local/vgpu/containers/{container-id}.cache`，使用 memory-mapped I/O 实现原子更新。

**支持的 AI 加速器**:

| 厂商 | 设备 |
|------|------|
| NVIDIA | 全系列 GPU (含 A100/H100 MIG 支持) |
| 华为 | 昇腾 910B, 910B3, 310P NPU |
| 寒武纪 | MLU 370, MLU 590 |
| 海光 | DCU Z100, Z100L |
| 天数智芯 | CoreX GPU |
| 摩尔线程 | MTT S4000 GPU |
| MetaX | MXC500 GPU |
| 壁仞 | (路线图中) |

**安装前提**: NVIDIA 驱动 >=440, nvidia-docker >2.0, K8s >=1.18, glibc >=2.17 & <2.30, Kernel >=3.10

**Pod 中声明 GPU 资源**:

```yaml
resources:
  limits:
    nvidia.com/gpu: 1              # GPU 资源名从 Hardware Profile 的 gpu.resource_name 读取
annotations:
  nvidia.com/gpumem: "8192"        # 显存配额 (MB), 前缀 = GPUVendorDomain (resource_name 去除 /gpu)
  nvidia.com/gpucores: "50"        # 算力配额 (%), 同上
```

注：GPU 资源名不再硬编码 `nvidia.com/gpu`，而是从 Hardware Profile 动态读取，
支持多厂商（NVIDIA: `nvidia.com/gpu`, AMD: `amd.com/gpu`, Intel: `gpu.intel.com/i915`）。

HAMi 的 MutatingWebhook 自动:
1. 读取 Pod 的 resource requests
2. 计算显存/算力限额
3. 注入 `LD_PRELOAD=/k8s-vgpu/lib/nvidia/libvgpu.so`
4. 设置 `CUDA_DEVICE_MEMORY_LIMIT` 等环境变量

**AIMA 最小部署策略**:

对于单节点场景：
- 只启用 hami-daemon (device-plugin)，禁用 scheduler 和 WebUI
- 资源预算: ~150m CPU + ~228Mi RAM (每 GPU 节点)
- Helm 参数: `scheduler.enabled=false`, `webui.enabled=false`

### 3.3 引擎部署 = 声明式 Pod YAML

AIMA 不编写容器生命周期管理代码。引擎部署 = 知识库生成 Pod YAML + kubectl apply。

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: vllm-glm4-flash
  labels:
    aima.dev/engine: vllm
    aima.dev/model: glm-4.7-flash
    aima.dev/slot: primary
  annotations:
    nvidia.com/gpumem: "8192"          # HAMi: 显存配额 (MB), 前缀从 GPUResourceName 推导
    nvidia.com/gpucores: "50"          # HAMi: 算力配额 (%), 前缀同上
spec:
  containers:
  - name: inference
    image: vllm/vllm-openai:latest
    command: ["vllm", "serve", "--model", "/models"]
    ports:
    - containerPort: 8000
    resources:
      limits:
        nvidia.com/gpu: "1"            # GPU 资源名从 Hardware Profile 读取
        cpu: "4"                       # (AMD 设备: amd.com/gpu, Intel: gpu.intel.com/i915)
        memory: "16384Mi"
    livenessProbe:
      httpGet:
        path: /health
        port: 8000
      initialDelaySeconds: 30
      periodSeconds: 10
    readinessProbe:
      httpGet:
        path: /health
        port: 8000
      initialDelaySeconds: 10
      periodSeconds: 5
    volumeMounts:
    - name: model-data
      mountPath: /models
      readOnly: true
  volumes:
  - name: model-data
    hostPath:
      path: /mnt/data/models
      type: DirectoryOrCreate
```

**GPU 资源名动态化**: Pod 模板中的 GPU 资源声明（`resources.limits` 和 HAMi annotations）
从 Hardware Profile 的 `gpu.resource_name` 字段读取，不再硬编码 `nvidia.com/gpu`。
HAMi annotation 的 vendor 前缀（如 `nvidia.com/gpumem`）通过 `GPUVendorDomain()` 从资源名自动推导。

**AIMA Go 代码在引擎部署中只做三件事:**
1. 从 Knowledge Layer 渲染 Pod YAML (模板 + 知识资产参数)
2. `kubectl apply -f pod.yaml`
3. `kubectl get pod` / `kubectl logs` (查询状态和日志)

健康检查、重启、资源限制、GPU 隔离——全部由 K3S + HAMi 声明式处理。

### 3.4 编排层扩展路径

```
MVP:   K3S + HAMi (daemon only) — 单节点 GPU 切分
 ↓
v1.0:  + kube-scheduler 原生调度策略 — 更灵活的放置
 ↓
v1.5:  + Volcano — 批量调度 / 队列 / 公平共享 (如需多租户)
 ↓
v2.0:  + Koordinator — 混部 QoS / CPU Burst / 潮汐调度 (如需极致利用率)
```

每次升级不改 AIMA 代码。K3S 生态的调度器插件直接生效。

### 3.5 基础设施栈管理 (Infrastructure Stack Management)

**核心问题：AIMA 依赖 K3S + HAMi + ZeroClaw，但这些组件需要 AIMA 特有的参数调优。
如何确保用户拿到 AIMA 后能一键就绪，同时保持与上游项目的独立性？**

#### 设计原则

AIMA 不 fork 任何上游项目。AIMA 拥有的是**如何配置这些组件的知识**——
版本兼容关系、AIMA 特有的启动参数、不同硬件下的配置变体。
这些知识本身就是 YAML 资产，遵循 P2 原则（知识胜于代码）。

#### Stack Component — 第 6 种知识资产

在 Hardware Profile / Partition Strategy / Engine Asset / Model Asset / Knowledge Note
之外，新增 **Stack Component** 资产类型，描述基础设施依赖：

```yaml
kind: stack_component
metadata:
  name: k3s
  version: "1.31.4+k3s1"
  description: "K3S with AIMA-optimized defaults for edge AI inference"

compatibility:
  aima_min: "0.1.0"

source:
  binary: "k3s"
  platforms: [linux/amd64, linux/arm64]
  # 离线安装包在 dist/{platform}/ 下预置

install:
  method: binary
  # AIMA 特有的低需求配置
  args:
    - flag: "--disable=traefik"
      rationale: "边缘设备不需要 Ingress Controller"
      source: "k3s-docs"
      verified: true
    - flag: "--disable=servicelb"
      rationale: "不需要 LoadBalancer"
      source: "k3s-docs"
      verified: true
    - flag: "--disable=metrics-server"
      rationale: "AIMA 通过 nvidia-smi 直接采集 GPU 指标"
      source: "hypothesis"
      verified: false
    - flag: "--kubelet-arg=max-pods=20"
      rationale: "边缘单节点场景不需要默认 110 pods 上限"
      source: "community"
      verified: false
  env:
    INSTALL_K3S_SKIP_DOWNLOAD: "true"   # 使用本地预置包

verify:
  command: "k3s kubectl get nodes"
  ready_condition: "Ready"
  timeout_s: 60

# 不同硬件画像的配置变体
profiles:
  nvidia-gb10-arm64:
    extra_args:
      - flag: "--kubelet-arg=kube-reserved=cpu=500m,memory=512Mi"
        rationale: "为系统保留资源，避免挤占推理 VRAM"
        verified: false
  nvidia-rtx4090-x86:
    extra_args:
      - flag: "--kubelet-arg=kube-reserved=cpu=1000m,memory=1Gi"
        rationale: "x86 服务器有更多 CPU/RAM 余量"
        verified: false

# 冷启动阶段待实验的问题
open_questions:
  - question: "GB10 unified memory 下 kubelet reserved 设多少合适？"
    hypothesis: "cpu=500m,memory=512Mi"
    test_method: "部署后观察 kubectl top node 和推理 VRAM 余量"
  - question: "关闭 metrics-server 是否影响 HAMi device plugin 上报？"
    hypothesis: "不影响，HAMi 用独立的 gRPC 注册"
    test_method: "关闭后部署多模型分区，观察 GPU 分配"
```

**每个配置值都有来源 (`source`) 和验证状态 (`verified`)**——
诚实区分"文档推荐的"、"社区实践的"和"假设待验证的"。
Agent 可以自动处理 `open_questions`，在真机上实验并将结果写回 Knowledge Note。

#### 冷启动知识获取策略

Stack Component 中的先验知识经历三个阶段：

```
阶段 1: 人工研究 (不可跳过)
  读文档 + 社区最佳实践 → 写初始 YAML → 标记 verified: false
  列出 open_questions → 等待真机验证

阶段 2: 真机验证 (Agent 辅助)
  aima init 在真机上运行 → 解决 open_questions
  Agent 记录结果为 Knowledge Note → 更新 verified: true

阶段 3: 社区飞轮
  不同硬件的用户贡献验证结果 → profiles 越来越丰富
  同型硬件自动复用已验证配置
```

#### aima init 工作流

```
aima init
  │
  ├── 1. 读 catalog/stack/*.yaml (知道要装什么、什么版本、什么配置)
  ├── 2. hardware.detect (检测当前硬件，选择对应 profile)
  ├── 3. 逐项检查已安装组件:
  │      ├── 已有且版本兼容 → 跳过 ✓
  │      ├── 已有但版本不兼容 → 提示升级
  │      └── 未安装 → 从 dist/ 本地包安装 + 应用 AIMA 配置
  ├── 4. 安装 K3S (dist/ 预置包 + stack YAML 配置参数)
  ├── 5. 安装 HAMi (helm install 本地 chart + stack YAML values)
  ├── 6. 逐项验证 (每个组件的 verify 条件)
  └── 7. 输出就绪状态
```

**Go 代码只需一个通用的 "stack installer"**——
读 stack YAML → 执行安装命令 → 应用配置 → 验证。
不需要为 K3S 和 HAMi 分别写安装逻辑。
增加新的栈组件 = 加一个 YAML 文件，遵循 P2 原则。

#### 离线安装包

```
dist/                          # 预构建安装包 (git-lfs 或独立 release)
  linux-amd64/
    k3s                        # K3S 二进制 (~70MB)
    hami-chart.tgz             # HAMi Helm chart (锁定版本+AIMA values)
    zeroclaw                   # ZeroClaw 二进制 (~8.8MB)
  linux-arm64/
    ...
```

`dist/` 确保完全离线安装。`aima init` 优先使用本地包，无网络时不尝试下载。

---

## 4. 知识架构

**这是架构的核心。知识按 PRD 的优化链路组织，以双形态（YAML + SQLite 关系表）存在。**

### 4.1 双层知识架构

知识以两种形态存在，各司其职：

| 维度 | YAML（编写/分发格式） | SQLite（查询/推理格式） |
|------|---------------------|----------------------|
| 受众 | 人类、git、go:embed | Agent、MCP 工具 |
| 优势 | 可读、可 diff、可版本管理 | 可查询、可 JOIN、可聚合 |
| 内容 | 静态知识资产定义 | 静态知识 + 动态实验数据 |
| 变更 | PR/编辑 → 重编译或 sync | Agent 探索 → 直接写入 |

```
边缘设备 (AIMA Go 二进制)
┌────────────────────────────────────────────────┐
│                                                  │
│  catalog/ (go:embed YAML) ── 社区共识知识         │
│    Hardware Profiles / Engine Assets              │
│    Model Assets / Partition Strategies            │
│         │                                        │
│         │ 启动时加载                               │
│         ▼                                        │
│  ┌──────────────────────────────────────────┐    │
│  │  SQLite (aima.db)                         │    │
│  │                                            │    │
│  │  静态知识表 (7 张, 启动时重建)               │    │
│  │    hardware_profiles, engine_assets,       │    │
│  │    engine_features, engine_hardware_compat,│    │
│  │    model_assets, model_variants,           │    │
│  │    partition_strategies                     │    │
│  │                                            │    │
│  │  动态知识表 (3 张, 持久保存)                 │    │
│  │    configurations, benchmark_results,      │    │
│  │    perf_vectors                             │    │
│  │                                            │    │
│  │  系统表 (5 张, 从 v1 保留)                   │    │
│  │    models, engines, knowledge_notes,       │    │
│  │    config, audit_log                       │    │
│  └──────────────┬───────────────────────────┘    │
│                  │                                │
│            MCP 工具                               │
│         (SQL 预处理后返回精炼结果)                  │
│                  │                                │
│            Agent (L3a/L3b)                        │
│         (基于精炼结果做高层决策)                    │
└────────────────────────────────────────────────┘
```

**核心设计原则**：基础设施层做数据预处理（filter/sort/aggregate/join），Agent 做高层决策。
Agent 不再直接消费大量原始 YAML 文本，而是通过 MCP 工具拿到已处理的精炼结果。

### 4.2 优化链路 → 知识结构

PRD（§3）定义了带约束的优化问题。知识资产直接对齐优化链路的每个环节:

```
需求声明 (what to run)
  ↓ 感知
硬件能力 + 约束 (what I have)               → Hardware Profile
  ↓ 划分
资源划分策略 (how to split resources)        → Partition Strategy
  ↓ 选择
引擎 + 配置 (how to run, 三重角色)           → Engine Asset
  ↓ 部署
模型 + 配置 (what variant, quantization)     → Model Asset
  ↓ 验证 + 积累
实测配置 + 多维性能数据 (feedback)           → Configuration + BenchmarkResult
探索叙事 + 洞察 (narrative)                  → Knowledge Note
```

**静态知识（go:embed YAML，编译时确定，启动时加载到 SQLite）**:

| 资产 | 对应优化链路 | 索引键 | SQLite 表 |
|------|------------|--------|----------|
| **Hardware Profile** | Supply: 硬件能力 + 约束 | `gpu_arch × cpu_arch` | hardware_profiles |
| **Partition Strategy** | Supply: 资源如何切分 | `hardware_profile × workload_pattern` | partition_strategies |
| **Engine Asset** | Engine: 引擎定义 + 三重角色 | `engine_type × gpu_arch` | engine_assets + engine_features + engine_hardware_compat |
| **Model Asset** | Model: 模型定义 + 硬件变体 | `model_name × hw_variant` | model_assets + model_variants |
| **Stack Component** | Infra: 基础设施依赖配置 | `component_name × platform` | —（不入 SQLite，仅启动时读取） |

**动态知识（运行时产生，SQLite 持久化）**:

| 资产 | 对应优化链路 | 索引键 | SQLite 表 |
|------|------------|--------|----------|
| **Configuration** | 4D 配置实例 (HW×Engine×Model×Config) | `hardware_id × engine_id × model_id` | configurations |
| **BenchmarkResult** | 多维性能数据 (延迟/吞吐/资源/功耗) | `config_id × concurrency × load_profile` | benchmark_results |
| **PerfVector** | 归一化性能向量 (用于相似度检索) | `config_id` | perf_vectors |
| **Knowledge Note** | 探索叙事 + 洞察（对 Configuration 的补充） | `hardware × model × engine` | knowledge_notes |

### 4.3 Hardware Profile

描述硬件的能力向量和约束。对应 PRD 优化模型中的 Resource 向量 R。

```yaml
kind: hardware_profile
metadata:
  name: nvidia-gb10-arm64
  description: "NVIDIA DGX Spark GB10, ARM64, 128GB unified memory"
hardware:
  gpu:
    arch: Blackwell
    vram_mib: 15360
    compute_capability: "10.0"
    cuda_cores: 2048
    resource_name: "nvidia.com/gpu"      # K8s GPU 资源名 (AMD: "amd.com/gpu", 无GPU留空)
  cpu:
    arch: arm64
    cores: 12
    freq_ghz: 3.0
  ram:
    total_mib: 131072
    bandwidth_gbps: 200
  unified_memory: true
constraints:
  tdp_watts: 100                         # PRD 能源约束
  power_modes: [15W, 30W, 60W, 100W]    # 可选功耗模式
  cooling: passive
partition:
  gpu_tools: [hami, engine_params]       # 可用的 GPU 切分方式
  cpu_tools: [k3s_cgroups]               # 可用的 CPU 切分方式
```

`gpu.resource_name` 用于 Pod YAML 生成时的 GPU 资源声明。
Resolver 通过 `findGPUResourceName()` 查找当前硬件的 `resource_name`，
默认 `"nvidia.com/gpu"`。AMD 设备使用 `"amd.com/gpu"`，无 GPU 设备留空。

### 4.4 Partition Strategy

描述在特定硬件上如何切分资源给多个工作负载。
直接对应 PRD 约束条件 (1) Σ cost ≤ effective_R。

```yaml
kind: partition_strategy
metadata:
  name: gb10-dual-model
  description: "GB10 上同时运行 2 个模型的资源划分方案"
target:
  hardware_profile: nvidia-gb10-arm64
  workload_pattern: dual_model           # single_model | dual_model | multi_model
slots:
  - name: primary
    gpu: {memory_mib: 10240, cores_percent: 60}
    cpu: {cores: 8}
    ram: {mib: 65536}
  - name: secondary
    gpu: {memory_mib: 4096, cores_percent: 30}
    cpu: {cores: 4}
    ram: {mib: 32768}
  - name: system_reserved
    gpu: {memory_mib: 1024, cores_percent: 10}
    cpu: {cores: 2}
    ram: {mib: 16384}
    note: "系统 + AIMA + Agent 保留"
```

### 4.5 Engine Asset

描述引擎在特定硬件上的行为，包含 PRD 定义的三重角色（连接器 + 分配器 + 放大器）信息。
**同时描述引擎的容器镜像信息，用于本地扫描和拉取。**

```yaml
kind: engine_asset
metadata:
  name: vllm-0.8-blackwell
  type: vllm
  version: "0.8"
image:
  name: vllm/vllm-openai
  tag: "latest"
  size_approx_mb: 8500                   # 镜像预估大小，用于空间检查
  platforms: [linux/amd64, linux/arm64]  # 支持的平台
  registries:                            # 按优先级排列的镜像源
    - docker.io/vllm/vllm-openai
    - registry.cn-hangzhou.aliyuncs.com/aima/vllm-openai   # 国内镜像
source:                                  # Native 运行时二进制来源（可选，容器专属引擎不需要）
  binary: "llama-server"                 # 二进制名（PATH 搜索 + distDir 搜索）
  platforms: [linux/amd64, linux/arm64, darwin/arm64, windows/amd64]
  download:                              # 按平台的下载 URL
    linux/amd64: "https://github.com/.../llama-server-linux-x64"
    darwin/arm64: "https://github.com/.../llama-server-macos-arm64"
  mirror:                                # 国内镜像（可选）
    linux/amd64: "https://mirror.example.com/.../llama-server-linux-x64"
hardware:
  gpu_arch: Blackwell
  vram_min_mib: 4096
startup:
  command: ["vllm", "serve", "--model", "{{.ModelPath}}"]
  default_args:
    port: 8000
    gpu_memory_utilization: 0.75
    max_model_len: 8192
  health_check:
    path: /health
    timeout: 5m
  warmup:                                # 部署后预热配置（可选）
    enabled: true                        # 是否启用预热
    prompt: "Hello"                      # dummy prompt 触发 CUDA kernel JIT
    max_tokens: 1                        # 最小生成量
    timeout_s: 30                        # 预热超时
api:
  protocol: openai
  base_path: /v1

# PRD 核心概念: 引擎三重角色
amplifier:                                    # 放大器特性
  features:
    - paged_attention
    - continuous_batching
    - flash_attention
  performance_gain: "2-4x throughput vs naive serving"
  resource_expansion:                         # 资源边界扩展 → effective_R(engine, R)
    cpu_offload: false
    ssd_offload: false
    npu_offload: false

partition_hints:                              # 给 Partition Strategy 的参考
  min_gpu_memory_mib: 4096
  recommended_gpu_cores_percent: 50

time_constraints:                             # PRD 时间约束
  cold_start_s: [30, 60]
  model_switch_s: [30, 60]                    # 需重启容器

power_constraints:                            # PRD 能源约束
  typical_draw_watts: [60, 80]
```

**`source` 字段**: 描述 native 二进制的来源信息。有此字段的引擎可在 Native Runtime 运行
（BinaryManager 自动下载），没有的只能通过容器运行（K3S Runtime）。

**`warmup` 字段**: 部署后自动预热。NativeRuntime 在 health check 通过后发送 dummy 推理请求，
提前完成 CUDA kernel 编译和模型权重 GPU 加载，减少用户首次推理的等待时间。

**引擎放大器对比示例**:

| 引擎 | resource_expansion | 效果 |
|------|-------------------|------|
| vLLM | cpu_offload: false | 纯 GPU 推理，性能最优但受限于 VRAM |
| llama.cpp | cpu_offload: true, n_gpu_layers 可调 | GPU+RAM 联合推理，16GB VRAM 设备可跑 70B 模型 |
| KTransformers | cpu_offload: true, expert-level | MoE 架构按专家粒度分配 GPU/CPU |

这直接对应 PRD 的 `effective_R(engine, R) ≥ R` 概念。

### 4.6 Model Asset

描述模型在不同硬件/引擎组合下的变体配置。
**同时描述模型的存储位置和下载源信息。**

```yaml
kind: model_asset
metadata:
  name: glm-4.7-flash
  type: llm
  family: glm
  parameter_count: "9B"
storage:
  formats: [safetensors, gguf]           # 支持的存储格式
  default_path_pattern: "{{.DataDir}}/models/{{.Name}}"
  sources:                               # 按优先级排列的下载源
    - type: huggingface
      repo: THUDM/GLM-4.7-Flash
    - type: modelscope                   # 国内镜像
      repo: ZhipuAI/GLM-4.7-Flash
    - type: local_path                   # 支持指定本地路径
      path: ""
variants:
  - name: glm-4.7-flash-blackwell-vllm
    hardware:
      gpu_arch: Blackwell
      vram_min_mib: 6144
      unified_memory: true
    engine: vllm
    format: safetensors
    default_config:
      gpu_memory_utilization: 0.50
      max_model_len: 8192
      dtype: bfloat16
    expected_performance:
      tokens_per_second: [18, 25]
      latency_first_token_ms: [30, 80]

  - name: glm-4.7-flash-blackwell-llamacpp
    hardware:
      gpu_arch: Blackwell
      vram_min_mib: 4096
    engine: llamacpp
    format: gguf
    default_config:
      n_gpu_layers: 33
      ctx_size: 8192
    expected_performance:
      tokens_per_second: [12, 18]
      latency_first_token_ms: [50, 150]
```

### 4.7 Knowledge Note

Agent 探索的结构化输出。角色从"唯一的动态知识载体"变为"对 Configuration 的补充叙事"。

```yaml
kind: knowledge_note
metadata:
  id: kn-abc123
  title: "GLM-4.7-Flash + vLLM optimal on GB10"
  tags: [tuning, gb10, glm-4.7-flash, vllm]
  created: "2026-02-20T14:30:00Z"

context:
  hardware_profile: nvidia-gb10-arm64
  partition_strategy: gb10-dual-model
  slot_used: primary

exploration:
  model: GLM-4.7-Flash
  engine: vllm
  trials:
    - config: {gpu_memory_utilization: 0.90, max_model_len: 8192}
      partition: {gpu_memory_mib: 10240, gpu_cores_percent: 60}
      result: {status: failed, error: "OOM after 30s"}
    - config: {gpu_memory_utilization: 0.80, max_model_len: 4096}
      partition: {gpu_memory_mib: 10240, gpu_cores_percent: 60}
      result:
        status: success
        throughput_tps: 21.2
        latency_p50_ms: 45
        startup_time_s: 42
        power_draw_watts: 72

recommendation:
  config: {gpu_memory_utilization: 0.80, max_model_len: 4096}
  confidence: high

insights: |
  GB10 unified memory 允许 gpu_mem_util 到 0.80。
  超过 0.85 时长上下文生成会 OOM。
  TRITON_MLA attention backend 对 GLM 架构有显著收益。

provenance:
  method: agent_tuning
  agent_model: "claude-opus-4"
  session_id: "ts-xyz789"
```

### 4.8 动态知识：Configuration + BenchmarkResult

**Configuration** 是知识图谱的核心节点——每个"真实测试过的 Hardware×Engine×Model×Config 组合"：

```yaml
# 存储在 SQLite configurations 表
id: cfg-abc123
hardware_id: nvidia-gb10-arm64
engine_id: vllm-0.8-blackwell
model_id: glm-4.7-flash
config: {"gpu_memory_utilization": 0.80, "max_model_len": 4096}
config_hash: "sha256:..."               # 用于去重
derived_from: cfg-xyz789                  # 演化链：从哪个配置派生
status: production                        # experiment → candidate → production
source: local                             # local | community | agent
```

**BenchmarkResult** 记录同一配置在不同负载条件下的多维性能数据：

```yaml
# 存储在 SQLite benchmark_results 表
config_id: cfg-abc123
concurrency: 32                           # 同一配置在不同并发下性能不同
input_len_bucket: "128-512"
throughput_tps: 21.2
ttft_ms_p95: 120
vram_usage_mib: 8100
power_draw_watts: 72
oom_occurred: false
```

**PerfVector** 从 benchmark_results 聚合生成，6 维归一化向量用于相似度检索：

```
[norm_ttft_p95, norm_tpot_p95, norm_throughput, norm_qps, norm_vram, norm_power]
```

向量相似度在 Go 内存中计算（100-1000 条 × 6 维 < 0.1ms），支持加权欧氏距离。
用途：跨硬件配置迁移推荐——"在 A100 上表现类似的配置，在 GB10 上对应哪个？"

### 4.9 L0 → L3b 知识解析

ConfigResolver 按优先级合并多层知识:

```
L0: engine_asset.default_args                 (go:embed YAML, always available)
 ↓ merge (高层 override 低层)
L1: 用户 CLI --config / --engine / --slot     (人类显式指定)
 ↓ merge
L2: knowledge_note.recommendation.config      (Agent/社区知识)
    + partition_strategy.slots                (资源划分策略)
 ↓ merge
L3a: Go Agent 实时决策 (无状态工具循环)        (简单动态优化)
 ↓ merge
L3b: ZeroClaw 实时决策 (持久记忆+跨会话)       (复杂动态优化)
```

合并逻辑: 简单的 `map[string]any` 合并，高层覆盖低层同名 key。
ResolvedConfig 记录每个 key 的来源层级，支持审计追踪。

**Auto-Resolve 兜底**: 当模型不在 YAML catalog 中时，`resolveWithFallback` 自动从 `models` 表
（`model scan` 写入的扫描记录）构建"合成 ModelAsset"：

```
cat.Resolve(model) → "not found in catalog"
  │
  ├── db.FindModelByName(model)  → 优先级: 精确名 → 不区分大小写 → 子串匹配
  │     └── 未找到 → 报错: "not found in catalog (also not found in scan database)"
  │     └── 无 format → 报错: "found on disk but has no format info"
  │
  ├── BuildSyntheticModelAsset(name, type, arch, params, format)
  │     └── format → engine 映射: safetensors→vllm, gguf→llamacpp, 未知→llamacpp
  │     └── 生成 gpu_arch="*" 通配变体，空 DefaultConfig（依赖引擎 L0 默认值）
  │
  ├── cat.RegisterModel(synth)  → 注册到内存 catalog（去重）
  ├── overrides["model_path"] = dbModel.Path  → 使用实际磁盘路径
  └── cat.Resolve(dbModel.Name) → 正常 L0 合并流程
```

这保证了 `model scan` 发现的任何模型都能"零 YAML"部署，同时保留完整的 L0→L1 合并链路。

### 4.10 YAML → SQLite 加载流程

每次 `aima start` 执行以下流程，确保 SQLite 与 go:embed YAML 一致：

```
aima start
  │
  ├── 1. 打开/创建 SQLite (~/.aima/aima.db)
  ├── 2. Schema 迁移 (IF NOT EXISTS + 版本检查)
  ├── 3. 清空静态知识表（FK-safe 顺序）
  │      DELETE FROM engine_hardware_compat;
  │      DELETE FROM engine_features;
  │      DELETE FROM model_variants;
  │      DELETE FROM engine_assets; DELETE FROM model_assets;
  │      DELETE FROM hardware_profiles; DELETE FROM partition_strategies;
  ├── 4. 加载 go:embed YAML → 解析 → INSERT 到关系表
  │      ├── catalog/hardware/*.yaml  → hardware_profiles
  │      ├── catalog/engines/*.yaml   → engine_assets + engine_features + engine_hardware_compat
  │      │     (gpu_arch: "*" 通配 → 为所有 hardware_profiles 创建兼容行)
  │      ├── catalog/models/*.yaml    → model_assets + model_variants
  │      │     (variant.gpu_arch 匹配 → 按 hardware_profile 展开)
  │      └── catalog/partitions/*.yaml → partition_strategies
  ├── 5. 动态知识表不清空（configurations, benchmark_results, perf_vectors 持久保留）
  ├── 6. ANALYZE（更新索引统计信息，帮助查询优化器）
  └── 7. 就绪
```

**关键设计**：YAML 中的一个 EngineAsset 可能展开为 3 张表的多行数据。
例如一个 `gpu_arch: "*"` 的通用引擎会在 engine_hardware_compat 中为每个 hardware_profile 创建一行。

### 4.11 知识生命周期与共享

```
Agent 探索 (L3a/L3b)
  → 产出 Configuration + BenchmarkResult + Knowledge Note
  → 保存到本地 SQLite
         │
         ├── 上报: aima knowledge sync --push (需联网)
         │         → POST 到中心端知识聚合服务 (v1.0)
         │         → config_hash 去重，新 benchmark 追加
         │
         ├── 拉取: aima knowledge sync --pull (需联网)
         │         → 按 hardware_id 过滤拉取社区知识
         │         → INSERT OR IGNORE 本地 SQLite
         │         → 标记 source='community'
         │
         ├── 导出: aima knowledge export → JSON 文件
         │         → USB/共享目录离线传递
         │
         └── 离线导入: aima knowledge import <路径>
                       → 从 JSON 文件加载
                       → 适用于隔离网络环境
```

**知识复用的具体过程**:

1. 设备 A 的 Agent 探索 GLM-4.7-Flash + vLLM on GB10
2. 产出 Configuration (gpu_mem_util=0.80) + BenchmarkResult (21.2 tps)
3. 上报到中心端 / 导出到 USB
4. 设备 B (同型硬件) 拉取/导入
5. 设备 B 的 Agent 通过 `knowledge.search_configs` 查到这个配置 → **直接从 0.80 开始微调**
6. 发现 0.82 更好 → 产出新 Configuration (derived_from=原配置) → 反哺社区

每次探索让全球同硬件设备受益。Configuration 的 `derived_from` 链记录完整的演化历史。

---

## 5. 模型生命周期管理

**核心问题：如何发现、注册、获取和管理本地模型文件。**

AIMA 的设计基于"本地优先"原则：模型文件的首要来源是本地磁盘，下载是补充手段。

### 5.1 模型发现：本地扫描

AIMA 启动时及用户手动触发时，扫描本地已存在的模型文件。

**扫描路径优先级** (可配置):

```
1. $AIMA_MODEL_DIR            (用户显式配置)
2. ~/.aima/models/             (AIMA 默认目录)
3. ~/.cache/huggingface/hub/   (HuggingFace 本地缓存)
4. ~/.ollama/models/           (Ollama 模型目录)
5. /mnt/data/models/           (挂载数据盘，常见于服务器/边缘设备)
6. 用户额外指定的扫描路径       (aima config model_scan_paths)
```

**扫描识别逻辑**:

```
扫描目录
  │
  ├── 发现 config.json + *.safetensors  → 识别为 HuggingFace 格式模型
  │   └── 解析 config.json 提取: model_type, hidden_size, num_layers
  │       → 匹配 Model Asset (如有) → 自动填充最佳配置
  │
  ├── 发现 *.gguf                       → 识别为 GGUF 格式模型
  │   └── 解析 GGUF header 提取: architecture, context_length, quantization
  │       → 匹配 Model Asset → 自动填充引擎偏好 (llamacpp)
  │
  ├── 发现 tokenizer_config.json         → 辅助识别模型类型 (llm/vlm/etc)
  │
  └── 发现 manifest.json (Ollama 格式)   → 提取模型信息
```

**扫描结果 → SQLite 注册**:

```sql
INSERT INTO models (id, name, type, path, format, size_bytes, detected_arch, detected_params)
VALUES ('sha256:...', 'GLM-4.7-Flash', 'llm', '/mnt/data/models/GLM-4.7-Flash',
        'safetensors', 18000000000, 'glm', '9B');
```

**与 Knowledge Layer 的关联**:
- 扫描发现模型后，自动在 Model Asset 中查找匹配项
- 匹配成功 → 该模型拥有完整的引擎推荐、硬件变体配置、性能预期
- 匹配失败 → **Auto-Resolve 兜底**：从扫描记录的 format 字段自动推断引擎
  (safetensors→vllm, gguf→llamacpp)，构建合成 ModelAsset，使用引擎 L0 默认配置部署

### 5.2 模型获取：下载与预加载

**获取方式优先级** (本地优先):

| 方式 | 场景 | 网络要求 | 优先级 |
|------|------|---------|--------|
| 本地已存在 | 扫描命中 | 无 | 最高 |
| 局域网共享 | 同网段其他 AIMA 设备 (mDNS) | 局域网 | 高 |
| USB/移动存储导入 | `aima model import /media/usb/model.gguf` | 无 | 高 |
| 离线预装包 | 设备出厂预装或系统管理员预置 | 无 | 高 |
| ModelScope 下载 | 国内网络环境优先 | 互联网 | 中 |
| HuggingFace 下载 | 国际网络环境 | 互联网 | 低 |

**下载流程** (需联网时):

```
aima model pull glm-4.7-flash
  │
  ├── 1. 查找 Model Asset YAML → 获取 sources 列表
  │
  ├── 2. 空间检查: 磁盘剩余空间 > 模型大小 × 1.2 (留 20% 余量)
  │
  ├── 3. 按 sources 优先级尝试下载:
  │      └── ModelScope (国内) → HuggingFace (国际) → 用户自定义源
  │
  ├── 4. 断点续传: 使用 HTTP Range 请求，支持中断后继续
  │      └── 下载进度持久化在 SQLite (已下载字节数、校验和)
  │
  ├── 5. 完整性校验: SHA256 校验
  │
  └── 6. 注册到 SQLite models 表 → 可立即部署
```

**离线预加载方案** (用于隔离网络):

```bash
# 在有网环境准备离线包
aima model export glm-4.7-flash --output /media/usb/

# 在隔离环境导入
aima model import /media/usb/glm-4.7-flash/
```

### 5.3 模型状态机

```
                 ┌─────────┐
                 │ Unknown │  (扫描发现但未识别)
                 └────┬────┘
                      │ 识别成功
                      ▼
┌──────────┐    ┌──────────┐    ┌──────────┐
│Downloading│──→│Registered│──→│ Deployed  │
│ (下载中)   │    │ (已注册)   │    │ (已部署)   │
└──────────┘    └──────────┘    └──────────┘
      │               │               │
      │ 失败/取消       │ 删除           │ undeploy
      ▼               ▼               ▼
┌──────────┐    ┌──────────┐    ┌──────────┐
│  Failed  │    │ Removed  │    │Registered│
└──────────┘    └──────────┘    └──────────┘
```

### 5.4 MCP 工具 (模型管理)

| 工具 | 功能 |
|------|------|
| `model.scan` | 扫描本地模型目录，发现并注册新模型 |
| `model.list` | 列出所有已注册模型 (含状态、大小、格式) |
| `model.pull` | 从远程源下载模型 (断点续传) |
| `model.import` | 从本地路径/USB 导入模型 |
| `model.remove` | 注销模型记录 (可选删除文件) |
| `model.info` | 查询模型详细信息 (含匹配的 Knowledge Note) |

---

## 6. 引擎镜像生命周期管理

**核心问题：如何发现、注册、获取和管理推理引擎的容器镜像。**

引擎镜像是部署推理服务的前提。与模型类似，AIMA 遵循"本地优先"原则。

### 6.1 引擎镜像发现：本地扫描

AIMA 利用 K3S 内置的 containerd 来管理容器镜像。扫描逻辑：

```
containerd 镜像列表 (ctr images ls / crictl images)
  │
  ├── 匹配 Engine Asset YAML 中的 image.name:tag
  │   └── vllm/vllm-openai:latest       → 标记为 "vllm" 引擎可用
  │   └── ghcr.io/ggerganov/llama.cpp:server → 标记为 "llamacpp" 引擎可用
  │
  ├── 识别 Ollama 兼容镜像 (如已安装 Ollama)
  │
  └── 扫描结果注册到 SQLite engines 表
```

**注册信息**:

```sql
CREATE TABLE engines (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,               -- vllm | llamacpp | ollama | sglang | ...
    image TEXT NOT NULL,              -- 完整镜像名 (含 registry)
    tag TEXT NOT NULL,
    size_bytes INTEGER,
    platform TEXT,                    -- linux/amd64 | linux/arm64
    available BOOLEAN DEFAULT TRUE,   -- 镜像是否在本地
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

### 6.2 引擎镜像获取

**获取方式优先级** (本地优先):

| 方式 | 场景 | 网络要求 |
|------|------|---------|
| 本地已存在 | containerd 已有镜像 | 无 |
| 离线导入 OCI tar | `aima engine import /media/usb/vllm.tar` | 无 |
| 局域网 Registry | 企业内部镜像仓库 | 局域网 |
| 国内镜像 | registry.cn-hangzhou.aliyuncs.com | 互联网 (国内) |
| Docker Hub | docker.io | 互联网 (国际) |

**拉取流程**:

```
aima engine pull vllm
  │
  ├── 1. 查找 Engine Asset YAML → 获取 image.registries 列表
  │
  ├── 2. 空间检查: 磁盘剩余 > image.size_approx_mb × 1.5
  │
  ├── 3. 按 registries 优先级 + 网络环境自动选择:
  │      ├── 检测网络可达性 (timeout 3s)
  │      ├── 国内 IP → 优先使用国内镜像源
  │      └── 国际 IP → 使用 Docker Hub
  │
  ├── 4. 通过 containerd (ctr/crictl) 拉取:
  │      └── crictl pull <registry>/<image>:<tag>
  │
  ├── 5. 拉取成功 → 更新 SQLite engines 表
  │
  └── 6. Agent 可通过 deploy.apply 使用此引擎
```

**离线预装方案**:

```bash
# 在有网环境导出 OCI 镜像
aima engine export vllm --output /media/usb/vllm-latest.tar

# 在隔离环境导入
aima engine import /media/usb/vllm-latest.tar
# 内部执行: ctr -n k8s.io images import vllm-latest.tar
```

### 6.3 引擎可用性矩阵

AIMA 在部署前检查引擎是否可用：

```
deploy.apply(engine=vllm, model=glm-4.7-flash)
  │
  ├── 检查 1: 模型文件是否已注册? (SQLite models 表)
  │   └── 否 → 提示用户: "模型未找到，运行 aima model pull glm-4.7-flash"
  │
  ├── 检查 2: 引擎镜像是否已存在? (SQLite engines 表 / containerd)
  │   └── 否 → 自动尝试拉取 (如有网络) / 提示离线导入
  │
  ├── 检查 3: 硬件兼容? (Engine Asset gpu_arch 匹配 Hardware Profile)
  │   └── 否 → 报错: "vLLM 不支持当前 GPU 架构"
  │
  └── 全部通过 → 生成 Pod YAML → kubectl apply
```

### 6.4 Native 引擎二进制管理

除容器镜像外，AIMA 还管理 native 引擎二进制（用于非 K3S 环境：Windows、macOS、无 K3S 的 Linux）。

**`BinaryManager` (`internal/engine/binary.go`)** 负责 native 引擎二进制的解析、下载和缓存：

```
BinaryManager.Resolve(ctx, source)
  │
  ├── 1. distDir 查找: ~/.aima/dist/{os}-{arch}/{binary}
  │      → 预装或之前下载的二进制
  │
  ├── 2. PATH 查找: which/where {binary}
  │      → 用户手动安装到 PATH 的二进制
  │
  └── 3. 自动下载:
         ├── 检查 platform 兼容性 (source.platforms)
         ├── 选择 URL: 优先 mirror (国内)，fallback 到 download (国际)
         ├── 下载到 distDir
         ├── chmod +x (非 Windows)
         └── 返回完整路径
```

**与 NativeRuntime 的集成**:
- `BinaryManager` 通过 `BinaryResolveFunc` 函数类型注入到 `NativeRuntime`
- `NativeRuntime.Deploy()` 在 `findInDist` 失败后调用 `resolveBinary` 作为第三级 fallback
- 类型转换在 `main.go` 的 `selectRuntime()` 中完成，避免 runtime ↔ engine 包直接依赖

**binary 缓存目录**:
```
~/.aima/
  dist/
    linux-amd64/
      llama-server           # llamacpp binary
    darwin-arm64/
      llama-server
    windows-amd64/
      llama-server.exe
```

### 6.5 MCP 工具 (引擎管理)

| 工具 | 功能 |
|------|------|
| `engine.scan` | 扫描 containerd 已有镜像，匹配 Engine Asset |
| `engine.list` | 列出所有已注册引擎 (含可用状态) |
| `engine.pull` | 拉取引擎镜像 (自动选择最快源) |
| `engine.import` | 从 OCI tar 文件导入镜像 |
| `engine.remove` | 删除引擎镜像 |

---

## 7. Agent 架构 — 双皮层 (Dual Cortex)

### 7.1 核心命题

**远程强 LLM+Agent 框架 与 本地轻量 Agent 并不冲突，可以共存。**

AIMA 实现两级本地 Agent：
- **L3a: Go Agent** — 内置于 AIMA 二进制，无状态/会话级，处理简单查询
- **L3b: ZeroClaw Sidecar** — 可选的独立进程，持久记忆+跨会话学习，处理复杂任务

两者共享同一套 MCP 工具，对外部 Agent（Claude Code / GPT 等）完全透明。

### 7.2 L3a: Go Agent (内置轻量)

Go Agent 是编译进 AIMA 二进制的极简 Agent Loop：

```
用户: aima ask "我有什么 GPU?"
  │
  ▼
Go Agent Loop (最多 30 轮):
  1. 构建系统提示 (含 MCP 工具定义)
  2. 发送给 LLM Provider (本地模型 or 云端 API)
  3. 收到 tool_call → 执行 MCP 工具 → 结果追加上下文
  4. 收到 text → 返回给用户
  5. 重复 3-4 直到 LLM 不再调用工具
```

**特性**:
- ~400 行 Go 代码，无外部依赖
- 无持久记忆 (每次对话独立)
- 单一 LLM 后端 (最近一次检测到的可用模型)
- ~0 额外内存开销
- 适合：简单查询、一次性操作、快速响应

**LLM Provider 检测优先级**:

```
1. AIMA 自身部署的本地模型 (localhost:8080/v1)  → 零网络依赖
2. 用户配置的 API Key (Anthropic/OpenAI/...)    → 需联网
3. 不可用 → 降级到 L2 知识解析 (无 Agent)
```

### 7.3 L3b: ZeroClaw Sidecar (可选增强)

ZeroClaw 是 Rust 实现的轻量 AI Agent 运行时 (~8.8 MB 二进制, <5 MB RAM, <10ms 冷启动)。
它用 **trait 驱动架构** 定义了 Agent 系统的 8 个核心可替换组件:

| ZeroClaw 组件 | 职责 | AIMA 如何利用 |
|--------------|------|-------------|
| **Provider** | LLM 后端 | 指向 AIMA 部署的本地模型 or 云端 API |
| **Channel** | 通信接入 | stdio pipe (AIMA 发任务给 ZeroClaw) |
| **Tool** | 能力执行 | MCP Client 连回 AIMA MCP Server |
| **Memory** | 持久记忆 | SQLite + FTS5 全文搜索 + 向量相似度 |
| **Security** | 访问控制 | 配对认证 + 沙箱 + 白名单 |
| **Tunnel** | 网络暴露 | 可选 (Cloudflare / Tailscale) |
| **Identity** | Agent 人格 | AIMA 设备大脑身份定义 |
| **Observability** | 监控 | 日志 + 指标 |

**ZeroClaw 弥补 Go Agent 缺失的关键能力**:

| 能力 | Go Agent (L3a) | ZeroClaw (L3b) |
|------|---------------|----------------|
| 持久记忆 | 无 (文件级对话存储) | SQLite + FTS5 + 向量相似度 + 混合排序 |
| 跨会话学习 | 无 | 完整记忆系统，跨对话积累经验 |
| 安全沙箱 | 工具白名单 | 配对认证 + 沙箱 + 白名单 + 工作区限制 |
| 多 LLM Provider | 单一后端 | 22+ 模型服务开箱即用 |
| Agent 人格 | 无 | Markdown 格式 Identity 角色定义 |

### 7.4 Sidecar 通信架构

```
AIMA Go Binary
  │
  ├── ZeroClaw Lifecycle Manager (start/stop/health)
  │     │
  │     └── 启动 ZeroClaw 二进制:
  │           --channel stdio              (接收任务)
  │           --provider openai            (LLM 后端)
  │           --provider-base-url localhost:8080/v1  (AIMA 自己的推理)
  │           --tool-mcp stdio:aima        (连回 AIMA MCP Server)
  │           --memory-path ~/.aima/zeroclaw.db
  │           --identity ~/.aima/zeroclaw-identity.md
  │
  └── 两条通信通道:
        1. stdio pipe (AIMA → ZeroClaw): 发送任务请求
        2. MCP client (ZeroClaw → AIMA): 调用 ~20 个 MCP 工具
```

**优雅之处：ZeroClaw 本身就是 MCP Client，AIMA 本身就是 MCP Server。
协议已经存在，无需发明新接口。**

### 7.5 Agent Dispatcher — 任务路由

`aima ask` 命令的 Agent Dispatcher 根据任务复杂度自动路由：

```bash
aima ask "..."                # 自动路由 (简单→L3a, 复杂→L3b)
aima ask --local "..."        # 强制 L3a (Go Agent, 快速, 无状态)
aima ask --deep "..."         # 强制 L3b (ZeroClaw, 持久记忆)
aima ask --session abc "..."  # 继续 ZeroClaw 会话 (跨对话记忆)
```

**路由启发式** (简单规则，可被 --local/--deep 覆盖):

| 信号 | 路由目标 |
|------|---------|
| 单步操作 ("有什么 GPU?", "部署 qwen3-8b") | L3a |
| 多步推理 ("为什么模型慢?", "优化所有配置") | L3b |
| 需要历史上下文 ("上次调优结果如何?") | L3b |
| 需要规划 ("为5个模型规划GPU分配") | L3b |
| ZeroClaw 不可用 | L3a (降级) |
| 无可用 LLM | L2 (知识解析, 无 Agent) |

**任务路由决策矩阵**:

| 任务示例 | L3a (Go Agent) | L3b (ZeroClaw) |
|---------|---------------|----------------|
| `aima ask "我有什么GPU?"` | **首选** | 过度 |
| `aima ask "部署 qwen3-8b"` | **首选** | 过度 |
| `aima ask "为什么我的模型慢?"` | 可用 | **更好** |
| `aima ask "优化所有模型配置"` | 不足 | **首选** |
| `aima ask "分析上周性能趋势"` | 不能(无记忆) | **首选** |
| `aima ask "为5个模型规划GPU分配"` | 勉强 | **首选** |
| 后台自愈 (检测→诊断→恢复) | 简单场景 | 复杂场景 |
| 跨会话知识综合 | 不能 | **首选** |

**自适应行为** (根据环境自动调整):

| 环境条件 | aima ask 行为 |
|---------|-------------|
| 无 LLM 可用 | 降级到 L2 (knowledge.resolve) |
| 仅本地 LLM | L3a (Go Agent + 本地模型) |
| 有云端 API Key | L3a (Go Agent + 云端 LLM) |
| ZeroClaw + 本地 LLM | 自动路由 L3a/L3b |
| ZeroClaw + 云端 API | 完整 L3b 能力 |

### 7.6 MCP — Agent 与 AIMA 的接口协议

MCP (Model Context Protocol) 是 Anthropic 发起、Linux Foundation 托管的开放协议，
用 JSON-RPC 2.0 标准化 LLM 应用与外部工具/数据源的集成。

**架构**:
```
Host (Claude Code / IDE / 自定义应用)
  │
  └── MCP Client ──── stdio/SSE ────→ MCP Server (AIMA)
                                          │
ZeroClaw Sidecar ── stdio ──────────→ MCP Server (AIMA)  [同一接口]
                                          │
Go Agent (内置) ── 直接调用 ──────→ MCP Tools (内部)       [同一逻辑]
                                          │
                                          ├── Tools   (Agent 可调用的操作)
                                          ├── Resources (可读取的数据)
                                          └── Prompts  (预定义的工作流模板)
```

**三种 Agent 走同一代码路径**——外部 Agent (MCP over stdio/SSE)、ZeroClaw (MCP over stdio)、
Go Agent (直接调用)，保证行为一致。

**三种服务器原语**:

| 原语 | 控制方 | 用途 | AIMA 示例 |
|------|--------|------|----------|
| **Tools** | LLM 驱动 | Agent 可调用的函数 | deploy.apply, knowledge.resolve |
| **Resources** | 应用驱动 | 可读取的上下文数据 | 硬件状态, 部署列表, 知识索引 |
| **Prompts** | 用户驱动 | 预定义的操作模板 | 模型部署向导, 故障排查流程 |

**传输协议**:
- **stdio** — 本地 Agent (Host 启动 AIMA 作为子进程) / ZeroClaw Sidecar
- **SSE (Server-Sent Events)** — 远程 Agent (HTTP 长连接)
- **Streamable HTTP** — 2025-11-25 规范新增的通用传输

### 7.7 ~20+ 个 MCP 工具

按 PRD 的 Supply / Demand / Control / Feedback 组织:

**硬件感知 (Supply)**:
1. `hardware.detect` — 检测 GPU/CPU/RAM，返回能力向量 + 功耗模式
2. `hardware.metrics` — 实时资源利用率 + 功耗 + 温度

**模型管理 (Supply/Demand)**:
3. `model.scan` — 扫描本地模型目录，发现并注册新模型
4. `model.list` — 列出所有已注册模型
5. `model.pull` — 下载模型 (断点续传)
6. `model.import` — 从本地路径导入模型
7. `model.info` — 查询模型详细信息

**引擎管理 (Supply/Demand)**:
8. `engine.scan` — 扫描 containerd 镜像，匹配 Engine Asset
9. `engine.list` — 列出可用引擎
10. `engine.pull` — 拉取引擎镜像

**编排 (Supply ↔ Demand 绑定)**:
11. `deploy.apply` — 生成并提交 Pod YAML 到 K3S
12. `deploy.delete` — 删除部署
13. `deploy.status` — 查询 Pod 状态 + 容器日志
14. `deploy.list` — 列出所有部署及资源使用

**推理 (Demand, 代理到引擎)**:
15. `inference.chat` — 对话补全 (代理到引擎 OpenAI API)
16. `inference.complete` — 文本补全
17. `inference.embed` — 生成嵌入向量
18. `inference.models` — 列出当前可用模型

**知识 (Control + Feedback)**:
19. `knowledge.search` — 搜索知识 (by hardware / model / engine / tags)
20. `knowledge.save` — 保存 Knowledge Note
21. `knowledge.resolve` — 解析最优配置 (L0→L2 多层合并)
22. `knowledge.list_engines` — 列出可用引擎定义 (从 SQLite 查询)
23. `knowledge.list_profiles` — 列出硬件 Profile (从 SQLite 查询)
24. `knowledge.generate_pod` — 从知识资产生成 Pod YAML

**知识查询 (Control + Feedback, 增强 — SQLite 关系查询驱动)**:
25. `knowledge.search_configs` — 多维配置搜索：支持约束过滤/排序/聚合，返回预处理后的精炼结果
26. `knowledge.compare` — 对比 N 个配置的多维性能（Agent 不需自行组装对比表）
27. `knowledge.similar` — 基于性能向量距离找相似配置（跨硬件迁移推荐）
28. `knowledge.lineage` — 查询配置演化链（WITH RECURSIVE）
29. `knowledge.gaps` — 发现知识空白：哪些 HW×Engine×Model 组合未被充分测试
30. `knowledge.aggregate` — 分组聚合统计（按引擎/硬件/模型维度的均值/分布）

**系统**:
31. `shell.exec` — 执行 shell 命令 (白名单 + 审计)
32. `system.config` — 读写 AIMA 配置

### 7.8 "往 Agent 沉淀" 的具体含义

以下能力在传统方案中由代码实现，AIMA 架构中由 Agent 通过 MCP 工具组合完成:

| 能力 | 传统方案 (代码实现) | Agent-centric (MCP 工具组合) |
|------|-------------------|---------------------------|
| 调优 | 编码搜索策略 + 基准测试框架 | Agent: deploy → inference × N → knowledge.save |
| 基准测试 | 专用测试框架 + 报告生成 | Agent: inference.chat × N + LLM 统计分析 |
| 故障恢复 | 告警规则 + 重试逻辑 | Agent: hardware.metrics → LLM 诊断 → deploy |
| 工作流编排 | DSL 解析器 + 执行引擎 | Agent: 自行编排 MCP 工具调用序列 |
| 资源规划 | 资源调度算法 | Agent: 读 Partition Strategy + LLM 推理 |
| 模型选择 | 格式→引擎映射规则 | Agent: knowledge.resolve + LLM 泛化能力 |
| 模型发现 | 定时扫描 + 复杂匹配规则 | Agent: model.scan + LLM 智能匹配 |
| 引擎选择 | 硬编码兼容性矩阵 | Agent: engine.list + knowledge + LLM 推理 |

**关键优势**: Agent 可以处理知识库未预见的场景——因为 LLM 具有泛化能力。
代码只能处理预编程的情况。

### 7.9 Agent 决策循环

```
┌──────────────────────────────────────────────────────┐
│                                                        │
│  ┌──────────┐    ┌──────────┐    ┌──────────┐         │
│  │ Perceive │───→│  Reason  │───→│   Act    │         │
│  │ 感知      │    │  推理     │    │  行动    │         │
│  │           │    │          │    │          │         │
│  │ hardware. │    │ knowledge│    │ deploy.  │         │
│  │ detect    │    │ .resolve │    │ apply    │         │
│  │ model.scan│    │ + LLM    │    │ model.   │         │
│  │ engine.   │    │ 推理能力  │    │ pull     │         │
│  │ scan      │    │          │    │ engine.  │         │
│  │ hardware. │    │          │    │ pull     │         │
│  │ metrics   │    │          │    │          │         │
│  └──────────┘    └──────────┘    └──────────┘         │
│       ↑                               │                │
│       │          ┌──────────┐         │                │
│       └──────────│  Learn   │←────────┘                │
│                  │  学习     │                           │
│                  │ knowledge│                           │
│                  │ .save    │                           │
│                  └──────────┘                           │
└──────────────────────────────────────────────────────┘
```

每一步对应具体的 MCP 工具调用。Agent 不需要理解 AIMA 内部实现，
只需要理解工具的 inputSchema 和返回格式。

---

## 8. 自举场景 (Self-Bootstrapping)

**这是整个架构最令人兴奋的部分——AIMA 部署 LLM → Agent 用这个 LLM 思考 → 优化 LLM 部署。**

```
Phase 0: 全新安装 (完全离线可完成)
  $ aima init                               # 安装+配置 K3S/HAMi (见 §3.5)
  $ aima start                              # 启动 K3S + MCP Server
  # model.scan → 发现预装模型
  # engine.scan → 发现预装镜像

Phase 1: 部署本地模型 (L0+L2, 零网络)
  $ aima deploy llama3.2-3b                 # 知识匹配 → K3S Pod → 推理就绪
                                            # OpenAI API at localhost:8080

Phase 2: Go Agent 激活 (L3a)
  # AIMA 自动检测自己部署的模型作为 LLM 后端
  $ aima ask "我能跑什么模型?"              # Go Agent 调用 MCP 工具 + 本地 LLM 推理

Phase 3: 安装 ZeroClaw (L3b, 可选)
  $ aima agent install                      # 下载 ZeroClaw (~8.8MB)
  # ZeroClaw Provider 指向 localhost:8080   # 用 AIMA 自己部署的模型

Phase 4: 完全自治运行
  $ aima ask "优化一切"
  # ZeroClaw: hardware.detect → model.scan → engine.list
  #         → knowledge.search → deploy × N → benchmark
  #         → knowledge.save → 选择最优 → 部署生产
  # 全部在本地运行。零网络依赖。完全自治。
```

**美丽的递归**：AIMA 部署 LLM → ZeroClaw 用这个 LLM 思考 → ZeroClaw 优化 LLM 的部署
→ LLM 跑得更好 → ZeroClaw 推理质量提升 → 进一步优化...

这就是 "AI-Inference-Managed-by-AI" 理念的完美实现。

---

## 9. 基础设施层 — Go 二进制

Go 二进制是架构中唯一需要编写和维护的代码。
目标: **极薄、极简**，AI coding agent 容易理解和修改。

### 9.1 模块划分

| 模块 | 职责 |
|------|------|
| `internal/hal/` | 硬件检测 + 实时监控 (nvidia-smi, /proc, 功耗读取) |
| `internal/k3s/` | K3S 客户端封装 (kubectl apply / get / delete / logs) |
| `internal/proxy/` | HTTP 推理代理 + OpenAI 兼容 API 路由 |
| `internal/knowledge/` | 知识目录加载 (go:embed YAML) + SQLite 关系化加载 + L0→L3 解析 + 知识查询引擎 (query.go) + 向量相似度 (similarity.go) + Pod YAML 模板渲染 |
| `internal/state/` | SQLite 统一状态存储 (v2: 16 张表，含静态知识表 + 动态知识表 + 系统表) |
| `internal/mcp/` | MCP 服务器 (JSON-RPC) + 工具实现 |
| `internal/cli/` | 人类 CLI + HTTP server 启动 |
| `internal/model/` | 模型扫描 + 注册 + 下载 + 导入 |
| `internal/engine/` | 引擎镜像扫描 + 注册 + 拉取 + 导入 + Native 二进制管理 (BinaryManager) |
| `internal/runtime/` | Multi-Runtime 抽象: K3S Runtime (Pod 部署) + Native Runtime (进程管理 + 预热) |
| `internal/agent/` | Go Agent Loop (L3a) + Agent Dispatcher |
| `internal/zeroclaw/` | ZeroClaw Lifecycle Manager (start/stop/health/install) |

### 9.2 SQLite Schema (v2: 知识查询引擎)

SQLite 单文件 (`~/.aima/aima.db`) 从"状态存储"升级为"知识查询引擎"，包含三类表：

**知识的两种存在形态**：YAML（编写/分发格式，人类可读）↔ SQLite（查询/推理格式，Agent 高效消费）

```sql
-- ====================================================================
-- 静态知识表（启动时从 go:embed YAML 重新加载，每次启动重建）
-- ====================================================================

CREATE TABLE hardware_profiles (
    id TEXT PRIMARY KEY,              -- "nvidia-gb10-arm64"
    name TEXT NOT NULL,
    gpu_arch TEXT NOT NULL,           -- "Blackwell", "Ada"
    gpu_vram_mib INTEGER,
    gpu_compute_cap TEXT,
    cpu_arch TEXT,
    cpu_cores INTEGER,
    ram_mib INTEGER,
    unified_memory BOOLEAN DEFAULT FALSE,
    tdp_watts INTEGER,
    power_modes TEXT,                 -- JSON array
    gpu_tools TEXT,                   -- JSON array
    raw_yaml TEXT
);

CREATE TABLE engine_assets (
    id TEXT PRIMARY KEY,              -- "vllm-0.8-blackwell"
    type TEXT NOT NULL,               -- "vllm", "llamacpp"
    version TEXT,
    image_name TEXT,
    image_tag TEXT,
    image_size_mb INTEGER,
    api_protocol TEXT,
    cold_start_s_min INTEGER,
    cold_start_s_max INTEGER,
    power_watts_min INTEGER,
    power_watts_max INTEGER,
    perf_gain_desc TEXT,
    raw_yaml TEXT
);

CREATE TABLE engine_features (        -- 一引擎多特性（多对多）
    engine_id TEXT NOT NULL REFERENCES engine_assets(id),
    feature TEXT NOT NULL,            -- "paged_attention", "flash_attention", ...
    PRIMARY KEY (engine_id, feature)
);

CREATE TABLE engine_hardware_compat ( -- 引擎-硬件兼容性（启动时按 gpu_arch 自动匹配）
    engine_id TEXT NOT NULL REFERENCES engine_assets(id),
    hardware_id TEXT NOT NULL REFERENCES hardware_profiles(id),
    vram_min_mib INTEGER,
    cpu_offload BOOLEAN DEFAULT FALSE,
    ssd_offload BOOLEAN DEFAULT FALSE,
    npu_offload BOOLEAN DEFAULT FALSE,
    min_gpu_mem_mib INTEGER,
    recommended_cores_pct INTEGER,
    PRIMARY KEY (engine_id, hardware_id)
);

CREATE TABLE model_assets (
    id TEXT PRIMARY KEY,              -- "glm-4.7-flash"
    name TEXT NOT NULL,
    type TEXT NOT NULL,               -- "llm", "vlm", "asr", "tts"
    family TEXT,
    param_count TEXT,
    formats TEXT,                     -- JSON array
    sources TEXT,                     -- JSON array of download sources
    raw_yaml TEXT
);

CREATE TABLE model_variants (         -- 模型 × 硬件 × 引擎 的具体推荐配置
    id TEXT PRIMARY KEY,
    model_id TEXT NOT NULL REFERENCES model_assets(id),
    hardware_id TEXT NOT NULL REFERENCES hardware_profiles(id),
    engine_type TEXT NOT NULL,
    format TEXT,
    default_config TEXT NOT NULL,     -- JSON
    expected_perf TEXT,               -- JSON
    vram_min_mib INTEGER
);

CREATE TABLE partition_strategies (
    id TEXT PRIMARY KEY,
    hardware_id TEXT NOT NULL,
    workload_pattern TEXT NOT NULL,
    slots TEXT NOT NULL,              -- JSON array of slot definitions
    raw_yaml TEXT
);

-- ====================================================================
-- 动态知识表（Agent 探索产出 + 社区同步，持久保存）
-- ====================================================================

-- 配置实例 — 知识图谱的核心节点
-- 每个"真实测试过的 Hardware×Engine×Model×Config 组合"
CREATE TABLE configurations (
    id TEXT PRIMARY KEY,
    hardware_id TEXT NOT NULL,
    engine_id TEXT NOT NULL,
    model_id TEXT NOT NULL,
    partition_slot TEXT,
    config TEXT NOT NULL,             -- JSON: 完整配置参数
    config_hash TEXT NOT NULL,        -- SHA256 用于去重
    derived_from TEXT REFERENCES configurations(id),
    status TEXT DEFAULT 'experiment', -- experiment|candidate|production
    tags TEXT,                        -- JSON array
    source TEXT DEFAULT 'local',      -- local|community|agent
    device_id TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 基准测试结果 — 多维性能数据
CREATE TABLE benchmark_results (
    id TEXT PRIMARY KEY,
    config_id TEXT NOT NULL REFERENCES configurations(id),
    concurrency INTEGER NOT NULL DEFAULT 1,
    input_len_bucket TEXT,            -- "0-128", "128-512", "512-2048"
    output_len_bucket TEXT,
    modality TEXT DEFAULT 'text',
    ttft_ms_p50 REAL, ttft_ms_p95 REAL, ttft_ms_p99 REAL,
    tpot_ms_p50 REAL, tpot_ms_p95 REAL,
    throughput_tps REAL, qps REAL,
    vram_usage_mib INTEGER, ram_usage_mib INTEGER,
    power_draw_watts REAL, gpu_utilization_pct REAL,
    error_rate REAL DEFAULT 0,
    oom_occurred BOOLEAN DEFAULT FALSE,
    stability TEXT,
    duration_s INTEGER, sample_count INTEGER,
    tested_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    agent_model TEXT, notes TEXT
);

-- 性能向量（从 benchmark_results 聚合，用于相似度检索）
CREATE TABLE perf_vectors (
    config_id TEXT PRIMARY KEY REFERENCES configurations(id),
    norm_ttft_p95 REAL, norm_tpot_p95 REAL, norm_throughput REAL,
    norm_qps REAL, norm_vram REAL, norm_power REAL,
    avg_throughput REAL, avg_ttft_p95 REAL, avg_vram_mib REAL,
    benchmark_count INTEGER,
    updated_at DATETIME
);

-- ====================================================================
-- 系统表（从 v1 保留）
-- ====================================================================

CREATE TABLE models (                 -- 已注册的本地模型文件
    id TEXT PRIMARY KEY, name TEXT, type TEXT, path TEXT,
    format TEXT, size_bytes INTEGER, detected_arch TEXT,
    detected_params TEXT, status TEXT DEFAULT 'registered',
    download_progress REAL, created_at DATETIME
);

CREATE TABLE engines (                -- 已注册的引擎镜像
    id TEXT PRIMARY KEY, type TEXT, image TEXT, tag TEXT,
    size_bytes INTEGER, platform TEXT, available BOOLEAN,
    created_at DATETIME
);

CREATE TABLE knowledge_notes (        -- Agent 探索的知识笔记
    id TEXT PRIMARY KEY, title TEXT, tags TEXT,
    hardware_profile TEXT, model TEXT, engine TEXT,
    content TEXT, confidence TEXT, created_at DATETIME
);

CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT, updated_at DATETIME);
CREATE TABLE audit_log (id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_type TEXT, tool_name TEXT, arguments TEXT,
    result_summary TEXT, created_at DATETIME);
```

**YAML → SQLite 加载流程**（每次启动执行）：
1. 打开/创建 SQLite → 2. Schema 迁移 (IF NOT EXISTS + 版本检查) → 3. 清空静态知识表 → 4. 加载 go:embed YAML → INSERT 到关系表 → 5. 动态表不清空 → 6. ANALYZE 更新统计

**ZeroClaw 使用独立数据库** (`~/.aima/zeroclaw.db`)，
职责分离：AIMA 系统状态在 `aima.db`，Agent 记忆在 `zeroclaw.db`。

### 9.3 目录结构

```
aima/
├── cmd/aima/main.go                  # 入口
├── internal/
│   ├── hal/                          # 硬件抽象
│   │   ├── detect.go                 # GPU/CPU/RAM 检测
│   │   └── metrics.go                # 实时监控 + 功耗
│   ├── k3s/                          # K3S 客户端
│   │   └── client.go                 # apply/get/delete/logs 封装
│   ├── proxy/                        # 推理代理
│   │   ├── handler.go                # OpenAI API 代理
│   │   └── router.go                 # 请求路由
│   ├── knowledge/                    # 知识层
│   │   ├── loader.go                 # go:embed YAML 加载+解析 + SQLite 关系化加载
│   │   ├── resolver.go               # L0→L3b 配置解析
│   │   ├── podgen.go                 # 从知识生成 Pod YAML
│   │   ├── query.go                  # 知识查询引擎 (Search/Compare/Gaps/Aggregate/Lineage)
│   │   └── similarity.go            # 向量相似度 (Similar + RefreshPerfVectors)
│   ├── state/                        # 状态存储
│   │   └── sqlite.go                 # SQLite CRUD
│   ├── model/                        # 模型生命周期
│   │   ├── scanner.go                # 本地模型扫描+识别
│   │   ├── downloader.go             # 断点续传下载 (HF/ModelScope)
│   │   └── importer.go               # 离线导入
│   ├── engine/                       # 引擎生命周期 (镜像 + Native 二进制)
│   │   ├── scanner.go                # containerd 镜像扫描
│   │   ├── puller.go                 # 镜像拉取 (多 registry)
│   │   ├── importer.go               # OCI tar 导入
│   │   └── binary.go                 # Native 二进制管理 (BinaryManager: 下载/缓存/版本)
│   ├── runtime/                      # Multi-Runtime 抽象 (§3.0)
│   │   ├── runtime.go                # Runtime 接口 + DeployRequest/Status 类型
│   │   ├── k3s.go                    # K3S Runtime (Pod YAML → kubectl apply)
│   │   └── native.go                 # Native Runtime (exec 二进制 + 健康检查 + 预热)
│   ├── mcp/                          # MCP 服务器
│   │   ├── server.go                 # JSON-RPC 2.0 服务器
│   │   └── tools.go                  # 工具实现
│   ├── agent/                        # Agent 系统
│   │   ├── agent.go                  # Go Agent Loop (L3a)
│   │   └── dispatcher.go             # L3a/L3b 路由决策
│   ├── zeroclaw/                     # ZeroClaw 集成
│   │   ├── manager.go                # Lifecycle: Start/Stop/Health
│   │   └── installer.go              # 按平台下载 ZeroClaw 二进制
│   ├── stack/                        # 基础设施栈管理
│   │   └── installer.go              # 通用 stack installer (读 YAML → 安装 → 验证)
│   └── cli/                          # 人类界面
│       ├── root.go                   # Cobra 根命令
│       ├── init.go                   # aima init (基础设施栈安装)
│       ├── deploy.go                 # aima deploy / undeploy
│       ├── model.go                  # aima model scan/pull/import/list
│       ├── engine.go                 # aima engine scan/pull/import/list
│       ├── knowledge.go              # aima knowledge list/sync/import
│       ├── agent.go                  # aima agent install/status
│       ├── ask.go                    # aima ask (Agent 入口)
│       └── server.go                 # HTTP server 启动
├── catalog/                          # 知识资产 (go:embed)
│   ├── embed.go                      # //go:embed 入口
│   ├── hardware/                     # Hardware Profile YAML
│   ├── engines/                      # Engine Asset YAML
│   ├── models/                       # Model Asset YAML
│   ├── partitions/                   # Partition Strategy YAML
│   └── stack/                        # Stack Component YAML (K3S/HAMi/ZeroClaw 配置)
├── dist/                             # 离线安装包 (git-lfs 或独立 release)
│   ├── linux-amd64/                  # K3S, HAMi chart, ZeroClaw 等
│   └── linux-arm64/
└── go.mod
```

---

## 10. 人类界面

### CLI 命令

```bash
# 初始化与生命周期
aima init                                 # 安装+配置基础设施栈 (K3S/HAMi, 见 §3.5)
aima start                                # 启动 AIMA (检查 K3S, 启动 MCP+HTTP)
aima stop                                 # 停止所有服务

# 部署 (最常用)
aima deploy <model> [--engine] [--slot]   # 部署模型 (知识自动匹配)
aima undeploy <name>                      # 停止部署
aima status                               # 系统状态 (硬件+部署+资源+功耗)

# 推理快捷方式
aima chat <model> "message"               # 快速对话

# 模型管理
aima model scan                           # 扫描本地模型
aima model list                           # 列出已注册模型
aima model pull <model>                   # 下载模型 (断点续传)
aima model import <path>                  # 从本地路径/USB 导入
aima model remove <model>                 # 注销模型

# 引擎管理
aima engine scan                          # 扫描本地引擎镜像
aima engine list                          # 列出可用引擎
aima engine pull <engine>                 # 拉取引擎镜像
aima engine import <path>                 # 从 OCI tar 导入
aima engine remove <engine>               # 删除引擎镜像

# 知识管理
aima knowledge list                       # 列出知识资产
aima knowledge sync [--push|--pull]       # 同步社区知识 (需联网)
aima knowledge import <path>              # 离线导入知识包
aima knowledge export [--output]          # 导出知识 (供离线传递)

# Agent
aima ask "指令"                           # 让 Agent 执行任务 (自动路由 L3a/L3b)
aima ask --local "指令"                   # 强制 Go Agent (L3a)
aima ask --deep "指令"                    # 强制 ZeroClaw (L3b)
aima ask --session <id> "指令"            # 继续 ZeroClaw 会话
aima agent install                        # 安装 ZeroClaw
aima agent status                         # 查看 Agent 状态

# 配置
aima config [key] [value]                 # 读写配置
```

**设计原则**: CLI 命令是 MCP 工具的人类友好包装。
`aima deploy qwen3-8b` 内部调用 `model.scan` → `engine.scan` → `knowledge.resolve`
→ `knowledge.generate_pod` → `deploy.apply`。
CLI 永不实现 MCP 工具之外的逻辑——确保 Agent 和人类走同一条代码路径。

### HTTP API

| 端点 | 方法 | 功能 |
|------|------|------|
| `/v1/chat/completions` | POST | OpenAI 兼容对话 (代理到引擎) |
| `/v1/embeddings` | POST | 嵌入向量 (代理到引擎) |
| `/v1/models` | GET | 可用模型列表 |
| `/health` | GET | AIMA 健康状态 |
| `/status` | GET | 系统状态 (硬件+部署+资源) |

HTTP API 是纯代理——请求直接转发到引擎容器，AIMA 只做路由。

### MCP Server

- 协议: JSON-RPC 2.0 over stdio (本地 Agent) 或 SSE (远程 Agent)
- 工具: 第 7.7 节定义的 20+ 个工具
- 资源: 硬件状态、部署列表、知识索引
- 安全: 工具白名单 + 操作审计日志

---

## 11. 数据流

### Flow 1: 部署 (aima deploy → 推理就绪)

```
用户: aima deploy glm-4.7-flash --engine vllm
  │
  ▼
CLI: 解析参数，调用内部 MCP 工具链
  │
  ├── model.scan → 确认模型文件存在
  │   └── 不存在? → 提示: aima model pull glm-4.7-flash
  │
  ├── engine.scan → 确认 vllm 镜像存在
  │   └── 不存在? → 自动尝试拉取 (有网) / 提示离线导入 (无网)
  │
  ▼
resolveWithFallback(engine=vllm, model=glm-4.7-flash, hw=detect())
  │  ├── 尝试 catalog.Resolve() → 成功则直接合并 L0→L1→L2
  │  └── catalog 未匹配? → db.FindModelByName() → BuildSyntheticModelAsset()
  │       → format→engine 映射 → 注册合成 ModelAsset → 重试 Resolve
  ▼
knowledge.generate_pod(resolved_config, partition_strategy)
  │  模板渲染: Engine Asset 镜像/命令 + 配置参数 + Slot 资源限制 → Pod YAML
  ▼
deploy.apply(pod_yaml)
  │  执行: kubectl apply -f <生成的 Pod YAML>
  ▼
K3S: 创建 Pod → containerd 启动容器
  │  HAMi: libvgpu.so 注入 → 限制显存 8192MB / 算力 50%
  ▼
K3S: livenessProbe /health → Pod Ready
  │
  ▼
proxy: 注册路由 glm-4.7-flash → Pod IP:8000
  │
  ▼
用户: curl localhost:8080/v1/chat/completions   ✓
```

### Flow 2: Agent 调优 (探索 → 知识沉淀)

```
Agent: hardware.detect()
  │  → {gpu: Blackwell, vram: 15360, cpu: arm64, tdp: 100W}
  │
  ▼
Agent: model.scan() → engine.scan()
  │  → 确认本地资源就绪
  │
  ▼
Agent: knowledge.search(hardware=gb10, model=glm-4.7-flash)
  │  → 找到已有 Note (confidence: medium) 或无匹配
  │
  ▼
Agent (LLM 推理): 分析已有 Notes, 评估是否值得继续探索
  │
  ▼
Agent: deploy.apply(config_1) → inference.chat × N → 记录 throughput/latency
  │     deploy.delete()
  │     deploy.apply(config_2) → inference.chat × N → 记录性能
  │     deploy.delete()
  │     ...
  │
  ▼
Agent (LLM 推理): 比较所有 trial → 选择最优
  │
  ▼
Agent: knowledge.save(Knowledge Note — 含所有 trial + 推荐 + 洞察)
  │
  ▼
Agent: deploy.apply(最优配置) → 投入生产
```

### Flow 3: 离线模型预装与部署

```
管理员 (有网环境):
  aima model pull glm-4.7-flash            # 下载模型
  aima engine pull vllm                     # 拉取引擎镜像
  aima model export glm-4.7-flash -o /usb/ # 导出模型到 USB
  aima engine export vllm -o /usb/          # 导出镜像到 USB
  aima knowledge export -o /usb/            # 导出知识到 USB

边缘设备 (完全离线):
  # USB 插入
  aima model import /media/usb/glm-4.7-flash/
  aima engine import /media/usb/vllm-latest.tar
  aima knowledge import /media/usb/knowledge/
  aima deploy glm-4.7-flash                # 完全离线部署，零联网
```

### Flow 4: 知识同步 (本地 → 社区 → 其他设备)

```
设备 A:
  Agent 探索 → knowledge.save → SQLite
  │
  ▼
  aima knowledge sync --push (需联网)
  │  → 导出 YAML → git push 到社区仓库
  │
  ▼
社区仓库 (GitHub catalog/)
  │  hardware/nvidia-gb10-arm64.yaml
  │  engines/vllm/vllm-0.8-blackwell.yaml
  │  notes/kn-abc123-glm4-vllm-gb10.yaml
  │
  ▼
设备 B (同型硬件):
  aima knowledge sync --pull (需联网)
  │  → git pull (按 hardware fingerprint 过滤)
  │  → 导入 Knowledge Notes 到 SQLite
  │
  ── 或 ──
  aima knowledge import /usb/knowledge/  (离线方式)
  │
  ▼
  aima deploy glm-4.7-flash
  │  → knowledge.resolve 自动命中设备 A 的最优配置
  │  → 零探索、直接部署
```

---

## 12. 开源工具选型

### 核心工具栈

| 用途 | 选型 | 版本要求 | 选择理由 |
|------|------|---------|---------|
| 容器编排 | **K3S** | v1.31+ | 单二进制 <70MB, 内置 containerd, ARM64 原生支持 |
| GPU 虚拟化 | **HAMi** | v2.4+ | CNCF Sandbox, MB 级显存切分, 多厂商支持 |
| 状态存储 | **SQLite** | modernc.org/sqlite | 纯 Go, 零 CGO, 嵌入式, 零运维 |
| Agent 接口 | **MCP** | 2025-11-25 spec | Anthropic + Linux Foundation 标准, JSON-RPC 2.0 |
| 服务发现 | **mDNS** | hashicorp/mdns | 零配置局域网发现, 零外部依赖 |
| CLI 框架 | **Cobra** | spf13/cobra | Go CLI 事实标准 |
| HTTP | **net/http** | 标准库 | 不需要额外框架 |
| 日志 | **log/slog** | 标准库 (Go 1.21+) | 结构化日志, 内置 |
| YAML | **gopkg.in/yaml.v3** | — | 稳定, 广泛使用 |
| Sidecar Agent | **ZeroClaw** | 可选 | ~8.8MB, <5MB RAM, MCP 原生, Rust 二进制 |

### 总资源开销估算

```
K3S server (精简配置)     ~1.6 GB RAM peak (含 kubelet+apiserver+containerd)
                          ~6% CPU (Intel 8375C 参考值)
HAMi daemon               ~128 Mi RAM, ~100m CPU (每 GPU 节点)
AIMA Go 二进制             ~30 MB 磁盘, ~50 Mi RAM runtime
ZeroClaw (可选)           ~8.8 MB 磁盘, ~5 Mi RAM runtime
```

这些组件在 **8 GB 边缘设备** (如 Jetson Orin Nano) 上的部署需要进一步实测验证——
K3S 的 1.6 GB 峰值内存可能在极端边缘场景下需要优化
（如使用 `--kube-apiserver-arg` 限制并发、减少 watch 缓存等）。

对于 **16 GB+ 设备** (如 DGX Spark, RTX 工作站, AI PC)，overhead 完全可接受。

### 替换策略

每个工具可独立替换:

| 工具 | 替代方案 | 替换范围 |
|------|---------|---------|
| K3S | 标准 K8s, MicroK8s | 改 kubeconfig 路径 |
| HAMi | MIG, MPS, Flex:ai | 改 Pod YAML 的 resource 字段 |
| SQLite | PostgreSQL | 改状态存储模块 |
| MCP | gRPC, REST | 改 MCP 服务器模块 |
| mDNS | Consul, etcd | 改服务发现模块 |
| ZeroClaw | 任何 MCP Client | Sidecar 接口不变 |

---

## 13. 扩展模型

### 新引擎 (通常零 Go 代码)

1. 写 Engine Asset YAML: 镜像、启动命令、默认配置、三重角色信息、镜像源列表
2. 放入 `catalog/engines/` 目录
3. 重新编译 (go:embed) 或实现热加载
4. Agent / 用户即可通过 `aima deploy --engine xxx` 使用

### 新模型 (零 Go 代码)

1. 写 Model Asset YAML: 硬件变体、引擎映射、默认配置、下载源列表
2. 放入 `catalog/models/` 目录

> **快速部署 (零 YAML)**: 如果模型已通过 `model scan` 发现且有 format 信息，
> `aima deploy <model>` 会自动构建合成 ModelAsset（format→engine 映射 + 引擎 L0 默认值），
> 无需手动写 YAML 即可部署。写 YAML 仍推荐用于生产优化（精确配置、性能预期、多硬件变体）。

### 新硬件

1. 写 Hardware Profile YAML: 能力向量、约束、可用切分工具
2. 如果硬件检测方式完全不同于 nvidia-smi / /proc → 加少量 Go 检测代码
3. 如果走标准接口 → 零代码

### 新资源划分策略 (零 Go 代码)

1. 写 Partition Strategy YAML: 目标硬件、工作负载模式、Slot 定义

### 新栈组件 (零 Go 代码)

1. 写 Stack Component YAML: 版本兼容、安装参数 (含 rationale)、验证条件、硬件变体
2. 放入 `catalog/stack/` 目录
3. 将离线安装包放入 `dist/{platform}/`
4. `aima init` 自动发现并安装

### 新 MCP 工具

1. 在 MCP 工具模块中添加工具函数
2. 注册到工具表
3. Agent 通过 `tools/list` 自动发现

### 新模型下载源 (零 Go 代码)

1. 在 Model Asset YAML 的 `storage.sources` 中添加新条目
2. 如果是新协议 (非 HTTP) → 在 `internal/model/downloader.go` 添加适配

### 新镜像仓库 (零 Go 代码)

1. 在 Engine Asset YAML 的 `image.registries` 中添加新地址
2. containerd 标准协议，自动兼容

### 扩展编排能力 (零 AIMA 代码)

| 需求 | 方案 |
|------|------|
| 批量调度 / 队列 | K3S + Volcano |
| 混部 QoS / CPU Burst | K3S + Koordinator |
| 更细 GPU 切分 / MIG | HAMi 升级配置 |
| 多节点集群 | K3S agent 加入 (原生) |

---

## 14. 架构不变量

不可违反的架构约束:

**INV-1: 不为引擎类型写代码。** 引擎行为定义在 YAML。Pod YAML 从知识模板生成。
添加新引擎 = 写 YAML。

**INV-2: 不为模型类型写代码。** 模型元数据在 YAML。模型类型是知识，不是代码分支。

**INV-3: 最小化运行时管理。** K3S 管 Pod 的创建、监控、重启、销毁。
Native runtime 只做极简进程管理（start/stop/logs），不实现重启、健康恢复等容器级特性。
AIMA 通过 `runtime.Runtime` 接口统一抽象，上层代码（MCP 工具、CLI）不直接依赖 K3S 或进程管理。

**INV-4: 职责分离的状态存储。** AIMA 系统状态（模型注册、引擎注册、知识笔记、配置）
在 `aima.db` 单文件中。ZeroClaw Agent 记忆在独立的 `zeroclaw.db` 中。
两个数据库职责清晰：系统状态 vs Agent 记忆，互不干扰。
Go Agent (L3a) 的对话历史不持久化（或仅文件级临时存储）。

**INV-5: MCP 工具即真相。** CLI 是 MCP 工具的包装。CLI 永不实现 MCP 工具之外的逻辑。
Agent (L3a/L3b/外部) 和人类走同一条代码路径。

**INV-6: 探索即知识。** Agent 每次探索必须产出 Knowledge Note。
探索过程不允许仅存在于 Agent 上下文中——必须结构化持久存储。

**INV-7: 知识对齐优化链路。** 知识资产的组织严格对齐 PRD 优化链路:
Hardware Profile → Partition Strategy → Engine Asset → Model Asset → Knowledge Note。

**INV-8: 离线可用。** 所有核心功能（部署、推理、知识查询、L0-L3a Agent）
必须在完全离线状态下可用。网络连接只提供增强能力（知识同步、模型下载、云端 LLM）。

---

## 15. 与 PRD 需求的映射

| PRD 需求 | ID | 架构组件 |
|----------|-----|---------|
| 硬件检测 + 能力向量 | S1, S2 | hal 模块 (nvidia-smi, /proc) |
| 功耗模式检测 | S3 | hal 模块 (TDP + power_mode) |
| 资源预算估算 | S4 | Engine Asset partition_hints |
| 可插拔切分后端 | S5 | K3S + HAMi (可替换) |
| effective_R 建模 | S6 | Engine Asset amplifier.resource_expansion |
| 一条命令部署 | D1 | CLI + model.scan + engine.scan + knowledge.resolve + deploy.apply |
| 多模型并行 | D2 | Partition Strategy + 多 Pod |
| 引擎自动选择 | D3 | Engine Asset + Agent LLM 推理 |
| App 需求声明 | D4 | Model Asset + 依赖解析 |
| 时间约束感知 | D5 | Engine Asset time_constraints |
| 内嵌 Recipe | K1 | catalog/ (go:embed YAML) |
| 硬件指纹匹配 | K2 | Hardware Profile + knowledge.resolve |
| L0→L2 渐进解析 | K3 | knowledge/resolver (ConfigResolver) |
| 知识含性能数据 | K4 | Knowledge Note exploration.trials |
| 调优反哺 | K5 | Agent → knowledge.save |
| 知识同步 | K6, K7 | CLI + git (aima knowledge sync) + 离线导入 |
| Agent 被动模式 | A1 | MCP Server + tools/call (L3a/L3b) |
| Agent 主动巡检 | A2 | ZeroClaw 定时调用 hardware.metrics (L3b) |
| 自动调优 | A3 | Agent 编排 deploy + inference + knowledge 工具 |
| 故障自愈 | A4 | Agent: detect → diagnose → deploy |
| 引擎切换评估 | A5 | Engine Asset time/power_constraints + Agent 推理 |
| OpenAI 兼容 API | F1 | proxy 模块 (请求代理到引擎) |
| mDNS 服务广播 | F2 | mDNS 模块 (hashicorp/mdns) |
| Benchmark | F3 | Agent: inference.chat × N |
| 功耗监控 | F4 | hal 模块 hardware.metrics |

---

*本文档从 PRD 的优化模型出发设计系统架构。*
*Go 二进制是极薄基础设施 + 轻量 Agent 便利层，K3S + HAMi 是声明式编排层，*
*知识资产对齐优化链路 (Hardware → Partition → Engine → Model → Note)，*
*双皮层 Agent (L3a Go Agent + L3b ZeroClaw Sidecar) 实现从简单查询到复杂自治的完整覆盖，*
*外部 Agent 通过 MCP 工具承担最复杂的智能决策。*
*渐进智能 L0→L3b 确保任何条件下都有可用解——Agent 是锦上添花，不是必需品。*
*本地优先设计确保完全离线可用——网络连接是增强，不是前提。*
