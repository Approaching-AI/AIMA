# Qwen3.6 GB10 DFlash Round 6 Runtime Findings

Date: `2026-04-23`  
Host: `gb10-4t (100.91.39.109)`  
Model: `Qwen3.6-35B-A3B`  
Drafter: `Qwen3.6-35B-A3B-DFlash`  
Image: `qujing/vllm-gb10-dflash-fa2:latest`

## 背景

前几轮已经把这条 GB10 patched DFlash 路线的主配置收敛到：

- `max_num_batched_tokens=8192`
- `num_speculative_tokens=10`
- `gpu_memory_utilization=0.92`
- `attention_backend=FLASH_ATTN`
- `block_size=1280`
- `enforce_eager=true`

Round 6 不再继续扫 `spec` 或 `mbt`，而是直接验证两个更底层、也更可能留下性能空间的运行时变量：

1. `enforce_eager`
2. `block_size`

## 统一口径

- 输入档位：`32K`、`64K`
- 输出长度：`1K`
- `concurrency=1`
- `requests=1`
- `rounds=3`
- `warmup=2`
- 工具：`aima benchmark run`

固定运行约束：

- `--attention-backend FLASH_ATTN`
- `--max-num-batched-tokens 8192`
- `--gpu-memory-utilization 0.92`
- `--speculative-config {"method":"dflash","model":"...","num_speculative_tokens":10}`
- `VLLM_KV_CACHE_LAYOUT=NHD`
- `VLLM_SKIP_GDN_PREFILL_WARMUP=1`
- `FLA_GDN_FIX_BT=1`

## 实测组合

本轮实际完成了 3 组：

1. `control_eager_block1280`
2. `noneager_block1280`
3. `eager_block1024`

原计划中的 `eager_block1536` 和 `eager_block2048` 没有继续跑完；在前 3 组已经给出足够强信号后，实验主动收口，避免继续占用远端机器时间。

原始结果文件：

- [qwen3.6-gb10-dflash-round6-runtime-sweep-results.json](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round6-runtime-sweep-results.json)

## 结果

### 32K input / 1K output

| Config | Ready s | TTFT ms | TPOT ms | Throughput tok/s |
|---|---:|---:|---:|---:|
| `control_eager_block1280` | 310.1 | 6505.690 | 32.276 | 25.386 |
| `noneager_block1280` | 440.2 | 6247.472 | 29.068 | 28.397 |
| `eager_block1024` | 305.1 | 6960.116 | 32.111 | 25.913 |

### 64K input / 1K output

| Config | Ready s | TTFT ms | TPOT ms | Throughput tok/s |
|---|---:|---:|---:|---:|
| `control_eager_block1280` | 310.1 | 14686.699 | 37.937 | 18.685 |
| `noneager_block1280` | 440.2 | 14287.901 | 36.142 | 19.876 |
| `eager_block1024` | 305.1 | 14065.362 | 39.990 | 18.499 |

## 对比分析

### 1. `enforce_eager=false` 是目前最明确的新收益点

相对当前默认值 `control_eager_block1280`：

- `32K`
  - `TTFT -4.0%`
  - `TPOT -9.9%`
  - `throughput +11.9%`
- `64K`
  - `TTFT -2.7%`
  - `TPOT -4.7%`
  - `throughput +6.4%`

这说明在当前这条 patched GB10 DFlash 路线上，`non-eager` 并不是“可能有收益但容易炸”的纯猜测，而是已经跑出真实正收益的方向。

同时也要明确代价：

- `ready_s` 从 `310.1s` 增加到 `440.2s`
- 启动成本约增加 `42.0%`

所以这条线更适合：

- 长时间驻留的稳定服务
- 重视 steady-state 吞吐和 TPOT 的线上推理

不太适合：

- 频繁重启
- 对启动时延特别敏感的临时实例

### 2. `block_size=1024` 没有带来净收益

这组最重要的信号不是最终表格，而是启动日志：

- vLLM 没有真的按 `1024` 运行
- 它把 attention block size 自动改成了 `1136`

具体日志是：

> `Setting attention block size to 1136 tokens to ensure that attention page size is >= mamba page size.`

这意味着：

- `1024` 在这个 hybrid/mamba 模型上不是一个稳定、直接的控制量
- 它会被底层 page-size 约束改写

从结果看，`1024 -> 1136` 这条线没有兑现收益：

- `32K`
  - `TTFT +7.0%`
  - `TPOT -0.5%`
  - `throughput +2.1%`
- `64K`
  - `TTFT -4.2%`
  - `TPOT +5.4%`
  - `throughput -1.0%`

也就是说：

- `32K` 只有非常轻微的吞吐改善，但 TTFT 明显变差
- `64K` 甚至出现了 throughput 回退

所以 `block_size=1024` 可以排除，不值得作为默认值方向继续投入。

### 3. `non-eager` 还顺带改善了 speculative acceptance

服务侧 metrics 也给出了一致方向：

- `32K`
  - mean acceptance length: `2.288 -> 2.408`
  - draft acceptance rate: `22.88% -> 24.08%`
- `64K`
  - mean acceptance length: `2.078 -> 2.114`
  - draft acceptance rate: `20.78% -> 21.14%`

幅度不大，但方向和 TPOT/throughput 的改善一致，说明这不是单纯测试噪声。

## 当前结论

到 Round 6 为止，当前最值得推进的新默认值候选已经出现：

- `max_num_batched_tokens=8192`
- `num_speculative_tokens=10`
- `gpu_memory_utilization=0.92`
- `block_size=1280`
- `enforce_eager=false`

如果目标是：

- `32K/64K input`
- `1K output`
- `concurrency=1`
- 服务常驻

那么它比现有 eager 默认值更好。

## 下一步建议

后续实验优先级建议改成：

1. 在 `non-eager + block_size=1280` 上补一轮业务 prompt 复验
2. 再做 `block_size=1536` 与 `block_size=2048`，但只在 `non-eager` 上测
3. 如果 `non-eager` 结论继续稳定，再把它写入 YAML 作为新的 GB10 DFlash 推荐配置

不建议继续做：

- `block_size=1024`
- 再往 `spec` 或 `mbt` 上做大 sweep

因为当前最大的新增收益，已经明确来自 `enforce_eager=false`。
