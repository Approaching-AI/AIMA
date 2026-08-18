# Qwen3.6 DFlash Round 12 Findings (Scheduler Combo)

Date: 2026-04-23
Host: `gb10-4t` (`100.91.39.109`)
Model: `Qwen3.6-35B-A3B`
Path: `DFlash`

## Goal

Round 11 showed three scheduler-side candidates:

- `--max-num-seqs 1`
- `--no-async-scheduling`
- `--no-scheduler-reserve-full-isl`

This round tested whether they are additive under the current DFlash business workload:

- business prompt
- `32K/64K input + 1K output`
- `concurrency=1`
- `requests=1`
- `rounds=3`
- `warmup=2`
- `block_size=1280`
- `num_speculative_tokens=10`
- `max_num_batched_tokens=8192`
- `gpu_memory_utilization=0.92`
- `enforce_eager=false`
- `VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=1`

Raw result file:

- [qwen3.6-gb10-dflash-round12-combo-business-results.json](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round12-combo-business-results.json)

Runner:

- [qwen3.6-gb10-dflash-round12-combo-business.py](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round12-combo-business.py)

## Results

### Absolute Metrics

| Case | Ready s | 32K TTFT ms | 32K TPOT ms | 32K TPS | 64K TTFT ms | 64K TPOT ms | 64K TPS |
|---|---:|---:|---:|---:|---:|---:|---:|
| `max_num_seqs=1` | 370.1 | 6314.525 | 31.155 | 26.917 | 14473.336 | 38.253 | 19.074 |
| `max_num_seqs=1 + no_reserve` | 390.1 | 6115.513 | 31.399 | 26.681 | 14066.497 | 39.352 | 18.793 |
| `max_num_seqs=1 + no_async` | 390.1 | 5954.709 | 30.644 | 27.324 | 13806.607 | 37.139 | 19.746 |
| `max_num_seqs=1 + no_reserve + no_async` | 395.1 | 6618.746 | 31.433 | 26.393 | 15318.326 | 35.942 | 19.514 |

### Relative Delta vs `max_num_seqs=1`

| Case | 32K TTFT | 32K TPOT | 32K TPS | 64K TTFT | 64K TPOT | 64K TPS |
|---|---:|---:|---:|---:|---:|---:|
| `max_num_seqs=1 + no_reserve` | -3.2% | +0.8% | -0.9% | -2.8% | +2.9% | -1.5% |
| `max_num_seqs=1 + no_async` | -5.7% | -1.6% | +1.5% | -4.6% | -2.9% | +3.5% |
| `max_num_seqs=1 + no_reserve + no_async` | +4.8% | +0.9% | -1.9% | +5.8% | -6.0% | +2.3% |

### Acceptance Signals

| Case | 32K draft acceptance | 32K mean acceptance length | 64K draft acceptance | 64K mean acceptance length |
|---|---:|---:|---:|---:|
| `max_num_seqs=1` | 23.0% | 2.30 | 20.9% | 2.09 |
| `max_num_seqs=1 + no_reserve` | 22.6% | 2.26 | 21.2% | 2.12 |
| `max_num_seqs=1 + no_async` | 23.1% | 2.31 | 22.2% | 2.22 |
| `max_num_seqs=1 + no_reserve + no_async` | 22.2% | 2.22 | 22.2% | 2.22 |

## Interpretation

### Keep `max_num_seqs=1`

Round 11 already showed this as the strongest scheduler-side lever for the target shape. Round 12 used it as the base for all combinations.

It fits the workload because the benchmark is single-stream long-context generation. The default larger sequence capacity adds CUDA graph and scheduler overhead that this workload does not use.

### Add `no_async_scheduling` for single-stream long-context

`max_num_seqs=1 + no_async` is the best practical combo in this round:

- `32K TPS +1.5%` vs `max_num_seqs=1`
- `64K TPS +3.5%` vs `max_num_seqs=1`
- TTFT improves at both 32K and 64K
- TPOT improves at both 32K and 64K

This is now the retained recommendation for the single-stream `32K/64K input + 1K output` DFlash path.

### Do not retain `no_scheduler_reserve_full_isl`

`no_reserve` does not stack cleanly:

- `max_num_seqs=1 + no_reserve` lowers TTFT but worsens TPOT and end-to-end TPS.
- `max_num_seqs=1 + no_reserve + no_async` has the best 64K TPOT, but TTFT regresses enough that end-to-end TPS is worse than `max_num_seqs=1 + no_async`.

For practical single-request latency and total throughput, do not add `--no-scheduler-reserve-full-isl`.

## Retained Settings

Recommended for current DFlash business path:

- `--max-num-seqs 1`
- `--no-async-scheduling`
- keep scheduler reserve default enabled
- keep `--block-size 1280`
- keep `num_speculative_tokens=10`
- keep `--max-num-batched-tokens 8192`
- keep `enforce_eager=false`
- keep `VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=1`

Closed or low-priority for this path:

- `--no-scheduler-reserve-full-isl`
- concurrent partial prefill
- `block_size=1536`
- `block_size=2048`
- `num_speculative_tokens=11`
- `num_speculative_tokens=12`

## Cleanup

Remote benchmark process completed successfully.

- no residual `qwen36-dflash-exp` container
- GPU returned to idle
