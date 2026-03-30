# AIMA Versioning Redesign

Date: 2026-03-30
Status: adopted for new work

Current development line: `v0.2`
Latest product release: `v0.2.0`

## Current Assessment

The repository had four version-management problems:

1. Release truth was split across multiple places.
   `AGENTS.md` still described `v0.0.x`, `CHANGELOG.md` described `v0.2.0`, and MCP initialization
   hardcoded `serverInfo.version = 0.1.0`.
2. Product tags and non-product tags shared the same namespace.
   Tags such as `v0.1.0-images` and `v0.0.1-metax` looked like product releases but were really
   bundle or hotfix labels. This polluted `git describe`.
3. The documented Git Flow was not the actual operating model.
   `v0.2.0` was released on `master`, but `develop` was not back-merged, so builds from `develop`
   reported a pre-`v0.2.0` version baseline.
4. Different kinds of versions were mixed together.
   Product release version, MCP protocol version, DB/import schema version, and catalog component
   versions are different concerns and should not be interpreted as one numbering system.

## New Model

### 1. Separate Version Domains

| Domain | Meaning | Example | Authority |
|--------|---------|---------|-----------|
| Product release version | AIMA release identity | `v0.2.0` | Annotated git tag |
| Development line | Current development train for `develop` | `v0.2` | `internal/buildinfo/series.txt` |
| Development build version | Non-release build identity | `v0.2-dev` | Build metadata |
| MCP protocol version | MCP wire compatibility | `2024-11-05` | Protocol spec |
| DB/import schema version | Internal storage/import compatibility | `user_version=8`, `schema_version=1` | Code/schema |
| Catalog/component version | Upstream dependency version | `k3s 1.31.4+k3s1` | YAML data |
| Asset bundle tag | Non-product artifact bundle identity | `assets/2026-03-25` | Release packaging |

### 2. Branching Model

Use a develop-based model:

- `master` contains product releases only.
- `develop` is the integration branch for the active development line.
- `feat/*`, `fix/*`, and `docs/*` branch from `develop`.
- `release/*` branches from `develop` for a concrete SemVer cut.
- `hotfix/*` branches from `master` for urgent production fixes.

This preserves a stable release branch while keeping one explicit development train.

### 3. Tag Policy

- Only annotated tags matching `vX.Y.Z` are product releases.
- One release commit gets one product tag.
- Product release names/codenames belong in GitHub release notes, not in tag names.
- Non-product bundles must use a separate namespace such as `assets/<date>` or
  `bundle/<name>/<date>`.

Historical tags remain in the repository, but future tooling must ignore them for release detection.

### 4. SemVer Rules While Pre-1.0

- Minor (`0.y.0`) for user-visible features, new MCP tools, new runtimes, and intentional contract changes.
- Patch (`0.y.z`) for fixes, packaging, docs, and catalog corrections without intentional capability expansion.
- `1.0.0` when the CLI/MCP surface and deployment workflow are stable enough to support compatibility expectations.

### 5. Development Line and Build Metadata

The current development line comes from `internal/buildinfo/series.txt`.
Build metadata comes from `internal/buildinfo`.

- Tagged release build: exact tag value, for example `v0.2.0`
- Non-tagged build: development line plus `-dev`, for example `v0.2-dev`
- Additional fields: `GitCommit`, `BuildTime`

This metadata is shared by CLI output and MCP `serverInfo.version`.
The standard project build is `make build`; plain `go build ./cmd/aima` is acceptable for local
debugging but should not be treated as a version-authoritative release artifact.
Use `make version-audit` to inspect legacy product-like tags before a release.

### 6. How to Move to the Next Line

When the team decides that `develop` should start the next train:

1. Release or abandon the remaining `v0.2.x` work.
2. Update `internal/buildinfo/series.txt` from `v0.2` to `v0.3` on `develop`.
3. Keep release tags exact (`v0.3.0`, `v0.3.1`, ...).
4. Do not use `v0.3` itself as a git tag; it is the development-line label only.

## Immediate Repository Rules

1. Treat `v0.2.0` as the latest product release.
2. Stop creating new `vX.Y.Z-*` tags for images, bundles, or vendor-specific variants.
3. Keep `internal/buildinfo/series.txt` at `v0.2` for the current development line.
4. Release from `develop` through `release/*`, and tag on `master`.
5. Keep `CHANGELOG.md` aligned only with product releases.
6. Migrate legacy pseudo-release tags according to `design/TAG_MIGRATION.md`.
