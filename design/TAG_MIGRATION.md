# AIMA Legacy Tag Migration

Date: 2026-03-30

## Goal

Clean up historical git tags so product releases and non-product bundles do not share the same
namespace.

## Current Tag Classification

Keep as product release tags:

- `v0.0.1`
- `v0.2.0`

Legacy product-like tags to phase out:

- `v0.1.0-images`
  Purpose: stack and airgap bundle hosting
  Proposed replacement: `bundle/stack/2026-02-26`
- `v0.0.1-metax`
  Purpose: hotfix alias for MetaX support
  Proposed replacement: none. Preserve history in changelog/release notes instead of a second product-like tag.

## Migration Order

1. Republish assets currently hosted under `v0.1.0-images` to a non-product tag.
   Recommended tag: `bundle/stack/2026-02-26`
2. Update all catalog/docs URLs that still reference `v0.1.0-images`.
3. Verify downloads from the new bundle tag.
4. Delete the old remote tag `v0.1.0-images`.
5. Delete the old remote tag `v0.0.1-metax` after confirming no workflow depends on it.

Do not delete either legacy tag before steps 1-3 are complete.
Do not switch catalog download URLs to `bundle/stack/2026-02-26` until that tag has been pushed
and the release assets are confirmed reachable.

## Commands

### 1. Create the replacement bundle tag locally

```bash
make bundle-tag
git push origin refs/tags/bundle/stack/2026-02-26
```

`9510958` is the commit currently referenced by `v0.1.0-images`.

### 2. Update catalog/docs references

Current known references:

- `catalog/stack/k3s.yaml`
- `catalog/stack/hami.yaml`
- `catalog/stack/docker.yaml`
- `catalog/stack/nvidia-ctk.yaml`
- `docs/stack.md`

### 3. Verify

```bash
make version-audit
```

Optional strict mode after cleanup:

```bash
./scripts/audit-versioning.sh --strict
```

### 4. Remove the old remote tags

```bash
git push origin :refs/tags/v0.1.0-images
git push origin :refs/tags/v0.0.1-metax
```

Optional local cleanup:

```bash
git tag -d v0.1.0-images
git tag -d v0.0.1-metax
```

## Policy Going Forward

- Product releases use only `vX.Y.Z`.
- Bundles use a non-product namespace such as `bundle/<name>/<date>`.
- Do not create a second tag for the same product release commit.
- If a hotfix needs documentation, record it in release notes or changelog, not in a new pseudo-release tag.
