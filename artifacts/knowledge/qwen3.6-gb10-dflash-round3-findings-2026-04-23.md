# Qwen3.6 GB10 DFlash 调优 Round 3 结果

时间：`2026-04-23`

机器：`gb10-4t (100.91.39.109)`

模型：`/home/qujing/aima-codex-qwen36/models/Qwen3.6-35B-A3B`

Draft model：`/home/qujing/aima-codex-qwen36/models/Qwen3.6-35B-A3B-DFlash`

镜像：`qujing/vllm-gb10-dflash-fa2:latest`

## 测试目标

在上一轮已经确认 `max_num_batched_tokens=8192`、`gpu_memory_utilization=0.92` 是稳定带之后，本轮只继续细扫：

- `num_speculative_tokens=10`
- `num_speculative_tokens=11`
- `num_speculative_tokens=13`

目标负载不变：

- `32K/64K input`
- `1K output`
- `concurrency=1`
- 工具：`aima benchmark run`
- `requests=1`
- `rounds=3`
- `warmup=2`

固定运行约束：

- `--block-size 1280`
- `--attention-backend FLASH_ATTN`
- `VLLM_KV_CACHE_LAYOUT=NHD`
- `VLLM_SKIP_GDN_PREFILL_WARMUP=1`
- `FLA_GDN_FIX_BT=1`

## 稳定配置总表

### 32K input / 1K output

| Config | TTFT ms | TPOT ms | Throughput tok/s | Mean acceptance length | Draft acceptance rate |
|---|---:|---:|---:|---:|---:|
| `mbt8192_spec8_gmu092` | 6317.836 | 33.969 | 25.140 | 2.068 | 25.85% |
| `mbt8192_spec10_gmu092` | 6094.347 | 32.066 | 25.616 | 2.107 | 21.07% |
| `mbt8192_spec11_gmu092` | 6260.455 | 32.477 | 26.075 | 2.459 | 22.35% |
| `mbt8192_spec12_gmu092` | 6324.543 | 31.904 | 25.990 | 2.341 | 19.51% |
| `mbt8192_spec13_gmu092` | 6280.319 | 32.710 | 25.017 | 2.321 | 17.86% |
| `mbt8192_spec15_gmu092_rerun` | 6435.538 | 32.137 | 25.731 | 2.595 | 17.30% |

### 64K input / 1K output

| Config | TTFT ms | TPOT ms | Throughput tok/s | Mean acceptance length | Draft acceptance rate |
|---|---:|---:|---:|---:|---:|
| `mbt8192_spec8_gmu092` | 14398.992 | 42.083 | 17.820 | 2.073 | 25.91% |
| `mbt8192_spec10_gmu092` | 13927.394 | 38.454 | 19.210 | 2.190 | 21.90% |
| `mbt8192_spec11_gmu092` | 14175.324 | 40.671 | 18.310 | 2.168 | 19.71% |
| `mbt8192_spec12_gmu092` | 14101.654 | 41.326 | 18.700 | 2.291 | 19.09% |
| `mbt8192_spec13_gmu092` | 14263.840 | 41.800 | 17.870 | 2.288 | 17.60% |
| `mbt8192_spec15_gmu092_rerun` | 14460.669 | 43.542 | 17.313 | 2.220 | 14.80% |

## 结果解读

### 1. 当前最优默认值从 `spec12` 改成 `spec10`

如果目标是混合覆盖 `32K` 和 `64K` 两档，当前最合理的默认值是：

- `max_num_batched_tokens=8192`
- `num_speculative_tokens=10`
- `gpu_memory_utilization=0.92`

原因：

- 相比上一轮最优 `spec12`：
  - `32K`：
    - `TTFT` 下降约 `3.6%`
    - `TPOT` 基本持平，略差约 `0.5%`
    - `throughput` 略低约 `1.4%`
  - `64K`：
    - `TTFT` 下降约 `1.2%`
    - `TPOT` 下降约 `6.9%`
    - `throughput` 提升约 `2.7%`

整体看，`spec10` 在 `64K` 的收益更扎实，而 `32K` 的损失很小，所以更适合作为统一默认值。

### 2. `spec11` 只在 `32K throughput` 上略微领先，但优势太小

`spec11` 在 `32K` 的 `throughput` 是全表最高：

- `26.075 tok/s`

但它相对 `spec10` 的优势只有约：

- `+1.8%` `32K throughput`

而到了 `64K`：

- `throughput` 比 `spec10` 低约 `4.7%`
- `TPOT` 也更差

所以如果目标不是“只追 32K 单点吞吐”，`spec11` 不值得做默认值。

### 3. 峰值不在 `12` 的右侧

`spec13` 的结果已经说明，继续把 `num_speculative_tokens` 往 `12` 右边推，没有带来收益：

- `32K` 和 `64K` 都没有超过 `spec10/spec11/spec12`
- `throughput` 已经明显回落

这意味着当前这台 GB10 上，DFlash 的有效峰值大概率就在：

- `spec10`
- `spec11`
- `spec12`

这段区间里。

### 4. 接受率不是唯一目标

从表里能看到：

- `spec8` 的 `draft acceptance rate` 最高
- 但它的端到端吞吐并不是最好

这说明当前工作负载下，不能只追 acceptance。更重要的是：

- `prefill`
- `decode`
- draft 长度带来的验证成本

三者的综合平衡。

## 当前推荐

### 默认推荐

用于 `32K/64K input + 1K output + concurrency=1` 的当前默认值：

- `max_num_batched_tokens=8192`
- `num_speculative_tokens=10`
- `gpu_memory_utilization=0.92`

### 特殊说明

如果只做 `32K`，并且只关心单点 `throughput`，可以考虑：

- `num_speculative_tokens=11`

但这个收益很小，更像测试波动边缘，不建议取代 `spec10` 做统一默认值。

## 仍然不推荐的方向

上一轮排除掉的两个方向，本轮没有推翻：

- `max_num_batched_tokens=32768`
  - 启动和 warmup 成本明显更差
  - 在当前 GB10 patched DFlash 路线上没有表现出净收益
- `gpu_memory_utilization=0.94`
  - 已经被实测打出 `Triton Error [CUDA]: out of memory`
  - 不属于稳定配置

## 下一步建议

如果继续追这条线，优先级建议如下：

1. 用 `spec10` 作为新默认值，先补一轮重复验证
2. 补真实业务 prompt，而不是只看 AIMA synthetic prompt
3. 如果要继续冲更高上限，不要先扫 `spec`，而要回到更底层的问题：
   - 为什么 `max_num_batched_tokens=32768` 在这条 GB10 路线上拖垮启动和请求阶段
   - 是否能在不触发 `FLA/Triton OOM` 的前提下，进一步优化 prefill 路径
