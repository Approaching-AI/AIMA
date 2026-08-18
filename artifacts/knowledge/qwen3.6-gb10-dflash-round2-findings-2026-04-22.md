# Qwen3.6 GB10 DFlash 调优 Round 2 结果

> 本文结论已被 `qwen3.6-gb10-dflash-round3-findings-2026-04-23.md` 覆盖。  
> Round 2 主要完成了稳定带的初步收敛；当前默认推荐请以后续 Round 3 为准。

时间：`2026-04-22`

机器：`gb10-4t (100.91.39.109)`

模型：`/home/qujing/aima-codex-qwen36/models/Qwen3.6-35B-A3B`

Draft model：`/home/qujing/aima-codex-qwen36/models/Qwen3.6-35B-A3B-DFlash`

镜像：`qujing/vllm-gb10-dflash-fa2:latest`

## 测试口径

- 工具：`aima benchmark run`
- `concurrency=1`
- `requests=1`
- `rounds=3`
- `warmup=2`
- `max_tokens=1024`
- 输入档位：`32768`、`65536`
- 固定运行约束：
  - `--block-size 1280`
  - `--attention-backend FLASH_ATTN`
  - `VLLM_KV_CACHE_LAYOUT=NHD`
  - `VLLM_SKIP_GDN_PREFILL_WARMUP=1`
  - `FLA_GDN_FIX_BT=1`

## 稳定配置结果

### 32K input / 1K output

| Config | TTFT ms | TPOT ms | Throughput tok/s | Mean acceptance length | Draft acceptance rate |
|---|---:|---:|---:|---:|---:|
| `mbt8192_spec8_gmu092` | 6317.836 | 33.969 | 25.140 | 2.068 | 25.85% |
| `mbt8192_spec12_gmu092` | 6324.543 | 31.904 | 25.990 | 2.341 | 19.51% |
| `mbt8192_spec15_gmu092_rerun` | 6435.538 | 32.137 | 25.731 | 2.595 | 17.30% |

### 64K input / 1K output

| Config | TTFT ms | TPOT ms | Throughput tok/s | Mean acceptance length | Draft acceptance rate |
|---|---:|---:|---:|---:|---:|
| `mbt8192_spec8_gmu092` | 14398.992 | 42.083 | 17.820 | 2.073 | 25.91% |
| `mbt8192_spec12_gmu092` | 14101.654 | 41.326 | 18.700 | 2.291 | 19.09% |
| `mbt8192_spec15_gmu092_rerun` | 14460.669 | 43.542 | 17.313 | 2.220 | 14.80% |

## 失败或不推荐配置

### `mbt32768_spec15_gmu092`

- `READY` 时间约 `385.4s`
- 进入 benchmark 后长时间不收口，`32K + 1K` 未在可接受窗口内完成
- 当前 GB10 patched stack 下，不像可用默认值

### `mbt32768_spec8_gmu092`

- 超过 `13min` 仍未 ready
- `/v1/models` 返回 `000`
- 日志明确显示：
  - `Chunked prefill is enabled with max_num_batched_tokens=32768`
  - `Available KV cache memory: 38.6 GiB`
- 相比 `8192` 路线，这条线的启动和 warmup 成本明显更差

### `mbt8192_spec15_gmu094`

- `READY` 时间约 `421.3s`
- 首个 benchmark 阶段触发 Triton FLA OOM
- 关键错误：
  - `RuntimeError: Triton Error [CUDA]: out of memory`
  - 后续 `EngineDeadError`
  - API 层返回 `500`

## 当前结论

1. 在 `32K/64K input + 1K output + concurrency=1` 这条目标负载下，当前最优稳定点是 `mbt8192_spec12_gmu092`。
2. `num_speculative_tokens=12` 比 `8` 和 `15` 更平衡：
   - `32K` 下吞吐最高
   - `64K` 下吞吐也最高
   - `TTFT` 在三条稳定线上也是最好的一档
3. `max_num_batched_tokens=32768` 在这台 GB10 的当前 patched DFlash 路线上不是净收益，主要问题是启动/warmup 成本和稳定性。
4. `gpu_memory_utilization=0.94` 已经越过稳定边界，至少在当前 `FLASH_ATTN + FLA/Triton` 路径上会直接打出 OOM。

## 建议的下一轮

优先继续细扫 `num_speculative_tokens`，不要再碰 `32768` 或 `gmu=0.94`：

- `mbt8192_spec10_gmu092`
- `mbt8192_spec11_gmu092`
- `mbt8192_spec13_gmu092`

如果只允许保留一个当前默认值，先用：

- `max_num_batched_tokens=8192`
- `num_speculative_tokens=12`
- `gpu_memory_utilization=0.92`
