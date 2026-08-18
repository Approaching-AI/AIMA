import argparse
import gc
import json
import os
import time
from itertools import product
from pathlib import Path

import torch

from vllm.model_executor.layers.fused_moe import fused_topk, override_config
from vllm.model_executor.layers.fused_moe.config import FusedMoEQuantConfig
from vllm.model_executor.layers.fused_moe.fused_moe import (
    fused_experts,
    get_config_file_name,
    get_default_config,
)
from vllm.triton_utils import triton
from vllm.utils.torch_utils import set_random_seed

E = 256
N = 512
K = 2048
TOPK = 8
DTYPE = torch.bfloat16

DEFAULT_BATCH_SIZES = [1, 2, 4, 8, 16, 24, 32, 48, 64, 96, 128, 256, 512, 1024, 2048, 4096, 8192]


def clear_cache():
    gc.collect()
    torch.cuda.empty_cache()
    try:
        if hasattr(triton, "runtime") and hasattr(triton.runtime, "cache"):
            triton.runtime.cache.clear()
    except Exception:
        pass
    gc.collect()


def sort_config(config):
    keys = [
        "BLOCK_SIZE_M",
        "BLOCK_SIZE_N",
        "BLOCK_SIZE_K",
        "GROUP_SIZE_M",
        "num_warps",
        "num_stages",
        "SPLIT_K",
    ]
    return {k: int(config[k]) for k in keys if k in config}


def safe_v1_config(m):
    if m <= 4:
        block_m = 16
        block_n = 32
        group_m = 1
    elif m <= 8:
        block_m = 16
        block_n = 128
        group_m = 1
    elif m <= 24:
        block_m = 16
        block_n = 64
        group_m = 64 if m == 16 else 1
    elif m <= 48:
        block_m = 16
        block_n = 32
        group_m = 64 if m == 48 else 1
    elif m <= 256:
        block_m = 16
        block_n = 64 if m == 64 else 128
        group_m = 1
    elif m <= 512:
        block_m = 32
        block_n = 128
        group_m = 1
    elif m <= 1536:
        block_m = 64
        block_n = 128
        group_m = 1
    else:
        block_m = 128
        block_n = 128
        group_m = 16 if m in (2048, 4096) else 1
    return {
        "BLOCK_SIZE_M": block_m,
        "BLOCK_SIZE_N": block_n,
        "BLOCK_SIZE_K": 64,
        "GROUP_SIZE_M": group_m,
        "num_warps": 4,
        "num_stages": 2,
        "SPLIT_K": 1,
    }


def candidate_configs(m):
    candidates = []

    def add(config):
        config = sort_config({**config, "SPLIT_K": config.get("SPLIT_K", 1)})
        if config not in candidates:
            candidates.append(config)

    add(get_default_config(m, E, N, K, TOPK, None, None))
    add(safe_v1_config(m))

    block_m_values = [16, 32, 64, 128]
    block_n_values = [64, 128, 256]
    block_k_values = [64, 128]
    group_m_values = [1, 16, 64]
    warp_values = [4, 8]
    stage_values = [2, 3]

    if m <= 32:
        block_m_values = [16, 32]
        block_n_values = [32, 64, 128]
        group_m_values = [1, 16]
    elif m <= 128:
        block_m_values = [16, 32, 64]
        group_m_values = [1, 16, 64]

    for block_m, block_n, block_k, group_m, warps, stages in product(
        block_m_values,
        block_n_values,
        block_k_values,
        group_m_values,
        warp_values,
        stage_values,
    ):
        if m * 2 < block_m and block_m != 16:
            continue
        if block_n == 256 and block_k == 128:
            continue
        if block_n == 256 and stages > 2:
            continue
        add(
            {
                "BLOCK_SIZE_M": block_m,
                "BLOCK_SIZE_N": block_n,
                "BLOCK_SIZE_K": block_k,
                "GROUP_SIZE_M": group_m,
                "num_warps": warps,
                "num_stages": stages,
                "SPLIT_K": 1,
            }
        )
    return candidates


class MoeCase:
    def __init__(self, m, num_iters):
        self.m = m
        self.num_iters = num_iters
        self.x = torch.randn(m, K, dtype=DTYPE, device="cuda") / 10
        self.w1 = torch.randn(E, 2 * N, K, dtype=DTYPE, device="cuda") / 10
        self.w2 = torch.randn(E, K, N, dtype=DTYPE, device="cuda") / 10
        self.gating = torch.randn(m, E, dtype=torch.float32, device="cuda")
        self.quant_config = FusedMoEQuantConfig.make(quant_dtype=None)

    def run_once(self, config):
        with override_config(config):
            topk_weights, topk_ids, _ = fused_topk(
                self.x, self.gating, TOPK, renormalize=True
            )
            return fused_experts(
                self.x,
                self.w1,
                self.w2,
                topk_weights,
                topk_ids,
                quant_config=self.quant_config,
            )

    def time_config(self, config):
        try:
            for _ in range(2):
                self.run_once(config)
            torch.cuda.synchronize()

            start = torch.cuda.Event(enable_timing=True)
            end = torch.cuda.Event(enable_timing=True)
            start.record()
            for _ in range(self.num_iters):
                self.run_once(config)
            end.record()
            end.synchronize()
            return start.elapsed_time(end) / self.num_iters * 1000.0
        except Exception as exc:
            torch.cuda.synchronize()
            return {"error": f"{type(exc).__name__}: {exc}"}


def tune_batch(m, num_iters, max_candidates):
    case = MoeCase(m, num_iters)
    configs = candidate_configs(m)
    if max_candidates and len(configs) > max_candidates:
        configs = configs[:max_candidates]
    best_config = None
    best_us = float("inf")
    records = []

    for idx, config in enumerate(configs):
        timed = case.time_config(config)
        if isinstance(timed, dict):
            records.append({"config": config, **timed})
        else:
            records.append({"config": config, "kernel_us": timed})
            if timed < best_us:
                best_us = timed
                best_config = config
        if idx and idx % 40 == 0:
            clear_cache()

    del case
    clear_cache()

    if best_config is None:
        raise RuntimeError(f"no valid config for batch {m}")
    return {
        "batch_size": m,
        "candidate_count": len(configs),
        "best_config": best_config,
        "best_kernel_us": best_us,
        "top10": sorted(
            [r for r in records if "kernel_us" in r], key=lambda x: x["kernel_us"]
        )[:10],
        "errors": [r for r in records if "error" in r][:20],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--save-dir", required=True)
    parser.add_argument("--result-json", required=True)
    parser.add_argument("--batch-size", type=int, nargs="*", default=DEFAULT_BATCH_SIZES)
    parser.add_argument("--num-iters", type=int, default=8)
    parser.add_argument("--max-candidates", type=int, default=0)
    parser.add_argument("--seed", type=int, default=0)
    args = parser.parse_args()

    set_random_seed(args.seed)
    torch.set_default_device("cuda")
    torch.cuda.synchronize()

    result = {
        "model_shape": {
            "num_experts": E,
            "moe_intermediate_size": N,
            "hidden_size": K,
            "topk": TOPK,
            "dtype": "bfloat16",
        },
        "device_name": torch.cuda.get_device_name(),
        "batch_sizes": args.batch_size,
        "num_iters": args.num_iters,
        "batches": [],
    }
    best_configs = {}
    start = time.time()
    for m in args.batch_size:
        batch_result = tune_batch(m, args.num_iters, args.max_candidates)
        result["batches"].append(batch_result)
        best_configs[str(m)] = batch_result["best_config"]
        Path(args.result_json).write_text(json.dumps(result, indent=2))
        print(
            f"batch={m} best={batch_result['best_kernel_us']:.2f}us "
            f"config={batch_result['best_config']}",
            flush=True,
        )
    result["elapsed_s"] = time.time() - start

    save_dir = Path(args.save_dir)
    save_dir.mkdir(parents=True, exist_ok=True)
    config_name = get_config_file_name(E, N, None, None)
    config_path = save_dir / config_name
    config_path.write_text(
        json.dumps({"triton_version": triton.__version__, **best_configs}, indent=4)
        + "\n"
    )
    result["config_path"] = str(config_path)
    Path(args.result_json).write_text(json.dumps(result, indent=2))
    print(f"wrote {config_path}", flush=True)


if __name__ == "__main__":
    main()
