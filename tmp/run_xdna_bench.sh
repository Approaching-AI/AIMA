#!/usr/bin/env bash
set -euo pipefail
cd ~/llama.cpp
mkdir -p /tmp/xdna-bench

run_case() {
  local name="$1"
  shift
  local envs=("$@")
  local log="/tmp/xdna-bench/${name}.log"
  local tfile="/tmp/xdna-bench/${name}.time"
  rm -f "$log" "$tfile"
  /usr/bin/time -f '%e' -o "$tfile" env \
    GGML_LOG_LEVEL=INFO \
    GGML_XDNA_OFFLOAD_MOE=1 \
    GGML_XDNA_SELFTEST=0 \
    GGML_XDNA_PREFILL_N_MIN=1 \
    GGML_XDNA_MOE_LOG_LIMIT=0 \
    "${envs[@]}" \
    ./build-xdna/bin/test-backend-ops test -b XDNA -o MUL_MAT_ID > "$log" 2>&1

  local elapsed
  elapsed=$(cat "$tfile")
  local pass_line
  pass_line=$(grep -E '[0-9]+/[0-9]+ tests passed' "$log" | tail -n 1 || true)
  local backend_ok
  backend_ok=$(grep -E 'Backend XDNA: .*OK' "$log" | tail -n 1 || true)
  printf '%s\t%s\t%s\t%s\n' "$name" "$elapsed" "$pass_line" "$backend_ok"
}

echo -e 'case\tseconds\tpass\tbackend'
run_case npu_off GGML_XDNA_RUNNER_ENABLE=0
run_case npu_async_e1 GGML_XDNA_RUNNER_ENABLE=1 GGML_XDNA_RUNNER_ASYNC=1 GGML_XDNA_TRIGGER_EVERY=1
run_case npu_async_e8 GGML_XDNA_RUNNER_ENABLE=1 GGML_XDNA_RUNNER_ASYNC=1 GGML_XDNA_TRIGGER_EVERY=8
run_case npu_sync_e1 GGML_XDNA_RUNNER_ENABLE=1 GGML_XDNA_RUNNER_ASYNC=0 GGML_XDNA_TRIGGER_EVERY=1
