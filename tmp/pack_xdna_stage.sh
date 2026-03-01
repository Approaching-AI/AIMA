#!/usr/bin/env bash
set -euo pipefail
ts=$(date +%Y%m%d-%H%M%S)
stage=~/artifacts/amd395-xdna-stage-$ts
mkdir -p ~/artifacts
mkdir -p "$stage"
cp ~/llama.cpp/ggml/src/ggml-xdna/ggml-xdna.cpp "$stage/"
cp ~/llama.cpp/docs/amd395-xdna-moe-progress-20260226.md "$stage/"
cp /tmp/xdna-test-none.log "$stage/" 2>/dev/null || true
cp /tmp/xdna-test-moe.log "$stage/" 2>/dev/null || true
cp /tmp/ggml-xdna-selftest.log "$stage/" 2>/dev/null || true
cp /tmp/ggml-xdna-runner.log "$stage/" 2>/dev/null || true
tar -czf "$stage.tar.gz" -C ~/artifacts "amd395-xdna-stage-$ts"
echo "$stage.tar.gz"
