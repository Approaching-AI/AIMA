#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

series="$(tr -d '[:space:]' <internal/buildinfo/series.txt)"
if [[ ! "$series" =~ ^v[0-9]+\.[0-9]+$ ]]; then
  echo "invalid development series: $series" >&2
  exit 1
fi

version="${series}-dev"
git_commit="${GIT_COMMIT:-$(git rev-parse HEAD)}"
if [[ ! "$git_commit" =~ ^([0-9a-fA-F]{40}|[0-9a-fA-F]{64})$ ]]; then
  echo "GIT_COMMIT must be a full 40 or 64 character hexadecimal commit: $git_commit" >&2
  exit 1
fi

build_time="${BUILD_TIME:-$(date -u '+%Y-%m-%dT%H:%M:%SZ')}"
if [[ ! "$build_time" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]; then
  echo "BUILD_TIME must use UTC RFC 3339 format (YYYY-MM-DDTHH:MM:SSZ): $build_time" >&2
  exit 1
fi

output_dir="${OUTPUT_DIR:-$ROOT_DIR/dist/amd395-windows}"
short_commit="${git_commit:0:12}"
filename="aima-windows-amd64-${version}-amd-strix-halo-${short_commit}.exe"
module="github.com/jguan/aima/internal/buildinfo"
ldflags="-s -w -X ${module}.Version=${version} -X ${module}.BuildTime=${build_time} -X ${module}.GitCommit=${git_commit}"

mkdir -p "$output_dir"

GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
  -buildvcs=false \
  -ldflags "$ldflags" \
  -o "$output_dir/$filename" \
  ./cmd/aima

if command -v sha256sum >/dev/null 2>&1; then
  checksum="$(sha256sum "$output_dir/$filename" | awk '{print $1}')"
else
  checksum="$(shasum -a 256 "$output_dir/$filename" | awk '{print $1}')"
fi
printf '%s  %s\n' "$checksum" "$filename" >"$output_dir/checksums.txt"

cat >"$output_dir/build-metadata.json" <<EOF
{
  "version": "$version",
  "git_commit": "$git_commit",
  "build_time": "$build_time",
  "target_os": "windows",
  "target_arch": "amd64",
  "filename": "$filename"
}
EOF

printf 'AMD395 Windows package written to %s\n' "$output_dir"
printf 'Artifact file: %s\n' "$filename"
