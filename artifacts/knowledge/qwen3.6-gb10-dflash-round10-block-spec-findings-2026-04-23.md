# Qwen3.6 DFlash Round 10 Findings (Block Size + Spec Resweep)

Date: 2026-04-23
Host: `gb10-4t` (`100.91.39.109`)
Model: `Qwen3.6-35B-A3B`
Path: `DFlash`

## Goal

Keep the current useful runtime settings and continue testing non-GMU knobs:

- `enforce_eager=false`
- `VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=1`
- `gpu_memory_utilization=0.92`
- `max_num_batched_tokens=8192`
- business prompt
- `32K/64K input + 1K output + concurrency=1`

This round focused on:

1. `block_size=1536`
2. `block_size=2048`
3. `num_speculative_tokens=11`
4. `num_speculative_tokens=12`

Reference baseline remained:

- `block_size=1280`
- `num_speculative_tokens=10`
- `non-eager`
- `cgprof on`
- `gmu=0.92`

Reference result file:

- [qwen3.6-gb10-dflash-round8-cgprof-business-results.json](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round8-cgprof-business-results.json)

This round's raw result file:

- [qwen3.6-gb10-dflash-round10-block-spec-business-results.json](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round10-block-spec-business-results.json)

Runner script:

- [qwen3.6-gb10-dflash-round10-block-spec-business.py](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round10-block-spec-business.py)

## Results

### Compared to Current Best Business Baseline

| Case | Ready s | 32K TTFT ms | 32K TPOT ms | 32K TPS | 64K TTFT ms | 64K TPOT ms | 64K TPS |
|---|---:|---:|---:|---:|---:|---:|---:|
| Baseline `block1280/spec10` | 385.2 | 6173.510 | 30.780 | 27.159 | 14294.397 | 37.614 | 19.690 |
| `block1536/spec10` | 395.2 | 6208.305 | 31.578 | 26.439 | 14305.705 | 40.952 | 18.686 |
| `block2048/spec10` | 405.2 | 6287.157 | 31.141 | 26.417 | 14456.648 | 38.537 | 18.931 |
| `block1280/spec11` | 405.1 | 6395.258 | 32.822 | 25.881 | 14508.115 | 37.867 | 19.042 |
| `block1280/spec12` | 400.1 | 6534.802 | 31.461 | 26.175 | 14729.064 | 38.183 | 19.121 |

### Relative Delta vs Baseline

| Case | 32K TTFT | 32K TPOT | 32K TPS | 64K TTFT | 64K TPOT | 64K TPS |
|---|---:|---:|---:|---:|---:|---:|
| `block1536/spec10` | +0.6% | +2.6% | -2.6% | +0.1% | +8.9% | -5.1% |
| `block2048/spec10` | +1.8% | +1.2% | -2.7% | +1.1% | +2.5% | -3.9% |
| `block1280/spec11` | +3.6% | +6.6% | -4.7% | +1.5% | +0.7% | -3.3% |
| `block1280/spec12` | +5.9% | +2.2% | -3.6% | +3.0% | +1.5% | -2.9% |

## Acceptance Signals

| Case | 32K mean acceptance length | 32K draft acceptance rate | 64K mean acceptance length | 64K draft acceptance rate |
|---|---:|---:|---:|---:|
| Baseline `block1280/spec10` | 2.242 | 0.224 | 2.226 | 0.223 |
| `block1536/spec10` | 2.287 | 0.229 | 2.301 | 0.230 |
| `block2048/spec10` | 2.253 | 0.225 | 2.218 | 0.222 |
| `block1280/spec11` | 2.254 | 0.205 | 2.258 | 0.205 |
| `block1280/spec12` | 2.360 | 0.197 | 2.313 | 0.193 |

## Interpretation

### 1. Block size is effectively closed for now

Both `1536` and `2048` are net regressions against the current baseline.

- `block1536` is clearly worse at both `32K` and `64K`
- `block2048` is slightly less bad than `1536`, but still loses across TTFT/TPOT/TPS

Important detail:

- `block1536` did **not** lose because speculative acceptance got worse
- acceptance was slightly better than baseline
- but both prefill and decode segments became slower

That points to a runtime/kernel efficiency issue on this patched GB10 path, not a drafter quality issue.

### 2. Re-testing `spec=11/12` on the new best runtime did not beat `spec=10`

Earlier rounds had already narrowed the old runtime face to `spec=10`.
This round re-checked `11` and `12` on the newer best business baseline (`non-eager + cgprof`).

Result:

- neither `spec11` nor `spec12` beats `spec10`
- `spec12` gets slightly closer at `64K`, but still remains behind
- both variants worsen TTFT noticeably at `32K`

Acceptance detail:

- `spec12` raises mean acceptance length
- but lowers draft acceptance rate because it drafts more tokens
- that extra drafting does not translate into higher end-to-end throughput

### 3. Current best business baseline remains unchanged

After finishing this round, the best retained configuration is still:

- `enforce_eager=false`
- `VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=1`
- `gpu_memory_utilization=0.92`
- `max_num_batched_tokens=8192`
- `block_size=1280`
- `num_speculative_tokens=10`

## Conclusion

Round 10 did not find a better replacement for the current best business baseline.

The tested knobs can now be treated as low-priority or closed:

- `block_size=1536`
- `block_size=2048`
- `num_speculative_tokens=11`
- `num_speculative_tokens=12`

The more promising next directions are no longer `block/spec` tweaks. The remaining likely levers are:

1. scheduler / partial-prefill parameters
2. deeper runtime/kernel path differences on GB10
3. fused-MoE config gap (`Using default MoE config. Performance might be sub-optimal!`)

## Cleanup

Remote benchmark process completed successfully.

- no residual `qwen36-dflash-exp` container
- GPU returned to idle
