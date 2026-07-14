#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKFLOW="$ROOT_DIR/.github/workflows/amd395-windows-build.yml"

test -f "$WORKFLOW"

required_contract=(
  'name: AMD395 Windows Build'
  'pull_request:'
  'push:'
  '- amd395-win'
  'contents: read'
  'github.event.pull_request.head.sha || github.sha'
  'uses: actions/checkout@v6'
  'uses: actions/setup-go@v6'
  'go-version-file: go.mod'
  'go test ./...'
  'make amd395-build-test'
  'bash ./scripts/package-amd395-windows.sh'
  'sha256sum -c checksums.txt'
  'uses: actions/upload-artifact@v7'
  'retention-days: 30'
  'if-no-files-found: error'
)

for expected in "${required_contract[@]}"; do
  if ! grep -F -- "$expected" "$WORKFLOW" >/dev/null; then
    echo "workflow contract missing: $expected" >&2
    exit 1
  fi
done
