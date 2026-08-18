import json
import os
import re
import subprocess
import time
import urllib.request
from pathlib import Path

PORT = int(os.environ.get("QWEN36_SWEEP_PORT", "18082"))
NAME = os.environ.get("QWEN36_SWEEP_NAME", "qwen36-dflash-concurrency-sweep")
MODEL_DIR = os.environ.get("QWEN36_MODEL_DIR", "/home/qujing/aima-codex-qwen36/models")
AIMA_BIN = os.environ.get("AIMA_BIN", "/home/qujing/aima-current-5a89755")
IMAGE = os.environ.get("QWEN36_IMAGE", "qujing/vllm-gb10-dflash-fa2:latest")
MODEL = "/models/Qwen3.6-35B-A3B"
DRAFT = "/models/Qwen3.6-35B-A3B-DFlash"
OUT = Path(
    os.environ.get(
        "QWEN36_SWEEP_OUT",
        "/tmp/qwen3.6-gb10-dflash-concurrency-sweep.json",
    )
)

INPUT_BUCKETS = [int(x) for x in os.environ.get("QWEN36_SWEEP_INPUTS", "1024,32768,65536,131072").split(",") if x]
OUTPUT_BUCKETS = [int(x) for x in os.environ.get("QWEN36_SWEEP_OUTPUTS", "128,512").split(",") if x]
CONCURRENCY = int(os.environ.get("QWEN36_SWEEP_CONCURRENCY", "4"))
REQUESTS = int(os.environ.get("QWEN36_SWEEP_REQUESTS", "4"))
ROUNDS = int(os.environ.get("QWEN36_SWEEP_ROUNDS", "1"))
WARMUP = int(os.environ.get("QWEN36_SWEEP_WARMUP", "1"))
MAX_RETRIES = int(os.environ.get("QWEN36_SWEEP_MAX_RETRIES", "1"))
MIN_OUTPUT_RATIO = os.environ.get("QWEN36_SWEEP_MIN_OUTPUT_RATIO", "1")
MAX_NUM_SEQS = os.environ.get("QWEN36_SWEEP_MAX_NUM_SEQS", "1")
MAX_NUM_BATCHED_TOKENS = os.environ.get("QWEN36_SWEEP_MAX_NUM_BATCHED_TOKENS", "8192")
MAX_MODEL_LEN = os.environ.get("QWEN36_SWEEP_MAX_MODEL_LEN", "262144")
BLOCK_SIZE = os.environ.get("QWEN36_SWEEP_BLOCK_SIZE", "1280")
GPU_MEMORY_UTILIZATION = os.environ.get("QWEN36_SWEEP_GPU_MEMORY_UTILIZATION", "0.92")
NUM_SPECULATIVE_TOKENS = int(os.environ.get("QWEN36_SWEEP_NUM_SPECULATIVE_TOKENS", "10"))
ASYNC_SCHEDULING = os.environ.get("QWEN36_SWEEP_ASYNC_SCHEDULING", "0").lower() in ("1", "true", "yes", "on")

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
    GPU_MEMORY_UTILIZATION,
    "--max-model-len",
    MAX_MODEL_LEN,
    "--block-size",
    BLOCK_SIZE,
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
    MAX_NUM_BATCHED_TOKENS,
    "--max-num-seqs",
    MAX_NUM_SEQS,
]

if not ASYNC_SCHEDULING:
    BASE_ARGS.append("--no-async-scheduling")


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
        d["request_prefill_time_seconds_sum"] / d["request_prefill_time_seconds_count"]
        if d["request_prefill_time_seconds_count"]
        else None
    )
    d["avg_decode_s_per_req"] = (
        d["request_decode_time_seconds_sum"] / d["request_decode_time_seconds_count"]
        if d["request_decode_time_seconds_count"]
        else None
    )
    return d


def docker_stats():
    res = run(
        [
            "docker",
            "stats",
            "--no-stream",
            "--format",
            "{{json .}}",
            NAME,
        ],
        check=False,
        timeout=20,
    )
    if res.returncode != 0 or not res.stdout.strip():
        return {}
    try:
        return json.loads(res.stdout.strip().splitlines()[-1])
    except json.JSONDecodeError:
        return {"raw": res.stdout.strip()}


def nvidia_smi():
    res = run(
        [
            "nvidia-smi",
            "--query-gpu=name,memory.used,memory.total,utilization.gpu,power.draw",
            "--format=csv,noheader,nounits",
        ],
        check=False,
        timeout=20,
    )
    if res.returncode != 0:
        return {"error": res.stderr.strip() or res.stdout.strip()}
    return {"raw": res.stdout.strip()}


def bench(input_tokens, output_tokens):
    res = run(
        [
            AIMA_BIN,
            "benchmark",
            "run",
            "--endpoint",
            f"http://127.0.0.1:{PORT}",
            "--model",
            "Qwen3.6-35B-A3B",
            "--concurrency",
            str(CONCURRENCY),
            "--requests",
            str(REQUESTS),
            "--rounds",
            str(ROUNDS),
            "--max-tokens",
            str(output_tokens),
            "--input-tokens",
            str(input_tokens),
            "--warmup",
            str(WARMUP),
            "--min-output-ratio",
            MIN_OUTPUT_RATIO,
            "--max-retries",
            str(MAX_RETRIES),
            "--no-save",
            "--notes",
            f"retained-dflash concurrency={CONCURRENCY} input={input_tokens} output={output_tokens} max_num_seqs={MAX_NUM_SEQS}",
        ],
        timeout=1800,
    )
    return json.loads(res.stdout)


def launch():
    subprocess.run(["docker", "rm", "-f", NAME], text=True, capture_output=True)
    spec = json.dumps(
        {
            "method": "dflash",
            "model": DRAFT,
            "num_speculative_tokens": NUM_SPECULATIVE_TOKENS,
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


def result_summary(payload, metrics_delta):
    result = payload.get("result", {})
    profile = payload.get("benchmark_profile", {})
    tpot = result.get("tpot_p50_ms") or 0
    ttft = result.get("ttft_p50_ms") or 0
    actual_input = result.get("avg_input_tokens") or profile.get("avg_input_tokens")
    actual_output = result.get("avg_output_tokens") or profile.get("avg_output_tokens")
    return {
        "actual_input_tokens": actual_input,
        "actual_output_tokens": actual_output,
        "successful_requests": result.get("successful_requests"),
        "failed_requests": result.get("failed_requests"),
        "error_rate": result.get("error_rate"),
        "ttft_p50_ms": ttft,
        "ttft_p95_ms": result.get("ttft_p95_ms"),
        "tpot_p50_ms": tpot,
        "tpot_p95_ms": result.get("tpot_p95_ms"),
        "decode_tps_from_tpot_p50": (1000.0 / tpot) if tpot else None,
        "end_to_end_output_tps": result.get("throughput_tps"),
        "qps": result.get("qps"),
        "duration_ms": result.get("duration_ms"),
        "draft_acceptance_rate": metrics_delta.get("draft_acceptance_rate"),
        "mean_acceptance_length": metrics_delta.get("mean_acceptance_length"),
        "avg_prefill_s_per_req": metrics_delta.get("avg_prefill_s_per_req"),
        "avg_decode_s_per_req": metrics_delta.get("avg_decode_s_per_req"),
    }


def main():
    result = {
        "label": "retained_dflash_concurrency_sweep",
        "host": run(["hostname"], check=False).stdout.strip(),
        "image": IMAGE,
        "model": MODEL,
        "draft": DRAFT,
        "aima_bin": AIMA_BIN,
        "input_buckets": INPUT_BUCKETS,
        "output_buckets": OUTPUT_BUCKETS,
        "concurrency": CONCURRENCY,
        "requests": REQUESTS,
        "rounds": ROUNDS,
        "warmup": WARMUP,
        "config": {
            "gpu_memory_utilization": float(GPU_MEMORY_UTILIZATION),
            "max_model_len": int(MAX_MODEL_LEN),
            "block_size": int(BLOCK_SIZE),
            "max_num_batched_tokens": int(MAX_NUM_BATCHED_TOKENS),
            "max_num_seqs": int(MAX_NUM_SEQS),
            "async_scheduling": ASYNC_SCHEDULING,
            "speculative_method": "dflash",
            "num_speculative_tokens": NUM_SPECULATIVE_TOKENS,
            "moe_tuned_config": False,
        },
        "system_before": {
            "nvidia_smi": nvidia_smi(),
        },
        "runs": [],
    }
    try:
        launch()
        result["ready_s"] = wait_ready()
        result["startup_log_tail"] = logs_tail()
        for input_tokens in INPUT_BUCKETS:
            for output_tokens in OUTPUT_BUCKETS:
                item = {
                    "input_tokens_target": input_tokens,
                    "output_tokens_target": output_tokens,
                }
                started = time.time()
                try:
                    before = metrics()
                    item["docker_stats_before"] = docker_stats()
                    item["benchmark"] = bench(input_tokens, output_tokens)
                    after = metrics()
                    item["docker_stats_after"] = docker_stats()
                    item["metrics_delta"] = delta(before, after)
                    item["summary"] = result_summary(
                        item["benchmark"], item["metrics_delta"]
                    )
                    item["status"] = "ok"
                except Exception as exc:
                    item["status"] = "error"
                    item["error"] = str(exc)
                    item["log_tail"] = logs_tail()
                item["elapsed_s"] = round(time.time() - started, 1)
                result["runs"].append(item)
                OUT.write_text(json.dumps(result, indent=2, ensure_ascii=False))
    finally:
        result["final_log_tail"] = logs_tail() if result.get("ready_s") else ""
        result["system_after"] = {"nvidia_smi": nvidia_smi()}
        subprocess.run(["docker", "rm", "-f", NAME], text=True, capture_output=True)
        OUT.write_text(json.dumps(result, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
