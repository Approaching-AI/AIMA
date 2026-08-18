# Qwen3.6-35B-A3B on GB10 DFlash: Round16 MoE End-to-End Guided Search

Date: 2026-04-23
Host: gb10-4t / 100.91.39.109
Image: `qujing/vllm-gb10-dflash-fa2:latest`
Benchmark: AIMA `benchmark.run`

## Goal

Test whether a narrower end-to-end guided MoE config search can improve the practical 64K input / 1K output DFlash path.

The hypothesis was that round15 `autotune-v1` regressed 32K because it changed small decode-like buckets, while long-context gains might come from larger prefill buckets. Round16 therefore kept small buckets at vLLM defaults and only replaced selected large buckets with round15 autotuned values.

## Method

All candidates used the same DFlash runtime:

- `VLLM_KV_CACHE_LAYOUT=NHD`
- `VLLM_SKIP_GDN_PREFILL_WARMUP=1`
- `FLA_GDN_FIX_BT=1`
- `VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=1`
- `--gpu-memory-utilization 0.92`
- `--max-model-len 262144`
- `--block-size 1280`
- `--attention-backend FLASH_ATTN`
- `--mm-encoder-attn-backend TORCH_SDPA`
- `--skip-mm-profiling`
- `--max-num-batched-tokens 8192`
- `--max-num-seqs 1`
- `--no-async-scheduling`
- `--speculative-config {"method":"dflash","model":"/models/Qwen3.6-35B-A3B-DFlash","num_speculative_tokens":10}`

Screening benchmark:

- Input target: 65536
- Actual prompt tokens: 58314
- Output tokens: 1024
- Concurrency: 1
- Requests: 1
- Rounds: 2
- Warmup: 1

## Candidates

All candidates include a full `E=256,N=512,device_name=NVIDIA_GB10.json` file. Buckets not listed below use vLLM default config.

- `moe_e2e_big512plus`: override `512,1024,2048,4096,8192`.
- `moe_e2e_big1024plus`: override `1024,2048,4096,8192`.
- `moe_e2e_big4096plus`: override `4096,8192`.

## Results

Baseline references:

- Round12 retained 64K/1K: TTFT 13.807s, TPOT 37.139ms, TPS 19.746.
- Round15 autotune-v1 64K/1K: TTFT 13.712s, TPOT 37.810ms, TPS 19.815.

Round16 screening results:

| Candidate | TTFT | TPOT | TPS | Acceptance |
| --- | ---: | ---: | ---: | ---: |
| `moe_e2e_big512plus` | 13.839s | 40.382ms | 18.567 | 20.8% |
| `moe_e2e_big1024plus` | 13.830s | 39.067ms | 19.035 | 22.5% |
| `moe_e2e_big4096plus` | 14.071s | 38.211ms | 19.262 | 21.9% |

Best round16 candidate vs round12 retained:

- `moe_e2e_big4096plus`
- TTFT: +1.91%
- TPOT: +2.89% slower
- TPS: -2.45%

Best round16 candidate vs round15 autotune-v1:

- TTFT: +2.62%
- TPOT: +1.06% slower
- TPS: -2.79%

## Conclusion

Round16 did not find a useful MoE config improvement. The hypothesis that only large-bucket replacement would preserve decode behavior while improving long-context prefill was not supported by the 64K/1K AIMA benchmark.

Current MoE config status:

- Mechanism is solved.
- H100 seed is invalid on GB10.
- Safe-v1 is stable but slower.
- Autotune-v1 is the best MoE-config candidate so far, but still only roughly matches the retained default and regresses 32K.
- Large-bucket-only guided variants are worse than both round12 retained and round15 autotune-v1.

Recommendation: do not continue broad MoE config search as the main DFlash acceleration path. Keep round12 retained DFlash config as the practical default. If MoE config is revisited, it should be a very small follow-up only after a better objective signal is available, for example per-layer MoE timing or a request-shape-specific trace showing MoE as the bottleneck.
