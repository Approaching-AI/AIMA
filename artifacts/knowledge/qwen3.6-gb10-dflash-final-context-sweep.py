import json
import re
import subprocess
import time
import urllib.request
from pathlib import Path

PORT = 18081
NAME = "qwen36-dflash-final-sweep"
MODEL_DIR = "/home/qujing/aima-codex-qwen36/models"
IMAGE = "qujing/vllm-gb10-dflash-fa2:latest"
MODEL = "/models/Qwen3.6-35B-A3B"
DRAFT = "/models/Qwen3.6-35B-A3B-DFlash"
OUT = Path("/tmp/qwen3.6-gb10-dflash-final-context-sweep.json")

INPUT_BUCKETS = [1024, 2048, 4096, 8192, 16384, 32768, 65536, 131072, 262144]

BASE_ARGS = [
    "--host",
    "0.0.0.0",
    "--port",
    "8000",
    "--served-model-name",
    "Qwen3.6-35B-A3B",
    "--dtype",
    "bfloat16",
    "--gpu-memory-utilization",
    "0.92",
    "--max-model-len",
    "262144",
    "--block-size",
    "1280",
    "--trust-remote-code",
    "--language-model-only",
    "--reasoning-parser",
    "qwen3",
    "--attention-backend",
    "FLASH_ATTN",
    "--mm-encoder-attn-backend",
    "TORCH_SDPA",
    "--skip-mm-profiling",
    "--max-num-batched-tokens",
    "8192",
    "--max-num-seqs",
    "1",
    "--no-async-scheduling",
]


def run(cmd, check=True, timeout=None):
    res = subprocess.run(cmd, text=True, capture_output=True, timeout=timeout)
    if check and res.returncode != 0:
        raise RuntimeError(
            f"cmd failed: {cmd}\nstdout={res.stdout}\nstderr={res.stderr}"
        )
    return res


def wait_ready(timeout=1500):
    url = f"http://127.0.0.1:{PORT}/v1/models"
    start = time.time()
    while time.time() - start < timeout:
        try:
            with urllib.request.urlopen(url, timeout=5) as r:
                if r.status == 200:
                    return round(time.time() - start, 1)
        except Exception:
            pass
        state = run(
            ["docker", "inspect", "-f", "{{.State.Running}}", NAME],
            check=False,
            timeout=10,
        )
        if state.returncode == 0 and state.stdout.strip() == "false":
            raise RuntimeError("container exited before ready")
        time.sleep(5)
    raise TimeoutError("ready timeout")


def metrics():
    txt = urllib.request.urlopen(
        f"http://127.0.0.1:{PORT}/metrics", timeout=30
    ).read().decode()
    out = {}
    for key in [
        "spec_decode_num_drafts_total",
        "spec_decode_num_draft_tokens_total",
        "spec_decode_num_accepted_tokens_total",
        "request_prefill_time_seconds_count",
        "request_prefill_time_seconds_sum",
        "request_decode_time_seconds_count",
        "request_decode_time_seconds_sum",
        "kv_cache_usage_perc",
    ]:
        m = re.search(rf"vllm:{re.escape(key)}[^\n]* ([0-9.eE+-]+)$", txt, re.M)
        out[key] = float(m.group(1)) if m else 0.0
    return out


def delta(a, b):
    d = {k: b.get(k, 0) - a.get(k, 0) for k in b}
    drafts = d["spec_decode_num_drafts_total"]
    draft_tokens = d["spec_decode_num_draft_tokens_total"]
    accepted = d["spec_decode_num_accepted_tokens_total"]
    d["mean_acceptance_length"] = accepted / drafts if drafts else None
    d["draft_acceptance_rate"] = accepted / draft_tokens if draft_tokens else None
    d["avg_prefill_s_per_req"] = (
        d["request_prefill_time_seconds_sum"]
        / d["request_prefill_time_seconds_count"]
        if d["request_prefill_time_seconds_count"]
        else None
    )
    d["avg_decode_s_per_req"] = (
        d["request_decode_time_seconds_sum"] / d["request_decode_time_seconds_count"]
        if d["request_decode_time_seconds_count"]
        else None
    )
    return d


def bench(input_tokens):
    res = run(
        [
            "/home/qujing/aima",
            "benchmark",
            "run",
            "--endpoint",
            f"http://127.0.0.1:{PORT}",
            "--model",
            "Qwen3.6-35B-A3B",
            "--concurrency",
            "1",
            "--requests",
            "1",
            "--rounds",
            "3",
            "--max-tokens",
            "512",
            "--input-tokens",
            str(input_tokens),
            "--warmup",
            "2",
            "--min-output-ratio",
            "1",
            "--max-retries",
            "1",
            "--no-save",
            "--notes",
            f"final-retained-dflash context-sweep {input_tokens//1024}k 512out",
        ],
        timeout=5200,
    )
    return json.loads(res.stdout)


def launch():
    subprocess.run(["docker", "rm", "-f", NAME], text=True, capture_output=True)
    spec = json.dumps(
        {
            "method": "dflash",
            "model": DRAFT,
            "num_speculative_tokens": 10,
        },
        separators=(",", ":"),
    )
    cmd = [
        "docker",
        "run",
        "-d",
        "--name",
        NAME,
        "--gpus",
        "all",
        "-e",
        "VLLM_KV_CACHE_LAYOUT=NHD",
        "-e",
        "VLLM_SKIP_GDN_PREFILL_WARMUP=1",
        "-e",
        "FLA_GDN_FIX_BT=1",
        "-e",
        "VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=1",
        "-p",
        f"{PORT}:8000",
        "-v",
        f"{MODEL_DIR}:/models",
        IMAGE,
        MODEL,
        *BASE_ARGS,
        "--speculative-config",
        spec,
    ]
    run(cmd, timeout=120)


def logs_tail(lines=260):
    res = run(["docker", "logs", "--tail", str(lines), NAME], check=False, timeout=60)
    return (res.stdout + res.stderr)[-24000:]


def main():
    result = {
        "label": "final_retained_dflash_context_sweep",
        "image": IMAGE,
        "model": MODEL,
        "draft": DRAFT,
        "input_buckets": INPUT_BUCKETS,
        "output_tokens": 512,
        "concurrency": 1,
        "requests": 1,
        "rounds": 3,
        "warmup": 2,
        "config": {
            "gpu_memory_utilization": 0.92,
            "max_model_len": 262144,
            "block_size": 1280,
            "max_num_batched_tokens": 8192,
            "max_num_seqs": 1,
            "async_scheduling": False,
            "speculative_method": "dflash",
            "num_speculative_tokens": 10,
            "moe_tuned_config": False,
        },
        "runs": [],
    }
    try:
        launch()
        result["ready_s"] = wait_ready()
        result["startup_log_tail"] = logs_tail()
        for input_tokens in INPUT_BUCKETS:
            item = {"input_tokens_target": input_tokens}
            try:
                before = metrics()
                item["benchmark"] = bench(input_tokens)
                after = metrics()
                item["metrics_delta"] = delta(before, after)
                item["status"] = "ok"
            except Exception as exc:
                item["status"] = "error"
                item["error"] = str(exc)
                item["log_tail"] = logs_tail()
            result["runs"].append(item)
            OUT.write_text(json.dumps(result, indent=2, ensure_ascii=False))
    finally:
        subprocess.run(["docker", "rm", "-f", NAME], text=True, capture_output=True)
        result["final_log_tail"] = logs_tail() if False else ""
        OUT.write_text(json.dumps(result, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
