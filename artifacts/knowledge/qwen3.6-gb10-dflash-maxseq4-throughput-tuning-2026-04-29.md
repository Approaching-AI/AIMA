# Qwen3.6-35B-A3B GB10-4T DFlash max_num_seqs=4 throughput tuning

Date: 2026-04-29

Scope:
- Device: `gb10-4t` / `aitopatom-66c4`
- Model: `Qwen3.6-35B-A3B`
- Draft model: `Qwen3.6-35B-A3B-DFlash`
- Engine image: `qujing/vllm-gb10-dflash-fa2:latest`
- AIMA binary: `/home/qujing/aima-current-5a89755`
- Fixed serving cap: `max_num_seqs=4`
- Fixed client load: concurrency 4, requests 4, rounds 1, warmup 1
- Matrix: input 8K / 32K / 64K, output 512

## Executive conclusion

With service-side `max_num_seqs=4`, the current best-throughput choices are context-length dependent:

| Input | Best e2e throughput config | E2E output TPS | Decode TPS | TTFT p50/p95 | Decode gate |
|---:|---|---:|---:|---:|---|
| 8K | `max_model_len=131072`, `mbt=8192`, `spec=10`, async off | 73.30 | 22.54 | 4.335s / 6.062s | pass |
| 32K | `max_model_len=262144`, `mbt=8192`, `spec=6`, async off | 39.35 | 14.64 | 15.899s / 23.577s | fail |
| 64K | `max_model_len=262144`, `mbt=8192`, `spec=10`, async off | 22.61 | 9.56 | 35.997s / 55.260s | fail |

If `decode >= 20 tok/s` is a hard usability gate, only the 8K case is acceptable under `max_num_seqs=4`. For 32K and 64K, none of the tested engine/DFlash knobs got close to 20 tok/s decode; the best 32K decode is 14.64 tok/s, and the best 64K decode is 9.56 tok/s.

If the objective is aggregate e2e throughput at fixed 4-concurrency, the gains are modest:
- 8K: 73.30 vs 70.63 tok/s, +3.8% from lowering `max_model_len` to 131K.
- 32K: 39.35 vs 38.67 tok/s, +1.8% from lowering DFlash speculative tokens to 6.
- 64K: no tested knob beats the baseline `spec=10`, `mbt=8192`, `max_model_len=262K`.

Recommended retained profile for a single mixed 8K/32K/64K lane:

```text
max_num_seqs=4
max_num_batched_tokens=8192
max_model_len=262144
num_speculative_tokens=10
async_scheduling=false
```

Recommended split-lane option:
- Short-context 8K lane: `max_model_len=131072`, `spec=10`, `mbt=8192`, async off.
- 32K-only throughput lane: `spec=6`, `max_model_len=262144`, `mbt=8192`, async off, but mark decode below usability gate.
- 64K lane: keep baseline and treat it as throughput-limited under true 4-way concurrency.

## Full result table

Sorted by e2e output throughput within each input bucket.

| Config | Input | TTFT p50/p95 s | TPOT p50 ms | Decode tok/s | E2E out tok/s | QPS | DFlash accept | Gate |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| mlen131k spec10 mbt8192 | 8K | 4.335/6.062 | 44.36 | 22.54 | 73.30 | 0.143 | 0.280 | pass |
| baseline spec10 mbt8192 mlen262k | 8K | 4.518/6.346 | 47.48 | 21.06 | 70.63 | 0.138 | 0.271 | pass |
| spec6 mbt8192 mlen262k | 8K | 6.641/10.304 | 45.01 | 22.22 | 67.64 | 0.132 | 0.418 | pass |
| mbt4096 spec10 mlen262k | 8K | 4.942/7.085 | 50.49 | 19.80 | 66.39 | 0.130 | 0.273 | fail |
| async spec10 mbt8192 mlen262k | 8K | 5.704/7.035 | 48.05 | 20.81 | 66.25 | 0.129 | 0.273 | pass |
| spec4 mbt8192 mlen262k | 8K | 6.636/10.312 | 55.55 | 18.00 | 56.80 | 0.111 | 0.509 | fail |
| spec6 mbt8192 mlen262k | 32K | 15.899/23.577 | 68.31 | 14.64 | 39.35 | 0.077 | 0.384 | fail |
| async spec10 mbt8192 mlen262k | 32K | 16.646/23.665 | 69.81 | 14.32 | 38.84 | 0.076 | 0.257 | fail |
| baseline spec10 mbt8192 mlen262k | 32K | 16.049/23.845 | 68.72 | 14.55 | 38.67 | 0.076 | 0.254 | fail |
| mlen131k spec10 mbt8192 | 32K | 15.631/23.267 | 74.67 | 13.39 | 37.47 | 0.073 | 0.245 | fail |
| spec4 mbt8192 mlen262k | 32K | 16.272/23.989 | 75.59 | 13.23 | 36.76 | 0.072 | 0.456 | fail |
| mbt4096 spec10 mlen262k | 32K | 17.297/26.578 | 75.10 | 13.32 | 36.44 | 0.071 | 0.244 | fail |
| baseline spec10 mbt8192 mlen262k | 64K | 35.997/55.260 | 104.58 | 9.56 | 22.61 | 0.044 | 0.243 | fail |
| spec6 mbt8192 mlen262k | 64K | 35.831/54.668 | 106.93 | 9.35 | 22.34 | 0.044 | 0.351 | fail |
| async spec10 mbt8192 mlen262k | 64K | 36.816/55.279 | 105.23 | 9.50 | 21.77 | 0.043 | 0.242 | fail |
| mlen131k spec10 mbt8192 | 64K | 35.580/54.451 | 113.83 | 8.78 | 21.33 | 0.042 | 0.232 | fail |
| spec4 mbt8192 mlen262k | 64K | 36.795/56.081 | 116.56 | 8.58 | 21.07 | 0.041 | 0.464 | fail |
| mbt4096 spec10 mlen262k | 64K | 39.256/61.728 | 109.43 | 9.14 | 20.63 | 0.040 | 0.239 | fail |

## Interpretation

`spec=6` improves DFlash acceptance rate but does not materially improve decode speed. At 32K it is the best e2e throughput row, but only by 1.8% over baseline and still only 14.64 tok/s decode.

`spec=4` overcorrects. Acceptance rate rises, but draft length becomes too short and both decode and e2e throughput fall.

`max_num_batched_tokens=4096` is not useful here. It worsens all three buckets and pushes 8K below the 20 tok/s decode gate.

`max_model_len=131K` is useful only for the 8K lane. It improves 8K e2e throughput and decode, but hurts 32K/64K.

`async_scheduling=true` is not a retained win. It slightly improves 32K e2e vs baseline, but loses at 8K and 64K and is worse than `spec=6` for 32K.

## Source artifacts

Raw result JSON:
- `artifacts/knowledge/qwen3.6-gb10-dflash-4conc-maxseq4-baseline-8k32k64k-results.json`
- `artifacts/knowledge/qwen3.6-gb10-dflash-4conc-maxseq4-spec6-8k32k64k-results.json`
- `artifacts/knowledge/qwen3.6-gb10-dflash-4conc-maxseq4-mbt4096-spec10-8k32k64k-results.json`
- `artifacts/knowledge/qwen3.6-gb10-dflash-4conc-maxseq4-mlen131k-spec10-8k32k64k-results.json`
- `artifacts/knowledge/qwen3.6-gb10-dflash-4conc-maxseq4-spec4-8k32k64k-results.json`
- `artifacts/knowledge/qwen3.6-gb10-dflash-4conc-maxseq4-async-spec10-8k32k64k-results.json`

Operational note:
- The remote run ended with no leftover `qwen36-dflash` container and GPU utilization returned to 0%.
