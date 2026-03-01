#!/usr/bin/env bash
set -euo pipefail

MODEL=${1:-/home/quings/data/models/Qwen3-30B-A3B-Q4_K_M.gguf}
OUT=${2:-/tmp/bench-30b-moe-xdna}
mkdir -p "$OUT"
cd ~/llama.cpp

run_case() {
  local name="$1"
  shift
  /usr/bin/time -f "%e" -o "$OUT/${name}.wall" env "$@" \
    ./build-xdna/bin/llama-bench \
      -m "$MODEL" -r 2 -o jsonl -n 1 -p 2048 -b 2048 -ub 512 \
      -ngl 999 -fa 1 -t 16 -dev Vulkan0/XDNA \
      > "$OUT/${name}.jsonl" 2> "$OUT/${name}.err"

  python3 - "$OUT/${name}.jsonl" "$OUT/${name}.wall" "$name" <<"PY"
import json,sys
jf,wf,name=sys.argv[1],sys.argv[2],sys.argv[3]
rows=[]
for ln in open(jf,'r',encoding='utf-8',errors='ignore'):
    ln=ln.strip()
    if ln.startswith('{'):
        rows.append(json.loads(ln))
pref=[r for r in rows if r.get('n_prompt')==2048 and r.get('n_gen')==0]
dec=[r for r in rows if r.get('n_prompt')==0 and r.get('n_gen')==1]
wall=open(wf).read().strip()
pt=pref[0]['avg_ts'] if pref else None
dt=dec[0]['avg_ts'] if dec else None
print(f"{name}\tprefill_tps={pt}\tdecode_tps={dt}\twall={wall}s")
PY
}

echo "case\tprefill_tps\tdecode_tps\twall"
run_case offload_off \
  GGML_XDNA_OFFLOAD_MOE=0 GGML_XDNA_RUNNER_ENABLE=0 GGML_XDNA_SELFTEST=0 GGML_LOG_LEVEL=INFO
run_case offload_on_e8 \
  GGML_XDNA_OFFLOAD_MOE=1 GGML_XDNA_RUNNER_ENABLE=1 GGML_XDNA_RUNNER_ASYNC=1 GGML_XDNA_TRIGGER_EVERY=8 \
  GGML_XDNA_SELFTEST=0 GGML_XDNA_PREFILL_N_MIN=1 GGML_XDNA_MOE_LOG_LIMIT=0 GGML_LOG_LEVEL=INFO
run_case offload_on_e1 \
  GGML_XDNA_OFFLOAD_MOE=1 GGML_XDNA_RUNNER_ENABLE=1 GGML_XDNA_RUNNER_ASYNC=1 GGML_XDNA_TRIGGER_EVERY=1 \
  GGML_XDNA_SELFTEST=0 GGML_XDNA_PREFILL_N_MIN=1 GGML_XDNA_MOE_LOG_LIMIT=0 GGML_LOG_LEVEL=INFO
