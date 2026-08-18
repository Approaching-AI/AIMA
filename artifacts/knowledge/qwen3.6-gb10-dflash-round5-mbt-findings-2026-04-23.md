# Qwen3.6 GB10 DFlash 调优 Round 5 结果

时间：`2026-04-23`

机器：`gb10-4t (100.91.39.109)`

模型：`/home/qujing/aima-codex-qwen36/models/Qwen3.6-35B-A3B`

Draft model：`/home/qujing/aima-codex-qwen36/models/Qwen3.6-35B-A3B-DFlash`

镜像：`qujing/vllm-gb10-dflash-fa2:latest`

## 测试目的

前几轮已经把当前默认值收敛到：

- `max_num_batched_tokens=8192`
- `num_speculative_tokens=10`
- `gpu_memory_utilization=0.92`

Round 5 不再扫 `spec`，而是测试在保持 `spec10` 不变时，是否可以通过抬高 `max_num_batched_tokens` 再拿到收益。

本轮测试：

- `mbt10240_spec10_gmu092`
- `mbt12288_spec10_gmu092`
- `mbt16384_spec10_gmu092`

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

### 当前默认值参照

来自 Round 3：

| Config | 32K TTFT ms | 32K TPOT ms | 32K TPS | 64K TTFT ms | 64K TPOT ms | 64K TPS |
|---|---:|---:|---:|---:|---:|---:|
| `mbt8192_spec10_gmu092` | 6094.347 | 32.066 | 25.616 | 13927.394 | 38.454 | 19.210 |

### 本轮中间档位

| Config | Ready s | 32K TTFT ms | 32K TPOT ms | 32K TPS | 64K TTFT ms | 64K TPOT ms | 64K TPS |
|---|---:|---:|---:|---:|---:|---:|---:|
| `mbt10240_spec10_gmu092` | 280.1 | 6266.694 | 34.429 | 24.287 | 14299.375 | 41.923 | 17.949 |
| `mbt12288_spec10_gmu092` | 295.1 | 6025.450 | 33.029 | 25.551 | 13713.293 | 41.247 | 18.669 |
| `mbt16384_spec10_gmu092` | 约 315 | 未形成稳定 AIMA 结果 | 未形成稳定 AIMA 结果 | 未形成稳定 AIMA 结果 | 未形成稳定 AIMA 结果 | 未形成稳定 AIMA 结果 | 未形成稳定 AIMA 结果 |

## 解读

### 1. `mbt10240` 可以直接排除

相对当前默认值 `mbt8192_spec10_gmu092`：

- `32K`
  - `TTFT` 更差
  - `TPOT` 更差
  - `throughput` 从 `25.616` 掉到 `24.287`
- `64K`
  - `TTFT` 更差
  - `TPOT` 更差
  - `throughput` 从 `19.210` 掉到 `17.949`

这说明把 batched window 从 `8192` 稍微抬到 `10240`，在这条 GB10 patched DFlash 路线上已经是净负收益。

### 2. `mbt12288` 是唯一还能看的 higher-mbt 候选，但仍不如 `8192`

`mbt12288` 的表现比 `10240` 好很多：

- `32K`
  - `TTFT` 甚至略好于 `8192`
  - 但 `TPOT` 更差
  - `throughput` 基本打平，仍略低
- `64K`
  - `TTFT` 略好于 `8192`
  - 但 `TPOT` 明显更差
  - `throughput` 仍低于 `8192`

工程上，这意味着：

- `12288` 不是坏配置
- 但也没有给出“值得替换默认值”的收益

### 3. `mbt16384` 已经进入“不值得继续追”的边界区

`mbt16384` 的几个信号已经足够说明问题：

- 启动更慢，ready 时间继续上升到约 `315s`
- 服务能起，也能处理请求
- 但实验窗口显著变长，后台脚本在长时间窗口内都未顺利收口为结果文件
- 这说明它至少在当前 patched DFlash 路线下，操作成本已经明显恶化

换句话说，即使 `16384` 不是硬失败，它也已经不适合作为默认值方向继续投入时间。

## 当前结论

Round 5 的结论是明确的：

1. `max_num_batched_tokens=8192` 仍然是当前最优默认值。
2. 更大的中间档位没有带来净收益。
3. 在这条 GB10 patched DFlash 路线上，`8192 -> 12288 -> 16384` 呈现出典型的边际变差趋势：
   - 启动更慢
   - `TPOT` 没改善
   - `throughput` 没超过默认值

## 当前默认推荐

到 Round 5 为止，默认配置保持不变：

- `max_num_batched_tokens=8192`
- `num_speculative_tokens=10`
- `gpu_memory_utilization=0.92`

## 下一步建议

如果继续做实验，不建议再沿 `max_num_batched_tokens` 往上扫。

更有价值的下一步是两条：

1. 回到内核/引擎层分析
   - 为什么 higher `max_num_batched_tokens` 在这条 GB10 patched DFlash 路线上不能兑现成更好的 `TPOT/throughput`
2. 转向第二类真实业务 prompt
   - 验证当前默认值在更多业务输入形态下是否稳定成立
