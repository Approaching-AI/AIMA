#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage:
  speak.sh <text> [--filename output.wav] [--voice default]
EOF
  exit 2
}

if [[ "${1:-}" == "" || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
fi

text="${1:-}"
shift || true

filename="$(date +%Y-%m-%d-%H-%M-%S)-speech.wav"
voice="default"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --filename) filename="${2:-}"; shift 2 ;;
    --voice) voice="${2:-}"; shift 2 ;;
    *) echo "Unknown arg: $1" >&2; usage ;;
  esac
done

AIMA_BASE_URL="${AIMA_BASE_URL:-http://127.0.0.1:6188/v1}"
OPENCLAW_CONFIG_PATH="${OPENCLAW_CONFIG_PATH:-$HOME/.openclaw/openclaw.json}"

resolve_tts_model() {
  python3 - <<'PY'
import json
import os
from pathlib import Path

path = Path(os.environ.get("OPENCLAW_CONFIG_PATH", Path.home() / ".openclaw" / "openclaw.json"))
fallback = "qwen3-tts-0.6b"
try:
    data = json.loads(path.read_text())
except Exception:
    print(fallback)
    raise SystemExit(0)

tts = data.get("messages", {}).get("tts", {})
providers = tts.get("providers", {})
model = providers.get("openai", {}).get("model") or tts.get("openai", {}).get("model")
if isinstance(model, str) and model:
    print(model)
else:
    print(fallback)
PY
}

TTS_MODEL="${AIMA_TTS_MODEL:-$(resolve_tts_model)}"

outdir="${HOME}/.openclaw/workspace/audio"
mkdir -p "$outdir"
outpath="${outdir}/${filename}"

# Call AIMA TTS API (OpenAI-compatible /v1/audio/speech)
curl -sS -X POST "${AIMA_BASE_URL}/audio/speech" \
  -H "Content-Type: application/json" \
  -d "{\"model\": \"${TTS_MODEL}\", \"input\": $(printf '%s' "$text" | python3 -c 'import sys,json; print(json.dumps(sys.stdin.read()))'), \"voice\": \"${voice}\"}" \
  -o "$outpath"

size=$(stat -c%s "$outpath" 2>/dev/null || stat -f%z "$outpath" 2>/dev/null || echo 0)
if [[ "$size" -lt 100 ]]; then
  echo "Error: TTS returned empty or error response" >&2
  cat "$outpath" >&2
  exit 1
fi

echo "Audio saved: ${outpath} (${size} bytes)"
echo "MEDIA: ${outpath}"
