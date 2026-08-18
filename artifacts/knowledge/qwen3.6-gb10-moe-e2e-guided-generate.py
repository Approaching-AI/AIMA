import json
from pathlib import Path

from vllm.model_executor.layers.fused_moe.fused_moe import (
    get_config_file_name,
    get_default_config,
)
from vllm.triton_utils import triton

E = 256
N = 512
K = 2048
TOPK = 8
BUCKETS = [1, 2, 4, 8, 16, 24, 32, 48, 64, 96, 128, 256, 512, 1024, 2048, 4096, 8192]


def sorted_config(config):
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


def default_configs():
    return {
        str(m): sorted_config(get_default_config(m, E, N, K, TOPK, None, None))
        for m in BUCKETS
    }


def write_variant(name, overrides, output_root):
    cfg = default_configs()
    for key in overrides:
        cfg[str(key)] = sorted_config(AUTOTUNE[str(key)])
    out_dir = output_root / name
    out_dir.mkdir(parents=True, exist_ok=True)
    out_file = out_dir / get_config_file_name(E, N, None, None)
    out_file.write_text(json.dumps({"triton_version": triton.__version__, **cfg}, indent=4) + "\n")
    return str(out_file)


AUTOTUNE = json.loads(Path("/autotune/E=256,N=512,device_name=NVIDIA_GB10.json").read_text())

output_root = Path("/out")
variants = {
    "qwen36-gb10-e2e-big512plus": [512, 1024, 2048, 4096, 8192],
    "qwen36-gb10-e2e-big1024plus": [1024, 2048, 4096, 8192],
    "qwen36-gb10-e2e-big4096plus": [4096, 8192],
}

manifest = {}
for name, buckets in variants.items():
    manifest[name] = {
        "overridden_buckets": buckets,
        "config_path": write_variant(name, buckets, output_root),
    }

(output_root / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
print(json.dumps(manifest, indent=2))
