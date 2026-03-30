#!/usr/bin/env bash
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

tag="${1:-bundle/stack/2026-02-26}"
source_tag="${2:-v0.1.0-images}"
target_commit="$(git rev-list -n 1 "$source_tag")"

if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  echo "tag already exists locally: $tag"
  git rev-parse "$tag"
  exit 0
fi

git tag -a "$tag" "$target_commit" -m "Legacy stack bundle tag replacing $source_tag"
echo "created local tag $tag -> $target_commit"
echo "push with: git push origin refs/tags/$tag"
