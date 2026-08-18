# Qwen3.6-35B-A3B GB10-4T DFlash concurrency sweep

Date: 2026-04-29
Host: `gb10-4t` (`qujing@100.91.39.109`)
AIMA commit tested: `5a89755` (`develop`)
AIMA binary: `/home/qujing/aima-current-5a89755`
Benchmark path: AIMA `benchmark run`

## Configuration under test

Retained catalog variant:
`qwen3.6-35b-a3b-blackwell-vllm-dflash`

Key settings from `catalog/models/qwen3.6-35b-a3b.yaml`:

- Base model: `/models/Qwen3.6-35B-A3B`
- DFlash draft model: `/models/Qwen3.6-35B-A3B-DFlash`
- Image: `qujing/vllm-gb10-dflash-fa2:latest`
- `max_model_len=262144`
- `gpu_memory_utilization=0.92`
- `block_size=1280`
- `max_num_batched_tokens=8192`
- `max_num_seqs=1`
- `num_speculative_tokens=10`
- `VLLM_KV_CACHE_LAYOUT=NHD`
- `VLLM_SKIP_GDN_PREFILL_WARMUP=1`
- `FLA_GDN_FIX_BT=1`
- `VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=1`

vLLM reported `GPU KV cache size: 360,960 tokens` and
`Maximum concurrency for 262,144 tokens per request: 3.00x`.

## Method

Each benchmark cell used:

- client concurrency: `4`
- requests: `4`
- rounds: `1`
- warmup requests: `1`
- `min_output_ratio=1`
- AIMA no-save mode

The target matrix was:

- input tokens: `1024`, `32768`, `65536`, `131072`
- output tokens: `128`, `512`

The reported `decode_tps` is derived from p50 TPOT as:
`decode_tps = 1000 / tpot_p50_ms`.

## Retained best config results

Important caveat: the retained best config is a single-sequence service
configuration (`max_num_seqs=1`). Under 4-client load, vLLM logs showed the
expected queueing pattern such as `Running: 1 reqs, Waiting: 3 reqs`.

Cold start to ready: `388.2s`.

| target in/out | actual in/out | TTFT p50 | TTFT p95 | TPOT p50 | decode tps | e2e output tps | QPS | draft accept |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 1024/128 | 934/128 | 4.326s | 8.017s | 18.33ms | 54.56 | 47.17 | 0.369 | 0.345 |
| 1024/512 | 933/512 | 19.348s | 36.254s | 23.89ms | 41.86 | 40.83 | 0.080 | 0.238 |
| 32768/128 | 29150/128 | 18.380s | 30.118s | 21.93ms | 45.60 | 14.91 | 0.117 | 0.408 |
| 32768/512 | 29150/512 | 37.593s | 65.278s | 28.27ms | 35.37 | 24.95 | 0.049 | 0.264 |
| 65536/128 | 58276/128 | 39.122s | 62.465s | 26.03ms | 38.41 | 7.54 | 0.059 | 0.419 |
| 65536/512 | 58277/512 | 63.435s | 108.997s | 37.47ms | 26.69 | 15.45 | 0.030 | 0.246 |
| 131072/128 | 116532/128 | 95.190s | 149.046s | 37.21ms | 26.87 | 3.19 | 0.025 | 0.389 |
| 131072/512 | 116532/512 | 130.019s | 213.730s | 53.79ms | 18.59 | 8.17 | 0.016 | 0.232 |

## Supplemental max_num_seqs=4 control

This is not the retained best catalog configuration. It changes only
`max_num_seqs` from `1` to `4` to show what true four-way scheduling does to
TTFT and per-request decode behavior. The 512-output cells were tested for
1K, 32K, and 64K input.

vLLM reported `GPU KV cache size: 368,640 tokens` and
`Maximum concurrency for 262,144 tokens per request: 3.07x`.

Cold start to ready: `397.6s`.

| target in/out | actual in/out | TTFT p50 | TTFT p95 | TPOT p50 | decode tps | e2e output tps | QPS | draft accept |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 1024/512 | 933/512 | 0.639s | 1.480s | 42.08ms | 23.76 | 88.98 | 0.174 | 0.257 |
| 32768/512 | 29149/512 | 15.859s | 23.600s | 70.77ms | 14.13 | 39.33 | 0.077 | 0.252 |
| 65536/512 | 58277/512 | 35.161s | 53.846s | 104.66ms | 9.56 | 22.89 | 0.045 | 0.244 |

## Readout

The retained best DFlash config is optimized for long-context single-stream
throughput and stability, not low-latency four-way serving. At client
concurrency 4, TTFT is dominated by queueing because `max_num_seqs=1`.

For 512-token outputs under the retained config, p50 decode throughput falls
with context length:

- 1K input: `41.86 tok/s`
- 32K input: `35.37 tok/s`
- 64K input: `26.69 tok/s`
- 128K input: `18.59 tok/s`

The `max_num_seqs=4` control removes most short-context queueing and improves
aggregate output throughput, but per-request TPOT degrades sharply. At 1K/512,
TTFT improves from `19.348s` to `0.639s`, while p50 TPOT worsens from
`23.89ms` to `42.08ms`.

For latency-sensitive four-way serving, the retained config should not be
treated as a concurrency-optimized default. A separate serving profile with
`max_num_seqs=4` needs its own acceptance target because it trades much lower
TTFT for slower per-request decode.

## Artifacts

- Retained config result JSON:
  `artifacts/knowledge/qwen3.6-gb10-dflash-concurrency-sweep-retained-maxseq1-results.json`
- `max_num_seqs=4` control result JSON:
  `artifacts/knowledge/qwen3.6-gb10-dflash-concurrency-sweep-maxseq4-results.json`
- Runner:
  `artifacts/knowledge/qwen3.6-gb10-dflash-concurrency-sweep.py`

