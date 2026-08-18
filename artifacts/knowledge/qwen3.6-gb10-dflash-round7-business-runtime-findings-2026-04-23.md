# Qwen3.6 GB10 DFlash Round 7 Business Runtime Findings

Date: `2026-04-23`  
Host: `gb10-4t (100.91.39.109)`  
Model: `Qwen3.6-35B-A3B`  
Drafter: `Qwen3.6-35B-A3B-DFlash`  
Image: `qujing/vllm-gb10-dflash-fa2:latest`

## 背景

Round 6 已经证明，在 synthetic prompt 下：

- `enforce_eager=false`
- `block_size=1280`

相对当前默认 `enforce_eager=true`，可以拿到明确正收益。

但那个结论还不够，因为真正要决定默认值，必须看真实业务风格 prompt 下是否仍然成立。因此 Round 7 只做一件事：

- 在同一条业务 prompt 上，对比 `eager1280` 与 `non-eager1280`

## 统一口径

- Prompt: [qwen3.6-gb10-dflash-round4-business-prompt.txt](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round4-business-prompt.txt)
- 输入档位：`32K`、`64K`
- 输出长度：`1K`
- `concurrency=1`
- `requests=1`
- `rounds=3`
- `warmup=2`
- 工具：`aima benchmark run --prompt ...`

固定运行约束：

- `max_num_batched_tokens=8192`
- `num_speculative_tokens=10`
- `gpu_memory_utilization=0.92`
- `block_size=1280`
- `attention_backend=FLASH_ATTN`
- `VLLM_KV_CACHE_LAYOUT=NHD`
- `VLLM_SKIP_GDN_PREFILL_WARMUP=1`
- `FLA_GDN_FIX_BT=1`

原始结果：

- [qwen3.6-gb10-dflash-round7-business-runtime-results.json](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round7-business-runtime-results.json)

## 结果

### 32K input / 1K output

| Config | Ready s | TTFT ms | TPOT ms | Throughput tok/s |
|---|---:|---:|---:|---:|
| `business_eager_block1280` | 265.1 | 6346.090 | 32.895 | 26.266 |
| `business_noneager_block1280` | 385.1 | 6299.419 | 30.754 | 27.018 |

### 64K input / 1K output

| Config | Ready s | TTFT ms | TPOT ms | Throughput tok/s |
|---|---:|---:|---:|---:|
| `business_eager_block1280` | 265.1 | 14421.963 | 41.700 | 18.290 |
| `business_noneager_block1280` | 385.1 | 14484.791 | 40.202 | 18.312 |

## 对比分析

### 1. `non-eager` 在业务 prompt 下仍然更好，但收益明显收窄

相对 `business_eager_block1280`：

- `32K`
  - `TTFT -0.7%`
  - `TPOT -6.5%`
  - `throughput +2.9%`
- `64K`
  - `TTFT +0.4%`
  - `TPOT -3.6%`
  - `throughput +0.1%`

这和 Round 6 synthetic prompt 下的收益相比，已经明显缩小：

- synthetic `32K`: `throughput +11.9%`
- business `32K`: `throughput +2.9%`
- synthetic `64K`: `throughput +6.4%`
- business `64K`: `throughput +0.1%`

也就是说：

- `non-eager` 的方向仍然是对的
- 但在更贴近真实业务的 prompt 上，它并没有把 end-to-end 吞吐再明显抬高一大截

### 2. 业务 prompt 下，`64K` 已经非常接近 prefill 主导

从结果看：

- `64K` 下 `TPOT` 的确改善了
- 但 `TTFT` 几乎没有改善，甚至略差
- 最终 `throughput` 只比 eager 高 `0.1%`

这说明在这类业务 prompt 上：

- decode 路径优化并没有充分转化成端到端收益
- 主要瓶颈更像还停在 prefill / scheduler / 长上下文初始化这一侧

### 3. 启动成本仍然是显著代价

`non-eager` 的 `ready_s`：

- `265.1s -> 385.1s`
- 增幅约 `+45.3%`

所以如果只看业务 prompt 的最终收益，当前结论会比 Round 6 更保守：

- `32K`：有一定价值
- `64K`：收益非常有限
- 启动成本仍然明显偏高

### 4. acceptance 没有带来额外惊喜

业务 prompt 下，`non-eager` 的 speculative acceptance 并没有比 eager 更高：

- `32K`
  - mean acceptance length: `2.336 -> 2.319`
  - acceptance rate: `23.36% -> 23.19%`
- `64K`
  - mean acceptance length: `2.160 -> 2.133`
  - acceptance rate: `21.60% -> 21.33%`

也就是说：

- `non-eager` 的收益更像来自运行时执行路径本身
- 不是因为 draft acceptance 明显变好了

## 当前结论

Round 7 给出的最重要信息是：

1. `non-eager` 在业务 prompt 下没有翻车，方向仍然成立。
2. 但它在真实风格输入上的收益，比 synthetic prompt 下小得多。
3. 对 `64K + 1K output` 这种更贴近真实场景的负载，当前 DFlash 的真正瓶颈大概率已经不只是 decode，而是 prefill / long-context runtime 路径。

因此，当前不应该把“`non-eager` 在 synthetic 下快很多”直接外推成“线上会明显快很多”。

## 建议

接下来更值得做的，不再是继续围绕 `eager/non-eager` 做更多重复 A/B，而是转去定位为什么真实业务场景下收益收窄：

1. 显式验证 `chunked prefill / partial prefill` 相关参数
2. 检查 `non-eager` 下 CUDA graph / compile 带来的 KV cache 预算下降是否抵消了收益
3. 采更细的 `/metrics` 与日志，对比 `prefill` 与 `decode` 的真实占比

如果现在就要给工程默认值建议：

- 不建议仅凭 Round 6 synthetic 结果，就直接把 `non-eager` 升成线上默认
- 更稳妥的表述是：
  - `non-eager` 是一个有潜力的候选配置
  - 但在真实业务 prompt 下，当前收益不足以单独支撑默认值切换
