#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

dev_series="$(tr -d '\n' < internal/buildinfo/series.txt 2>/dev/null || echo "v0.2")"
exact_tag="$(git tag --points-at HEAD --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-version:refname | head -n 1)"
version="${exact_tag:-${dev_series}-dev}"
out_dir="${1:-$ROOT_DIR/build/release/$version}"

hash_cmd() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$@"
    return
  fi
  shasum -a 256 "$@"
}

mkdir -p "$out_dir"

make all

cp "$ROOT_DIR/build/aima-darwin-arm64" "$out_dir/aima-darwin-arm64"
cp "$ROOT_DIR/build/aima-linux-amd64" "$out_dir/aima-linux-amd64"
cp "$ROOT_DIR/build/aima-linux-arm64" "$out_dir/aima-linux-arm64"
cp "$ROOT_DIR/build/aima.exe" "$out_dir/aima-windows-amd64.exe"

(
  cd "$out_dir"
  rm -f checksums.txt
  hash_cmd \
    aima-darwin-arm64 \
    aima-linux-amd64 \
    aima-linux-arm64 \
    aima-windows-amd64.exe \
    > checksums.txt
)

printf 'release assets written to %s\n' "$out_dir"
