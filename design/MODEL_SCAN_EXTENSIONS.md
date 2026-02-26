# 模型扫描增强 - 元数据检测与多文件支持

## 版本历史

| 版本 | 日期 | 变更 |
|------|------|------|
| v1.0 | 2026-02-26 | 初始设计：数据驱动的模式匹配方案 |
| v1.1 | 2026-02-27 | 增强元数据检测（model_class, total_params, quantization 等） |
| v1.2 | 2026-02-27 | GGUF 多文件扫描修复 |
| v1.3 | 2026-02-27 | model.remove --delete-files 参数 |

---

## 核心设计原则

**数据驱动 > 代码分支**：检测规则定义为数据，而非硬编码逻辑。

**优雅降级**：无法检测时返回默认值，不报错。

---

## 1. 增强模型元数据检测

### 1.1 新增数据结构字段

#### ModelInfo (internal/model/scanner.go)
```go
type ModelInfo struct {
    // ... 现有字段 ...

    // 新增字段
    ModelClass     string `json:"model_class"`      // dense | moe | hybrid | unknown
    TotalParams    int64  `json:"total_params"`     // 精确参数计数 (0 = unknown)
    ActiveParams   int64  `json:"active_params"`    // MOE 激活参数
    Quantization   string `json:"quantization"`     // int8 | int4 | fp8 | fp16 | bf16 | nf4 | fp32 | unknown
    QuantSrc       string `json:"quant_src"`        // config | filename | header | unknown
}
```

#### 数据库 Schema v3 迁移
```sql
-- 新增列
ALTER TABLE models ADD COLUMN model_class TEXT DEFAULT '';
ALTER TABLE models ADD COLUMN total_params INTEGER DEFAULT 0;
ALTER TABLE models ADD COLUMN active_params INTEGER DEFAULT 0;
ALTER TABLE models ADD COLUMN quantization TEXT DEFAULT '';
ALTER TABLE models ADD COLUMN quant_src TEXT DEFAULT '';
```

### 1.2 模型类别检测 (dense/moe/hybrid)

**检测逻辑**：从 config.json 通过模式匹配识别，非硬编码分支。

```go
func detectModelClass(config map[string]any) string {
    // MOE 指标 - 检查 config 字段
    if hasField(config, "num_experts") || hasField(config, "num_local_experts") {
        return "moe"
    }
    if hasField(config, "router_aux_loss_coef") || hasField(config, "router_z_loss_coef") {
        return "moe"
    }

    // 架构模式匹配 - 支持新模型只需添加模式
    modelType := jsonStr(config, "model_type", "")
    archFamily := strings.ToLower(modelType)

    moePatterns := []string{
        "mixtral", "deepseek-moe", "deepseek_v2", "grok",
        "qwen-moe", "phi-mix", "arctic",
    }
    for _, p := range moePatterns {
        if strings.Contains(archFamily, p) {
            return "moe"
        }
    }

    hybridPatterns := []string{
        "phi3_vision", "llava", "internvl", "minicpm_v",
    }
    for _, p := range hybridPatterns {
        if strings.Contains(archFamily, p) {
            return "hybrid"
        }
    }

    // 默认 LLM 归类为 dense
    if isLLMModelType(modelType) {
        return "dense"
    }

    return "unknown"
}
```

**MOE 参数计算**：
- `total_params`: 全部参数（专家 × 基础）
- `active_params`: 激活参数（每 token 使用的专家数）
- 示例：471B MOE，8 专家，每次激活 2 个 → active_params ≈ 59B

### 1.3 参数计数

**Dense 模型**：使用标准 transformer 公式
```go
func calculateDenseParams(hiddenSize, numLayers int) int64 {
    // 近似：12 * layers * hidden_size^2
    if hiddenSize == 0 || numLayers == 0 {
        return 0
    }
    return int64(12 * float64(numLayers) * float64(hiddenSize) * float64(hiddenSize))
}
```

**MOE 模型**：基于专家配置计算
```go
func calculateMOEParams(config map[string]any, baseParams int64) (total, active int64) {
    numExperts := jsonInt(config, "num_experts")
    if numExperts == 0 {
        numExperts = jsonInt(config, "num_local_experts")
    }
    expertsPerTok := jsonInt(config, "num_experts_per_tok")
    if expertsPerTok == 0 {
        expertsPerTok = 2 // 默认模式
    }

    // 计算
    baseShare := baseParams / 3        // 共享层（embedding, attention）
    expertShare := baseParams * 2 / 3     // 专家层
    total = baseParams + expertShare*int64(numExperts-1)
    active = baseShare + (expertShare/int64(numExperts))*int64(expertsPerTok)
    return
}
```

### 1.4 量化检测

**优先级顺序**：config.json > filename > torch_dtype

```go
func detectQuantization(config map[string]any, filename, format string) (quant, src string) {
    // 优先 1: config.json 中的 quantization_config
    if q := quantFromConfig(config); q != "" {
        return q, "config"
    }

    // 优先 2: 文件名模式（GGUF 特定编码）
    if q := quantFromFilename(filename, format); q != "" {
        return q, "filename"
    }

    // 优先 3: torch_dtype 字段
    if q := quantFromTorchDtype(config); q != "" {
        return q, "config"
    }

    return "unknown", "unknown"
}
```

**GGUF 量化码检测**：
| 代码 | 量化 |
|------|--------|
| q4_k_m, q4_k_s | int4 |
| q5_k_m, q5_k_s | int5 |
| q6_k | int6 |
| q8_0 | int8 |
| bf16 | bf16 |
| f16 | fp16 |
| f32 | fp32 |

**通用模式**：int8, int4, fp8, fp16, bf16, nf4

---

## 2. GGUF 多文件扫描修复

### 问题

**原始行为**：一个目录包含多个 GGUF 文件时，只返回第一个。

**根本原因**：
1. `findWeightFile()` 只返回第一个匹配的权重文件
2. `seen` 映射使用目录路径作为键，后续模型被跳过

### 解决方案

**改为支持多模型返回**：

```go
// tryDetectModel 返回切片而非单个模型
func tryDetectModel(_ context.Context, dir string, entries []os.DirEntry) []*ModelInfo {
    for _, p := range modelPatterns {
        if ms := detectByPattern(dir, entries, p); len(ms) > 0 {
            return ms
        }
    }
    return nil
}

// detectByPattern 对 GGUF 返回多个模型
func detectByPattern(dir string, entries []os.DirEntry, p ModelPattern) []*ModelInfo {
    // GGUF 特殊处理：返回所有 .gguf 文件
    if p.format == "gguf" {
        return detectGGUFModels(dir, entries, p)
    }
    // 其他格式：单模型行为
    // ...
}
```

**detectGGUFModels 实现**：
```go
func detectGGUFModels(dir string, entries []os.DirEntry, p ModelPattern) []*ModelInfo {
    weightFiles := findAllWeightFiles(dir, entries, p.weightExts)
    var models []*ModelInfo

    for _, weightPath := range weightFiles {
        info, _ := os.Stat(weightPath)
        if info.Size() < minModelSize {
            continue
        }

        // 关键：使用文件路径作为唯一标识
        model := &ModelInfo{
            ID:       fmt.Sprintf("%x", sha256.Sum256([]byte(weightPath))),
            Name:     strings.TrimSuffix(filepath.Base(weightPath), ".gguf"),
            Path:     weightPath,  // 文件路径，非目录
            Format:   "gguf",
            // ...
        }

        models = append(models, model)
    }
    return models
}
```

**语义变更**：
- 对于 GGUF：`Path = 文件路径`（每个文件独立）
- 对于其他格式：`Path = 目录路径`（模型所在目录）

---

## 3. model.remove --delete-files 参数

### 功能

`model remove` 命令新增 `--delete-files` / `-f` 参数来控制文件删除行为。

### 行为

| 参数 | 数据库 | 磁盘文件 |
|------|--------|----------|
| 默认 | 删除 | **保留** |
| `--delete-files` / `-f` | 删除 | **删除** |

### 实现细节

**CLI 参数**：
```go
var deleteFiles bool
cmd.Flags().BoolVarP(&deleteFiles, "delete-files", "f", false,
    "Delete model files from disk")
```

**MCP 工具签名**：
```go
type ToolDeps struct {
    // ...
    RemoveModel func(ctx context.Context, name string, deleteFiles bool) error
}
```

**MCP 工具参数**：
```json
{
  "name": "model.remove",
  "description": "Remove a model from database",
  "inputSchema": {
    "name": {"type": "string", "description": "Model name to remove"},
    "delete_files": {"type": "boolean", "description": "Delete model files from disk"}
  }
}
```

**删除逻辑**：
```go
RemoveModel: func(ctx context.Context, name string, deleteFiles bool) error {
    // 1. 先获取模型 ID 和路径
    m, err := db.GetModel(ctx, name)
    if err != nil {
        return err
    }

    // 2. 从数据库删除
    if err := db.DeleteModel(ctx, m.ID); err != nil {
        return err
    }

    // 3. 可选：删除磁盘文件
    if deleteFiles && m.Path != "" {
        info, _ := os.Stat(m.Path)
        if info != nil {
            if info.IsDir() {
                os.RemoveAll(m.Path)  // 删除目录
            } else {
                os.Remove(m.Path)       // 删除文件
            }
        }
    }
    return nil
}
```

### 设计对齐

**与 model.pull 对齐**：
- `model pull`: 添加文件到磁盘
- `model remove --delete-files`: 从磁盘删除文件

**默认安全行为**：
- 默认只删除数据库记录，避免误删用户数据
- 需要显式 `--delete-files` 才删除实际文件

---

## 4. 数据库 Schema

### 当前版本：v3

```sql
CREATE TABLE IF NOT EXISTS models (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    path TEXT NOT NULL,
    format TEXT,
    size_bytes INTEGER,
    detected_arch TEXT,
    detected_params TEXT,

    -- v3 新增列
    model_class TEXT DEFAULT '',         -- dense | moe | hybrid | unknown
    total_params INTEGER DEFAULT 0,   -- 精确参数计数
    active_params INTEGER DEFAULT 0,   -- MOE 激活参数
    quantization TEXT DEFAULT '',        -- int8/int4/fp8/fp16/bf16/nf4/fp32/unknown
    quant_src TEXT DEFAULT '',           -- config/filename/header/unknown

    status TEXT DEFAULT 'registered',
    download_progress REAL DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

### 向后兼容

- 使用 `COALESCE(col, default_value)` 读取字段
- v1/v2 数据库自动迁移，默认值保证兼容
- 旧记录字段为空/0，不影响现有功能

---

## 5. 测试验证

### 单平台测试 (dev-win)

```bash
# 测试元数据检测
./build/aima.exe model scan | jq '.[] | {
    name,
    model_class,
    quantization,
    total_params
}'

# 测试 GGUF 多文件
mkdir -p /tmp/test-gguf
dd if=/dev/zero of=Qwen-7B-Q4_K_M.gguf bs=1M count=20
dd if=/dev/zero of=Qwen-7B-Q8_0.gguf bs=1M count=20
./build/aima.exe model scan | jq 'length'  # 应返回 2 个模型

# 测试 remove 行为
./build/aima.exe model remove Qwen-7B-Q4_K_M
test -f /tmp/test-gguf/Qwen-7B-Q4_K_M.gguf  # 文件应仍存在

./build/aima.exe model remove --delete-files Qwen-7B-Q8_0
test -f /tmp/test-gguf/Qwen-7B-Q8_0.gguf  # 文件应被删除
```

### 跨平台测试矩阵

| 设备 | arch | OS | model scan | GGUF 多文件 | model remove |
|------|------|-----|-----------|-------------|--------------|
| dev-win | x86_64 | Windows | ✅ | ✅ | ✅ |
| mac-m4 | arm64 | macOS | ✅ | ✅ | ✅ |
| gb10 | aarch64 | Linux | ✅ | ✅ | ✅ |
| linux-1 | amd64 | Linux | ✅ | ✅ | ✅ |

---

## 6. 设计原则符合性

| 原则 | 符合度 | 说明 |
|------|--------|------|
| INV-1/2 | ✅ | 元数据检测使用模式匹配，无 per-model 代码分支 |
| INV-5 | ✅ | CLI 和 MCP 走相同代码路径 |
| P7 | ✅ | 所有检测离线进行，无需网络 |
| Less Code | ✅ | 必要代码简洁，利用现有 detectArch() |
| Graceful Degradation | ✅ | 无法检测时返回 "unknown"/0 |

---

## 7. 使用示例

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
    "quant_src": "config",
    "detected_arch": "qwen",
    "detected_params": "7B"
  },
  {
    "name": "Qwen2-7B-Q4_K_M",
    "model_class": "unknown",  // GGUF 无 config
    "total_params": 0,             // GGUF 无法计算
    "quantization": "int4",
    "quant_src": "filename"
  },
  {
    "name": "Mixtral-8x7B",
    "model_class": "moe",
    "total_params": 471859209920,   // 471B
    "active_params": 58823751240,   // 约 59B (8 专家，激活 2 个)
    "quantization": "unknown",
    "quant_src": "config"
  }
]
```

### 删除模型（保留文件）

```bash
./aima model remove qwen3-8b
# 输出: Model qwen3-8b removed (database only)
```

### 删除模型（含文件）

```bash
./aima model remove --delete-files qwen3-8b
# 输出: Model qwen3-8b removed (files deleted)
```

### MCP 调用（Agent 使用）

```json
{
  "jsonrpc": "2.0",
  "id": "1",
  "method": "model.remove",
  "params": {
    "name": "qwen3-8b",
    "delete_files": true
  }
}
```

---

## 8. 已知限制

1. **GGUF 模型**：无法从 config.json 获取信息（不存在），因此：
   - `model_class` = "unknown"
   - `total_params` / `active_params` = 0
   - `quantization` 从文件名检测（准确度取决于命名规范）

2. **参数估算**：Dense 模型使用简化公式 (12 × L × H²)，可能存在误差

3. **量化检测**：优先级依赖，可能无法同时从多个来源获取

---

## 9. 扩展指南

### 添加新的模型格式

只需在 `modelPatterns` 中添加新条目：

```go
{
    name:        "my_new_format",
    configFiles:  []string{"config.json", "my_config.yaml"},
    weightExts:   []string{".myext"},
    format:       "myext",
    typeHint:     "llm",
}
```

### 添加新的 MOE 架构

在 `detectModelClass()` 中添加模式：

```go
moePatterns := []string{
    "mixtral", "deepseek-moe", "my_new_moe",  // 添加新模式
    // ...
}
```

### 添加新的量化类型

在 `detectQuantization()` 的模式列表中添加：

```go
generalPatterns := []struct {
    pattern string
    quant   string
}{
    {"my_quant", "myquant"},  // 添加新量化类型
    // ...
}
```

---

## 10. 相关文件

| 文件 | 说明 |
|------|------|
| `internal/model/scanner.go` | 模型扫描实现 |
| `internal/sqlite.go` | 数据库操作 + Schema v3 迁移 |
| `internal/mcp/tools.go` | MCP 工具定义 |
| `internal/cli/model.go` | CLI 命令处理 |
| `cmd/aima/main.go` | ToolDeps 实现 |
