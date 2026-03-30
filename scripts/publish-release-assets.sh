#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

tag="${1:-}"
if [[ -z "$tag" ]]; then
  tag="$(git tag --points-at HEAD --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-version:refname | head -n 1)"
fi

if [[ -z "$tag" ]]; then
  printf 'error: no release tag provided and HEAD is not tagged with vX.Y.Z\n' >&2
  exit 1
fi

if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  printf 'error: tag must match vX.Y.Z, got %s\n' "$tag" >&2
  exit 1
fi

asset_dir="${2:-$ROOT_DIR/build/release/$tag}"

if ! command -v gh >/dev/null 2>&1; then
  printf 'error: gh CLI is required\n' >&2
  exit 1
fi

if [[ ! -d "$asset_dir" ]]; then
  printf 'error: asset directory not found: %s\n' "$asset_dir" >&2
  exit 1
fi

required_assets=(
  "aima-darwin-arm64"
  "aima-linux-amd64"
  "aima-linux-arm64"
  "aima-windows-amd64.exe"
  "checksums.txt"
)

for asset in "${required_assets[@]}"; do
  if [[ ! -f "$asset_dir/$asset" ]]; then
    printf 'error: missing required asset: %s\n' "$asset_dir/$asset" >&2
    exit 1
  fi
done

if ! gh release view "$tag" >/dev/null 2>&1; then
  gh release create "$tag" \
    --verify-tag \
    --title "Release $tag" \
    --generate-notes
fi

gh release upload "$tag" \
  "$asset_dir/aima-darwin-arm64" \
  "$asset_dir/aima-linux-amd64" \
  "$asset_dir/aima-linux-arm64" \
  "$asset_dir/aima-windows-amd64.exe" \
  "$asset_dir/checksums.txt" \
  --clobber

printf 'published release assets for %s from %s\n' "$tag" "$asset_dir"
