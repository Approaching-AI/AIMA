# Qwen3.6 GB10 DFlash 调优 Round 4 结果

时间：`2026-04-23`

机器：`gb10-4t (100.91.39.109)`

模型：`/home/qujing/aima-codex-qwen36/models/Qwen3.6-35B-A3B`

Draft model：`/home/qujing/aima-codex-qwen36/models/Qwen3.6-35B-A3B-DFlash`

镜像：`qujing/vllm-gb10-dflash-fa2:latest`

Prompt 文件：

- [qwen3.6-gb10-dflash-round4-business-prompt.txt](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round4-business-prompt.txt)

原始结果 JSON：

- [qwen3.6-gb10-dflash-round4-business-results.json](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round4-business-results.json)

## 测试目的

Round 3 已经在 AIMA synthetic prompt 上把默认值收敛到：

- `max_num_batched_tokens=8192`
- `num_speculative_tokens=10`
- `gpu_memory_utilization=0.92`

Round 4 不再继续扫 synthetic，而是换成业务风格中文 prompt，验证这个默认值在更真实的输入形态下是否仍然成立。

本轮只对比两组：

- `mbt8192_spec10_gmu092`
- `mbt8192_spec12_gmu092`

统一口径：

- `32K/64K input`
- `1K output`
- `concurrency=1`
- `requests=1`
- `rounds=3`
- `warmup=2`

固定运行约束：

- `--block-size 1280`
- `--attention-backend FLASH_ATTN`
- `VLLM_KV_CACHE_LAYOUT=NHD`
- `VLLM_SKIP_GDN_PREFILL_WARMUP=1`
- `FLA_GDN_FIX_BT=1`

## 结果

### 32K input / 1K output

| Config | avg_input_tokens | TTFT ms | TPOT ms | Throughput tok/s | Mean acceptance length | Draft acceptance rate |
|---|---:|---:|---:|---:|---:|---:|
| `mbt8192_spec10_gmu092_business` | 29173 | 6676.442 | 32.420 | 25.574 | 2.241 | 22.41% |
| `mbt8192_spec12_gmu092_business` | 29173 | 6574.381 | 31.590 | 25.567 | 2.362 | 19.69% |

### 64K input / 1K output

| Config | avg_input_tokens | TTFT ms | TPOT ms | Throughput tok/s | Mean acceptance length | Draft acceptance rate |
|---|---:|---:|---:|---:|---:|---:|
| `mbt8192_spec10_gmu092_business` | 58314 | 14769.426 | 37.198 | 18.847 | 2.224 | 22.24% |
| `mbt8192_spec12_gmu092_business` | 58314 | 14786.582 | 39.842 | 18.651 | 2.276 | 18.96% |

## 解读

### 1. `spec10` 在业务 prompt 上仍然成立

本轮最重要的结论是：

- 业务 prompt 没有推翻 Round 3 的默认值

`spec10` 和 `spec12` 在 `32K` 基本打平：

- `throughput` 几乎一样
  - `25.574` vs `25.567 tok/s`
- `spec12` 的 `TTFT/TPOT` 略好
- 但差距很小，不足以改变默认值判断

到了 `64K`，`spec10` 的优势重新出现：

- `TTFT` 略低
- `TPOT` 更低，约好 `6.6%`
- `throughput` 更高，约好 `1.1%`

所以如果目标是统一覆盖 `32K` 和 `64K`，`spec10` 仍然是更平衡的选择。

### 2. 业务 prompt 没有暴露出“synthetic 最优、真实场景失真”的问题

从本轮结果看：

- `spec10` 在 business prompt 上的趋势，与 synthetic prompt 下的趋势一致
- `spec12` 没有因为 prompt 风格更真实，就明显反超 `spec10`

这说明上一轮得到的默认值，不只是 synthetic benchmark 的偶然结果。

### 3. `32K` 与 `64K` 的侧重点仍然不同

这轮再次说明：

- `32K` 下多个配置差距很小，更多是细微权衡
- `64K` 更容易把差距拉开

因此如果未来还要继续优化“统一默认值”，应优先看：

- `64K`
- `TPOT`
- `throughput`

而不是只盯 `32K` 的单点吞吐。

## 当前默认推荐

到 Round 4 为止，当前最合理的默认配置仍然是：

- `max_num_batched_tokens=8192`
- `num_speculative_tokens=10`
- `gpu_memory_utilization=0.92`

适用范围：

- `32K/64K input`
- `1K output`
- `concurrency=1`
- 当前这条 GB10 patched DFlash 运行路径

## 下一步建议

如果继续做实验，优先级建议如下：

1. 用 `spec10` 补一轮重复验证，确认波动区间
2. 再换第二类真实业务 prompt
   - 例如：信息抽取型 prompt
   - 或：技术文档问答型 prompt
3. 如果还想再往上抬性能，不应继续只扫 `spec`，而应转去分析：
   - 当前 patched DFlash 路径的 prefill 开销
   - 为什么 `32768` 这条 chunked prefill 线在 GB10 上失效
