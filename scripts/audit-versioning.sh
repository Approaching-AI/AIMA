#!/usr/bin/env bash
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

curl_ok() {
  curl -fsI -L --retry 3 --retry-all-errors --retry-delay 2 --max-redirs 3 "$1" >/dev/null 2>&1
}

strict=0
if [[ "${1:-}" == "--strict" ]]; then
  strict=1
fi

product_tags=()
legacy_tags=()

while IFS= read -r tag; do
  [[ -z "$tag" ]] && continue
  if [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    product_tags+=("$tag")
  else
    legacy_tags+=("$tag")
  fi
done < <(git tag --list 'v*' --sort=version:refname)

echo "Product release tags:"
if [[ ${#product_tags[@]} -eq 0 ]]; then
  echo "  (none)"
else
  printf '  %s\n' "${product_tags[@]}"
fi

echo
echo "Legacy product-like tags:"
if [[ ${#legacy_tags[@]} -eq 0 ]]; then
  echo "  (none)"
else
  printf '  %s\n' "${legacy_tags[@]}"
fi

echo
echo "Legacy tag references in tracked docs/catalog:"
for tag in "${legacy_tags[@]}"; do
  echo "  [$tag]"
  rg -n --fixed-strings "$tag" AGENTS.md catalog docs design README.md README_zh.md || true
done

echo
echo "Suggested migration targets:"
for tag in "${legacy_tags[@]}"; do
  case "$tag" in
    v0.1.0-images)
      echo "  $tag -> bundle/stack/2026-02-26"
      ;;
    v0.0.1-metax)
      echo "  $tag -> no replacement tag; keep history in release notes/changelog only"
      ;;
    *)
      echo "  $tag -> choose a non-product namespace before next cleanup"
      ;;
  esac
done

replacement_tag="bundle/stack/2026-02-26"
echo
echo "Remote replacement tag status:"
if git ls-remote --exit-code --tags origin "refs/tags/$replacement_tag" >/dev/null 2>&1; then
  echo "  $replacement_tag exists on origin"
else
  echo "  $replacement_tag missing on origin"
fi

legacy_asset="https://github.com/Approaching-AI/AIMA/releases/download/v0.1.0-images/hami-airgap-images-amd64.tar"
replacement_asset="https://github.com/Approaching-AI/AIMA/releases/download/$replacement_tag/hami-airgap-images-amd64.tar"
echo
echo "Asset probe:"
if curl_ok "$legacy_asset"; then
  echo "  legacy asset reachable: $legacy_asset"
else
  echo "  legacy asset missing:   $legacy_asset"
fi
if curl_ok "$replacement_asset"; then
  echo "  replacement reachable:  $replacement_asset"
else
  echo "  replacement missing:    $replacement_asset"
fi

echo
echo "Replacement bundle asset completeness:"
expected_assets=(
  docker-27.5.1-amd64.tgz
  docker-27.5.1-arm64.tgz
  hami-airgap-images-amd64.tar
  hami-airgap-images-arm64.tar
  k3s-airgap-images-amd64.tar.zst
  k3s-airgap-images-arm64.tar.zst
  k3s-amd64
  k3s-arm64
  nvidia-container-toolkit-1.17.5-deb-amd64.tar.gz
  nvidia-container-toolkit-1.17.5-deb-arm64.tar.gz
)
missing_assets=0
for asset in "${expected_assets[@]}"; do
  url="https://github.com/Approaching-AI/AIMA/releases/download/$replacement_tag/$asset"
  if curl_ok "$url"; then
    echo "  ok      $asset"
  else
    echo "  missing $asset"
    missing_assets=1
  fi
done

if [[ $strict -eq 1 && ${#legacy_tags[@]} -gt 0 ]]; then
  echo
  echo "strict mode: legacy product-like tags still exist"
  exit 1
fi

if [[ $strict -eq 1 && $missing_assets -ne 0 ]]; then
  echo
  echo "strict mode: replacement bundle is incomplete"
  exit 1
fi
