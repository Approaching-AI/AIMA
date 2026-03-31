#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage:
  transcribe.sh <audio-file> [--out /path/to/out.txt] [--json]
EOF
  exit 2
}

if [[ "${1:-}" == "" || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
fi

input="${1:-}"
shift || true

out=""
response_format="text"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) out="${2:-}"; shift 2 ;;
    --json) response_format="json"; shift 1 ;;
    *) echo "Unknown arg: $1" >&2; usage ;;
  esac
done

if [[ ! -f "$input" ]]; then
  echo "File not found: $input" >&2
  exit 1
fi

AIMA_BASE_URL="${AIMA_BASE_URL:-http://127.0.0.1:6188/v1}"
OPENCLAW_CONFIG_PATH="${OPENCLAW_CONFIG_PATH:-$HOME/.openclaw/openclaw.json}"

resolve_asr_model() {
  python3 - <<'PY'
import json
import os
from pathlib import Path

path = Path(os.environ.get("OPENCLAW_CONFIG_PATH", Path.home() / ".openclaw" / "openclaw.json"))
fallback = "qwen3-asr-1.7b"
try:
    data = json.loads(path.read_text())
except Exception:
    print(fallback)
    raise SystemExit(0)

models = (
    data.get("tools", {})
    .get("media", {})
    .get("audio", {})
    .get("models", [])
)
for entry in models:
    model = entry.get("model")
    if isinstance(model, str) and model:
        print(model)
        break
else:
    print(fallback)
PY
}

ASR_MODEL="${AIMA_ASR_MODEL:-$(resolve_asr_model)}"

# Call AIMA ASR API (OpenAI-compatible /v1/audio/transcriptions)
result=$(curl -sS -X POST "${AIMA_BASE_URL}/audio/transcriptions" \
  -F "model=${ASR_MODEL}" \
  -F "file=@${input}" \
  -F "response_format=${response_format}")

if [[ "$out" != "" ]]; then
  mkdir -p "$(dirname "$out")"
  echo "$result" > "$out"
  echo "$out"
else
  # Extract text from JSON response
  if [[ "$response_format" == "json" ]]; then
    echo "$result"
  else
    echo "$result" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('text',''))" 2>/dev/null || echo "$result"
  fi
fi
