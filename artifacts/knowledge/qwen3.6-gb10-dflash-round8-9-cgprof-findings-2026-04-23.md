# Qwen3.6 GB10 DFlash Round 8-9 CUDAGraph Memory Profiling Findings

Date: `2026-04-23`  
Host: `gb10-4t (100.91.39.109)`  
Model: `Qwen3.6-35B-A3B`  
Drafter: `Qwen3.6-35B-A3B-DFlash`  
Image: `qujing/vllm-gb10-dflash-fa2:latest`

## 背景

Round 7 已经把业务 prompt 下的当前最好运行面收敛到：

- `enforce_eager=false`
- `block_size=1280`
- `max_num_batched_tokens=8192`
- `num_speculative_tokens=10`
- `gpu_memory_utilization=0.92`

但在 `non-eager` 路径里，vLLM 日志明确给出了一条新的线索：

- `VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=1`

这会改变 CUDA graph 内存估算方式，从而影响有效 KV cache 预算。Round 8-9 就专门验证这一层。

## 统一口径

- Prompt: [qwen3.6-gb10-dflash-round4-business-prompt.txt](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round4-business-prompt.txt)
- 输入档位：`32K`、`64K`
- 输出长度：`1K`
- `concurrency=1`
- `requests=1`
- `rounds=3`
- `warmup=2`

固定运行约束：

- `enforce_eager=false`
- `block_size=1280`
- `max_num_batched_tokens=8192`
- `num_speculative_tokens=10`
- `attention_backend=FLASH_ATTN`
- `VLLM_KV_CACHE_LAYOUT=NHD`
- `VLLM_SKIP_GDN_PREFILL_WARMUP=1`
- `FLA_GDN_FIX_BT=1`

原始结果：

- [qwen3.6-gb10-dflash-round8-cgprof-business-results.json](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round8-cgprof-business-results.json)
- [qwen3.6-gb10-dflash-round9-cgprof-gmu094-business-results.json](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round9-cgprof-gmu094-business-results.json)

## 测试组合

对照基线来自 Round 7：

- `business_noneager_block1280`

新增验证了 3 组：

1. `business_noneager_cgprof_gmu092`
2. `business_noneager_cgprof_gmu09435`
3. `business_noneager_cgprof_gmu094`

## 结果总表

### 32K input / 1K output

| Config | Ready s | TTFT ms | TPOT ms | Throughput tok/s | 结果 |
|---|---:|---:|---:|---:|---|
| `business_noneager_block1280` | 385.1 | 6299.419 | 30.754 | 27.018 | Baseline |
| `business_noneager_cgprof_gmu092` | 385.2 | 6173.510 | 30.780 | 27.159 | OK |
| `business_noneager_cgprof_gmu09435` | - | - | - | - | 启动失败 |
| `business_noneager_cgprof_gmu094` | 390.1 | 6877.394 | 49.459 | 17.563 | OK，但明显退化 |

### 64K input / 1K output

| Config | Ready s | TTFT ms | TPOT ms | Throughput tok/s | 结果 |
|---|---:|---:|---:|---:|---|
| `business_noneager_block1280` | 385.1 | 14484.791 | 40.202 | 18.312 | Baseline |
| `business_noneager_cgprof_gmu092` | 385.2 | 14294.397 | 37.614 | 19.690 | OK |
| `business_noneager_cgprof_gmu09435` | - | - | - | - | 启动失败 |
| `business_noneager_cgprof_gmu094` | 390.1 | 15702.477 | 72.166 | 11.468 | OK，但明显退化 |

## 对比分析

### 1. `VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=1 + gmu=0.92` 是当前最有价值的新改动

相对业务 prompt 下的 `business_noneager_block1280`：

- `32K`
  - `TTFT -2.0%`
  - `TPOT +0.1%`
  - `throughput +0.5%`
- `64K`
  - `TTFT -1.3%`
  - `TPOT -6.4%`
  - `throughput +7.5%`

这组最值得注意的是：

- `32K` 基本持平略好
- `64K` 有实打实提升

也就是说，这条线对更长的上下文更有价值。

### 2. `gmu=0.9435` 已经过界，启动前就失败

这组没有进入 benchmark。

现场日志里最关键的报错是：

`Free memory on device cuda:0 (112.71/119.7 GiB) on startup is less than desired GPU memory utilization (0.9435, 112.93 GiB).`

这说明：

- 在 `cgprof` 打开之后，`0.9435` 已经超过了当前这台 GB10 的安全启动边界
- 这不是“请求阶段偶发抖动”，而是启动前预算校验直接失败

### 3. `gmu=0.94` 虽然能起，但性能大幅恶化

相对基线：

- `32K`
  - `TTFT +9.2%`
  - `TPOT +60.8%`
  - `throughput -35.0%`
- `64K`
  - `TTFT +8.4%`
  - `TPOT +79.5%`
  - `throughput -37.4%`

这说明：

- `0.94` 虽然没有像 `0.9435` 那样在启动前就失败
- 但它已经明显越过“有效优化”区间
- 进入了能跑但性能显著变坏的区域

### 4. 这轮日志还给出一个有价值的新事实

在 `business_noneager_cgprof_gmu092` 里，vLLM 自己打印的建议值不是此前另一轮看到的 `0.9435`，而是：

- 当前 `gmu=0.9200`
- 等效于未启用 graph profiling 时的 `0.8778`
- 若想维持同等有效 KV cache，大约要提到 `0.9622`

这说明：

- “建议 gmu”不是固定值
- 它会随这条运行面的实际 graph/profiling 结果变化
- 因此不能机械套用前一轮日志里的推荐数值

## 当前结论

到现在为止，这条线上真正应该保留的是：

- `non-eager`
- `block_size=1280`
- `max_num_batched_tokens=8192`
- `num_speculative_tokens=10`
- `gpu_memory_utilization=0.92`
- `VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=1`

也就是：

- `cgprof` 这个变量本身是有用的
- 但不能和更高的 `gmu` 一起盲目往上推

## 建议

后续如果继续追这条线，优先级应该是：

1. 把 `VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=1` 纳入新的候选基线
2. 如果还要继续试 `gmu`，只考虑非常小的邻近值，例如 `0.925`、`0.93`
3. 不再测试：
   - `gmu=0.9435`
   - `gmu=0.94`

原因很简单：

- `0.9435` 已经确认启动前预算失败
- `0.94` 已经确认虽然能跑，但吞吐和 TPOT 都明显恶化
