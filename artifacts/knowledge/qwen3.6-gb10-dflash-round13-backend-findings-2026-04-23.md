# Qwen3.6 DFlash Round 13 Findings (Backend Levers)

Date: 2026-04-23
Host: `gb10-4t` (`100.91.39.109`)
Model: `Qwen3.6-35B-A3B`
Path: `DFlash`

## Goal

After scheduler/block/spec/GMU sweeps, this round checked lower-level backend levers that could plausibly explain why DFlash gains plateau around the practical 32K/64K business workload:

- GDN prefill backend
- DBO / microbatching
- FlashInfer MoE backends
- chunked prefill state

Target workload remained:

- business prompt
- `64K input + 1K output`
- `concurrency=1`
- `requests=1`
- `rounds=3`
- `warmup=2`

Base retained settings:

- `--max-num-seqs 1`
- `--no-async-scheduling`
- `--block-size 1280`
- `--max-num-batched-tokens 8192`
- `num_speculative_tokens=10`
- `enforce_eager=false`
- `VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=1`

Raw / partial result files:

- [qwen3.6-gb10-dflash-round13-moe-env-partial-results.json](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round13-moe-env-partial-results.json)
- [qwen3.6-gb10-dflash-round13b-gdn-flashinfer-partial-results.json](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round13b-gdn-flashinfer-partial-results.json)
- [qwen3.6-gb10-dflash-round13d-moe-screen-results.json](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round13d-moe-screen-results.json)

Runner:

- [qwen3.6-gb10-dflash-round13-moe-gdn-dbo-screen.py](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round13-moe-gdn-dbo-screen.py)

## Confirmed Runtime State

The current recommended DFlash path starts with:

```text
Chunked prefill is enabled with max_num_batched_tokens=8192.
enable_prefix_caching=False
enable_chunked_prefill=True
Using Triton/FLA GDN prefill kernel
Using TRITON backend for Unquantized MoE
```

This matters because several suspected levers are already decided by the engine:

- chunked prefill is already on
- prefix caching is off, so repeated benchmark rounds are not being inflated by prefix cache hits
- GDN prefill is already on Triton/FLA
- unquantized MoE is on TRITON

## Baseline Re-Runs

| File | Case | Ready s | 64K TTFT ms | 64K TPOT ms | 64K TPS |
|---|---|---:|---:|---:|---:|
| round13 partial | `business_best64_rerun` | 360.2 | 13920.791 | 40.680 | 18.521 |
| round13b partial | `business_best64_rerun` | 360.1 | 14202.042 | 38.934 | 19.403 |

The same retained baseline still lands in the same range as round12:

- TTFT roughly `13.8s-14.5s`
- TPOT roughly `37ms-41ms`
- end-to-end TPS roughly `18.5-19.7`

So backend experiments should be judged against that range, not against old synthetic-prompt numbers.

## Tested Backend Levers

### `--gdn-prefill-backend flashinfer`

This did not produce a new runtime path.

Startup log:

```text
GDN prefill backend 'flashinfer' is selected but cannot use this kernel on the current platform. Falling back to Triton/FLA.
Using Triton/FLA GDN prefill kernel
```

Conclusion:

- FlashInfer GDN prefill is not available on this current GB10 image/config.
- The default `auto` path already resolves to Triton/FLA.
- Do not keep testing `gdn_prefill_backend=triton`; it is the current default.

### `--enable-dbo`

This failed at config validation before engine startup.

Observed error:

```text
Microbatching currently only supports the deepep_low_latency and deepep_high_throughput all2all backend. allgather_reducescatter is not supported.
```

Conclusion:

- DBO is not a valid lever for this single-card GB10 path unless DeepEP kernels/backend are introduced.
- Do not include `--enable-dbo` in further single-GPU sweeps.

### `--moe-backend flashinfer_trtllm`

This failed before ready.

Observed error:

```text
ValueError: FlashInfer TRTLLM MoE backend is not available for this configuration.
```

Conclusion:

- FlashInfer TRTLLM MoE is not available for current Qwen3.6 BF16 DFlash path on this image.

### `--enable-expert-parallel --moe-backend flashinfer_cutlass`

This also failed before ready.

Observed error:

```text
ValueError: FlashInfer CUTLASS MoE backend is not available for this configuration.
```

Conclusion:

- Forcing EP does not make FlashInfer CUTLASS MoE available in this single-GPU configuration.
- Current practical MoE backend remains TRITON.

## Interpretation

The main backend-level escape hatches are closed on the current image:

- GDN FlashInfer cannot be used.
- DBO needs DeepEP and is not valid here.
- FlashInfer MoE backends are not available for this configuration.

That means the remaining gains are unlikely to come from simple CLI backend switches. The plateau around `~19-20` end-to-end TPS at `64K input + 1K output` is consistent with:

- prefill remaining a large part of total latency
- DFlash acceptance around only `~21-23%`
- TRITON MoE remaining the only available unquantized MoE path

## Retained Recommendation

Keep the current DFlash business recommendation:

- `--max-num-seqs 1`
- `--no-async-scheduling`
- `--block-size 1280`
- `--max-num-batched-tokens 8192`
- `num_speculative_tokens=10`
- `enforce_eager=false`
- `VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=1`

Closed for this current image/config:

- `--gdn-prefill-backend flashinfer`
- `--enable-dbo`
- `--moe-backend flashinfer_trtllm`
- `--enable-expert-parallel --moe-backend flashinfer_cutlass`

## Next Useful Direction

Further improvement probably needs one of these, not more small CLI sweeps:

- a newer image where FlashInfer GDN or FlashInfer MoE is actually available for this model/config
- a custom MoE config/kernel path for Qwen3.6 BF16 on GB10
- drafter/acceptance work, because DFlash acceptance is still only around `21-23%` on the business prompt
