# M1000 MUSA GPU Performance Tuning Guide

## Device Profile
- **SoC**: Moore Threads M1000, ARM Cortex-A78 12-core @ 2.65GHz
- **Memory**: 62GB unified (GPU shares system RAM)
- **MUSA SDK**: 4.1.4, kernel module `mtgpu`
- **Runtime**: Native (not Docker/K3S)
- **Engine**: vLLM-MUSA 0.9.2 (conda env `v1.3`)

## Constraints
- **enforce-eager required**: MUSA graphs compilation unstable
- **tp=1 only**: Single GPU, no tensor parallel
- **GPTQ-only**: FP8/AWQ not supported on MUSA
- **max_num_seqs=1**: Single request at a time (M1000 compute budget)
- **Native runtime**: vLLM binary at `/usr/local/bin/vllm` (symlink from conda)

## Optimal Parameters (Qwen3-30B-A3B-GPTQ-Int4)

### Recommended Config (verified by benchmark)
```yaml
gpu_memory_utilization: 0.70   # 0.80 gives <1% improvement, 0.70 safer
max_model_len: 16384           # Max supported context
max_num_seqs: 1                # Single sequence only
block_size: 32
enforce_eager: true
swap_space: 0
dtype: auto
quantization: gptq
```

### Pre-startup Checklist
1. `echo 3 > /proc/sys/vm/drop_caches` (clear pagecache, needs sudo)
2. Ensure `ENABLE_MUSA_MMA=1` and `TRITON_CACHE_DIR=/tmp/triton` env vars
3. Ensure `LD_LIBRARY_PATH` includes `/usr/local/musa/lib`

## Performance Profile

### Decode Speed (tokens/second)
| Context Length | TPOT (s) | tok/s |
|---------------|----------|-------|
| ≤1K           | 0.079    | 12.7  |
| 4K            | 0.084    | 11.9  |
| 8K            | 0.096    | 10.4  |
| 12K           | 0.110    | 9.1   |

### Prefill Speed (TTFT)
| Input Tokens | TTFT (s) | Prefill tok/s |
|-------------|----------|---------------|
| 128         | 0.94-1.20| ~118          |
| 1024        | 2.3-3.4  | ~300*         |
| 4096        | 17-22    | ~200*         |
| 8192        | 64       | ~128*         |
| 12288       | 74       | ~166*         |

*TTFT includes overhead, effective prefill rate varies.

### GMU Comparison
| GMU | Cold Start | TPOT @1K | TPOT @4K | Notes |
|-----|-----------|----------|----------|-------|
| 0.70| ~100s     | 0.080s   | 0.084s   | Recommended (safer) |
| 0.80| ~50s      | 0.081s   | 0.085s   | Slightly faster cold start |

## Tuning Knobs (Priority Order)

1. **gpu_memory_utilization**: 0.70 is sweet spot. Higher doesn't help decode.
2. **max_model_len**: Reduce to 4096 if only short conversations needed (faster KV cache allocation).
3. **block_size**: 32 tested, no reason to change.
4. **swap_space**: Keep 0 (unified memory, swap defeats purpose).

## Known Limitations
- **Long context TTFT is slow**: enforce-eager means no CUDA/MUSA graph optimization for prefill
- **No concurrent requests**: max_num_seqs=1 is a hard limit on M1000 compute
- **No Docker**: vLLM-MUSA not available as container image for ARM64

## Troubleshooting
- `CreatePlatform failed`: GPU device not accessible, check `/dev/mtgpu.0`
- `MUSA initialization error`: Display may need to be connected
- `libmusart.so.4 not found`: Run `ldconfig` after adding `/usr/local/musa/lib` to ld.so.conf.d
- `--tp 1` rejected: Use `--tensor-parallel-size 1` or omit (default is 1)
