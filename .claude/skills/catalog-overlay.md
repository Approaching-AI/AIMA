# Catalog Overlay — Runtime YAML Hot-Update System

## Architecture

AIMA's knowledge system uses a dual-source L0 layer:

```
L0: Factory (go:embed) + Disk Overlay (~/.aima/catalog/) → 合并后重建 SQLite
     │  合并规则: 同 metadata.name → overlay 整体替换 factory
     │  陈旧保护: overlay._base_digest ≠ factory SHA256 → WARN
     ↓
L1: 用户 CLI 参数 (--config, --engine, --slot)
     ↓
L2: SQLite 动态知识 (benchmark, knowledge notes, configurations)
     ↓
L3a: Agent 决策
```

### Key Insight: `fs.FS` Interface

Both `embed.FS` and `os.DirFS()` implement Go's `fs.FS` interface. `LoadCatalog(fs.FS)` works for both without any adapter code. This is the zero-cost abstraction that makes overlay possible.

## Overlay Directory Structure

```
~/.aima/catalog/
  hardware/*.yaml      # Hardware Profile overlays
  engines/*.yaml       # Engine Asset overlays
  models/*.yaml        # Model Asset overlays
  partitions/*.yaml    # Partition Strategy overlays
  stack/*.yaml         # Stack Component overlays
```

## Staleness Protection (_base_digest)

When an overlay shadows a factory asset, it can include a `_base_digest` field — the SHA256 of the factory YAML it was based on:

```yaml
_base_digest: "sha256:a1b2c3d4..."
kind: engine_asset
metadata:
  name: vllm-ada
```

**Merge behavior:**

| Scenario | Behavior |
|----------|----------|
| Overlay has no `_base_digest` | `slog.Info` "overlay shadows factory" |
| `_base_digest` matches factory SHA256 | Silent (overlay is fresh) |
| `_base_digest` doesn't match | `slog.Warn` "factory changed, review recommended" |
| Overlay adds new asset (no factory match) | Silent |
| Single overlay file parse error | Skip that file, warn, continue loading |

**Why digest, not version?**
- AIMA Version is often "dev" — unreliable
- Digest is content-addressed — detects ANY change
- No schema version to maintain
- Backwards-compatible — missing `_base_digest` = no check

## MCP Tools

### `catalog.override`
Agent writes overlay YAML at runtime:
- Validates YAML kind + metadata.name
- Auto-injects `_base_digest` from factory (if shadowing)
- Writes to `{dataDir}/catalog/{kind_dir}/{name}.yaml`
- Returns path, action (created/replaced), warning

### `catalog.status`
Shows factory/overlay state:
- Factory count, overlay count
- Shadowed assets with stale/fresh status

## CLI Command

`aima catalog status` — wraps the `catalog.status` MCP tool, displays JSON.

## Key Functions (loader.go)

| Function | Purpose |
|----------|---------|
| `LoadCatalog(fs.FS)` | Strict loading (factory) — any error = fail |
| `LoadCatalogLenient(fs.FS)` | Lenient loading (overlay) — per-file errors collected, not fatal |
| `MergeCatalog(base, overlay)` | Simple merge (V1, still used internally) |
| `MergeCatalogWithDigests(base, overlay, digests, overlayFS)` | Merge + staleness detection |
| `ComputeDigests(fs.FS)` | SHA256 of each YAML → map[name]digest |
| `CollectNames(cat)` | All asset names in a catalog |
| `KindToDir(kind)` | "engine_asset" → "engines/" mapping |
| `ParseAssetPublic(data, path)` | Parse YAML into catalog (exported for MCP tool) |
| `ExtractOverlayDigestsFromDir(dir)` | Read `_base_digest` from overlay files |

## Startup Flow (main.go)

```go
// Step 3: Load factory catalog
cat, _ := knowledge.LoadCatalog(catalog.FS)
factoryDigests := knowledge.ComputeDigests(catalog.FS)

// Step 3b: Merge overlay (lenient + staleness)
overlayCat, warnings := knowledge.LoadCatalogLenient(os.DirFS(overlayDir))
for _, w := range warnings { slog.Warn("overlay skipped", "reason", w) }
cat, staleWarns := knowledge.MergeCatalogWithDigests(cat, overlayCat, factoryDigests, os.DirFS(overlayDir))
for _, w := range staleWarns { slog.Warn(w) }
```

## Distribution Pattern

Edge devices don't need binary updates for knowledge changes:
```bash
# Only sync YAML files
rsync -av overlay-yamls/ user@edge:~/.aima/catalog/
# Or single file
scp engines/custom-engine.yaml user@edge:~/.aima/catalog/engines/
# Takes effect on next aima command
```

## Common Mistakes

1. **Don't use `LoadCatalog` for overlay** — use `LoadCatalogLenient`. A broken overlay file should not crash the system.
2. **Don't forget `_base_digest` injection** in `catalog.override` — without it, staleness detection won't work for agent-written overlays.
3. **macOS cross-compiled binary needs codesign** — `codesign --sign - --force ~/aima` or SIGKILL (exit 137).
4. **mergeSlice is generic** — `mergeSlice[T any](base, overlay []T, name func(T) string)` works for all asset types.

## Testing

```bash
# Unit tests
go test ./internal/knowledge/ -run "TestMergeCatalog|TestLoadCatalogLenient|TestComputeDigests|TestStaleness|TestKindToDir" -v

# E2E: create stale overlay and verify warning
mkdir -p ~/.aima/catalog/engines
echo '_base_digest: "sha256:wrong"
kind: engine_asset
metadata:
  name: vllm-ada' > ~/.aima/catalog/engines/vllm-ada.yaml
aima catalog status  # should show stale warning
```
