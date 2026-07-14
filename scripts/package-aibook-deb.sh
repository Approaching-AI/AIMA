#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

BINARY="${1:-$ROOT_DIR/build/aima-linux-arm64}"
VERSION="${2:-}"
OUT_DIR="${3:-$ROOT_DIR/build/release}"
ARCH="arm64"
PACKAGE="aima-aibook"
TEMPLATE_DIR="$ROOT_DIR/packaging/aibook-deb"
STAGE_DIR="$ROOT_DIR/build/${PACKAGE}-root"

if [ ! -d "$TEMPLATE_DIR" ]; then
  printf 'missing package template: %s\n' "$TEMPLATE_DIR" >&2
  exit 1
fi

if [ ! -f "$BINARY" ]; then
  if [ -f "$ROOT_DIR/build-arm64/aima" ]; then
    BINARY="$ROOT_DIR/build-arm64/aima"
  else
    printf 'missing arm64 binary: %s\n' "$BINARY" >&2
    printf 'run: GOOS=linux GOARCH=arm64 go build -ldflags "$LDFLAGS" -o build/aima-linux-arm64 ./cmd/aima\n' >&2
    exit 1
  fi
fi

if [ -z "$VERSION" ]; then
  product_version="$(tr -d '\n' < "$ROOT_DIR/packaging/aibook-version.txt" 2>/dev/null || echo "v0.4.1")"
  build_date="$(date +%Y%m%d)"
  VERSION="${product_version#v}+aibook${build_date}.1"
fi

case "$VERSION" in
  v*) VERSION="${VERSION#v}" ;;
esac

if ! command -v dpkg-deb >/dev/null 2>&1; then
  printf 'dpkg-deb is required to build the AIBook deb package\n' >&2
  exit 1
fi

if command -v readelf >/dev/null 2>&1; then
  if readelf -S "$BINARY" | grep -Eq '\.debug_|\.symtab'; then
    printf 'refusing to package unstripped binary: %s\n' "$BINARY" >&2
    printf 'run: make aibook-deb\n' >&2
    printf 'or rebuild with Go linker flags that include -s -w before packaging.\n' >&2
    exit 1
  fi
fi

rm -rf "$STAGE_DIR"
mkdir -p "$STAGE_DIR" "$OUT_DIR"
cp -a "$TEMPLATE_DIR/." "$STAGE_DIR/"
find "$STAGE_DIR" -type d -exec chmod 0755 {} +

install -m 0755 "$BINARY" "$STAGE_DIR/usr/local/bin/aima"
chmod 0755 "$STAGE_DIR/usr/local/bin/aima-ui"
chmod 0755 "$STAGE_DIR/DEBIAN/postinst" "$STAGE_DIR/DEBIAN/prerm" "$STAGE_DIR/DEBIAN/postrm"

installed_size_kib="$(du -sk "$STAGE_DIR" | awk '{print $1}')"
sed -i \
  -e "s/^Version:.*/Version: $VERSION/" \
  -e "s/^Architecture:.*/Architecture: $ARCH/" \
  -e "s/^Installed-Size:.*/Installed-Size: $installed_size_kib/" \
  "$STAGE_DIR/DEBIAN/control"

OUT_PATH="$OUT_DIR/${PACKAGE}_${VERSION}_${ARCH}.deb"
dpkg-deb --build --root-owner-group "$STAGE_DIR" "$OUT_PATH"

printf 'AIBook deb package written to %s\n' "$OUT_PATH"
