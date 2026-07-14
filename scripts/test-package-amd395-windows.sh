#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

mkdir -p "$TMP_DIR/bin" "$TMP_DIR/out"

cat >"$TMP_DIR/bin/go" <<'FAKE_GO'
#!/usr/bin/env bash

set -euo pipefail

printf 'GOOS=%s\nGOARCH=%s\nCGO_ENABLED=%s\nARGS=%s\n' \
  "${GOOS:-}" "${GOARCH:-}" "${CGO_ENABLED:-}" "$*" >"$FAKE_GO_RECORD"

while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    printf 'fake windows executable\n' >"$1"
    exit 0
  fi
  shift
done

echo "fake go did not receive -o" >&2
exit 1
FAKE_GO
chmod +x "$TMP_DIR/bin/go"

commit="0123456789abcdef0123456789abcdef01234567"
build_time="2026-07-14T08:00:00Z"
series="$(tr -d '[:space:]' <"$ROOT_DIR/internal/buildinfo/series.txt")"
version="${series}-dev"
expected="aima-windows-amd64-${version}-amd-strix-halo-0123456789ab.exe"

PATH="$TMP_DIR/bin:$PATH" \
FAKE_GO_RECORD="$TMP_DIR/go-record.txt" \
GIT_COMMIT="$commit" \
BUILD_TIME="$build_time" \
OUTPUT_DIR="$TMP_DIR/out" \
bash "$ROOT_DIR/scripts/package-amd395-windows.sh"

test -s "$TMP_DIR/out/$expected"

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$TMP_DIR/out" && sha256sum -c checksums.txt)
else
  (cd "$TMP_DIR/out" && shasum -a 256 -c checksums.txt)
fi

python3 - "$TMP_DIR/out/build-metadata.json" "$commit" "$expected" "$version" "$build_time" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as fh:
    data = json.load(fh)

assert data == {
    "version": sys.argv[4],
    "git_commit": sys.argv[2],
    "build_time": sys.argv[5],
    "target_os": "windows",
    "target_arch": "amd64",
    "filename": sys.argv[3],
}
PY

grep -F 'GOOS=windows' "$TMP_DIR/go-record.txt"
grep -F 'GOARCH=amd64' "$TMP_DIR/go-record.txt"
grep -F 'CGO_ENABLED=0' "$TMP_DIR/go-record.txt"
grep -F "Version=$version" "$TMP_DIR/go-record.txt"
grep -F "BuildTime=$build_time" "$TMP_DIR/go-record.txt"
grep -F "GitCommit=$commit" "$TMP_DIR/go-record.txt"

if PATH="$TMP_DIR/bin:$PATH" \
  FAKE_GO_RECORD="$TMP_DIR/short-go-record.txt" \
  GIT_COMMIT="0123456789ab" \
  BUILD_TIME="$build_time" \
  OUTPUT_DIR="$TMP_DIR/short-out" \
  bash "$ROOT_DIR/scripts/package-amd395-windows.sh"
then
  echo "package script accepted a shortened commit instead of a full SHA" >&2
  exit 1
fi
