#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

AIMA_SYNC_REF="${AIMA_SYNC_REF:-origin/develop}"
AIMA_SYNC_FETCH="${AIMA_SYNC_FETCH:-true}"
AIMA_SYNC_ASSET_DIR="${AIMA_SYNC_ASSET_DIR:-}"
AIMA_DOWNLOAD_SSH_TARGET="${AIMA_DOWNLOAD_SSH_TARGET:-qjyw}"
AIMA_DOWNLOAD_REMOTE_ROOT="${AIMA_DOWNLOAD_REMOTE_ROOT:-/root/aima-service/.data/aima-downloads}"
AIMA_DOWNLOAD_VERSION_LABEL="${AIMA_DOWNLOAD_VERSION_LABEL:-}"
AIMA_DOWNLOAD_SSH_KEY="${AIMA_DOWNLOAD_SSH_KEY:-}"
AIMA_DOWNLOAD_SSH_KEY_FILE="${AIMA_DOWNLOAD_SSH_KEY_FILE:-}"
AIMA_DOWNLOAD_STRICT_HOST_KEY_CHECKING="${AIMA_DOWNLOAD_STRICT_HOST_KEY_CHECKING:-accept-new}"

required_assets=(
  aima-darwin-arm64
  aima-linux-amd64
  aima-linux-arm64
  aima-windows-amd64.exe
  checksums.txt
)

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

remote_quote() {
  printf '%q' "$1"
}

normalize_path() {
  local value="$1"
  value="${value%/}"
  printf '%s' "$value"
}

resolve_asset_dir() {
  local worktree="$1"
  local out_dir="$2"

  if [[ -n "$AIMA_SYNC_ASSET_DIR" ]]; then
    [[ -d "$AIMA_SYNC_ASSET_DIR" ]] || fail "AIMA_SYNC_ASSET_DIR does not exist: $AIMA_SYNC_ASSET_DIR"
    (cd "$ROOT_DIR" && realpath "$AIMA_SYNC_ASSET_DIR")
    return
  fi

  (cd "$worktree" && bash scripts/package-release.sh "$out_dir" >&2)
  printf '%s' "$out_dir"
}

verify_assets() {
  local asset_dir="$1"
  local asset

  for asset in "${required_assets[@]}"; do
    [[ -f "$asset_dir/$asset" ]] || fail "missing release asset: $asset_dir/$asset"
  done
}

write_manifest() {
  local asset_dir="$1"
  local ref="$2"
  local commit="$3"
  local version_label="$4"
  local built_at

  built_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  cat >"$asset_dir/official-site-sync.json" <<JSON
{
  "ref": "$ref",
  "commit": "$commit",
  "version_label": "$version_label",
  "built_at": "$built_at"
}
JSON
}

build_ssh_command() {
  ssh_cmd=(ssh)

  if [[ -n "$AIMA_DOWNLOAD_SSH_KEY_FILE" ]]; then
    ssh_cmd+=(-i "$AIMA_DOWNLOAD_SSH_KEY_FILE" -o IdentitiesOnly=yes)
  fi
  if [[ -n "$AIMA_DOWNLOAD_STRICT_HOST_KEY_CHECKING" ]]; then
    ssh_cmd+=(-o "StrictHostKeyChecking=$AIMA_DOWNLOAD_STRICT_HOST_KEY_CHECKING")
  fi
}

need_cmd git
need_cmd tar
need_cmd realpath

tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/aima-official-sync.XXXXXX")"
cleanup() {
  rm -rf "$tmp_root"
}
trap cleanup EXIT INT TERM

if [[ -n "$AIMA_DOWNLOAD_SSH_KEY" ]]; then
  AIMA_DOWNLOAD_SSH_KEY_FILE="$tmp_root/official-site-sync.key"
  printf '%s\n' "$AIMA_DOWNLOAD_SSH_KEY" >"$AIMA_DOWNLOAD_SSH_KEY_FILE"
  chmod 600 "$AIMA_DOWNLOAD_SSH_KEY_FILE"
fi

if [[ "$AIMA_SYNC_FETCH" == "true" && -z "$AIMA_SYNC_ASSET_DIR" ]]; then
  say "Fetching develop ref from origin..."
  git fetch origin +refs/heads/develop:refs/remotes/origin/develop
fi

source_commit="$(git rev-parse "$AIMA_SYNC_REF")"
source_commit_short="$(git rev-parse --short=12 "$source_commit")"
version_label="${AIMA_DOWNLOAD_VERSION_LABEL:-develop-${source_commit_short}}"
remote_root="$(normalize_path "$AIMA_DOWNLOAD_REMOTE_ROOT")"
remote_tmp="${remote_root}/.sync-${version_label}-$$"
remote_release="${remote_root}/releases/${version_label}"
remote_latest="${remote_root}/latest"

worktree="$tmp_root/worktree"
asset_dir="$tmp_root/release-assets"

if [[ -z "$AIMA_SYNC_ASSET_DIR" ]]; then
  say "Creating temporary worktree for $AIMA_SYNC_REF ($source_commit_short)..."
  git worktree add --detach "$worktree" "$source_commit"
else
  worktree="$ROOT_DIR"
fi

asset_dir="$(resolve_asset_dir "$worktree" "$asset_dir")"
verify_assets "$asset_dir"
write_manifest "$asset_dir" "$AIMA_SYNC_REF" "$source_commit" "$version_label"
build_ssh_command

say "Syncing AIMA assets to ${AIMA_DOWNLOAD_SSH_TARGET}:${remote_release}"
"${ssh_cmd[@]}" "$AIMA_DOWNLOAD_SSH_TARGET" \
  "set -euo pipefail; mkdir -p $(remote_quote "$remote_root/releases"); rm -rf $(remote_quote "$remote_tmp"); mkdir -p $(remote_quote "$remote_tmp")"

tar -C "$asset_dir" -czf - . | "${ssh_cmd[@]}" "$AIMA_DOWNLOAD_SSH_TARGET" \
  "set -euo pipefail; tar -xzf - -C $(remote_quote "$remote_tmp")"

"${ssh_cmd[@]}" "$AIMA_DOWNLOAD_SSH_TARGET" \
  "set -euo pipefail; rm -rf $(remote_quote "$remote_release"); mv $(remote_quote "$remote_tmp") $(remote_quote "$remote_release"); ln -sfn $(remote_quote "releases/${version_label}") $(remote_quote "$remote_latest"); printf '%s\n' $(remote_quote "$version_label") > $(remote_quote "$remote_root/current.txt")"

say "Official site downloads now point to ${version_label}"
say "Latest URL: https://aimaserver.com/_downloads/aima/latest/"
