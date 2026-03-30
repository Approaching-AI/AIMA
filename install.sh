#!/bin/sh

set -eu

AIMA_REPO="${AIMA_REPO:-Approaching-AI/AIMA}"
AIMA_VERSION="${AIMA_VERSION:-}"
AIMA_INSTALL_DIR="${AIMA_INSTALL_DIR:-}"

say() {
  printf '%s\n' "$*"
}

fail() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

curl_get() {
  url="$1"
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    curl -fsSL \
      --retry 3 \
      --retry-delay 2 \
      --retry-all-errors \
      --connect-timeout 15 \
      --max-time 300 \
      -H "Authorization: Bearer ${GITHUB_TOKEN}" \
      "$url"
    return
  fi
  curl -fsSL \
    --retry 3 \
    --retry-delay 2 \
    --retry-all-errors \
    --connect-timeout 15 \
    --max-time 300 \
    "$url"
}

resolve_platform_asset() {
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"

  case "$os" in
    linux) os="linux" ;;
    darwin) os="darwin" ;;
    *)
      fail "unsupported OS: $os (supported: linux, darwin)"
      ;;
  esac

  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *)
      fail "unsupported architecture: $arch (supported: amd64, arm64)"
      ;;
  esac

  case "$os/$arch" in
    linux/amd64) printf 'aima-linux-amd64\n' ;;
    linux/arm64) printf 'aima-linux-arm64\n' ;;
    darwin/arm64) printf 'aima-darwin-arm64\n' ;;
    *)
      fail "unsupported platform: $os/$arch"
      ;;
  esac
}

resolve_latest_product_tag() {
  tags_json="$(curl_get "https://api.github.com/repos/${AIMA_REPO}/tags?per_page=100")"
  tag="$(
    printf '%s\n' "$tags_json" \
      | sed -n 's/.*"name"[[:space:]]*:[[:space:]]*"\(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)".*/\1/p' \
      | awk -F. '
        {
          tag = $0
          gsub(/^v/, "", $0)
          printf "%09d %09d %09d %s\n", $1, $2, $3, tag
        }
      ' \
      | sort \
      | tail -n 1 \
      | awk '{print $4}'
  )"

  [ -n "$tag" ] || fail "no product tag found in ${AIMA_REPO}"
  printf '%s\n' "$tag"
}

resolve_latest_installable_release() {
  releases_json="$(curl_get "https://api.github.com/repos/${AIMA_REPO}/releases?per_page=100")"
  tag="$(
    printf '%s\n' "$releases_json" \
      | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)".*/\1/p' \
      | awk -F. '
        {
          tag = $0
          gsub(/^v/, "", $0)
          printf "%09d %09d %09d %s\n", $1, $2, $3, tag
        }
      ' \
      | sort \
      | tail -n 1 \
      | awk '{print $4}'
  )"

  [ -n "$tag" ] || fail "no installable product release found in ${AIMA_REPO}"
  printf '%s\n' "$tag"
}

resolve_install_dir() {
  if [ -n "$AIMA_INSTALL_DIR" ]; then
    printf '%s\n' "$AIMA_INSTALL_DIR"
    return
  fi

  if [ -w /usr/local/bin ]; then
    printf '/usr/local/bin\n'
    return
  fi

  [ -n "${HOME:-}" ] || fail "HOME is not set and /usr/local/bin is not writable; set AIMA_INSTALL_DIR"
  printf '%s\n' "$HOME/.local/bin"
}

sha256_file() {
  file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
    return
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
    return
  fi
  fail "sha256sum or shasum is required for checksum verification"
}

install_binary() {
  src="$1"
  dst="$2"
  if command -v install >/dev/null 2>&1; then
    install -m 0755 "$src" "$dst"
    return
  fi
  cp "$src" "$dst"
  chmod 0755 "$dst"
}

need_cmd curl
need_cmd uname
need_cmd sed
need_cmd awk
need_cmd sort
need_cmd mktemp

asset="$(resolve_platform_asset)"
latest_tag="$(resolve_latest_product_tag)"
version="${AIMA_VERSION:-$(resolve_latest_installable_release)}"
install_dir="$(resolve_install_dir)"

tmpdir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT INT TERM

asset_url="https://github.com/${AIMA_REPO}/releases/download/${version}/${asset}"
checksums_url="https://github.com/${AIMA_REPO}/releases/download/${version}/checksums.txt"
asset_path="${tmpdir}/${asset}"
checksums_path="${tmpdir}/checksums.txt"

say "AIMA repo: ${AIMA_REPO}"
say "AIMA version: ${version}"
say "AIMA asset: ${asset}"

if [ -z "$AIMA_VERSION" ] && [ "$version" != "$latest_tag" ]; then
  say "Warning: latest product tag is ${latest_tag}, but latest installable binary release is ${version}."
fi

if ! curl_get "$asset_url" >"$asset_path"; then
  fail "release asset not found: ${asset_url}. Publish ${asset} for tag ${version} first, or set AIMA_VERSION/AIMA_REPO."
fi

if ! curl_get "$checksums_url" >"$checksums_path"; then
  fail "checksums.txt not found for ${version}. Publish release checksums before using the installer."
fi

expected="$(
  awk -v asset="$asset" '
    $2 == asset { print $1; exit }
  ' "$checksums_path"
)"
[ -n "$expected" ] || fail "checksum entry for ${asset} not found in checksums.txt"

actual="$(sha256_file "$asset_path")"
[ "$actual" = "$expected" ] || fail "checksum mismatch for ${asset}: expected ${expected}, got ${actual}"

mkdir -p "$install_dir"
install_binary "$asset_path" "${install_dir}/aima"

say "Installed to ${install_dir}/aima"

case ":${PATH:-}:" in
  *":${install_dir}:"*) ;;
  *)
    say "Add ${install_dir} to PATH if needed."
    ;;
esac

say "Run: aima version"
