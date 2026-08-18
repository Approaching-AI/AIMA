# 2026-04-27 Design-Principles Fix UAT

## Scope

Branch: `develop`

This UAT covers the fixes from the architecture/principles review:

- Central server code removed from this repo; edge Central client remains.
- Runtime catalog overlay uses `catalog/{central,user}` patch layers.
- `scenario.apply` is confirmable for Agent-initiated calls.
- Explorer/resolver compatibility is driven by YAML `supported_formats` / `supported_model_types`.
- MCP discovery/docs match the current 62-tool registry.

## Matrix

| Lane | Host | Scope | Result |
|------|------|-------|--------|
| Local dev-mac | macOS arm64 | Full Go gates, patch overlay, scenario dry-run, MCP tool discovery | PASS |
| cjwx linux-1 | `cjwx@100.121.255.97` (`qujing24`) | Linux amd64 binary smoke with isolated `AIMA_DATA_DIR` | PASS |

The cjwx host already had `/usr/local/bin/aima-serve` running on `:6188`; this UAT used `/tmp/aima-uat-20260427-design-fix`, did not stop or modify the live service, and cleaned the temp directory after evidence was copied back.

## Acceptance Checks

| Check | Evidence | Result |
|-------|----------|--------|
| Full test suite | `local/02-go-test-all.txt` | PASS |
| Vet | `local/03-go-vet-all.txt` | PASS |
| Scenario approval regression | `local/04-targeted-regression-tests.txt` | PASS |
| Catalog patch writes to user layer | `local/09-catalog-overlay-files.txt`, `cjwx/evidence/07-catalog-overlay-files.txt` | PASS |
| Catalog status counts model patch + scenario patch | `local/10b-catalog-status-after-all-patches.json`, `cjwx/evidence/08-catalog-status-after.json` | PASS (`overlay_assets=2`) |
| Scenario asset from patch is consumable | `local/12-scenario-show.json`, `cjwx/evidence/09-scenario-show.json` | PASS |
| Scenario dry-run does not deploy | `local/13-scenario-apply-dry-run.json`, `cjwx/evidence/10-scenario-apply-dry-run.json` | PASS (`dry_run=true`, status `dry_run`) |
| MCP full registry count | `local/16-mcp-full-count.txt`, `cjwx/evidence/11-mcp-tools-full.json` | PASS (`62`) |
| Explorer profile does not expose nonexistent `benchmark.ensure_assets` | `local/18-mcp-explorer-ensure-assets-check.txt`, `cjwx/evidence/12-mcp-tools-explorer.json` | PASS |
| Remote cleanup | `cjwx/05-remote-cleanup.txt` plus post-check | PASS |

## UAT Findings Fixed During Run

1. `catalog status` returned an empty `kind` for shadowed assets.
   Fixed by adding `knowledge.CollectNameKinds()` and populating `shadowed[].kind`.

2. `catalogSize()` did not count deployment scenarios, so a loaded scenario patch was not reflected in `overlay_assets`.
   Fixed by including `DeploymentScenarios` and `BenchmarkProfileTiers` in catalog size accounting.

3. CLI/docs still described `catalog override` as writing a full overlay asset.
   Updated wording to user-owned catalog patches.

## Conclusion

PASS. The new behavior holds locally and on cjwx with isolated state. No live remote deployments were started, stopped, or changed.
