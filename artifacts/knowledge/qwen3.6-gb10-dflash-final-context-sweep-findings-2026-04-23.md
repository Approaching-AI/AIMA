# Qwen3.6-35B-A3B GB10 DFlash Final Context Sweep

## 结论

这轮使用当前保留的 DFlash 配置完成了 1K/2K/4K/8K/16K/32K/64K/128K/256K 全量单路 benchmark，输出统一 512 token，9 个 bucket 全部成功。

核心结论：

- 当前 DFlash 配置是可用的，能稳定跑到 target 262144（actual prompt 233039）并完成 512-token 输出。
- 32K/64K 是最贴近实用的强项：相对 baseline，端到端 TPS 分别约 +43% / +44%；相对 MTP，分别约 +29% / +67%。
- 128K/256K 的端到端仍明显优于 baseline，主要来自 TTFT/prefill 改善；但 decode TPS 已经开始低于 baseline，说明超长上下文下 DFlash 的瓶颈转到 decode/acceptance/attention-cache 路径。
- 与旧 DFlash 512-output 表相比，当前保留配置 TTFT 全线更低，但 128K/256K decode 更慢；这轮应作为最新保留配置的最终参照。

## 测试配置

Host: `gb10-4t` (`100.91.39.109`)

Image: `qujing/vllm-gb10-dflash-fa2:latest`

Benchmark: `AIMA benchmark.run`

Shape:

- `concurrency=1`
- `requests=1`
- `rounds=3`
- `warmup=2`
- `output_tokens=512`
- `min_output_ratio=1`

关键启动参数：

```text
--gpu-memory-utilization 0.92
--max-model-len 262144
--block-size 1280
--dtype bfloat16
--trust-remote-code
--language-model-only
--reasoning-parser qwen3
--attention-backend FLASH_ATTN
--mm-encoder-attn-backend TORCH_SDPA
--skip-mm-profiling
--max-num-batched-tokens 8192
--max-num-seqs 1
--no-async-scheduling
--speculative-config {"method":"dflash","model":"/models/Qwen3.6-35B-A3B-DFlash","num_speculative_tokens":10}
```

运行时环境：

```text
VLLM_KV_CACHE_LAYOUT=NHD
VLLM_SKIP_GDN_PREFILL_WARMUP=1
FLA_GDN_FIX_BT=1
VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=1
```

说明：

- Runtime 日志确认 `Chunked prefill is enabled with max_num_batched_tokens=8192`。
- 本轮没有启用 MoE tuned config；此前 `safe-v1`、`targeted_autotune_v1`、round16 large-bucket variants 均未成为更强默认。
- Ready time: `376.4s`。
- Raw JSON: `artifacts/knowledge/qwen3.6-gb10-dflash-final-context-sweep-results.json`。

## Final DFlash Results

| target input | actual prompt | TTFT (s) | TPOT (ms) | decode tok/s | end-to-end tok/s | acceptance | mean accept len |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 1K | 934 | 0.353 | 23.976 | 41.7 | 39.785 | 24.4% | 2.44 |
| 2K | 1844 | 0.494 | 24.353 | 41.1 | 38.856 | 23.5% | 2.35 |
| 4K | 3664 | 0.771 | 24.821 | 40.3 | 38.324 | 25.4% | 2.54 |
| 8K | 7304 | 1.317 | 22.112 | 45.2 | 40.543 | 27.4% | 2.74 |
| 16K | 14586 | 2.744 | 27.768 | 36.0 | 30.555 | 24.3% | 2.43 |
| 32K | 29149 | 5.932 | 29.363 | 34.1 | 24.679 | 26.3% | 2.63 |
| 64K | 58277 | 13.749 | 38.113 | 26.2 | 15.220 | 23.3% | 2.33 |
| 128K | 116532 | 35.169 | 50.205 | 19.9 | 8.380 | 23.4% | 2.34 |
| 256K | 233039 | 109.196 | 74.197 | 13.5 | 3.482 | 25.0% | 2.50 |

## 对照 Baseline

Baseline 数据来自 `qwen3.6-35b-a3b-blackwell-vllm-bf16` 的现有 AIMA knowledge。对照使用最接近的 actual prompt bucket，并按 512 output 估算 baseline end-to-end TPS。

| DFlash actual | baseline actual | TTFT delta | decode delta | end-to-end TPS delta |
|---:|---:|---:|---:|---:|
| 29149 | 32791 | -42.8% | +28.0% | +42.8% |
| 58277 | 65558 | -50.6% | +6.2% | +44.3% |
| 116532 | 128024 | -57.3% | -7.4% | +73.7% |
| 233039 | 224024 | -50.1% | -25.9% | +68.0% |

分析：

- 32K/64K 的 DFlash 提升比较健康，既降低 TTFT，也提升或维持 decode。
- 128K/256K 的端到端提升主要来自 TTFT 明显下降；decode 本身已经低于 baseline。
- 如果业务关注首 token 和总耗时，DFlash 在超长上下文仍有价值；如果只看持续 decode TPS，128K 以后需要继续优化。

## 对照 MTP

MTP 数据来自 `qwen3.6-35b-a3b-blackwell-vllm-mtp` 的现有 AIMA knowledge。MTP 没有稳定 256K 记录，所以只对齐到 128K。

| DFlash actual | MTP actual | TTFT delta | decode delta | end-to-end TPS delta |
|---:|---:|---:|---:|---:|
| 29149 | 29150 | -31.7% | +20.3% | +29.1% |
| 58277 | 58277 | -41.9% | +67.1% | +67.3% |
| 116532 | 116532 | -51.2% | +28.5% | +71.9% |

分析：

- 当前 DFlash 在 32K/64K/128K 均明显强于 native MTP。
- MTP 的长上下文 decode 在 64K 以后掉得更快；DFlash 的主要优势不是 acceptance 极高，而是 FA2 + DFlash 路径让长上下文整体耗时更低。

## 对照旧 DFlash 表

旧 DFlash 512-output 表也来自同一 knowledge 文件。当前 sweep 使用最终保留配置，并应覆盖旧表作为最新参照。

| actual prompt | TTFT delta vs old DFlash | decode delta vs old DFlash | end-to-end TPS delta |
|---:|---:|---:|---:|
| 934 | -31.5% | +11.2% | +10.1% |
| 1844 | -25.3% | -3.4% | -3.6% |
| 3664 | -26.5% | +3.8% | +6.6% |
| 7304 | -32.3% | +24.2% | +26.8% |
| 14586 | -21.0% | -0.2% | +5.4% |
| 29149 | -14.8% | +8.5% | +12.1% |
| 58277 | -11.4% | -0.6% | +3.8% |
| 116532 | -7.4% | -17.0% | -2.9% |
| 233039 | +1.8% | -10.1% | -3.9% |

分析：

- 当前配置的短中上下文 TTFT 更好，32K/64K 端到端也更好。
- 128K/256K 相比旧 DFlash 出现 decode 退化，需要单独追踪；可疑变量包括 FA2 paged-KV 长序列行为、chunked prefill 后的 decode cache locality、DFlash draft acceptance 在长上下文下的波动，以及 benchmark prompt 内容导致的接受率差异。
- 这轮不是 500%-600% 加速场景；按单路实用长上下文来看，合理结论是 32K/64K 相对 baseline 约 43%-44% 端到端提升，128K/256K 端到端提升更大但主要来自 TTFT。
