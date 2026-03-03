# Model Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的模型管理功能。

## 接口定义

### CLI 命令

| 命令 | 功能 |
|------|------|
| `aima model scan` | 扫描本地模型目录，检测并注册到数据库 |
| `aima model list` | 列出所有已注册模型 |
| `aima model info <name>` | 获取模型详细信息 |
| `aima model pull <name>` | 从远程源下载模型 |
| `aima model import <path>` | 从本地路径导入模型 |
| `aima model remove <name>` | 从数据库删除模型记录 |
| `aima model remove --delete-files <name>` | 删除模型记录并从磁盘删除文件 |

### MCP 工具

| 工具 | JSON-RPC 方法 | 功能 |
|------|---------------|------|
| `model.scan` | `model.scan` | 扫描本地模型 |
| `model.list` | `model.list` | 列出所有模型 |
| `model.get` | `model.info` | 获取模型详情 |
| `model.pull` | `model.pull` | 下载模型 |
| `model.import` | `model.import` | 导入模型 |
| `model.delete` | `model.remove` | 删除模型 |

---

## 数据结构

### ModelInfo (internal/model/scanner.go)

```go
type ModelInfo struct {
    ID             string `json:"id"`
    Name           string `json:"name"`
    Type           string `json:"type"`
    Path           string `json:"path"`
    Format         string `json:"format"`
    SizeBytes      int64  `json:"size_bytes"`
    DetectedArch   string `json:"detected_arch"`
    DetectedParams string `json:"detected_params"`

    // v1.1 增强元数据字段
    ModelClass     string `json:"model_class"`      // dense | moe | hybrid | unknown
    TotalParams    int64  `json:"total_params"`     // 精确参数计数
    ActiveParams   int64  `json:"active_params"`    // MOE 激活参数
    Quantization   string `json:"quantization"`     // int8 | int4 | fp8 | fp16 | bf16 | nf4 | unknown
    QuantSrc       string `json:"quant_src"`        // config | filename | unknown
}
```

### Model (internal/sqlite.go)

数据库表定义，包含所有元数据字段。

---

## 硬件感知 Variant 匹配

Model Asset YAML 中的 variant 可声明硬件约束，配置解析器在选择 variant 时会强制检查：

| YAML 字段 | 类型 | 含义 | 过滤行为 |
|-----------|------|------|---------|
| `hardware.gpu_arch` | string | GPU 架构匹配 | 精确匹配 > 通配 `*` |
| `hardware.vram_min_mib` | int | 最小显存要求 | 硬件显存不足时跳过该 variant |
| `hardware.unified_memory` | *bool | 是否要求统一显存 | 不匹配时跳过该 variant |

当硬件信息未知（零值）时跳过所有过滤，确保向后兼容。
详见 [knowledge.md](knowledge.md) 的"硬件感知 Variant 选择"章节。

---

## 核心算法

### 1. 模型类别检测

`detectModelClass(config map[string]any) string`

**MOE 指标**：
- `num_experts`, `num_local_experts`, `num_experts_per_tok`
- `router_aux_loss_coef`, `router_z_loss_coef`

**已知 MOE 架构**：
- mixtral, deepseek-moe, deepseek_v2, grok
- qwen-moe, phi-mix, arctic

**混合模型**：
- vision-language: llava, internvl, phi3_vision, minicpm_v

**默认**: 密集 transformer 模型归为 `dense`

### 2. 参数计数

**Dense 模型**: `calculateDenseParams(hiddenSize, numLayers) int64`
- 近似公式：`12 * layers * hidden_size²`

**MOE 模型**: `calculateMOEParams(config, baseParams) (total, active int64)`
- 基础层占 1/3，专家层占 2/3
- 激活参数 = 基础 + (专家总数/激活数) × 单个专家

### 3. 量化检测

`detectQuantization(config, filename, format) (quant, src string)`

**优先级**：
1. config.json 的 `quantization_config`
2. 文件名模式（GGUF 量化码）
3. torch_dtype 字段

**GGUF 量化码映射**：

| 代码 | 量化 |
|------|------|
| q4_k_m, q4_k_s | int4 |
| q5_k_m, q5_k_s | int5 |
| q6_k | int6 |
| q8_0 | int8 |
| bf16 | bf16 |
| f16 | fp16 |
| f32 | fp32 |

### 4. GGUF 多文件扫描

**v1.2 修复**：每个 GGUF 文件作为独立模型检测

- 问题：`findWeightFile()` 只返回第一个匹配文件
- 解决：`detectGGUFModels()` 返回所有 .gguf 文件
- 关键：使用文件路径作为唯一标识（非目录路径）

**v1.3 修复**：GGUF 路径匹配兼容

- 问题：`Import()` 和 `scanDirectory()` 使用精确目录匹配 (`m.Path == modelDir`)，GGUF 模型的 Path 是文件路径（非目录），导致导入后找不到模型
- 解决：同时匹配目录路径（safetensors/pytorch）和目录前缀（GGUF 文件路径）

---

## 数据库 Schema v3

```sql
-- v3 迁移：新增元数据字段
ALTER TABLE models ADD COLUMN model_class TEXT DEFAULT '';
ALTER TABLE models ADD COLUMN total_params INTEGER DEFAULT 0;
ALTER TABLE models ADD COLUMN active_params INTEGER DEFAULT 0;
ALTER TABLE models ADD COLUMN quantization TEXT DEFAULT '';
ALTER TABLE models ADD COLUMN quant_src TEXT DEFAULT '';
```

---

## 使用示例

### 扫描并查看元数据

```bash
# 扫描所有模型
./aima model scan

# 输出示例
[
  {
    "name": "qwen3-8b",
    "model_class": "dense",
    "total_params": 7247757312,
    "active_params": 7247757312,
    "quantization": "bf16",
    "quant_src": "config"
  },
  {
    "name": "Mixtral-8x7B",
    "model_class": "moe",
    "total_params": 471859209920,
    "active_params": 58823751240,
    "quantization": "unknown"
  }
]
```

### 删除模型

```bash
# 只删除数据库记录（保留文件）
./aima model remove qwen3-8b

# 删除数据库记录并删除文件
./aima model remove --delete-files qwen3-8b
```

---

## 相关文件

- `internal/model/scanner.go` - 模型扫描器实现
- `internal/sqlite.go` - 数据库操作
- `internal/cli/model.go` - CLI 命令处理
- `internal/mcp/tools.go` - MCP 工具定义

---

*最后更新：2026-03-03 (GGUF 路径匹配修复)*
