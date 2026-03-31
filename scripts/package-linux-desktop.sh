#!/usr/bin/env bash

set -euo pipefail

if [ "$#" -ne 4 ]; then
  printf 'usage: %s <binary> <arch> <version> <out-dir>\n' "$0" >&2
  exit 1
fi

BINARY="$1"
ARCH="$2"
VERSION="$3"
OUT_DIR="$4"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SVG_ICON="$ROOT_DIR/packaging/icons/aima.svg"
PNG_ICON="$ROOT_DIR/packaging/icons/aima-256.png"
BUNDLE_DIR="$OUT_DIR/aima-linux-${ARCH}-desktop"
ARCHIVE_PATH="$OUT_DIR/aima-linux-${ARCH}-desktop.tar.gz"

if [ ! -f "$BINARY" ]; then
  printf 'missing binary: %s\n' "$BINARY" >&2
  exit 1
fi
if [ ! -f "$SVG_ICON" ] || [ ! -f "$PNG_ICON" ]; then
  printf 'missing linux desktop icon assets\nrun ./scripts/build-platform-icons.sh first\n' >&2
  exit 1
fi

rm -rf "$BUNDLE_DIR" "$ARCHIVE_PATH"
mkdir -p "$BUNDLE_DIR/bin" "$BUNDLE_DIR/share/icons"

cp "$BINARY" "$BUNDLE_DIR/bin/aima"
chmod +x "$BUNDLE_DIR/bin/aima"
cp "$SVG_ICON" "$BUNDLE_DIR/share/icons/aima.svg"
cp "$PNG_ICON" "$BUNDLE_DIR/share/icons/aima-256.png"

cat > "$BUNDLE_DIR/install.sh" <<'EOF'
#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_ROOT="${XDG_DATA_HOME:-$HOME/.local/share}"
BIN_ROOT="${XDG_BIN_HOME:-$HOME/.local/bin}"
APP_DIR="$INSTALL_ROOT/applications"
ICON_ROOT="$INSTALL_ROOT/icons/hicolor"
ICON_SVG_DIR="$ICON_ROOT/scalable/apps"
ICON_PNG_DIR="$ICON_ROOT/256x256/apps"
EXEC_PATH="$BIN_ROOT/aima"
ICON_PATH="$ICON_PNG_DIR/aima.png"
DESKTOP_PATH="$APP_DIR/aima.desktop"

mkdir -p "$BIN_ROOT" "$APP_DIR" "$ICON_SVG_DIR" "$ICON_PNG_DIR"

install -m 0755 "$ROOT_DIR/bin/aima" "$EXEC_PATH"
install -m 0644 "$ROOT_DIR/share/icons/aima.svg" "$ICON_SVG_DIR/aima.svg"
install -m 0644 "$ROOT_DIR/share/icons/aima-256.png" "$ICON_PATH"

cat > "$DESKTOP_PATH" <<DESKTOP
[Desktop Entry]
Type=Application
Version=1.0
Name=AIMA
Comment=AI inference manager for edge devices
Exec=$EXEC_PATH
Icon=aima
Terminal=false
Categories=Development;Utility;
Keywords=AI;LLM;Inference;MCP;
StartupNotify=true
DESKTOP

if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database "$APP_DIR" >/dev/null 2>&1 || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
  gtk-update-icon-cache "$INSTALL_ROOT/icons/hicolor" >/dev/null 2>&1 || true
fi

printf 'installed AIMA desktop launcher at %s\n' "$DESKTOP_PATH"
EOF

chmod +x "$BUNDLE_DIR/install.sh"

cat > "$BUNDLE_DIR/README.txt" <<EOF
AIMA Linux desktop bundle
Version: $VERSION
Architecture: $ARCH

Run ./install.sh to install:
- ~/.local/bin/aima
- ~/.local/share/applications/aima.desktop
- ~/.local/share/icons/hicolor/.../aima.{svg,png}
EOF

tar -C "$OUT_DIR" -czf "$ARCHIVE_PATH" "$(basename "$BUNDLE_DIR")"

printf 'linux desktop bundle written to %s and %s\n' "$BUNDLE_DIR" "$ARCHIVE_PATH"
