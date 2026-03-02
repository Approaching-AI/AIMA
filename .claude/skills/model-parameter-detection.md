# 模型参数检测经验

> 覆盖 GGUF 二进制解析、MoE/Dense 参数计算、VLM 嵌套配置处理
> 基于：`internal/model/scanner.go` 的三轮修复
> 日期：2026-02-28

---

## 一、GGUF 二进制格式解析

### 文件头结构

```
Offset  Size   Field
0       4      magic: 0x47475546 ("GGUF" little-endian)
4       4      version: uint32 (通常 3)
8       8      tensor_count: uint64
16      8      kv_count: uint64
24      ...    KV pairs (变长)
```

### KV 对编码

每个 KV pair：
1. key: `uint64(len) + bytes` (无 null terminator)
2. value_type: `uint32` (枚举)
3. value: 根据 type 变长

```
Type ID → Go Type
0  UINT8      1 byte
1  INT8       1 byte
2  UINT16     2 bytes
3  INT16      2 bytes
4  UINT32     4 bytes
5  INT32      4 bytes
6  FLOAT32    4 bytes
7  BOOL       1 byte (uint8)
8  STRING     uint64(len) + bytes
9  ARRAY      uint32(elem_type) + uint64(count) + data
10 UINT64     8 bytes
11 INT64      8 bytes
12 FLOAT64    8 bytes
```

**ARRAY 处理**：不需要读取 array 内容，但需要高效跳过。固定大小类型可 `Seek(count * elemSize)`；STRING 类型必须逐个读 length 并跳过。

**vocab_size 特殊处理**：GGUF 没有 `{arch}.vocab_size` 字段。vocab_size 来自 `tokenizer.ggml.tokens` array 的 count（`ggufSkipArray` 返回 count）。

### GGUF Key → config.json 映射

```go
// scanner.go 中实际使用的映射
"general.architecture"                     → 确定 arch prefix
"{arch}.block_count"                       → "num_hidden_layers"
"{arch}.embedding_length"                  → "hidden_size"
"{arch}.feed_forward_length"               → "intermediate_size"
"{arch}.attention.head_count"              → "num_attention_heads"
"{arch}.attention.head_count_kv"           → "num_key_value_heads"
"{arch}.expert_count"                      → "num_experts"
"{arch}.expert_used_count"                 → "num_experts_per_tok"
"{arch}.expert_feed_forward_length"        → "moe_intermediate_size"      // ≠ feed_forward_length!
"{arch}.expert_shared_feed_forward_length" → "shared_expert_intermediate_size"
"tokenizer.ggml.tokens"                    → vocab_size (通过 array count)
```

**关键陷阱**：`feed_forward_length` 是 shared expert / dense FFN 的 intermediate_size。MoE expert 用 `expert_feed_forward_length`，两者通常不同（例如 Qwen3.5-35B-A3B: FFN=18944, expert_FFN=2560）。混淆会导致参数量偏差 7x。

### 量化信息

GGUF 文件名通常包含量化信息（如 `Q8_0`, `Q4_K_M`），`scanner.go` 从文件名提取。
`general.file_type` KV 也编码了量化类型，但文件名提取更稳定。

---

## 二、MoE 参数计算

### 公式（经验证准确到 ±2%）

```
每层 Expert MLP:
  expert_mlp = 3 * H * moe_I              # gate_proj + up_proj + down_proj
  total_expert_mlp = E * expert_mlp        # E = num_experts

每层 Shared Expert MLP (如果有):
  shared_expert_mlp = 3 * H * shared_I     # 与 expert 结构相同，不同 intermediate_size

每层 Router Gate:
  router = H * E                           # 每 expert 一个 logit

每层 GQA Attention:
  q_dim = (heads / kv_heads) * kv_heads * head_dim   # = heads * head_dim = H
  kv_dim = kv_heads * head_dim
  attn = H * (q_dim + 2*kv_dim + q_dim)              # Q, K, V projections + output

总参数:
  per_layer = total_expert_mlp + shared_expert_mlp + router + attn + 2*H (layernorm)
  total = L * per_layer + vocab * H (embed) + [vocab * H (LM head)] + H (final norm)

激活参数:
  active_expert_mlp = num_experts_per_tok * expert_mlp
  active_per_layer = active_expert_mlp + shared_expert_mlp + attn + 2*H
  active = L * active_per_layer + vocab * H + [vocab * H] + H
```

### LM Head 判断

`tie_word_embeddings: true` → embed 和 LM head 共享权重，不算两次
`tie_word_embeddings: false` → embed + LM head 各一份，总参数 +vocab*H

**VLM 陷阱**：`tie_word_embeddings` 在 **top-level config.json** 中，不在 `text_config` 中。
`calculateMOEParams(archConfig, topConfig)` 需要接收两个 config map。

### 实测验证

| 模型 | 计算值 | 官方值 | 误差 |
|------|--------|--------|------|
| Qwen3.5-35B-A3B | 34.13B / 2.93B | ~35B / ~3B | ~2.5% |
| Qwen3-0.6B (Dense) | 0.51B | 0.6B | ~15% (差 LM head) |

---

## 三、Dense 模型参数计算

### 粗算公式 vs 精算公式

```
粗算：12 * L * H²
  → 0.6B 模型估出 0.35B（偏差 42%）
  → 缺少 FFN intermediate_size、GQA attention 差异、vocab embedding

精算（calculateDenseParamsFromConfig）：
  每层 = GQA_attn + 3*H*I + 2*H (layernorm)
  总 = L * 每层 + vocab*H (embed) + [vocab*H (LM head)] + H (final norm)
```

**教训**：`12*L*H²` 只在 `H = I/4` 且 `heads = kv_heads` 时准确。现代模型几乎都不满足。

---

## 四、VLM text_config 嵌套模式

### 问题

VLM（视觉-语言模型）的 config.json 将文本模型参数嵌套在 `text_config` 中：

```json
{
  "model_type": "qwen3_5_moe",
  "tie_word_embeddings": false,       // ← top-level
  "text_config": {
    "hidden_size": 4096,              // ← 嵌套
    "num_hidden_layers": 40,
    "num_experts": 128,
    ...
  }
}
```

### 解决方案：resolveArchConfig

```go
func resolveArchConfig(config map[string]any) map[string]any {
    // 检查 text_config 是否存在
    if tc, ok := config["text_config"].(map[string]any); ok {
        if _, hasLayers := tc["num_hidden_layers"]; hasLayers {
            return tc
        }
    }
    return config
}
```

调用链：
1. `resolveArchConfig(config)` → archConfig
2. `calculateMOEParams(archConfig, config)` → 用 archConfig 取架构字段，用 config 取 tie_word_embeddings
3. `calculateDenseParamsFromConfig(archConfig, config)` → 同上

---

## 五、compressed-tensors 量化检测

### 问题

FP8 预量化模型（如 `Qwen3.5-35B-A3B-FP8`）使用 `quant_method: "compressed-tensors"`，
但实际 bit depth 嵌套在深层 config 中：

```json
{
  "quantization_config": {
    "quant_method": "compressed-tensors",
    "config_groups": {
      "group_0": {
        "weights": {
          "num_bits": 8,
          "type": "float"
        }
      }
    }
  }
}
```

### 解决方案

```go
func extractBitsFromConfigGroups(quantConfig map[string]any) int {
    groups := quantConfig["config_groups"].(map[string]any)
    for _, v := range groups {
        group := v.(map[string]any)
        if weights, ok := group["weights"].(map[string]any); ok {
            if bits := jsonInt(weights["num_bits"]); bits > 0 {
                return int(bits)
            }
        }
    }
    return 0
}
```

---

## 六、jsonInt 类型扩展

GGUF 解析后 metadata 值类型是 Go 的具体类型（uint32, uint64, int32, int64），
而 JSON 解析后是 float64。`jsonInt()` 需要处理所有类型：

```go
func jsonInt(v any) int64 {
    switch n := v.(type) {
    case float64:  return int64(n)    // JSON
    case int:      return int64(n)
    case int64:    return n
    case int32:    return int64(n)    // GGUF INT32
    case uint32:   return int64(n)    // GGUF UINT32
    case uint64:   return int64(n)    // GGUF UINT64
    }
    return 0
}
```

---

## 七、代码组织与去重

### attnParamsPerLayer 提取

GQA attention 计算在 dense 和 MoE 路径中完全相同。提取为：

```go
func attnParamsPerLayer(config map[string]any) int64 {
    H := jsonInt(config["hidden_size"])
    heads := jsonInt(config["num_attention_heads"])
    kvHeads := jsonInt(config["num_key_value_heads"])
    if kvHeads == 0 { kvHeads = heads }
    headDim := H / heads
    qDim := heads * headDim
    kvDim := kvHeads * headDim
    return H * (qDim + 2*kvDim + qDim) // Q, K, V, O projections
}
```

2 concrete uses → 满足抽象条件。提取后 net -28 行。

### 设计原则遵守

- 无 engine/model 类型分支（INV-1 ✅）
- 函数签名以 `config map[string]any` 为输入，不绑定特定模型
- GGUF 和 JSON 两条路径最终汇合到同一参数计算逻辑

---

更新：2026-02-28（GGUF 解析 + MoE/Dense 参数计算 + VLM 适配）
