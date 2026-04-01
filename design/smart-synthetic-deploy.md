# 智能合成部署：无先验知识模型的安全部署方案

> 状态：设计稿
> 目标：当 model scan 发现的模型没有 catalog YAML 时，利用扫描元数据自动估算资源需求，避免 OOM 崩溃

---

## 1. 问题分析

### 当前流程

```
model scan → DB(name/format/path/sizeBytes/totalParams/activeParams/quantization/modelClass)
     ↓
deploy → catalog miss → BuildSyntheticModelAsset()
     ↓
合成变体: gpu_arch="*", vram_min_mib=0, default_config={}, 无TP/GMU设置
     ↓
CheckFit: EstimatedVRAMMiB=0 → 所有VRAM校验跳过 → 盲目启动
```

### 核心问题

| 问题 | 根因 | 后果 |
|------|------|------|
| VRAM 估算为零 | 合成变体不设 `vram_min_mib` | CheckFit 形同虚设，大模型直接 OOM |
| 无 TP 设置 | 合成变体不设 `tensor_parallel_size` | 70B 模型塞单卡，必然崩溃 |
| GMU 使用引擎默认 | 合成变体无 `default_config` | vLLM 默认 0.90，统一内存设备可能饿死 OS |
| 不感知统一内存 | 合成变体无 `unified_memory` 标记 | GB10/M1000 的资源策略错误 |
| 格式-引擎盲配 | 只看文件格式选引擎 | safetensors 到 vllm 但可能显存不够跑 |

### 已有但未利用的信息

scan 阶段已经采集到 DB 中：
- `SizeBytes` — 模型文件总大小（权重磁盘体积）
- `TotalParams` — 总参数量（从 config.json / GGUF header 解析）
- `ActiveParams` — 活跃参数量（MoE 每 token 激活的参数）
- `Quantization` — 量化精度（int4/int8/fp8/fp16/bf16/fp32）
- `ModelClass` — 模型类别（dense/moe/hybrid）

这些信息足以做出合理的资源估算。

---

## 2. 设计方案：VRAM 估算器

### 2.1 估算公式

**核心原理**：模型推理的 VRAM 占用 = 权重内存 + KV Cache + 激活内存 + 引擎开销

```
VRAM_estimated = WeightsMiB + OverheadMiB
```

**权重内存估算（三条路径，按优先级）**：

| 优先级 | 条件 | 公式 | 精度 |
|--------|------|------|------|
| P1 | `SizeBytes > 0` | `WeightsMiB = SizeBytes / 1048576` | 最准（磁盘约等于内存） |
| P2 | `TotalParams > 0 && Quantization 已知` | `WeightsMiB = TotalParams * BytesPerParam / 1048576` | 较准 |
| P3 | `TotalParams > 0` | `WeightsMiB = TotalParams * 2 / 1048576`（假设 FP16） | 保守估计 |

**BytesPerParam 映射**：

| Quantization | BytesPerParam | 说明 |
|-------------|---------------|------|
| fp32        | 4.0           |      |
| fp16, bf16  | 2.0           |      |
| fp8         | 1.0           |      |
| int8        | 1.0           |      |
| int5, int6  | 0.75          | GGUF 近似 |
| int4, nf4   | 0.5           |      |
| unknown     | 2.0           | 保守假设 FP16 |

**KV Cache + 激活 + 引擎开销（简化模型）**：

```
OverheadMiB = max(WeightsMiB * 0.25, 1024)    // 至少 1GB 开销
```

> 说明：25% 的 overhead 覆盖了 KV cache（默认 context 长度下）、激活内存、CUDA context 等。
> 对于长 context 场景不够，但作为 OOM 防护的下界足够用。

**SizeBytes 优先的理由**：
- safetensors 格式：磁盘大小约等于内存大小（几乎 1:1，无压缩）
- GGUF 格式：磁盘大小 = 内存大小（量化后的权重直接 mmap）
- pytorch 格式：磁盘大小约等于内存大小（序列化开销极小）
- 这比参数量乘精度更准确，因为它天然包含了 embedding、LayerNorm 等非主体参数

### 2.2 TP（Tensor Parallelism）自动推断

```
单卡可用VRAM = hw.GPUVRAMMiB（离散GPU）
            或 hw.RAMTotalMiB * 安全系数（统一内存）

所需VRAM = VRAM_estimated

if 所需VRAM <= 单卡可用VRAM * 0.85:
    TP = 1
elif hw.GPUCount >= 2 && 所需VRAM <= 单卡可用VRAM * hw.GPUCount * 0.80:
    TP = ceil(所需VRAM / (单卡可用VRAM * 0.80))
    TP = min(TP, hw.GPUCount)
    // 对齐到 2 的幂（vLLM/SGLang 要求）
    TP = nextPowerOf2(TP)
else:
    // 放不下 -> 考虑 offload 或拒绝
    标记为 needs_offload
```

**对齐规则**：
- vLLM/SGLang 要求 TP 是 2 的幂：1, 2, 4, 8
- llamacpp 的 `n_gpu_layers` 不需要 TP，走 CPU offload 路径

### 2.3 GMU（GPU Memory Utilization）自动调节

**离散 GPU**：
```
if TP == 1:
    GMU = min(0.90, VRAM_estimated / (hw.GPUVRAMMiB * 0.95))
    GMU = max(GMU, 0.50)  // 下界：至少用一半显存
else:
    GMU = 0.90  // 多卡时引擎自己管分配
```

**统一内存设备（GB10, M1000, Apple Silicon）**：
```
// 必须给 OS 留足内存
OS_reserve = 16384 MiB  (>=64GB 系统)
           = 8192 MiB   (<64GB 系统)

可用显存 = hw.RAMTotalMiB - OS_reserve
GMU = min(0.85, 可用显存 / hw.RAMTotalMiB)
GMU = max(GMU, 0.30)  // 下界
```

> 注意：这与 CheckFit 中已有的统一内存保护逻辑互补。
> CheckFit 是事后修正（resolve 后降 GMU），这里是事前规划（合成时就设合理值）。

### 2.4 max_model_len 保守设定

无先验知识时，不知道模型能支撑多长的 context。保守策略：

```
if VRAM_estimated < 4096 MiB:
    max_model_len = 2048
elif VRAM_estimated < 8192 MiB:
    max_model_len = 4096
elif VRAM_estimated < 32768 MiB:
    max_model_len = 8192
else:
    max_model_len = 16384
```

用户随时可以通过 L1 override 提高。

---

## 3. 实现方案

### 3.1 修改点总览

```
涉及文件（仅 2 个）:
  internal/knowledge/resolver.go   — BuildSyntheticModelAsset + 新增 estimateVRAM
  cmd/aima/resolve.go              — resolveWithFallback 传递更多 scan 信息

不涉及:
  internal/model/scanner.go        — 无需改动，已有所需字段
  internal/knowledge/loader.go     — YAML 模型加载无需改动
  CheckFit                         — 无需改动，合成变体设好值后自然生效
  InferEngineType                  — 无需改动，已有 VRAM + TP 过滤
  findModelVariant                 — 无需改动，已有 gpu_arch + vram + gpu_count 过滤
  Pod 生成 / CLI / MCP tools       — 无需改动
```

### 3.2 核心改动：扩展 BuildSyntheticModelAsset 签名

**现状**：
```go
func (c *Catalog) BuildSyntheticModelAsset(
    name, modelType, family, paramCount, format string,
    requestedEngines ...string,
) ModelAsset
```

**改为接收 scan 元数据结构体**：
```go
type ScanMetadata struct {
    Name         string
    Type         string  // llm, asr, tts, etc.
    Family       string  // qwen, llama, etc.
    ParamCount   string  // "8B", "70B"
    Format       string  // safetensors, gguf, pytorch
    SizeBytes    int64   // 模型文件总大小
    TotalParams  int64   // 总参数量
    ActiveParams int64   // MoE 活跃参数
    Quantization string  // int4, fp16, etc.
    ModelClass   string  // dense, moe, hybrid
}

func (c *Catalog) BuildSyntheticModelAsset(
    meta ScanMetadata, hw HardwareInfo,
    requestedEngines ...string,
) ModelAsset
```

传入 `HardwareInfo` 使得合成时能感知硬件，生成有针对性的变体。

### 3.3 核心改动：生成智能合成变体

不再只生成 `gpu_arch="*"` 的空变体，而是：

**情况 A：硬件信息完整（GPUArch + GPUVRAMMiB 已知）**

生成一个精确匹配当前硬件的变体 + 通配符 fallback：

```yaml
# 精确变体
- name: "{model}-{gpuArch}-auto"
  hardware:
    gpu_arch: "{hw.GPUArch}"
    vram_min_mib: {estimated}
    unified_memory: {hw.UnifiedMemory}
    gpu_count_min: {TP}
  engine: "{inferred}"
  format: "{format}"
  default_config:
    gpu_memory_utilization: {计算值}
    max_model_len: {保守值}
    tensor_parallel_size: {TP}          # 仅 TP>1 时设置

# 通配符 fallback
- name: "{model}-auto-fallback"
  hardware:
    gpu_arch: "*"
    vram_min_mib: {estimated}
  engine: "{defaultEngine}"
  format: "{format}"
```

**情况 B：硬件信息不完整（无 GPU 或未检测到）**

保持现有行为（`gpu_arch="*"`, `vram_min_mib=0`），但在变体的
`expected_performance.vram_mib` 中记录估算值，仅供参考不阻断。

### 3.4 核心改动：resolveWithFallback 传递完整 scan 数据

**现状**（`cmd/aima/resolve.go:186-188`）：
```go
synth := cat.BuildSyntheticModelAsset(
    dbModel.Name, dbModel.Type, dbModel.DetectedArch,
    dbModel.DetectedParams, dbModel.Format, engineType)
```

**改为**：
```go
synth := cat.BuildSyntheticModelAsset(knowledge.ScanMetadata{
    Name:         dbModel.Name,
    Type:         dbModel.Type,
    Family:       dbModel.DetectedArch,
    ParamCount:   dbModel.DetectedParams,
    Format:       dbModel.Format,
    SizeBytes:    dbModel.SizeBytes,
    TotalParams:  dbModel.TotalParams,
    ActiveParams: dbModel.ActiveParams,
    Quantization: dbModel.Quantization,
    ModelClass:   dbModel.ModelClass,
}, hwInfo, engineType)
```

### 3.5 CheckFit 不需要改动

当合成变体正确设置了 `VRAMMinMiB` 后：
- `findModelVariant` 会自动过滤掉 VRAM 不够的变体（line 674）
- `estimateResources` 会正确填充 `ResourceEstimate`（line 278）
- CheckFit 的统一内存保护、动态 GMU 调节照常工作（line 969+）
- TP vs GPUCount 校验照常工作（line 1057）

**零改动，全部自动生效。** 这是方案最优雅的部分。

---

## 4. VRAM 估算公式：完整伪代码

```
func estimateVRAMMiB(meta ScanMetadata) int:
    weightsMiB := 0

    // P1: 磁盘大小（最准）
    if meta.SizeBytes > 0:
        weightsMiB = meta.SizeBytes / (1024 * 1024)

    // P2: 参数量 x 精度
    elif meta.TotalParams > 0:
        bpp := bytesPerParam(meta.Quantization)
        weightsMiB = int(meta.TotalParams * bpp / (1024 * 1024))

    // P3: 完全未知 -> 返回 0（放弃估算，走原有逻辑）
    else:
        return 0

    // Overhead: KV cache + activations + CUDA context
    overheadMiB := max(weightsMiB / 4, 1024)

    return weightsMiB + overheadMiB
```

### 特殊情况处理

| 情况 | 处理 |
|------|------|
| GGUF 文件 | P1 路径：SizeBytes 即权重大小（GGUF = mmap，1:1） |
| safetensors 多分片 | P1 路径：SizeBytes 是 scanner 已累加的总大小 |
| MoE 模型 | 权重 = TotalParams（全部专家必须加载），但 overhead 可以按 ActiveParams 估 |
| 量化格式未知 | 按 FP16（2 bytes）保守估计 |
| SizeBytes=0 但 TotalParams>0 | 走 P2 路径 |
| 两个都是 0 | 返回 0，不做 VRAM 约束（退化到现有行为） |

---

## 5. TP 推断：完整伪代码

```
func inferTP(estimatedVRAM int, hw HardwareInfo) int:
    if hw.GPUVRAMMiB == 0 || hw.GPUCount == 0:
        return 1  // 无硬件信息，不推断

    perGPU := hw.GPUVRAMMiB
    if hw.UnifiedMemory:
        // 统一内存：可用量 = 总RAM - OS预留
        osReserve := 16384
        if hw.RAMTotalMiB < 65536:
            osReserve = 8192
        perGPU = hw.RAMTotalMiB - osReserve

    // 单卡够用？（留 15% 余量给 KV cache 波动）
    if estimatedVRAM <= int(float64(perGPU) * 0.85):
        return 1

    // 多卡？
    if hw.GPUCount <= 1:
        return 1  // 单卡就是单卡，TP>1 也没用

    needed := ceilDiv(estimatedVRAM, int(float64(perGPU) * 0.80))
    tp := nextPowerOf2(needed)  // 对齐到 2 的幂

    if tp > hw.GPUCount:
        return hw.GPUCount  // 尽力而为

    return tp
```

---

## 6. 引擎选择增强

当前 `FormatToEngine` 只看文件格式。增加一条规则：

**如果估算 VRAM > 单卡显存 且 只有 1 张卡 且引擎不支持 offload**：
- 优先选择支持 CPU offload 的引擎（llamacpp 的 `n_gpu_layers`）
- 而不是一定选 vllm（vllm 不支持部分 offload，要么全放显存要么 OOM）

在 `BuildSyntheticModelAsset` 中：
- safetensors + 放不下 + 单卡：primary=vllm（可能换卡），fallback 优先级提高
- gguf + 放不下 + 单卡：primary=llamacpp（天然支持部分 offload）

---

## 7. 与现有架构的契合度

### 遵循的 Invariant

| 原则 | 如何遵循 |
|------|---------|
| INV-1: 不按引擎/模型类型写 if/switch | BytesPerParam 是数据表，不是 if/else；引擎选择仍走 catalog |
| INV-5: MCP 工具是单一真相 | 估算逻辑在 resolver.go，CLI 和 MCP 走同一路径 |
| Prime Directive: Less Code | 核心新增约 80 行（estimateVRAM + inferTP + 扩展签名） |
| Knowledge-Driven | 不硬编码任何引擎行为，BytesPerParam 可以未来放 YAML |

### 不需要改动的地方

- `internal/model/scanner.go` — 已有所需数据
- `internal/knowledge/loader.go` — YAML 加载无关
- `CheckFit` — 合成变体设好值后自动生效
- `InferEngineType` — 已有 VRAM + TP 过滤逻辑
- `findModelVariant` — 已有 gpu_arch + vram + gpu_count 过滤
- `estimateResources` — 已有 variant.Hardware.VRAMMinMiB fallback
- Pod 生成 — 无需改动
- CLI — 无需改动（thin CLI 原则）
- MCP tools — 无需改动

**改动收敛在 2 个文件**：`resolver.go`（核心）+ `resolve.go`（传参）。

---

## 8. 效果预测

### 改进前后对比

| 场景 | 改进前 | 改进后 |
|------|--------|--------|
| 单卡 8GB 部署 70B FP16 模型 | 盲启动 -> OOM 崩溃 | `fit: false`，原因："vram 140GB > 8GB" |
| GB10 部署 32B 模型 | GMU=0.90，OS 饿死 | GMU=0.72，OS 预留 16GB |
| 双卡 4090 部署 30B-A3B MoE | TP=1（默认），单卡爆 | TP=1（MoE 活跃参数小，单卡够），GMU=0.80 |
| 单卡 4060 部署 8B GGUF Q4 | 能跑但无配置 | `n_gpu_layers=999`，`ctx_size=4096` |
| scan 到完全未知格式 | 报错 "no format info" | 同现有行为（优雅退化） |
| scan 只有 SizeBytes 无 TotalParams | VRAM=0，盲启动 | VRAM=SizeBytes/1M * 1.25，有保护 |

### 不影响的场景

- 有 YAML 的模型：走正常 catalog 路径，完全不经过合成逻辑
- 用户显式指定所有 override：L1 覆盖合成的 default_config

---

## 9. 实现优先级

分两步交付：

### Phase 1：VRAM 保护（防 OOM）
- `estimateVRAMMiB()` 函数
- `BuildSyntheticModelAsset` 接收 `ScanMetadata` + `HardwareInfo`
- 合成变体带 `vram_min_mib`
- `resolveWithFallback` 传递完整 scan 数据

**效果**：deploy.dry_run 能正确报告 fit/不fit，deploy.apply 不再盲启动

### Phase 2：智能配置（提性能）
- `inferTP()` 自动设置 tensor_parallel_size
- GMU 自动调节（统一内存感知）
- `max_model_len` 保守设定
- 引擎选择增强（放不下时回退 offload 引擎）

**效果**：无 YAML 模型也能获得接近"有 YAML"的部署质量

---

## 10. 测试策略

单元测试（table-driven）：

```
TestEstimateVRAMMiB:
  - SizeBytes=8GB, Q=int4     -> ~10GB (8 * 1.25)
  - TotalParams=8B, Q=fp16    -> ~20GB (8 * 2 * 1.25)
  - TotalParams=70B, Q=int4   -> ~44GB (70 * 0.5 * 1.25)
  - SizeBytes=0, TotalParams=0 -> 0 (退化)
  - MoE: TotalParams=400B     -> ~1000GB

TestInferTP:
  - 10GB model, 24GB GPU, 1 card  -> TP=1
  - 140GB model, 24GB GPU, 2 cards -> TP=2 (仍不够但尽力)
  - 140GB model, 24GB GPU, 8 cards -> TP=8
  - 30GB model, 120GB unified      -> TP=1 (统一内存够)
  - 未知硬件 (VRAM=0)              -> TP=1 (退化)

TestBuildSyntheticWithEstimates:
  - 验证合成变体包含 vram_min_mib > 0
  - 验证合成变体包含 default_config 的 GMU/TP/max_model_len
  - 验证硬件未知时退化到现有行为
```

集成测试：在 test lab 的 9 台设备上验证:
1. scan 一个本地模型（无 catalog YAML）
2. deploy.dry_run -> 检查 fit report 是否合理
3. 对比有 YAML 的同模型 dry_run 结果
