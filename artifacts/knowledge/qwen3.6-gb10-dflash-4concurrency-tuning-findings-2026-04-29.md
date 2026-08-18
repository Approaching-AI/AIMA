# Qwen3.6-35B-A3B GB10-4T DFlash 4-concurrency tuning

Date: 2026-04-29
Host: `gb10-4t` (`aitopatom-66c4`, `qujing@100.91.39.109`)
Model: `Qwen3.6-35B-A3B`
Draft: `Qwen3.6-35B-A3B-DFlash`
Engine image: `qujing/vllm-gb10-dflash-fa2:latest`
Benchmark: AIMA `benchmark run`

All runs in this note use client `concurrency=4`, `requests=4`,
`rounds=1`, `warmup=1`, output target `512`, and no-save mode.

The hard usability gate for this tuning pass is:

```text
decode_tps = 1000 / tpot_p50_ms >= 20 tok/s
```

## Readout

For short 1K input, true 4-way scheduling (`max_num_seqs=4`) is best for
TTFT and aggregate throughput while still passing the decode gate:

- `maxseq4`: TTFT p50 `0.639s`, decode `23.76 tok/s`, e2e output
  `88.98 tok/s`
- `maxseq3`: TTFT p50 `1.386s`, decode `26.04 tok/s`, e2e output
  `60.56 tok/s`
- `maxseq2`: TTFT p50 `8.658s`, decode `29.95 tok/s`, e2e output
  `56.89 tok/s`

For 32K input, the usable service-side concurrency ceiling is `max_num_seqs=2`.
`maxseq3` and `maxseq4` improve TTFT and aggregate output TPS, but both fail
the per-request decode gate.

For 64K input, no tested true-concurrency profile passes the decode gate.
The only passing option is the retained single-sequence profile (`maxseq1`),
which preserves decode but queues 4-client load and has high TTFT.

## 32K / 512-token output

| config | TTFT p50 | TTFT p95 | TPOT p50 | decode TPS | e2e output TPS | gate |
|---|---:|---:|---:|---:|---:|---|
| maxseq1 mbt8192 spec10 mlen262k | 37.593s | 65.278s | 28.27ms | 35.37 | 24.95 | pass |
| maxseq2 mbt8192 spec6 mlen262k | 27.219s | 48.034s | 47.19ms | 21.19 | 29.44 | pass |
| maxseq2 mbt4096 spec10 mlen262k | 27.768s | 47.117s | 47.55ms | 21.03 | 30.30 | pass |
| maxseq2 mbt8192 spec10 mlen131k | 25.433s | 43.174s | 48.16ms | 20.76 | 31.93 | pass |
| maxseq2 mbt8192 spec10 mlen262k | 23.997s | 43.344s | 48.49ms | 20.62 | 31.46 | pass |
| maxseq2 mbt8192 spec10 mlen65k | 25.792s | 44.319s | 50.98ms | 19.61 | 30.87 | fail |
| maxseq3 mbt8192 spec10 mlen262k | 16.189s | 41.681s | 60.41ms | 16.55 | 34.45 | fail |
| maxseq4 mbt8192 spec10 mlen262k | 15.859s | 23.600s | 70.77ms | 14.13 | 39.33 | fail |

Recommended 32K service profile from this batch:

```yaml
max_num_seqs: 2
max_num_batched_tokens: 8192
max_model_len: 131072
num_speculative_tokens: 10
```

The retained 262K profile is nearly tied and has slightly lower TTFT p50, but
the 131K profile gives the best aggregate output TPS while staying above the
decode gate.

## 64K / 512-token output

| config | TTFT p50 | TTFT p95 | TPOT p50 | decode TPS | e2e output TPS | gate |
|---|---:|---:|---:|---:|---:|---|
| maxseq1 mbt8192 spec10 mlen262k | 63.435s | 108.997s | 37.47ms | 26.69 | 15.45 | pass |
| maxseq2 mbt8192 spec10 mlen131k | 46.882s | 77.094s | 61.31ms | 16.31 | 19.56 | fail |
| maxseq2 mbt8192 spec10 mlen262k | 46.911s | 79.799s | 72.86ms | 13.72 | 19.28 | fail |
| maxseq2 mbt8192 spec10 mlen65k | 47.106s | 78.991s | 76.00ms | 13.16 | 18.79 | fail |
| maxseq2 mbt8192 spec6 mlen262k | 48.613s | 80.957s | 76.85ms | 13.01 | 18.53 | fail |
| maxseq2 mbt4096 spec10 mlen262k | 51.433s | 87.146s | 78.22ms | 12.78 | 17.53 | fail |
| maxseq3 mbt8192 spec10 mlen262k | 35.460s | 76.964s | 83.11ms | 12.03 | 19.99 | fail |
| maxseq4 mbt8192 spec10 mlen262k | 35.161s | 53.846s | 104.66ms | 9.56 | 22.89 | fail |

64K conclusion: if decode must stay above `20 tok/s`, current DFlash/vLLM
settings cannot serve 4 active long-context requests concurrently. Choose:

- `maxseq1` when per-request decode quality is the hard requirement.
- `maxseq2` with `max_model_len=131072` when lower TTFT and aggregate output
  matter more, but mark it below the decode usability threshold.

## Tuning effects

- Raising service-side concurrency from `maxseq1` to `maxseq4` steadily lowers
  TTFT for short context but burns per-request decode.
- `maxseq2` is the 32K knee point: it is the highest tested server-side
  concurrency that still passes `decode >= 20`.
- Reducing `max_num_batched_tokens` from 8192 to 4096 slightly improves 32K
  decode (`20.62 -> 21.03`) but worsens TTFT and does not help 64K.
- Reducing speculative tokens from 10 to 6 slightly improves 32K decode
  (`20.62 -> 21.19`) but does not improve 64K enough to matter.
- Reducing `max_model_len` to 131K improves 64K decode versus 262K
  (`13.72 -> 16.31`) but still fails the gate; 65K regresses.

## Artifacts

- `artifacts/knowledge/qwen3.6-gb10-dflash-4conc-tune-maxseq2-mbt8192-results.json`
- `artifacts/knowledge/qwen3.6-gb10-dflash-4conc-tune-maxseq3-mbt8192-results.json`
- `artifacts/knowledge/qwen3.6-gb10-dflash-4conc-tune-maxseq2-mbt4096-results.json`
- `artifacts/knowledge/qwen3.6-gb10-dflash-4conc-tune-maxseq2-mbt8192-spec6-results.json`
- `artifacts/knowledge/qwen3.6-gb10-dflash-4conc-tune-maxseq2-mbt8192-mlen131k-results.json`
- `artifacts/knowledge/qwen3.6-gb10-dflash-4conc-tune-maxseq2-mbt8192-mlen65k-results.json`

