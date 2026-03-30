# Knowledge Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的知识架构、YAML 资产和知识查询引擎。

## 接口定义

### CLI 命令

| 命令 | 功能 |
|------|------|
| `aima knowledge list` | 列出知识资产 |
| `aima knowledge resolve <model>` | 解析最优配置 |
| `aima knowledge sync [--push\|--pull]` | 同步社区知识 |
| `aima knowledge import <path>` | 离线导入知识包 |
| `aima knowledge export [--output]` | 导出知识 |

### MCP 工具

| 工具 | JSON-RPC 方法 | 功能 |
|------|---------------|------|
| `knowledge.search` | `knowledge.search` | 搜索知识 |
| `knowledge.save` | `knowledge.save` | 保存 Knowledge Note |
| `knowledge.resolve` | `knowledge.resolve` | 解析最优配置 |
| `knowledge.list_engines` | `knowledge.list_engines` | 列出可用引擎定义 |
| `knowledge.list_profiles` | `knowledge.list_profiles` | 列出硬件 Profile |
| `knowledge.generate_pod` | `knowledge.generate_pod` | 从知识资产生成 Pod YAML |
| `knowledge.search_configs` | `knowledge.search_configs` | 多维配置搜索 |
| `knowledge.compare` | `knowledge.compare` | 对比配置性能 |
| `knowledge.similar` | `knowledge.similar` | 基于性能向量找相似配置 |
| `knowledge.lineage` | `knowledge.lineage` | 查询配置演化链 |
| `knowledge.gaps` | `knowledge.gaps` | 发现知识空白 |
| `knowledge.aggregate` | `knowledge.aggregate` | 分组聚合统计 |

---

## 双层知识架构

知识以两种形态存在，各司其职：

| 维度 | YAML（编写/分发格式） | SQLite（查询/推理格式） |
|------|---------------------|----------------------|
| 受众 | 人类、git、go:embed | Agent、MCP 工具 |
| 优势 | 可读、可 diff、可版本管理 | 可查询、可 JOIN、可聚合 |
| 内容 | 静态知识资产定义 | 静态知识 + 动态实验数据 |
| 变更 | go:embed 需重编译; overlay 目录 (`~/.aima/catalog/`) 免编译热更新 | Agent 探索 → 直接写入 |

### SQLite 表结构

**静态知识表** (7 张, 启动时从 go:embed YAML + overlay 目录合并后重建):
- `hardware_profiles` - 硬件能力向量
- `engine_assets` - 引擎定义
- `engine_features` - 引擎特性 (一对多)
- `engine_hardware_compat` - 引擎-硬件兼容性
- `model_assets` - 模型定义
- `model_variants` - 模型硬件变体
- `partition_strategies` - 资源划分策略

**动态知识表** (3 张, Agent 探索产出，持久保存):
- `configurations` - 配置实例
- `benchmark_results` - 基准测试结果
- `perf_vectors` - 性能向量 (归一化)

**系统表** (5 张, 从 v1 保留):
- `models` - 已注册模型文件
- `engines` - 已注册引擎镜像
- `knowledge_notes` - Agent 探索笔记
- `config` - AIMA 配置
- `audit_log` - 操作审计日志

---

## 知识资产类型

### 1. Hardware Profile

描述硬件的能力向量和约束。

```yaml
kind: hardware_profile
metadata:
  name: nvidia-gb10-arm64
  description: "NVIDIA DGX Spark GB10, ARM64, 128GB unified memory"
hardware:
  gpu:
    arch: Blackwell
    vram_mib: 15360
    compute_id: "10.0"
    compute_units: 2048
    resource_name: "nvidia.com/gpu"      # K8s GPU 资源名
    runtime_class_name: "nvidia"         # K8s runtimeClassName (可选)
  cpu:
    arch: arm64
    cores: 12
    freq_ghz: 3.0
  ram:
    total_mib: 131072
    bandwidth_gbps: 200
  unified_memory: true
constraints:
  tdp_watts: 100
  power_modes: [15W, 30W, 60W, 100W]
  cooling: passive
partition:
  gpu_tools: [hami, engine_params]
  cpu_tools: [k3s_cgroups]
container:                               # 厂商特定容器访问配置（K3S Pod 生成使用）
  env:                                   # 注入到容器的环境变量
    NVIDIA_VISIBLE_DEVICES: "all"
    NVIDIA_DRIVER_CAPABILITIES: "all"
    LD_LIBRARY_PATH: "/lib/aarch64-linux-gnu:/usr/local/nvidia/lib:/usr/local/nvidia/lib64"
```

#### container 字段说明

`container` 是 Hardware Profile 的可选字段，描述该硬件在 K3S 容器中运行推理时需要的厂商特定配置。
Pod 生成器（`podgen.go`）读取此字段，无需在 Go 代码中硬编码任何厂商逻辑。

| 子字段 | 类型 | 说明 | 示例 |
|--------|------|------|------|
| `devices` | `[]string` | 需要挂载到容器的宿主机设备 | `["/dev/kfd", "/dev/dri"]` (AMD ROCm) |
| `env` | `map[string]string` | 注入到容器的环境变量 | NVIDIA: `NVIDIA_VISIBLE_DEVICES`, AMD: `LD_PRELOAD` |
| `volumes` | `[]ContainerVolume` | 额外的 hostPath 挂载 | 自定义 lib 目录等 |
| `security` | `ContainerSecurity` | securityContext 配置 | `supplemental_groups: [44, 110]` (video+render) |

**AMD 硬件 Profile 示例**：
```yaml
container:
  devices:
    - /dev/kfd           # ROCm 内核融合驱动
    - /dev/dri           # DRM 渲染设备
  env:
    LD_PRELOAD: "/opt/rocm/lib/librocm_smi64.so"
  security:
    supplemental_groups: [44, 110]   # video (44) + render (110) 用户组
```

**设计原则**：所有厂商特定的容器行为（设备挂载、环境变量、安全上下文）定义在 YAML 中，
Go 代码只做通用渲染。添加新 GPU 厂商支持 = 写 Hardware Profile YAML，零 Go 代码修改。

### 2. Partition Strategy

描述在特定硬件上如何切分资源给多个工作负载。

```yaml
kind: partition_strategy
metadata:
  name: gb10-dual-model
  description: "GB10 上同时运行 2 个模型的资源划分方案"
target:
  hardware_profile: nvidia-gb10-arm64
  workload_pattern: dual_model
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
```

### 3. Engine Asset

描述引擎在特定硬件上的行为，包含三重角色（连接器+分配器+放大器）信息。

详见 [engine.md](engine.md)。

### 4. Model Asset

描述模型在不同硬件/引擎组合下的变体配置。

详见 [model.md](model.md)。

### 5. Stack Component

描述基础设施依赖（K3S、HAMi）的安装配置。

详见 [stack.md](stack.md)。

### 6. Deployment Scenario

描述在特定硬件上部署一组模型的完整方案，包括每个模型使用的引擎、配置参数、
部署后动作（如 OpenClaw 同步）和集成路由。

```yaml
kind: deployment_scenario
metadata:
  name: openclaw-multi
  description: "OpenClaw multimodal stack: LLM/VLM + TTS + ASR + ImageGen on single device"
target:
  hardware_profile: nvidia-gb10-arm64
deployments:
  - model: qwen3.5-9b
    engine: vllm-nightly-blackwell
    role: primary
    modalities: [text, vision]
    config:
      gpu_memory_utilization: 0.25
      max_model_len: 65536
  - model: qwen3-tts-0.6b
    engine: qwen-tts-fastapi-cuda-blackwell
    modalities: [tts]
  - model: qwen3-asr-1.7b
    engine: vllm-nightly-audio
    modalities: [asr]
  - model: z-image
    engine: z-image-diffusers
    modalities: [image_gen]
post_deploy:
  - action: openclaw_sync
integrations:
  openclaw:
    enabled: true
    routes:
      - path: /v1/chat/completions
        models: [qwen3.5-9b]
      - path: /v1/audio/speech
        models: [qwen3-tts-0.6b]
      - path: /v1/audio/transcriptions
        models: [qwen3-asr-1.7b]
      - path: /v1/images/generations
        models: [z-image]
verified:
  date: "2026-03-20"
  results: {llm_chat: pass, tts: pass, asr: pass, image_gen: pass, vlm: pass}
```

**设计意图**：partition_strategy 描述"如何分资源"，deployment_scenario 描述"部署什么、怎么部署"。
两者正交组合：同一硬件可有多个 scenario（如纯 LLM、多模态、开发用等），
同一 scenario 在不同硬件上可搭配不同 partition_strategy。

存放于 `catalog/scenarios/`，未来可通过 `aima scenario apply <name>` 一键部署。

---

## L0 → L3a 知识解析

ConfigResolver 按优先级合并多层知识:

```
L0: YAML catalog (go:embed + ~/.aima/catalog/ overlay 合并, staleness digest 检测)
 ↓ merge (高层 override 低层)
L1: 用户 CLI --config / --engine / --slot     (人类显式指定)
 ↓ merge
L2: knowledge_note.recommendation.config      (Agent/社区知识)
    + partition_strategy.slots                (资源划分策略)
 ↓ merge
L3a: Go Agent 实时决策 (工具调用循环)           (动态优化)
```

### 硬件感知 Variant 选择

`findModelVariant()` 在 gpu_arch 匹配的基础上，增加了两层硬件过滤：

```
findModelVariant(modelName, engineType, hw HardwareInfo)
  │
  for each variant:
  │  ├── engine 不匹配 → skip
  │  ├── VRAM 过滤: hw.GPUVRAMMiB > 0 && variant.VRAMMinMiB > hw.GPUVRAMMiB → skip
  │  ├── 统一显存过滤: variant.UnifiedMemory != nil && *variant.UnifiedMemory != hw.UnifiedMemory → skip
  │  ├── gpu_arch 精确匹配 → exactMatch
  │  └── gpu_arch == "*" → wildcardMatch
  │
  return exactMatch > wildcardMatch > error
```

`InferEngineType()` 同样在两轮扫描（精确 + 通配）中增加 VRAM 过滤，
跳过硬件显存不足的 variant 对应的引擎。

**零值 = 未知 = 跳过**：当 `HardwareInfo` 的 GPUVRAMMiB 为 0 时，不做任何过滤（向后兼容）。

### CheckFit — 部署前硬件适配性检查

在 Resolve 产出 `ResolvedConfig` 之后、部署之前，`CheckFit()` 根据运行时状态做动态调整：

```
CheckFit(resolved, hw) → FitReport { Fit, Warnings, Adjustments, Reason }
  │
  ├── 动态层: GPUMemFreeMiB > 0?
  │     ├── maxSafeGMU = (GPUMemFreeMiB - 512) / totalVRAM
  │     ├── maxSafeGMU < 0.1 → Fit=false, Reason="GPU memory insufficient"
  │     └── currentGMU > maxSafeGMU → Adjustments["gpu_memory_utilization"] = maxSafeGMU
  │
  └── RAM 检查: RAMAvailMiB > 0 && RAMAvailMiB < 2048 → Warning
```

调整后的参数标记来源为 `L0-auto`。采集失败（零值）时不做任何调整（graceful degradation）。

### Auto-Resolve 兜底

当模型不在 YAML catalog 中时，自动从 `models` 表构建"合成 ModelAsset"：

```
cat.Resolve(model) → "not found in catalog"
  │
  ├── db.FindModelByName(model)  → 优先级: 精确名 → 不区分大小写 → 子串匹配
  │     └── 未找到 → 报错
  │     └── 无 format → 报错
  │
  ├── cat.BuildSyntheticModelAsset(name, type, arch, params, format)
  │     └── format → engine 映射: 从 catalog supported_formats 动态构建
  │     └── 生成 gpu_arch="*" 通配变体，空 DefaultConfig
  │
  ├── cat.RegisterModel(synth)  → 注册到内存 catalog（去重）
  ├── overrides["model_path"] = dbModel.Path
  └── cat.Resolve(dbModel.Name) → 正常 L0 合并流程
```

---

## 知识查询引擎

### 配置搜索 (knowledge.search_configs)

支持多维过滤、排序、聚合：

```sql
SELECT c.*, pv.avg_throughput, pv.avg_ttft_p95
FROM configurations c
LEFT JOIN perf_vectors pv ON c.id = pv.config_id
WHERE c.hardware_id = ?
  AND c.model_id = ?
  AND c.status = 'production'
ORDER BY pv.avg_throughput DESC
LIMIT 10
```

### 性能对比 (knowledge.compare)

对比 N 个配置的多维性能：

```sql
SELECT c.id, c.config, br.*
FROM benchmark_results br
JOIN configurations c ON br.config_id = c.id
WHERE c.id IN (?, ?, ?)
ORDER BY br.concurrency, br.input_len_bucket
```

### 相似度检索 (knowledge.similar)

基于 6 维归一化性能向量找相似配置：

```
[norm_ttft_p95, norm_tpot_p95, norm_throughput, norm_qps, norm_vram, norm_power]
```

使用加权欧氏距离计算，支持跨硬件配置迁移推荐。

### 演化链查询 (knowledge.lineage)

使用 `WITH RECURSIVE` 查询配置演化历史：

```sql
WITH RECURSIVE lineage(id, derived_from, level) AS (
  SELECT id, derived_from, 0 FROM configurations WHERE id = ?
  UNION ALL
  SELECT c.id, c.derived_from, l.level + 1
  FROM configurations c
  JOIN lineage l ON c.id = l.derived_from
)
SELECT * FROM lineage ORDER BY level;
```

---

## 知识生命周期

```
Agent 探索 (L3a)
  → 产出 Configuration + BenchmarkResult + Knowledge Note
  → 保存到本地 SQLite
         │
         ├── 上报: aima knowledge sync --push (需联网)
         │         → POST 到中心端知识聚合服务
         │
         ├── 拉取: aima knowledge sync --pull (需联网)
         │         → 按 hardware_id 过滤拉取社区知识
         │
         ├── 导出: aima knowledge export → JSON 文件
         │
         └── 离线导入: aima knowledge import <路径>
```

### 知识复用示例

1. 设备 A 的 Agent 探索 GLM-4.7-Flash + vLLM on GB10
2. 产出 Configuration (gpu_mem_util=0.80) + BenchmarkResult (21.2 tps)
3. 上报到中心端 / 导出到 USB
4. 设备 B (同型硬件) 拉取/导入
5. 设备 B 的 Agent 通过 `knowledge.search_configs` 查到这个配置
6. **直接从 0.80 开始微调**，发现 0.82 更好 → 产出新 Configuration (derived_from=原配置)
7. 反哺社区

---

## 相关文件

- `internal/knowledge/loader.go` - YAML 加载+解析
- `internal/knowledge/resolver.go` - L0→L3 配置解析 + 硬件感知 variant 选择 + CheckFit
- `internal/knowledge/query.go` - 知识查询引擎
- `internal/knowledge/similarity.go` - 向量相似度
- `internal/knowledge/podgen.go` - Pod YAML 生成（厂商无关模板，含 imagePullPolicy:IfNotPresent）
- `catalog/embed.go` - go:embed 入口
- `catalog/scenarios/` - Deployment Scenario YAML（多模型部署方案）

---

*最后更新：2026-03-20*
