#!/usr/bin/env bash

set -euo pipefail

if [ "$#" -ne 3 ]; then
  printf 'usage: %s <binary> <version> <out-dir>\n' "$0" >&2
  exit 1
fi

BINARY="$1"
VERSION="$2"
OUT_DIR="$3"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ICNS_PATH="$ROOT_DIR/packaging/icons/aima.icns"
APP_NAME="AIMA.app"
APP_DIR="$OUT_DIR/$APP_NAME"
ZIP_PATH="$OUT_DIR/AIMA-darwin-arm64-app.zip"
bundle_version="${VERSION#v}"
if [[ "$bundle_version" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  bundle_version="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.${BASH_REMATCH[3]}"
elif [[ "$bundle_version" =~ ^([0-9]+)\.([0-9]+)-dev$ ]]; then
  bundle_version="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.0"
else
  bundle_version="0.0.0"
fi

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'missing required command: %s\n' "$1" >&2
    exit 1
  fi
}

need_cmd ditto

if [ ! -f "$BINARY" ]; then
  printf 'missing binary: %s\n' "$BINARY" >&2
  exit 1
fi
if [ ! -f "$ICNS_PATH" ]; then
  printf 'missing icon asset: %s\nrun ./scripts/build-platform-icons.sh first\n' "$ICNS_PATH" >&2
  exit 1
fi

rm -rf "$APP_DIR" "$ZIP_PATH"
mkdir -p "$APP_DIR/Contents/MacOS" "$APP_DIR/Contents/Resources"

cp "$BINARY" "$APP_DIR/Contents/MacOS/AIMA"
chmod +x "$APP_DIR/Contents/MacOS/AIMA"
cp "$ICNS_PATH" "$APP_DIR/Contents/Resources/AIMA.icns"

cat > "$APP_DIR/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key>
  <string>en</string>
  <key>CFBundleDisplayName</key>
  <string>AIMA</string>
  <key>CFBundleExecutable</key>
  <string>AIMA</string>
  <key>CFBundleIconFile</key>
  <string>AIMA.icns</string>
  <key>CFBundleIdentifier</key>
  <string>com.jguan.aima</string>
  <key>CFBundleInfoDictionaryVersion</key>
  <string>6.0</string>
  <key>CFBundleName</key>
  <string>AIMA</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleShortVersionString</key>
  <string>${bundle_version}</string>
  <key>CFBundleVersion</key>
  <string>${bundle_version}</string>
  <key>AIMABuildVersion</key>
  <string>${VERSION}</string>
  <key>LSMinimumSystemVersion</key>
  <string>13.0</string>
  <key>NSHighResolutionCapable</key>
  <true/>
</dict>
</plist>
EOF

ditto -c -k --sequesterRsrc --keepParent "$APP_DIR" "$ZIP_PATH"

printf 'macOS app written to %s and %s\n' "$APP_DIR" "$ZIP_PATH"
