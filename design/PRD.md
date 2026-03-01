# AIMA — 产品需求文档 (PRD)

> AI-Inference-Managed-by-AI
> 有限硬件资源上的 AI 推理优化与调度系统

---

## 1. 产品定位

### 一句话定义

> AIMA 是一个运行在 AI 设备上的**资源优化与调度系统**——在有限硬件资源的约束下，
> 自动为多个竞争性 AI 推理需求找到最优的资源分配方案。

### 核心命题

AI 推理设备的 TCO (总拥有成本) 中，硬件采购只占一半。另一半是：
- **配置成本** — 专家花数小时调参：该用哪个引擎？gpu_memory_utilization 设多少？量化到几 bit？
- **运维成本** — 模型更新、性能退化、资源冲突、故障恢复，持续消耗人力
- **浪费成本** — 配置不当导致 GPU 利用率 < 30%，或本可并行的模型被串行运行

AIMA 的使命：**用 AI Agent + 社区知识 + 互联网连接三大杠杆，将这三项成本趋近于零。**

### 优化空间

MRD 定义了 Model × Hardware × Engine 的 3D 匹配。
PRD 将其扩展为更完整的优化空间——4 个核心维度 + 2 个约束维度：

```
  ┌─ 核心维度 ──────────────────────────────────────────────────────┐
  │                                                                  │
  │  Resource (硬件能力向量)        Engine (三重角色)                  │
  │     │                             │                              │
  │     ├── GPU: arch, vram,          ├── 连接器: 格式→硬件执行       │
  │     │   compute, driver           ├── 分配器: 控制资源占用        │
  │     ├── CPU: cores, freq, isa     └── 放大器: 提升性能 +          │
  │     ├── RAM: total, bandwidth              扩展资源边界          │
  │     ├── Storage: capacity, speed                                 │
  │     └── NPU/其他加速器            Model (推理生产者)              │
  │                                      │                           │
  │  App (推理消费者—广义价值载体)        ├── 类型: llm|vlm|omni|...  │
  │     │                                └── 需求: vram, precision   │
  │     ├── 需求声明: 推理依赖                                       │
  │     └── 资源预算: cpu, memory                                    │
  │                                                                  │
  ├─ 约束维度 ──────────────────────────────────────────────────────┤
  │                                                                  │
  │  Time (时间约束)                  Energy (能源约束)               │
  │     │                               │                            │
  │     ├── 冷启动时间                   ├── 设备 TDP 功耗上限        │
  │     ├── 模型切换时间                 ├── 运行时功耗               │
  │     └── 资源重配置时间               └── 散热/供电物理限制        │
  │                                                                  │
  └──────────────────────────────────────────────────────────────────┘
```

**关键洞察：引擎的三重角色**

引擎不仅仅是"连接器"，它同时是：
1. **连接器** — 将模型格式适配到硬件执行 (GGUF→llamacpp, safetensors→vLLM)
2. **资源分配器** — 控制模型占用多少资源 (gpu_memory_utilization=0.5 → 只用 50% VRAM)
3. **性能放大器** — 同硬件同模型，不同引擎的性能天差地别 (PagedAttention, FlashAttention,
   Speculative Decoding)；更关键的是，引擎可以**扩展资源边界**——
   KTransformers/llama.cpp 的 CPU offload 让推理从纯 GPU+VRAM 延展到 CPU+RAM+GPU+VRAM，
   mooncake 的 prefill-decode 分离让集群协同成为可能，
   未来 NPU offload、SSD offload 将进一步扩展可用资源池。

这意味着引擎选择不仅决定"如何分配资源"，还决定"有多少资源可用"——
**引擎改变的不是目标函数，而是可行域本身**。

**App = 广义价值载体**

App 不限定运行形态——可以是 RAG 应用、语音助手、视频处理管线、或任何消费推理 API 的程序。
App 的本质是**需求声明 + 资源预算**。

**反向指导硬件选配**

AIMA 假设硬件已确定，在约束内做最优分配。但 AIMA 积累的知识生态（Recipe、Benchmark 数据、
引擎兼容性矩阵）天然可以反向回答："如果我要跑这些模型组合，该买什么硬件？"
这将 AIMA 从"配置工具"延伸为"采购顾问"——v2.0+ 愿景。

### 渐进增强：L0 → L3

```
  L3: Agent ────── LLM 动态优化 ───── 最优解 (动态)
   ↑ override
  L2: 知识库 ───── Recipe 确定性匹配 ── 良好解 (静态最佳实践)
   ↑ override
  L1: 人类 CLI ─── 手动指定参数 ───── 次优解
   ↑ override
  L0: 默认值 ───── 硬编码保守配置 ──── 可用解 (always available)
```

**每一层独立可用。高层增强低层，但不依赖低层存在。**
全新设备、无网络、无 Agent → L0 仍能启动可用的推理服务。
这是 AIMA 最核心的设计原则。

---

## 2. 客户旅程

### 用户角色

| 角色 | 代表 | 关键需求 |
|------|------|---------|
| 设备 (Device) | AI PC / 边缘盒子 / GPU 服务器 | 自动发现、零配置、自治运行 |
| Agent | LLM 驱动的自治体 | MCP/API 操控、工具调用、闭环优化 |
| 开发者 | 调用推理 API 的人 | 5 分钟启动、OpenAI 兼容、多模型并行 |
| 运维 | 管理设备群的人 | 仪表盘、告警、批量操作 |
| 贡献者 | 分享 Recipe 的人 | 导出知识、社区分享 |

### J1: 开发者 5 分钟体验

```
1. curl -sSL install.sh | bash          # 安装单二进制 (< 50MB)     [L0: 默认配置]
   aima init                             # 安装+配置 K3S/HAMi (离线包预置)
2. aima deploy qwen3-8b                  # 一条命令部署模型
   → 硬件检测: GB10, 128GB unified       # [L0: 自动检测]
   → 知识匹配: Recipe "gb10-qwen3-8b"    # [L2: 知识库匹配]
   → 引擎选择: vLLM, gpu_mem_util=0.5    # [L2: Engine Asset]
   → 性能放大: FlashAttention + 最优配置  # [L2: 放大器选择]
   → Docker 拉起推理容器
3. curl localhost:6188/v1/chat/completions  # OpenAI 兼容 API 立即可用
4. aima deploy whisper-large-v3           # 再部署一个 ASR 模型
   → 资源检查: 剩余 VRAM 足够             # [L2: 资源预算]
   → 引擎选择: whisper, gpu_mem_util=0.3
   → 两个模型并行运行
5. aima status                            # 查看资源利用率 + 功耗
```

每一步标注了工作的智能层级。注意：整个过程 **L3 Agent 完全未参与**，纯靠 L0+L2。

### J2: 边缘设备零配置

```
设备上电 → AIMA 预装/自启动
  → [L0] 硬件检测，生成能力向量 (含功耗上限)
  → [L2] 知识库匹配，找到 Recipe (引擎选择考虑启动时间和功耗)
  → [L2] 按 Recipe 部署推理服务
  → [L0] mDNS 广播 "_llm._tcp" 服务 (端口 6188)
  → [L0] 远程发现: 自动扫描局域网其他 AIMA 实例的模型并注册为远程 backend
  → 局域网应用写 localhost:6188 即可透明访问所有发现的推理模型

全程零人工。即使 Agent 不可用，L2 知识库即可完成全部部署。
无 GPU 的开发机只需 `aima serve --discover`，自动发现并路由到局域网 GPU 服务器。
```

### J3: Agent 自治运维 (v1.0)

Agent 通过 MCP 连接设备 → 查询 `device.detect` → 分析资源状态 → 调用 `model.deploy` →
监控 `device.metrics` → 发现性能退化 → 触发 `tuning.start` → 应用最优配置 → 导出 Skill。
Agent 还可以在资源空闲时**切换到更优引擎** (如从 llamacpp 切到 vLLM) 来放大性能，
但会评估切换时间成本是否可接受。人类只需看仪表盘。

### J4: 社区贡献知识 (v1.0)

开发者调优完成 → `aima recipe export` → 提交 PR 到 catalog/ → 社区 Review →
其他设备 `aima catalog sync` → 自动应用。一个人的调优经验，惠及所有同硬件设备。

### J5: 手机接入推理网络

```
Phase A: 手机作为远程客户端（v2.0, 已天然支持）
  → 手机 App（任何 HTTP 客户端）通过 mDNS 发现 LAN 内 AIMA 服务器
  → 调用 OpenAI 兼容 API（:6188）直接使用推理服务
  → AIMA 侧零改动，手机端只需实现 HTTP 调用 + mDNS 发现

Phase B: 手机作为推理设备（v3.0, 需开发）
  → 手机运行轻量 Agent（嵌入 llama.cpp），注册为远程推理节点
  → AIMA 通过 device.register / device.list MCP 工具管理远程设备
  → 手机端 on-device inference（Apple A-series NPU / Qualcomm Hexagon）
  → 移动端约束建模：电池、热管理、后台限制、网络不稳定
```

---

## 3. 优化模型

**这是 AIMA 的核心：将配置问题形式化为带约束的优化问题。**

### 形式化定义

```
给定:
  R = (gpu_vram, gpu_compute, cpu_cores, ram,    — 计算资源向量
       storage, power_budget)                     — 含功耗预算
  D = {d1, d2, ..., dn}                          — 需求集合

每个需求 di:
  类型:      model | app
  约束:      min_resources(di)                    — 最小资源需求
  时间约束:  max_switch_time(di)                  — App 可接受的最大切换时间
  实现路径:  Pi = {p1, p2, ..., pk}               — 可选的引擎+配置组合
  价值:      value(di)                            — 满足该需求的业务价值

每个实现路径 pj:
  引擎:      engine_type (vllm | llamacpp | ktransformers | ...)
  配置:      config_params (gpu_mem_util, quantization, cpu_offload_layers, ...)
  消耗:      cost(pj, R) → 资源子向量
  放大:      effective_R(pj, R) → R'              — 引擎可扩展有效资源 (R' ≥ R)
  启动时间:  startup_time(pj)                     — 冷启动耗时
  功耗:      power_draw(pj)                       — 运行时功耗

求解: 选择子集 S ⊆ D，并为每个 di ∈ S 选择路径 pj，使得:
  (1) Σ cost(selected_paths) ≤ effective_R       — 资源约束 (含引擎扩展)
  (2) ∀ app ∈ S: app.inference_deps ⊆ S          — 依赖约束
  (3) Σ power_draw(selected_paths) ≤ power_budget — 功耗约束
  (4) ∀ di ∈ S: startup_time(pj) ≤ max_switch_time(di)  — 时间约束
  (5) Σ value(di) 最大化                          — 目标函数
```

**与经典背包问题的关键区别**: 引擎选择可以改变"背包容量"本身。
选择 KTransformers + CPU offload 可能让有效 VRAM 从 16GB 变成 16GB+64GB RAM，
从而装入原本装不下的模型组合。这让问题从固定容量的组合优化变成**可变容量的联合优化**。

实践中 N 很小（一台设备通常 < 10 个需求），枚举 + 剪枝足够。
关键不在求解算法，而在**获得准确的 cost() 和 effective_R() 函数**——这正是知识系统的价值。

### 引擎的三重角色

引擎是优化系统中最关键的变量。它同时承担三个角色：

| 角色 | 功能 | 举例 |
|------|------|------|
| **连接器** | 模型格式 → 硬件执行 | GGUF → llamacpp, safetensors → vLLM |
| **分配器** | 控制模型的资源占用 | `gpu_memory_utilization=0.5` → 只用 50% VRAM |
| **放大器** | 提升性能 + 扩展资源边界 | 见下 |

**放大器的两层含义**：

1. **性能放大** — 同硬件同模型，不同引擎性能差异可达数倍：
   - PagedAttention (vLLM): 减少显存碎片，并发吞吐提升 2-4x
   - FlashAttention: 计算访存融合，推理速度提升 50%+
   - Speculative Decoding: 小模型预测+大模型验证，延迟降低 2-3x
   - Continuous Batching: 动态批处理，吞吐提升数倍

2. **资源边界扩展** — 引擎可以改变"有多少资源可用"：
   - **KTransformers**: CPU+RAM 参与推理，让 16GB VRAM 设备跑 70B 模型
   - **llama.cpp offload**: 逐层决定 GPU/CPU 分配，精细控制资源使用
   - **mooncake**: prefill-decode 分离，跨设备协同利用资源
   - **未来**: NPU offload, SSD offload (如 FlexGen), CXL 内存扩展

```
  传统视角:   硬件 VRAM = 16GB → 只能跑 ≤16GB 模型
  引擎放大后: 硬件 VRAM + RAM = 16GB + 64GB → 可跑 ~70GB 模型 (offload)
              │
              └── 可行域从 R 扩展到 effective_R(engine, R)
                  这不是调参优化，是搜索空间本身的扩大
```

因此引擎横跨 Demand 和 Supply 两个维度：
- Demand 侧：引擎是模型工作负载的一部分（选择什么引擎来运行模型）
- Supply 侧：引擎改变可用资源的有效边界

### 时间与能源：被忽视的约束

传统 AI 推理配置只关心"能不能跑"（VRAM 够不够）。
但真实部署还受两个关键约束：

**时间约束 (Reconfiguration Cost)**

| 操作 | 典型耗时 | 影响 |
|------|---------|------|
| vLLM 冷启动 (7B 模型) | 30-60s | 实时应用不可接受 |
| llamacpp 冷启动 (7B GGUF) | 3-5s | 可接受 |
| 模型热切换 (引擎内) | <1s | 理想 |
| MIG 重配置 | 需要重启 GPU 进程 | 影响所有共享用户 |
| Docker 容器重建 | 10-30s | 中等 |

时间敏感型 App（如实时对话、语音助手）需要切换时间 < 10s，
这直接排除了某些引擎+配置组合，哪怕它们在性能上更优。
**最优解必须在时间约束下才有意义。**

**能源约束 (Power Budget)**

| 场景 | 功耗上限 | 特点 |
|------|---------|------|
| AI 服务器 (H100 SXM) | 700W/GPU | 数据中心供电，约束松 |
| AI 工作站 (RTX 4090) | 450W (系统) | 家用电路，长时间运行需考虑 |
| 边缘设备 (Jetson Orin) | 15-60W (可配置) | TDP 模式直接限制算力 |
| AI PC (笔记本) | 45-100W (系统) | 电池续航 + 散热严格限制 |

Jetson Orin 的 15W/30W/60W 功耗模式直接决定了可用的 GPU 频率和核心数。
在 15W 模式下根本跑不动需要全部 GPU 核心的推理配置——
**功耗约束可以比 VRAM 约束更早触发不可行判定。**

### 5 层配置解析 (渐进增强)

```
  ┌─────────────────────────────────────────────┐
  │ L0: 硬编码默认值                              │  always available
  │     engine_type=auto, gpu_mem_util=0.9       │
  ├─────────────────────────────────────────────┤
  │ L1: 用户 CLI / Config 指定                    │  human knowledge
  │     aima deploy --engine vllm --config gpu_memory_utilization=0.5 │
  ├─────────────────────────────────────────────┤
  │ L2: 知识库匹配 (三级)                         │  community knowledge
  │   L2a: Engine Asset — 引擎在该硬件的默认配置   │
  │   L2b: Model Asset  — 模型在该硬件的变体配置   │
  │   L2c: Tuning 最优  — 实测最优配置            │
  ├─────────────────────────────────────────────┤
  │ L3: Agent 动态调整                            │  AI intelligence
  │     LLM 分析 metrics → 调参 → benchmark      │
  └─────────────────────────────────────────────┘
  高层 override 低层。每层独立可用。
```

核心洞察：**L2 知识库是杠杆最大的一层**。它把专家经验编码为可复用资产，
不需要 LLM、不需要网络、不需要人类在场，就能在新设备上产出良好配置。
Agent (L3) 是锦上添花——它让配置从"良好"变为"最优"。

### 硬件感知配置解析

配置解析不仅按 `gpu_arch` 匹配 variant，还利用硬件的**静态规格**和**动态运行时状态**做两层校验：

**静态层 — 在 variant 选择阶段过滤不可行方案**

`findModelVariant()` 和 `InferEngineType()` 检查 Model Asset YAML 中定义的约束：

| YAML 字段 | 含义 | 过滤规则 |
|-----------|------|---------|
| `vram_min_mib` | variant 最小显存要求 | `hw.GPUVRAMMiB > 0 && vram_min_mib > hw.GPUVRAMMiB` → 跳过 |
| `unified_memory` | 是否要求统一显存 | `variant 要求 unified ≠ 硬件实际` → 跳过 |

所有阈值在 YAML 中定义，Go 代码仅做数值比较（INV-2）。
`HardwareInfo` 字段为零值时跳过检查（graceful degradation，兼容旧调用方）。

**动态层 — 在部署前根据运行时状态自适应**

`CheckFit()` 在配置解析后、部署前运行：
- 采集 `hal.CollectMetrics()` 的实时 GPU 显存占用
- 计算安全的 `gpu_memory_utilization` 上限：`(GPU 空闲显存 - 512 MiB) / GPU 总显存`
- 自动调低超出安全阈值的 `gpu_memory_utilization`（标记为 `L0-auto` 来源）
- GPU 空闲显存不足 512 MiB 时拒绝部署
- 采集失败时不阻止部署（graceful degradation）

```
                  ┌── 静态层 ──────────────────────────────────────┐
HAL Detect ──→    │ HardwareInfo.GPUVRAMMiB / UnifiedMemory       │
(规格)            │   → findModelVariant: 跳过显存不够的 variant    │
                  │   → InferEngineType: 跳过显存不够的引擎         │
                  └──────────────────────────────────────────────┘
                  ┌── 动态层 ──────────────────────────────────────┐
HAL Metrics ──→   │ HardwareInfo.GPUMemFreeMiB / GPUMemUsedMiB    │
(运行时)          │   → CheckFit: 自动调低 gpu_memory_utilization   │
                  │   → 显存严重不足时拒绝部署                       │
                  └──────────────────────────────────────────────┘
```

**典型场景**：RTX 4060 (8GB Ada) 部署 qwen3-8b →
YAML 的 Ada-vllm variant 要求 `vram_min_mib: 16384` → 静态层跳过 → 自动落到 llamacpp wildcard variant。

### 需求声明的最小 Schema

```yaml
# 模型需求
type: llm                    # llm|vlm|omni|asr|tts|diffusion|video|embedding|rerank
min_vram_mb: 4096
performance: balanced        # latency | throughput | balanced

# App 需求 (广义价值载体)
inference_needs:
  - type: llm
    required: true
  - type: tts
    required: false          # 可选依赖
resource_budget:
  cpu_cores: 2
  memory_mb: 2048
time_constraints:
  max_switch_time_s: 10      # 模型切换不超过 10 秒
  max_cold_start_s: 60       # 首次启动不超过 60 秒
```

---

## 4. 系统架构

### 三个正交关注面 + 控制面

传统分层架构暗示严格的上下依赖。但 AIMA 的维度是正交的：
- Supply 独立演进：新硬件 → 新切分工具 → 新 ResourceSlot 类型
- Demand 独立演进：新模型 → 新引擎 → 新工作负载类型
- Control 独立演进：更好的知识 → 更聪明的 Agent → 更优的绑定

```
                ┌─────────────────────────────────────┐
                │        Control Plane (控制面)         │
                │   L3:Agent · L2:Knowledge · L1:Human │
                │         · L0:Default                 │
                │       "谁来决定绑定关系"              │
                └───────┬──────────────┬───────────────┘
                        │              │
        ┌───────────────▼──┐    ┌──────▼───────────────┐
        │  Demand (需求面)   │    │   Supply (供给面)     │
        │                    │    │                      │
        │  Model (推理生产者) │    │   GPU Slices         │
        │   + Engine(三重角色)│←──→│   (引擎参数/MIG/MPS)  │
        │                    │    │                      │
        │  App (推理消费者)   │    │   CPU/RAM Slices     │
        │   + Runtime        │←──→│   (Docker/cgroups)   │
        └──────────┬─────────┘    └──────────┬──────────┘
                   │     Feedback (反馈)      │
                   └───── metrics, bench ─────┘

         ─ ─ ─ Constraints (约束) ─ ─ ─ ─ ─ ─ ─ ─
         Time: 启动/切换/重配置时间
         Energy: TDP, 运行功耗, 散热限制
```

### Supply (供给面)

"我有什么资源，怎么切分"

1. **硬件检测** → 能力向量 (GPU arch/vram/compute, CPU cores, RAM, Storage, TDP)
   + **运行时状态** → 动态指标 (GPU 已用/空闲显存, 可用 RAM)。
   两者合并为 `HardwareInfo`，同时用于 variant 选择（静态层）和部署前适配（动态层）。
2. **资源切分** → 抽象 ResourceSlot，底层可插拔：

| 切分工具 | 颗粒度 | 隔离性 | 适用场景 |
|---------|--------|--------|---------|
| 引擎参数 (gpu_mem_util) | 最细 | 弱 (软隔离) | MVP 默认，跨硬件 |
| Docker cgroups (--cpus, --memory) | 容器级 | 中等 | MVP 默认 |
| NVIDIA MIG | 粗 (最多 7 片) | 最强 | A100/H100/B200 |
| NVIDIA MPS | 中 | 中等 | 多进程共享 GPU |
| HAMi (开源 vGPU) | 细 (任意比例) | 中等 | 需要细粒度隔离时 |

**关键：引擎可以扩展 Supply。** 当引擎支持 CPU offload 时，System RAM 变成了 GPU VRAM 的延伸。
Supply 面不仅是"硬件给了什么"，还包括"引擎让我能用什么"——effective_R ≥ R。

**MVP 只实现「引擎参数 + Docker cgroups」**——最简单、跨硬件、零外部依赖。
MIG/MPS/HAMi 作为可插拔后端，v1.0+ 按需添加。

### Demand (需求面)

"需要运行什么，各需要多少"

两类工作负载：
- **Model** — 推理生产者。包含模型文件 + 引擎（三重角色）+ 配置参数
- **App** — 推理消费者。声明推理依赖 + 资源预算 + 时间约束，不限运行形态

引擎是 Model 工作负载的组成部分——它不仅是连接器和分配器，还是放大器。
选择不同引擎意味着不同的性能上限和不同的有效资源边界。

### Control Plane (控制面)

"怎么将需求绑定到资源"

| 层级 | 智能源 | 解的质量 | 触发条件 |
|------|--------|---------|---------|
| L0: 默认值 | 硬编码保守配置 | 可用解 | Always |
| L1: 人类 CLI | 用户显式指定 | 次优解 | 用户传参 |
| L2: 知识库 | Recipe 确定性匹配 | 良好解 | 硬件指纹命中 |
| L3: Agent | LLM + 工具调用 | 最优解 (动态) | Agent 可用时 |

L0→L3 是渐进增强。即使所有高层全部不可用，L0 仍能产出一个可运行的配置。

### Feedback (反馈回路)

运行时指标 (latency, throughput, VRAM usage, power_draw) → 反哺知识库 → 改善未来绑定决策。
Benchmark 结果 → Tuning 最优配置 (L2c) → 下次部署自动应用。
功耗数据 → 修正 power_draw() 预估 → 提高约束判断准确性。
这是从"一次配置"到"持续优化"的闭环。

### Connectivity (正交维度)

连接性横切所有面，扩展单设备到网络：
- **mDNS** — 局域网零配置发现 (MVP)
- **Tunnel** — 远程安全访问 (v1.0)
- **知识同步** — 社区 Recipe 拉取 (v1.0)
- **Fleet** — 多设备统一管理 (v2.0)

---

## 5. Agent 与人类 Copilot

### Agent 的决策循环

```
  ┌──────────────────────────────────────────────────┐
  │                                                    │
  │  ┌──────────┐    ┌──────────┐    ┌──────────┐    │
  │  │  Perceive │───→│  Reason  │───→│   Act    │    │
  │  │  感知      │    │  推理     │    │  行动    │    │
  │  │           │    │          │    │          │    │
  │  │ device.   │    │ Config   │    │ deploy   │    │
  │  │ detect    │    │ Resolver │    │ tune     │    │
  │  │ metrics   │    │ + LLM    │    │ scale    │    │
  │  │ power     │    │          │    │ switch   │    │
  │  └──────────┘    └──────────┘    └──────────┘    │
  │       ↑                               │           │
  │       │          ┌──────────┐         │           │
  │       │          │  Learn   │         │           │
  │       │          │  学习     │         │           │
  │       │          │          │←────────┘           │
  │       │          │ export   │                      │
  │       │          │ skill    │                      │
  │       │          └────┬─────┘                      │
  │       │               │                            │
  │       │          ┌────▼─────┐                      │
  │       └──────────│ Feedback │                      │
  │                  │ 反馈      │                      │
  │                  │ bench,   │                      │
  │                  │ metrics, │                      │
  │                  │ power    │                      │
  │                  └──────────┘                      │
  └──────────────────────────────────────────────────┘
```

Perceive → Reason → Act → Feedback → Learn → Perceive...
每一步对应具体的原子操作（MCP 工具调用），Agent 不需要理解内部实现。

### Agent 三阶段

| 阶段 | 角色 | 自主程度 | 版本 | 典型行为 |
|------|------|---------|------|---------|
| 运维操作员 | 执行用户指令 | 半自治 | MVP | 用户说"部署 qwen3"→ Agent 执行 |
| 设备大脑 | 自主巡检+优化 | 高度自治 | v1.0 | 定时检查 metrics → 发现退化 → 自动调优 |
| 集群协调者 | 跨设备调度 | 分布式 | v2.0 | 在 Fleet 中选最优设备部署 |

### 分层智能 (愿景)

- **边缘 Agent**: 本地小模型或规则引擎，低延迟决策，处理 80% 日常运维
- **云端 Agent**: 更强 LLM，通过 Tunnel + MCP 远程介入复杂问题 (故障诊断、跨设备优化)
- 边缘优先，云端兜底。v2.0+ 考虑。

### 人类 Copilot 模型

核心设计：**人类不是必须的，但必须能随时介入。**

| 触发条件 | Agent 行为 | 人类动作 | 智能层级 |
|---------|-----------|---------|---------|
| 一切正常 | 自主处理 | 看仪表盘即可 | L3 |
| Agent 信心不足 | 提供 2-3 个选项 | 选择方案 | L3→L1 |
| 连续失败 3 次 | 停止重试，发告警 | 诊断 + 手动修复 | L3→L1 |
| Agent 不可用 | — | `aima deploy <model>` 手动部署 | L1 |
| 全新硬件无知识 | — | `aima deploy <model> --engine vllm --config '{...}'` | L1→L0 |
| 连默认值都失败 | — | 查日志，调整参数，反馈给社区 | L0→L1 |

**降级是平滑的**：从 L3 到 L0，用户体验从"全自动"渐变为"手动但可用"，
永远不会到达"完全不可用"。这是"渐进增强"设计的核心价值。

### 安全护栏

| 护栏 | 规则 |
|------|------|
| 工具调用上限 | 单次决策 ≤ 30 轮工具调用 |
| 破坏性操作 | 删除模型/停止服务需用户确认 (交互模式) |
| 资源消耗 | Agent 本身占用 < 5% 设备资源 |
| 回滚能力 | 每次变更记录快照，可一键回滚 |
| 审计日志 | 所有 Agent 操作记录到结构化日志 |

---

## 6. 知识生态

### 知识的本质

知识 = 将「专家在特定硬件上调优模型的经验」编码为「可匹配、可复用、可共享的结构化资产」。

知识是 **Agent 不在时的确定性兜底**。有了知识库，新设备不需要联网、不需要 LLM、
不需要人类，就能获得社区积累的最佳实践。

知识系统的价值在于提供准确的 cost()、effective_R()、startup_time()、power_draw() 函数——
让优化模型的 5 个约束都有据可依，而不是靠猜测。

### 知识的双形态

知识以两种形态存在：**YAML（编写/分发）** 和 **SQLite 关系表（查询/推理）**。

YAML 是人类和社区的接口——可读、可 diff、可版本管理。
SQLite 是 Agent 和 MCP 工具的接口——可 JOIN、可聚合、可约束过滤。
启动时 go:embed YAML 自动加载到 SQLite 关系表，Agent 动态产出的知识直接写入 SQLite。

### 静态知识资产（YAML → SQLite）

| 资产 | 内容 | 索引键 | 来源 | 对应层级 |
|------|------|--------|------|---------|
| Hardware Profile | 硬件能力向量 + 约束 | gpu_arch × cpu_arch | 内嵌 YAML | Supply |
| Engine Asset | 引擎定义 + 三重角色 + 硬件兼容性 | engine_type × gpu_arch | 内嵌 YAML | L2a |
| Model Asset | 模型定义 + 硬件变体配置 | model_name × hw_variant | 内嵌 YAML | L2b |
| Partition Strategy | 资源划分方案 | hardware × workload_pattern | 内嵌 YAML | Supply |
| Stack Component | 基础设施依赖配置 | component × platform | 内嵌 YAML | Infra |

### 动态知识资产（Agent 探索 → SQLite）

| 资产 | 内容 | 索引键 | 来源 | 对应层级 |
|------|------|--------|------|---------|
| Configuration | 4D 配置实例 (HW×Engine×Model×Config) + 演化链 | hardware × engine × model | Agent/社区 | L2c |
| BenchmarkResult | 多维性能数据 (延迟/吞吐/资源/功耗) | config × concurrency × load | Agent | Feedback |
| PerfVector | 归一化性能向量 (6 维, 用于相似度检索) | config_id | 聚合生成 | Feedback |
| Knowledge Note | 对 Configuration 的补充叙事 (探索过程+洞察) | hardware × model × engine | Agent/社区 | L3 辅助 |

### 知识流转

```
边缘设备                                        中心端 (v1.0)
┌─────────────────────┐                    ┌──────────────────────┐
│ Agent 探索 (L3a/L3b)│                    │ 知识聚合服务 (REST)   │
│  → Configuration    │    push            │  POST /api/v1/ingest │
│  → BenchmarkResult  │ ──────────────→   │  GET  /api/v1/query  │
│  → Knowledge Note   │                    │  GET  /api/v1/sync   │
│                     │    pull            │                      │
│ SQLite (aima.db)    │ ←──────────────   │  PostgreSQL + JSONB  │
│  知识查询引擎        │                    │  所有设备聚合知识     │
└─────────────────────┘                    └──────────────────────┘
         │
         └── 离线传递: export → JSON → USB → import
```

一个设备的调优经验 → 编码为 Configuration + BenchmarkResult →
上报到中心端 → 所有同硬件设备通过 pull 获得。
Configuration 的 `derived_from` 链记录完整的演化历史——
设备 B 从设备 A 的最优配置出发微调，产出更优配置，反哺全网。

### 三阶段知识同步

| 阶段 | 机制 | 特点 |
|------|------|------|
| MVP | go:embed 内嵌 YAML | 编译时打包，离线可用，更新需要重新发版 |
| v1.0 | 中心端 REST API (push/pull) + 离线 JSON 导入/导出 | 增量同步，config_hash 去重 |
| v2.0 | P2P 知识交换 | Fleet 内设备间直接同步，无需中心仓库 |

---

## 7. 功能需求

按优化系统的 5 个要素组织。

### 资源感知 (Supply)

| ID | 需求 | 优先级 | 版本 |
|----|------|--------|------|
| S1 | 自动检测 GPU/NPU/CPU，生成能力向量 | P0 | MVP |
| S2 | 实时监控资源利用率 (VRAM/CPU/RAM) | P0 | MVP |
| S3 | 检测设备 TDP / 功耗模式 | P1 | MVP |
| S4 | 资源预算估算 (部署前预测资源消耗) | P1 | v1.0 |
| S5 | 抽象 ResourceSlot，支持可插拔切分后端 | P1 | v1.0 |
| S6 | 引擎扩展资源边界建模 (effective_R) | P2 | v1.0 |

### 需求匹配 (Demand)

| ID | 需求 | 优先级 | 版本 |
|----|------|--------|------|
| D1 | `aima deploy <model>` 一条命令部署 (自动匹配引擎+配置) | P0 | MVP |
| D2 | 多模型并行部署 (资源不冲突) | P0 | MVP |
| D3 | 模型格式 → 引擎自动选择 (含放大器评估) | P1 | v1.0 |
| D4 | App 需求声明 → 自动补齐推理服务依赖 | P1 | v1.0 |
| D5 | 时间约束感知 (启动时间匹配 App 要求) | P2 | v1.0 |

### 知识系统 (Knowledge)

| ID | 需求 | 优先级 | 版本 |
|----|------|--------|------|
| K1 | 内嵌 Recipe 覆盖主流 硬件×模型 组合 | P0 | MVP |
| K2 | 硬件指纹匹配 → 自动选择最优 Recipe | P0 | MVP |
| K3 | 5 层配置解析 (L0→L2c) 渐进 override | P0 | MVP |
| K4 | Recipe 包含性能数据 (tokens/s, 启动时间, 功耗) | P1 | v1.0 |
| K5 | 调优结果自动反哺知识库 — Configuration + BenchmarkResult 持久化 | P1 | v1.0 |
| K6 | 中心端知识同步 (`aima knowledge sync --push/--pull`) | P1 | v1.0 |
| K7 | 离线知识导入导出 (`aima knowledge export/import`) | P1 | v1.0 |
| K8 | 知识查询引擎 — 多维搜索/对比/相似度/演化链/空白发现 | P1 | v1.0 |
| K9 | 中心端知识聚合服务 (PostgreSQL + REST API) | P2 | v1.0 |

### 智能调度 (Control / Solver)

| ID | 需求 | 优先级 | 版本 |
|----|------|--------|------|
| A1 | Agent 被动模式 (用户 chat/MCP 指令触发) | P0 | MVP |
| A2 | Agent 主动巡检 (定时检查健康+性能) | P1 | v1.0 |
| A3 | 自动调优 (参数搜索 + benchmark + 最优应用) | P1 | v1.0 |
| A4 | 故障自愈 (检测→诊断→恢复) | P1 | v1.0 |
| A5 | 引擎切换评估 (性能增益 vs 切换时间成本) | P2 | v1.0 |
| A6 | 云端 Agent 远程介入 (Tunnel + MCP) | P2 | v2.0 |
| A7 | 分布式调度 (Fleet 内跨设备) | P2 | v2.0 |

### 反馈闭环 (Feedback)

| ID | 需求 | 优先级 | 版本 |
|----|------|--------|------|
| F1 | OpenAI 兼容 API 暴露推理服务 | P0 | MVP |
| F2 | mDNS 服务广播 (局域网零配置发现) | P0 | MVP |
| F3 | Benchmark 性能测试 (tokens/s, latency) | P1 | v1.0 |
| F4 | 功耗监控与报告 | P2 | v1.0 |
| F5 | 知识有效性验证 (Recipe 应用后是否达预期) | P2 | v1.0 |
| F6 | 仪表盘 (TUI + Web) 可视化状态与指标 | P1 | v1.0 |

### 基础设施栈 (Infrastructure)

| ID | 需求 | 优先级 | 版本 |
|----|------|--------|------|
| I1 | `aima init` 一键安装+配置 K3S/HAMi (离线可完成) | P0 | MVP |
| I2 | Stack Component YAML 描述依赖版本、兼容性、安装参数 | P0 | MVP |
| I3 | 离线安装包 (`dist/`) 支持完全无网络部署 | P0 | MVP |
| I4 | 配置值标注来源 (source) 和验证状态 (verified) | P1 | MVP |
| I5 | 不同硬件画像的配置变体 (profiles) | P1 | v1.0 |
| I6 | Agent 自动处理 open_questions，真机验证配置假设 | P2 | v1.0 |

### 移动端支持 (Mobile)

| ID | 需求 | 优先级 | 版本 |
|----|------|--------|------|
| M1 | 手机硬件 Profile YAML（Apple A17+/A18 NPU, Qualcomm Snapdragon 8 Gen 3+ Hexagon, MediaTek Dimensity 9300+） | P2 | v3.0 |
| M2 | 远程设备注册协议（`device.register` / `device.list` MCP 工具，心跳 + 能力上报） | P2 | v3.0 |
| M3 | 远程 Agent Runtime（`internal/runtime/remote.go`，通过网络代理推理请求到远程设备） | P2 | v3.0 |
| M4 | 手机端 Agent App（iOS Swift / Android Kotlin，嵌入 llama.cpp，独立项目） | P2 | v3.0 |
| M5 | 移动端 Engine YAML（`llamacpp-ios`, `llamacpp-android`，描述移动端引擎特性和约束） | P2 | v3.0 |
| M6 | 移动端约束建模（电池电量、热管理 throttle、后台运行限制、网络不稳定，纳入优化空间的约束维度） | P2 | v3.0 |

---

## 8. 发布规划

### MVP — "L0+L1+L2 全部可用，L3 基础可用"

**核心交付**: 单设备上，一条命令部署多模型多模态推理，知识自动匹配。

| 交付项 | 对应需求 |
|--------|---------|
| `aima init` 一键基础设施安装 (K3S/HAMi, 离线可完成) | I1, I2, I3 |
| 硬件检测 + 能力向量 (含基础功耗检测) | S1, S2, S3 |
| `aima deploy` 单命令部署 | D1, D2 |
| 5 层配置解析 (L0-L2c) | K1, K2, K3 |
| OpenAI 兼容 API | F1 |
| mDNS 服务广播 | F2 |
| Agent 被动模式 (chat) | A1 |
| 单二进制 < 50MB | — |

时间和功耗在 MVP 中是"感知但不阻止"——记录数据，不用于约束求解。

### v1.0 — "L3 完全可用，知识生态运转"

在 MVP 基础上增加：

| 交付项 | 对应需求 |
|--------|---------|
| 引擎自动选择 (含放大器评估) | D3, D4 |
| ResourceSlot 可插拔 + 引擎资源扩展建模 | S4, S5, S6 |
| Agent 主动巡检 + 自动调优 | A2, A3, A4 |
| 引擎切换成本评估 | A5, D5 |
| Benchmark + 知识反哺 (含功耗数据) | F3, F4, F5, K4, K5 |
| 知识查询引擎 + 多维 MCP 工具 | K8 |
| 知识同步 (中心端 push/pull + 离线导入/导出) | K6, K7 |
| 中心端知识聚合服务 | K9 |
| Tunnel 远程访问 | — |
| 仪表盘 (TUI + Web) | F6 |

### v2.0 — "从单设备到设备网络"

| 交付项 | 对应需求 |
|--------|---------|
| Fleet 多设备管理 | A7 |
| 云端 Agent 远程介入 | A6 |
| P2P 知识交换 | — |
| 硬件选配建议 (基于知识生态反向推导) | — |
| 国产芯片深度支持 | — |
| 分布式 Agent + 跨设备资源编排 | — |
| 手机作为远程客户端（文档化：mDNS 发现 + OpenAI API 已天然支持） | — |

### v3.0 — "手机作为推理设备"

| 交付项 | 对应需求 |
|--------|---------|
| 手机硬件 Profile YAML（Apple A-series, Qualcomm Snapdragon, MediaTek Dimensity） | M1 |
| 远程设备注册协议（device.register / device.list） | M2 |
| 远程 Agent Runtime | M3 |
| 手机端 Agent App（iOS / Android，独立项目） | M4 |
| 移动端 Engine YAML（llamacpp-ios, llamacpp-android） | M5 |
| 移动端约束建模（电池、热管理、后台限制） | M6 |

---

## 9. 成功指标

### 北极星

> **管理的活跃 AIMA 设备数量**

每台运行 AIMA 的设备都在更高效地利用 AI 推理资源。

### 效率指标 (衡量优化效果)

| 指标 | 含义 | MVP 目标 | v1.0 目标 |
|------|------|---------|----------|
| 首次部署时间 | 从安装到推理可用 | < 5 分钟 | < 1 分钟 |
| 有效 VRAM 利用率 | 实际推理用量 / 可用量 | > 50% | > 70% |
| 引擎放大比 | effective_R / R | — | 可量化报告 |
| 知识命中率 | Recipe 自动匹配成功 / 总部署 | > 50% | > 80% |
| 重配置停机时间 | 模型切换导致的服务中断 | — | < 30s |
| Agent 自治率 | 无需人工干预的运维操作占比 | — | > 80% |

### 产品指标 (衡量产品能力)

| 指标 | MVP | v1.0 |
|------|-----|------|
| 同设备并行模型数 | ≥ 2 | ≥ 5 |
| 支持引擎数 | ≥ 3 | ≥ 8 |
| 硬件平台覆盖 | ≥ 2 | ≥ 5 |
| 支持推理模态数 | ≥ 3 | ≥ 9 |
| 二进制大小 | < 50 MB | < 50 MB |
| 99th percentile API 延迟 (代理开销) | < 10ms | < 5ms |

### 增长指标 (衡量生态健康)

| 指标 | Year 1 | Year 2 |
|------|--------|--------|
| GitHub Stars | 10K+ | 100K+ |
| 设备安装量 | 100K+ | 10M+ |
| 社区 Recipe 数 | 100+ | 1,000+ |
| 社区贡献者 | 50+ | 500+ |
| 知识覆盖的 硬件×模型 组合 | 200+ | 5,000+ |

---

## 10. 风险与缓解

| 风险 | 影响 | 概率 | 缓解策略 |
|------|------|------|---------|
| 硬件碎片化加速，新芯片不断涌现 | 知识库覆盖不足 | 高 | 社区 Recipe 飞轮 + 自动调优反哺 + 可插拔硬件适配 |
| Ollama/LM Studio 加速迭代，补齐多模态 | 差异化缩小 | 中 | 深耕优化模型 + 引擎放大器 + Agent + 知识生态护城河 |
| LLM 能力不稳定，Agent 做出错误决策 | 用户信任受损 | 中 | L0-L2 兜底 + 安全护栏 + 回滚机制 + 人类确认 |
| 知识库冷启动，初期 Recipe 覆盖低 | 新用户体验差 | 高 | 内嵌主流组合 + L0 保守默认值 + Auto-Resolve 兜底 (扫描记录→合成 ModelAsset→引擎 L0 默认) + 首批用户定向支持 |
| 引擎放大器效果不可预测 (offload 性能波动大) | 资源估算不准 | 中 | Benchmark 实测 + 保守估计 + 渐进尝试策略 |
| 国产芯片生态割裂 (驱动/SDK 不统一) | 适配成本高 | 中 | 引擎抽象层 + 社区贡献适配 + 优先支持头部芯片 |
| 功耗/散热约束在边缘场景比预期更严格 | 可用算力低于预期 | 中 | 功耗感知配置 + 动态降频策略 + 知识库记录实际功耗 |
| 开源社区活跃度不足 | 知识飞轮转不起来 | 中 | 自动调优主动产出 Recipe + 降低贡献门槛 + showcase 案例 |
| 移动端约束严苛（电池/热管理/后台限制） | on-device 推理体验差或不可用 | 中 | 轻量模型优先 + 动态 throttle + 云端/LAN 推理 fallback |

---

*本文档从第一性原理推演：AIMA 本质上是有限资源上的带约束组合优化问题。*
*引擎的三重角色（连接器+分配器+放大器）使其成为优化系统中最关键的变量——*
*它不仅决定如何分配资源，还决定有多少资源可用。*
*时间约束和能源约束让优化模型从"能不能跑"升级为"能不能在真实条件下跑"。*
*三个正交关注面 (Supply / Demand / Control) + 反馈闭环构成完整的优化系统。*
*L0→L3 渐进增强确保任何条件下都有可用解——Agent 是锦上添花，不是必需品。*
