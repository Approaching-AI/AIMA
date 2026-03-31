#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ICON_PATH="$ROOT_DIR/packaging/icons/aima.ico"
OUT_PATH="$ROOT_DIR/cmd/aima/aima_windows_amd64.syso"
RSRC_VERSION="v0.10.2"

if [ ! -f "$ICON_PATH" ]; then
  printf 'missing icon asset: %s\nrun ./scripts/build-platform-icons.sh first\n' "$ICON_PATH" >&2
  exit 1
fi

GOWORK=off go run "github.com/akavel/rsrc@${RSRC_VERSION}" -arch amd64 -ico "$ICON_PATH" -o "$OUT_PATH"

printf 'windows syso written to %s\n' "$OUT_PATH"
