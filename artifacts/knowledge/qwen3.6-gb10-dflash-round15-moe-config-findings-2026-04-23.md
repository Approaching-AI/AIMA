# Qwen3.6-35B-A3B on GB10 DFlash: Round15 MoE Tuned Config Findings

Date: 2026-04-23
Host: gb10-4t / 100.91.39.109
Image: `qujing/vllm-gb10-dflash-fa2:latest`
Model: `/home/qujing/aima-codex-qwen36/models/Qwen3.6-35B-A3B`
Draft: `/home/qujing/aima-codex-qwen36/models/Qwen3.6-35B-A3B-DFlash`

## Goal

Verify whether Qwen3.6-35B-A3B can use a user-provided vLLM fused MoE tuned config on GB10, and whether that improves the retained practical DFlash path for 64K input / 1K output / single stream.

## Baseline For Comparison

Retained DFlash practical config from round12:

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

Round12 64K/1K business result:

- TTFT: 13.807 s
- TPOT: 37.139 ms
- End-to-end TPS: 19.746
- Draft acceptance rate: 22.2%
- Mean acceptance length: 2.22

## MoE Config File Name

Qwen3.6-35B-A3B model config:

- `num_experts=256`
- `moe_intermediate_size=512`
- dtype: BF16/unquantized
- CUDA device name in container: `NVIDIA_GB10`

The vLLM fused MoE config file name for this shape is:

```text
E=256,N=512,device_name=NVIDIA_GB10.json
```

The file is loaded through:

```text
VLLM_TUNED_CONFIG_FOLDER=/moe-configs
```

## Trials

### H100 Same-Shape Seed

Source: built-in vLLM config copied from:

```text
E=256,N=512,device_name=NVIDIA_H100_80GB_HBM3.json
```

Remote path:

```text
/home/qujing/aima-codex-qwen36/moe-configs/qwen36-gb10-seed/E=256,N=512,device_name=NVIDIA_GB10.json
```

Result:

- Status: failed before ready.
- Root cause: Triton fused MoE shared memory overflow.
- Error: `Required: 147456, Hardware limit: 101376`.
- Interpretation: H100 same-shape MoE tile config is too aggressive for GB10 and must not be used as-is.

### GB10-Safe v1

Remote path:

```text
/home/qujing/aima-codex-qwen36/moe-configs/qwen36-gb10-safe-v1/E=256,N=512,device_name=NVIDIA_GB10.json
```

Config derivation:

- Start from H100 same-shape seed.
- Cap `BLOCK_SIZE_N` at 128.
- Cap `BLOCK_SIZE_K` at 64.
- Cap `num_stages` at 2.
- Cap `num_warps` at 4.

Runtime confirmation:

```text
Using configuration from /moe-configs/E=256,N=512,device_name=NVIDIA_GB10.json for MoE layer.
```

64K/1K business result:

- TTFT: 13.970 s
- TPOT: 39.896 ms
- End-to-end TPS: 18.712
- Draft acceptance rate: 22.1%
- Mean acceptance length: 2.21

Delta vs round12 retained config:

- TTFT: +1.18%
- TPOT: +7.42% slower
- End-to-end TPS: -5.24%
- Acceptance rate: effectively unchanged

## Conclusion

The MoE tuned config loading mechanism is solved: vLLM correctly loads a user-provided GB10 file when the filename and `VLLM_TUNED_CONFIG_FOLDER` are correct.

This is not yet a performance win. The direct H100 seed is invalid on GB10 due to shared memory overflow, and the conservative GB10-safe v1 runs but regresses 64K/1K throughput. Do not enable this MoE tuned config in the recommended DFlash runtime path.

## Next Useful Work

If this line is continued, use GB10-safe v1 only as a bootable starting point for targeted tile search. The search should vary one group of batch buckets at a time and must keep the round12 practical DFlash config as the control. A useful target is to recover or beat TPOT 37.139 ms at 64K/1K without increasing TTFT materially.

Do not spend more time testing the raw H100 seed.

### GB10 Targeted Autotune v1

Script:

```text
artifacts/knowledge/qwen3.6-gb10-moe-autotune-targeted.py
```

Remote output:

```text
/home/qujing/aima-codex-qwen36/moe-configs/qwen36-gb10-autotune-v1/E=256,N=512,device_name=NVIDIA_GB10.json
```

Autotune scope:

- Shape: `E=256`, `N=512`, `K=2048`, `topk=8`, BF16/unquantized.
- Batch buckets: `1,2,4,8,16,24,32,48,64,96,128,256,512,1024,2048,4096,8192`.
- Targeted GB10-safe candidate space, not vLLM's full 1920-config-per-bucket search.
- Runtime: about 292 seconds.

Selected long-context relevant buckets:

- `512`: `BLOCK_SIZE_M=32`, `BLOCK_SIZE_N=128`, `BLOCK_SIZE_K=128`, `GROUP_SIZE_M=16`, `num_warps=4`, `num_stages=3`.
- `1024`: `BLOCK_SIZE_M=64`, `BLOCK_SIZE_N=128`, `BLOCK_SIZE_K=128`, `GROUP_SIZE_M=16`, `num_warps=8`, `num_stages=3`.
- `2048`: `BLOCK_SIZE_M=32`, `BLOCK_SIZE_N=64`, `BLOCK_SIZE_K=128`, `GROUP_SIZE_M=64`, `num_warps=8`, `num_stages=3`.
- `4096`: `BLOCK_SIZE_M=128`, `BLOCK_SIZE_N=64`, `BLOCK_SIZE_K=128`, `GROUP_SIZE_M=16`, `num_warps=8`, `num_stages=3`.
- `8192`: `BLOCK_SIZE_M=64`, `BLOCK_SIZE_N=128`, `BLOCK_SIZE_K=64`, `GROUP_SIZE_M=1`, `num_warps=8`, `num_stages=3`.

Runtime confirmation:

```text
Using configuration from /moe-configs/E=256,N=512,device_name=NVIDIA_GB10.json for MoE layer.
```

32K/1K business result:

- TTFT: 6.025 s
- TPOT: 32.098 ms
- End-to-end TPS: 26.443
- Draft acceptance rate: 22.4%
- Mean acceptance length: 2.24

Delta vs round12 retained 32K:

- TTFT: +1.17%
- TPOT: +4.74% slower
- End-to-end TPS: -3.23%
- Acceptance rate: -2.75%

64K/1K business result:

- TTFT: 13.712 s
- TPOT: 37.810 ms
- End-to-end TPS: 19.815
- Draft acceptance rate: 22.4%
- Mean acceptance length: 2.24

Delta vs round12 retained 64K:

- TTFT: -0.68%
- TPOT: +1.81% slower
- End-to-end TPS: +0.35%
- Acceptance rate: +0.96%

Conclusion:

Targeted autotune v1 is technically successful and much better than safe-v1, but it is not a new default. It slightly helps 64K end-to-end TPS while regressing 32K and making TPOT worse in both tested buckets. Keep it as an experimental 64K-oriented candidate only.

The next useful MoE search should not optimize micro-kernel time alone. It should use AIMA benchmark as the objective and mutate only a few high-impact buckets around 512/1024/2048/4096/8192, because this run shows micro-kernel wins do not translate cleanly to DFlash end-to-end decode performance.
