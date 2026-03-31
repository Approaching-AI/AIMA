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

if [ "$(uname -s)" = "Darwin" ]; then
  bash "$ROOT_DIR/scripts/build-platform-icons.sh"
  bash "$ROOT_DIR/scripts/build-windows-syso.sh"
fi

make all

cp "$ROOT_DIR/build/aima-darwin-arm64" "$out_dir/aima-darwin-arm64"
cp "$ROOT_DIR/build/aima-linux-amd64" "$out_dir/aima-linux-amd64"
cp "$ROOT_DIR/build/aima-linux-arm64" "$out_dir/aima-linux-arm64"
cp "$ROOT_DIR/build/aima.exe" "$out_dir/aima-windows-amd64.exe"

if [ "$(uname -s)" = "Darwin" ] && [ -f "$ROOT_DIR/packaging/icons/aima.icns" ]; then
  bash "$ROOT_DIR/scripts/package-macos-app.sh" "$ROOT_DIR/build/aima-darwin-arm64" "$version" "$out_dir"
fi

if [ -f "$ROOT_DIR/packaging/icons/aima.svg" ] && [ -f "$ROOT_DIR/packaging/icons/aima-256.png" ]; then
  bash "$ROOT_DIR/scripts/package-linux-desktop.sh" "$ROOT_DIR/build/aima-linux-amd64" amd64 "$version" "$out_dir"
  bash "$ROOT_DIR/scripts/package-linux-desktop.sh" "$ROOT_DIR/build/aima-linux-arm64" arm64 "$version" "$out_dir"
fi

(
  cd "$out_dir"
  rm -f checksums.txt
  files=(
    aima-darwin-arm64
    aima-linux-amd64
    aima-linux-arm64
    aima-windows-amd64.exe
  )
  for optional in \
    AIMA-darwin-arm64-app.zip \
    aima-linux-amd64-desktop.tar.gz \
    aima-linux-arm64-desktop.tar.gz
  do
    if [ -f "$optional" ]; then
      files+=("$optional")
    fi
  done
  hash_cmd "${files[@]}" > checksums.txt
)

printf 'release assets written to %s\n' "$out_dir"
