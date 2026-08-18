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
OUT = Path("/tmp/qwen3.6-gb10-dflash-round10-block-spec-business.json")

CASES = [
    {
        "label": "business_noneager_cgprof_block1536_spec10",
        "block_size": "1536",
        "num_speculative_tokens": 10,
    },
    {
        "label": "business_noneager_cgprof_block2048_spec10",
        "block_size": "2048",
        "num_speculative_tokens": 10,
    },
    {
        "label": "business_noneager_cgprof_block1280_spec11",
        "block_size": "1280",
        "num_speculative_tokens": 11,
    },
    {
        "label": "business_noneager_cgprof_block1280_spec12",
        "block_size": "1280",
        "num_speculative_tokens": 12,
    },
]


def run(cmd, check=True, timeout=None):
    res = subprocess.run(
        cmd,
        text=True,
        capture_output=True,
        timeout=timeout,
    )
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


def bench(case_label, inp, prompt):
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
            str(inp),
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
            f"round10 {case_label} {inp//1024}k 1k business",
        ],
        timeout=2600,
    )
    return json.loads(res.stdout)


def launch_case(case):
    subprocess.run(["docker", "rm", "-f", NAME], text=True, capture_output=True)
    spec = json.dumps(
        {
            "method": "dflash",
            "model": DRAFT,
            "num_speculative_tokens": case["num_speculative_tokens"],
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
        case["block_size"],
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
        "--speculative-config",
        spec,
    ]
    run(cmd, timeout=120)


def main():
    prompt = PROMPT_FILE.read_text()
    all_results = {"cases": []}
    for case in CASES:
        result = {
            "label": case["label"],
            "block_size": case["block_size"],
            "num_speculative_tokens": case["num_speculative_tokens"],
            "gpu_memory_utilization": "0.92",
            "extra_env": [["VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS", "1"]],
            "enforce_eager": False,
            "prompt_file": str(PROMPT_FILE),
        }
        try:
            launch_case(case)
            result["ready_s"] = wait_ready()
            result["runs"] = []
            for inp in (32768, 65536):
                before = metrics()
                b = bench(case["label"], inp, prompt)
                after = metrics()
                d = delta(before, after)
                result["runs"].append(
                    {
                        "input_tokens_target": inp,
                        "benchmark": b,
                        "metrics_delta": d,
                    }
                )
            result["status"] = "ok"
        except Exception as e:
            result["status"] = "error"
            result["error"] = str(e)
            try:
                result["log_tail"] = run(
                    ["docker", "logs", "--tail", "200", NAME],
                    check=False,
                    timeout=60,
                ).stdout
            except Exception:
                result["log_tail"] = ""
        finally:
            subprocess.run(["docker", "rm", "-f", NAME], text=True, capture_output=True)
            all_results["cases"].append(result)
            OUT.write_text(json.dumps(all_results, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
