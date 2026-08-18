import json
import re
import subprocess
import time
import urllib.request
from pathlib import Path

PORT = 18081
NAME = "qwen36-dflash-exp"
MODEL_DIR = "/home/qujing/aima-codex-qwen36/models"
IMAGE = "qujing/vllm-gb10-dflash-fa2:latest"
MODEL = "/models/Qwen3.6-35B-A3B"
DRAFT = "/models/Qwen3.6-35B-A3B-DFlash"
PROMPT_FILE = Path("/tmp/qwen3.6-gb10-dflash-round4-business-prompt.txt")
OUT = Path("/tmp/qwen3.6-gb10-dflash-round14-cudagraph-screen.json")

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

CASES = [
    {
        "label": "cudagraph_capture_size_1",
        "extra_args": [
            "--cudagraph-capture-sizes",
            "1",
            "--max-cudagraph-capture-size",
            "1",
        ],
        "extra_env": [],
    },
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


def bench(case_label, prompt):
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
            "1024",
            "--input-tokens",
            "65536",
            "--warmup",
            "2",
            "--min-output-ratio",
            "1",
            "--max-retries",
            "1",
            "--no-save",
            "--prompt",
            prompt,
            "--notes",
            f"round14 {case_label} 64k 1k business screen",
        ],
        timeout=2800,
    )
    return json.loads(res.stdout)


def launch_case(case):
    subprocess.run(["docker", "rm", "-f", NAME], text=True, capture_output=True)
    spec = json.dumps(
        {
            "method": "dflash",
            "model": DRAFT,
            "num_speculative_tokens": 10,
        },
        separators=(",", ":"),
    )
    env = [
        ("VLLM_KV_CACHE_LAYOUT", "NHD"),
        ("VLLM_SKIP_GDN_PREFILL_WARMUP", "1"),
        ("FLA_GDN_FIX_BT", "1"),
        ("VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS", "1"),
        *case["extra_env"],
    ]
    cmd = [
        "docker",
        "run",
        "-d",
        "--name",
        NAME,
        "--gpus",
        "all",
    ]
    for key, value in env:
        cmd.extend(["-e", f"{key}={value}"])
    cmd.extend(
        [
            "-p",
            f"{PORT}:8000",
            "-v",
            f"{MODEL_DIR}:/models",
            IMAGE,
            MODEL,
            *BASE_ARGS,
            *case["extra_args"],
            "--speculative-config",
            spec,
        ]
    )
    run(cmd, timeout=120)


def logs_tail():
    res = run(["docker", "logs", "--tail", "220", NAME], check=False, timeout=60)
    return (res.stdout + res.stderr)[-20000:]


def write_results(all_results):
    OUT.write_text(json.dumps(all_results, indent=2, ensure_ascii=False))


def main():
    prompt = PROMPT_FILE.read_text()
    all_results = {"cases": []}
    for case in CASES:
        result = {
            "label": case["label"],
            "extra_args": case["extra_args"],
            "extra_env": case["extra_env"],
            "gpu_memory_utilization": "0.92",
            "max_num_batched_tokens": "8192",
            "max_num_seqs": "1",
            "async_scheduling": False,
            "block_size": "1280",
            "num_speculative_tokens": 10,
            "enforce_eager": False,
            "prompt_file": str(PROMPT_FILE),
        }
        try:
            launch_case(case)
            result["ready_s"] = wait_ready()
            before = metrics()
            result["benchmark"] = bench(case["label"], prompt)
            after = metrics()
            result["metrics_delta"] = delta(before, after)
            result["status"] = "ok"
            result["logs_tail"] = logs_tail()
        except Exception as e:
            result["status"] = "error"
            result["error"] = str(e)
            result["logs_tail"] = logs_tail()
        all_results["cases"].append(result)
        write_results(all_results)
    subprocess.run(["docker", "rm", "-f", NAME], text=True, capture_output=True)


if __name__ == "__main__":
    main()
