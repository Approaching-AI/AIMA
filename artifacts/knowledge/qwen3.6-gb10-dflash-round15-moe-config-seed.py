import json
import os
import re
import subprocess
import time
import urllib.request
from pathlib import Path

PORT = 18081
NAME = "qwen36-dflash-exp"
MODEL_DIR = "/home/qujing/aima-codex-qwen36/models"
MOE_CONFIG_DIR = os.environ.get(
    "MOE_CONFIG_DIR", "/home/qujing/aima-codex-qwen36/moe-configs/qwen36-gb10-seed"
)
IMAGE = "qujing/vllm-gb10-dflash-fa2:latest"
MODEL = "/models/Qwen3.6-35B-A3B"
DRAFT = "/models/Qwen3.6-35B-A3B-DFlash"
PROMPT_FILE = Path("/tmp/qwen3.6-gb10-dflash-round4-business-prompt.txt")
OUT = Path(
    os.environ.get(
        "ROUND15_OUT", "/tmp/qwen3.6-gb10-dflash-round15-moe-config-seed.json"
    )
)
INPUT_TOKENS = [
    int(value)
    for value in os.environ.get("ROUND15_INPUT_TOKENS", "65536").split(",")
    if value.strip()
]

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

CASE = {
    "label": os.environ.get("ROUND15_LABEL", "moe_config_h100_seed"),
    "extra_args": [],
    "extra_env": [
        ("VLLM_TUNED_CONFIG_FOLDER", "/moe-configs"),
    ],
}


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


def bench(case_label, input_tokens, prompt):
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
            str(input_tokens),
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
            f"round15 {case_label} {input_tokens//1024}k 1k business moe-config-seed",
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
            "-v",
            f"{MOE_CONFIG_DIR}:/moe-configs:ro",
            IMAGE,
            MODEL,
            *BASE_ARGS,
            *case["extra_args"],
            "--speculative-config",
            spec,
        ]
    )
    run(cmd, timeout=120)


def logs_tail(lines=260):
    res = run(["docker", "logs", "--tail", str(lines), NAME], check=False, timeout=60)
    return (res.stdout + res.stderr)[-24000:]


def moe_config_lines(logs):
    needles = ("Using configuration from", "Using default MoE config")
    return [line for line in logs.splitlines() if any(n in line for n in needles)]


def write_results(all_results):
    OUT.write_text(json.dumps(all_results, indent=2, ensure_ascii=False))


def main():
    prompt = PROMPT_FILE.read_text()
    all_results = {"cases": []}
    result = {
        "label": CASE["label"],
        "extra_args": CASE["extra_args"],
        "extra_env": CASE["extra_env"],
        "gpu_memory_utilization": "0.92",
        "max_num_batched_tokens": "8192",
        "max_num_seqs": "1",
        "async_scheduling": False,
        "block_size": "1280",
        "num_speculative_tokens": 10,
        "enforce_eager": False,
        "moe_config_dir": MOE_CONFIG_DIR,
        "prompt_file": str(PROMPT_FILE),
    }
    try:
        launch_case(CASE)
        result["ready_s"] = wait_ready()
        logs_after_ready = logs_tail()
        result["moe_config_log_lines_after_ready"] = moe_config_lines(logs_after_ready)
        result["runs"] = []
        for input_tokens in INPUT_TOKENS:
            before = metrics()
            benchmark = bench(CASE["label"], input_tokens, prompt)
            after = metrics()
            result["runs"].append(
                {
                    "input_tokens_target": input_tokens,
                    "benchmark": benchmark,
                    "metrics_delta": delta(before, after),
                }
            )
        if len(result["runs"]) == 1:
            result["benchmark"] = result["runs"][0]["benchmark"]
            result["metrics_delta"] = result["runs"][0]["metrics_delta"]
        result["status"] = "ok"
        result["logs_tail"] = logs_tail()
        result["moe_config_log_lines"] = moe_config_lines(result["logs_tail"])
    except Exception as e:
        result["status"] = "error"
        result["error"] = str(e)
        result["logs_tail"] = logs_tail()
        result["moe_config_log_lines"] = moe_config_lines(result["logs_tail"])
    finally:
        subprocess.run(["docker", "rm", "-f", NAME], text=True, capture_output=True)
        all_results["cases"].append(result)
        write_results(all_results)


if __name__ == "__main__":
    main()
