#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${AIMA_BIN:-}"
TMP_ROOT="${AIMA_FIRST_RUN_SMOKE_DIR:-}"
CLEANUP_TMP=0

if [ -z "$TMP_ROOT" ]; then
  TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/aima-first-run-smoke.XXXXXX")"
  CLEANUP_TMP=1
else
  mkdir -p "$TMP_ROOT"
fi

cleanup() {
  if [ "${AIMA_KEEP_SMOKE_DIR:-}" = "1" ]; then
    echo "first-run smoke artifacts kept at $TMP_ROOT" >&2
    return
  fi
  if [ "$CLEANUP_TMP" = "1" ]; then
    rm -rf "$TMP_ROOT"
  fi
}
trap cleanup EXIT

ARTIFACTS="$TMP_ROOT/artifacts"
DATA_DIR="$TMP_ROOT/data"
mkdir -p "$ARTIFACTS" "$DATA_DIR"

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required for first-run smoke JSON validation" >&2
  exit 127
fi

if [ -z "$BIN" ]; then
  BIN="$TMP_ROOT/aima"
  echo "==> building smoke binary: $BIN" >&2
  (cd "$ROOT_DIR" && go build -o "$BIN" ./cmd/aima)
fi

run_capture() {
  name="$1"
  shift
  echo "==> $name: aima $*" >&2
  AIMA_DATA_DIR="$DATA_DIR" "$BIN" "$@" >"$ARTIFACTS/$name.out" 2>"$ARTIFACTS/$name.err"
}

json_check() {
  file="$1"
  python3 - "$file" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    json.load(f)
PY
}

run_capture hal-detect hal detect
json_check "$ARTIFACTS/hal-detect.out"

run_capture onboarding-start-json onboarding start --json
json_check "$ARTIFACTS/onboarding-start-json.out"

NEXT_MODEL="$(python3 - "$ARTIFACTS/onboarding-start-json.out" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    payload = json.load(f)

next_model = str(payload.get("next_model") or "").strip()
next_command = str(payload.get("next_command") or "").strip()
status = payload.get("status") or {}
stack = status.get("stack_status") or {}
recommend = payload.get("recommend") or {}
recommendations = recommend.get("recommendations") or []

if not isinstance(stack.get("needs_init"), bool):
    raise SystemExit("status.stack_status.needs_init must be a boolean")
if not recommendations:
    raise SystemExit("onboarding start returned no recommendations")
if not next_model:
    raise SystemExit("onboarding start did not choose a next_model")
if next_command != "aima run " + next_model:
    raise SystemExit(f"next_command={next_command!r} does not match next_model={next_model!r}")

print(next_model)
PY
)"

run_capture onboarding-human onboarding --locale en
grep -F "AIMA first-run guide" "$ARTIFACTS/onboarding-human.out" >/dev/null
grep -F "Next: aima run $NEXT_MODEL" "$ARTIFACTS/onboarding-human.out" >/dev/null

run_capture onboarding-recommend-json onboarding recommend --json
json_check "$ARTIFACTS/onboarding-recommend-json.out"
python3 - "$ARTIFACTS/onboarding-recommend-json.out" "$NEXT_MODEL" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    payload = json.load(f)

next_model = sys.argv[2]
recommendations = payload.get("recommendations") or []
if not recommendations:
    raise SystemExit("onboarding recommend returned no recommendations")
top = str(recommendations[0].get("model_name") or "")
if top != next_model:
    raise SystemExit(f"top recommendation {top!r} does not match onboarding start next_model {next_model!r}")
PY

run_capture deploy-dry-run deploy "$NEXT_MODEL" --dry-run
json_check "$ARTIFACTS/deploy-dry-run.out"
python3 - "$ARTIFACTS/deploy-dry-run.out" "$NEXT_MODEL" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    payload = json.load(f)

next_model = sys.argv[2]
if payload.get("model") != next_model:
    raise SystemExit(f"dry-run model {payload.get('model')!r} does not match {next_model!r}")
if not payload.get("engine"):
    raise SystemExit("dry-run did not resolve an engine")
if not payload.get("runtime"):
    raise SystemExit("dry-run did not resolve a runtime")
PY

echo "first-run smoke passed: next_model=$NEXT_MODEL"
