#!/usr/bin/env bash
set -euo pipefail
cd ~/llama.cpp
MODEL=/home/quings/data/models/Qwen3-30B-A3B-Q4_K_M.gguf
OUT=/tmp/bench-30b-moe
mkdir -p "$OUT"

run_bench() {
  local name="$1"; shift
  echo "== $name =="
  /usr/bin/time -f '%e' -o "$OUT/${name}.wall" env "$@" \
    ./build-xdna/bin/llama-bench \
      -m "$MODEL" \
      -r 3 \
      -o jsonl \
      -n 1 \
      -p 2048 \
      -b 2048 \
      -ub 512 \
      -ngl 999 \
      -fa 1 \
      -t 16 \
      > "$OUT/${name}.jsonl" 2> "$OUT/${name}.err"
}

run_bench vk_only \
  GGML_XDNA_OFFLOAD_MOE=0 \
  GGML_XDNA_RUNNER_ENABLE=0 \
  GGML_XDNA_SELFTEST=0 \
  GGML_LOG_LEVEL=INFO \
  LLAMA_ARG_DEVICE=Vulkan0

run_bench vk_xdna_async_e8 \
  GGML_XDNA_OFFLOAD_MOE=1 \
  GGML_XDNA_RUNNER_ENABLE=1 \
  GGML_XDNA_RUNNER_ASYNC=1 \
  GGML_XDNA_TRIGGER_EVERY=8 \
  GGML_XDNA_SELFTEST=0 \
  GGML_XDNA_PREFILL_N_MIN=1 \
  GGML_XDNA_MOE_LOG_LIMIT=0 \
  GGML_LOG_LEVEL=INFO \
  LLAMA_ARG_DEVICE=Vulkan0,XDNA

run_bench vk_xdna_async_e1 \
  GGML_XDNA_OFFLOAD_MOE=1 \
  GGML_XDNA_RUNNER_ENABLE=1 \
  GGML_XDNA_RUNNER_ASYNC=1 \
  GGML_XDNA_TRIGGER_EVERY=1 \
  GGML_XDNA_SELFTEST=0 \
  GGML_XDNA_PREFILL_N_MIN=1 \
  GGML_XDNA_MOE_LOG_LIMIT=0 \
  GGML_LOG_LEVEL=INFO \
  LLAMA_ARG_DEVICE=Vulkan0,XDNA

python3 - <<'PY'
import json,glob,os
out='/tmp/bench-30b-moe'
for p in sorted(glob.glob(out+'/*.jsonl')):
    name=os.path.basename(p).replace('.jsonl','')
    rows=[]
    with open(p,'r',encoding='utf-8',errors='ignore') as f:
        for line in f:
            line=line.strip()
            if not line: continue
            try: rows.append(json.loads(line))
            except: pass
    tps=[]
    for r in rows:
        v=r.get('t/s')
        if isinstance(v,(int,float)): tps.append(float(v))
    avg=sum(tps)/len(tps) if tps else 0.0
    print(name,'runs',len(tps),'avg_tps',round(avg,4))
PY

for f in "$OUT"/*.wall; do echo "$(basename "$f") $(cat "$f")s"; done
