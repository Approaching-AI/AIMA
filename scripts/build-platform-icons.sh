#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATIC_DIR="$ROOT_DIR/internal/ui/static"
PACKAGING_DIR="$ROOT_DIR/packaging/icons"
SRC="$STATIC_DIR/favicon.svg"
TMP_DIR="$(mktemp -d)"
ICONSET_DIR="$TMP_DIR/aima.iconset"
FAVICON_PNG="$TMP_DIR/favicon-64.png"
trap 'rm -rf "$TMP_DIR"' EXIT

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'missing required command: %s\n' "$1" >&2
    exit 1
  fi
}

need_cmd sips
need_cmd iconutil

mkdir -p "$PACKAGING_DIR"
mkdir -p "$ICONSET_DIR"

render_png() {
  local size="$1"
  local out="$2"
  sips -s format png "$SRC" --resampleHeightWidthMax "$size" --out "$out" >/dev/null
}

render_png 64 "$FAVICON_PNG"
sips -s format ico "$FAVICON_PNG" --out "$STATIC_DIR/favicon.ico" >/dev/null
render_png 180 "$STATIC_DIR/apple-touch-icon.png"

cp "$SRC" "$PACKAGING_DIR/aima.svg"
render_png 256 "$PACKAGING_DIR/aima-256.png"
render_png 512 "$PACKAGING_DIR/aima-512.png"
render_png 1024 "$PACKAGING_DIR/aima-1024.png"
sips -s format ico "$PACKAGING_DIR/aima-256.png" --out "$PACKAGING_DIR/aima.ico" >/dev/null

render_png 16 "$ICONSET_DIR/icon_16x16.png"
render_png 32 "$ICONSET_DIR/icon_16x16@2x.png"
render_png 32 "$ICONSET_DIR/icon_32x32.png"
render_png 64 "$ICONSET_DIR/icon_32x32@2x.png"
render_png 128 "$ICONSET_DIR/icon_128x128.png"
render_png 256 "$ICONSET_DIR/icon_128x128@2x.png"
render_png 256 "$ICONSET_DIR/icon_256x256.png"
render_png 512 "$ICONSET_DIR/icon_256x256@2x.png"
render_png 512 "$ICONSET_DIR/icon_512x512.png"
render_png 1024 "$ICONSET_DIR/icon_512x512@2x.png"
iconutil -c icns "$ICONSET_DIR" -o "$PACKAGING_DIR/aima.icns"

printf 'platform icons written to %s and %s\n' "$STATIC_DIR" "$PACKAGING_DIR"
