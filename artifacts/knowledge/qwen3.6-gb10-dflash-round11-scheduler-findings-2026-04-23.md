# Qwen3.6 DFlash Round 11 Findings (Scheduler / Prefill Levers)

Date: 2026-04-23
Host: `gb10-4t` (`100.91.39.109`)
Model: `Qwen3.6-35B-A3B`
Path: `DFlash`

## Goal

Keep the useful settings from previous rounds and test non-GMU scheduler / prefill levers.

Fixed settings:

- `enforce_eager=false`
- `VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=1`
- `gpu_memory_utilization=0.92`
- `max_num_batched_tokens=8192`
- `block_size=1280`
- `num_speculative_tokens=10`
- business prompt
- `32K/64K input + 1K output + concurrency=1`

Tested levers:

- `--max-num-seqs 1`
- `--max-num-partial-prefills 2 --max-long-partial-prefills 1`
- `--no-scheduler-reserve-full-isl`
- `--no-async-scheduling`

Raw result files:

- [qwen3.6-gb10-dflash-round11-scheduler-business-results.json](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round11-scheduler-business-results.json)
- [qwen3.6-gb10-dflash-round11b-scheduler-business-results.json](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round11b-scheduler-business-results.json)

Runner:

- [qwen3.6-gb10-dflash-round11-scheduler-business.py](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round11-scheduler-business.py)

## Results

### Same-Round Baseline

| Case | Ready s | 32K TTFT ms | 32K TPOT ms | 32K TPS | 64K TTFT ms | 64K TPOT ms | 64K TPS |
|---|---:|---:|---:|---:|---:|---:|---:|
| `business_best_rerun` | 400.1 | 6277.032 | 31.552 | 25.910 | 14484.705 | 40.075 | 18.308 |
| `max_num_seqs=1` | 395.1 | 6120.938 | 30.893 | 27.236 | 13989.555 | 37.099 | 19.527 |
| `no_scheduler_reserve_full_isl` | 400.1 | 6228.575 | 31.932 | 26.264 | 14339.891 | 38.838 | 18.891 |
| `no_async_scheduling` | 385.1 | 6125.003 | 30.908 | 26.691 | 14132.469 | 39.098 | 19.299 |

### Relative Delta vs Same-Round Baseline

| Case | 32K TTFT | 32K TPOT | 32K TPS | 64K TTFT | 64K TPOT | 64K TPS |
|---|---:|---:|---:|---:|---:|---:|
| `max_num_seqs=1` | -2.5% | -2.1% | +5.1% | -3.4% | -7.4% | +6.7% |
| `no_scheduler_reserve_full_isl` | -0.8% | +1.2% | +1.4% | -1.0% | -3.1% | +3.2% |
| `no_async_scheduling` | -2.4% | -2.0% | +3.0% | -2.4% | -2.4% | +5.4% |

## Unsupported Case

`--max-num-partial-prefills 2 --max-long-partial-prefills 1` failed during engine config creation:

```text
NotImplementedError: Concurrent Partial Prefill is not supported. We recommend to remove Concurrent Partial Prefill from your config.
```

So concurrent partial prefill is closed for this patched DFlash path unless the upstream support gap is fixed.

## Interpretation

### `max_num_seqs=1` is the strongest new useful lever

This is the first scheduler-side setting that produced a clear positive result under the current best runtime face.

Observed runtime changes:

- CUDA graph capture sizes dropped from `39` to `1`
- `num_gpu_blocks_override` dropped from `512` to `16`
- available KV cache memory increased
- measured `32K` and `64K` both improved

This makes sense for the target workload:

- concurrency is explicitly `1`
- the service is tuned for single-stream long-context generation
- the default large `max_num_seqs` mostly adds runtime/cudagraph overhead that this workload does not use

### `no_async_scheduling` is also positive, but smaller

Disabling async scheduling improved this single-stream workload by:

- `32K TPS +3.0%`
- `64K TPS +5.4%`

This is counter to the general vLLM default guidance, but plausible here because the workload is single-stream and long-context. Async scheduling may not have enough concurrency to hide, while it still adds scheduling overhead or changes pacing.

### `no_scheduler_reserve_full_isl` is a smaller positive lever

This setting improved:

- `32K TPS +1.4%`
- `64K TPS +3.2%`

The gain is smaller than `max_num_seqs=1` and `no_async_scheduling`, but it is directionally positive.

### Partial-prefill is not a current tuning option

Concurrent partial prefill is unavailable for this configuration. It should not be included in the next parameter sweeps unless the engine implementation changes.

## Retained Candidate Settings

The new useful candidates to carry forward are:

- `--max-num-seqs 1`
- `--no-async-scheduling`
- `--no-scheduler-reserve-full-isl`

The next round should test whether these are additive:

1. `max_num_seqs=1`
2. `max_num_seqs=1 + no_scheduler_reserve_full_isl`
3. `max_num_seqs=1 + no_async_scheduling`
4. `max_num_seqs=1 + no_scheduler_reserve_full_isl + no_async_scheduling`

## Cleanup

Remote benchmark processes completed or were stopped cleanly.

- no residual `qwen36-dflash-exp` container
- GPU returned to idle
