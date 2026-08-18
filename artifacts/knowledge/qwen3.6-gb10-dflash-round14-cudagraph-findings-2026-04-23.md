# Qwen3.6 DFlash Round 14 Findings (CUDA Graph Capture Size)

Date: 2026-04-23
Host: `gb10-4t` (`100.91.39.109`)
Model: `Qwen3.6-35B-A3B`
Path: `DFlash`

## Goal

After `--max-num-seqs 1` became the retained setting for single-stream long-context, this round checked whether CUDA graph capture could be tightened further.

Tested case:

- `--cudagraph-capture-sizes 1`
- `--max-cudagraph-capture-size 1`

Base settings were unchanged:

- `64K input + 1K output`
- `concurrency=1`
- `num_speculative_tokens=10`
- `max_num_seqs=1`
- `no_async_scheduling`
- `block_size=1280`
- `max_num_batched_tokens=8192`
- `enforce_eager=false`

Raw result file:

- [qwen3.6-gb10-dflash-round14-cudagraph-screen-results.json](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round14-cudagraph-screen-results.json)

Runner:

- [qwen3.6-gb10-dflash-round14-cudagraph-screen.py](/Users/jguan/projects/AIMA/artifacts/knowledge/qwen3.6-gb10-dflash-round14-cudagraph-screen.py)

## Result

The service failed before ready.

Root cause:

```text
ValueError: No valid cudagraph sizes after rounding to multiple of 11 (num_speculative_tokens + 1 or tp if sequence parallelism is enabled) please adjust num_speculative_tokens (10) or max_cudagraph_capture_size (1) or cudagraph_capture_sizes ([1])
```

## Interpretation

For DFlash with `num_speculative_tokens=10`, vLLM requires CUDA graph capture sizes to align to:

```text
num_speculative_tokens + 1 = 11
```

So `capture_size=1` is not a valid way to shrink the graph set. This is a configuration constraint, not a benchmark result.

The current default path already performs its own spec-decode CUDA graph size adjustment. Further graph-size tuning should only use multiples of `11`, and only if there is a specific startup-memory or graph-capture problem to solve.

## Conclusion

Do not retain:

- `--cudagraph-capture-sizes 1`
- `--max-cudagraph-capture-size 1`

No change to the retained DFlash recommendation.
