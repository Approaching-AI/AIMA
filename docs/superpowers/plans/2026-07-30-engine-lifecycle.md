# Engine Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add generic, offline-capable Engine discovery, safe ensure/upgrade, immutable preinstall protection, version activation, and rollback without partner-specific code.

**Architecture:** Extend the Engine inventory with ownership, version, verification, and activation state. Reuse existing scan/pull/import primitives, but stage and strictly verify new managed assets before an atomic inventory activation. Preinstalled assets remain in place and are never physically deleted; running deployments stay pinned until an explicit redeploy.

**Tech Stack:** Go, SQLite v21, existing Engine Asset YAML, Docker/crictl, Native BinaryManager, MCP JSON-RPC, Cobra.

## Global Constraints

- Complete `2026-07-30-generic-deployment-recovery.md` first; this plan uses SQLite v21 after recovery's v20 and pins Engine Asset/version into deployment intent.
- Do not add partner, product, engine-type, model-type, hardware-vendor, or path-specific Go branches.
- Preinstalled and legacy assets cannot be overwritten, moved, or physically deleted.
- AIMA may physically delete only `managed` or `imported` assets whose canonical location is below `AIMA_DATA_DIR`.
- Native managed versions live at `<AIMA_DATA_DIR>/dist/<platform>/<asset-name>/<version-or-digest>/`.
- A network install or upgrade is blocked when its Catalog entry lacks the required SHA256/OCI digest.
- Digest/version mismatch must return an error and leave the active version unchanged.
- `engine.ensure` defaults to plan-only; mutation requires `apply=true`.
- Activation and rollback do not restart running deployments.
- All core scan, preinstall reuse, offline import, activation, and rollback functions work without internet.
- Use existing dependencies only and keep zero CGO.
- The supplied source snapshot has no `.git`; execute commit steps only in a real clone with `.git`, branched from `develop`.

---

## File Structure

**Create**

- `internal/engine/lifecycle.go` — pure version comparison, plan generation, safe path layout, staged activation inputs.
- `internal/engine/lifecycle_test.go`
- `cmd/aima/engine_lifecycle.go` — Catalog/inventory/download orchestration backing MCP ToolDeps.
- `cmd/aima/engine_lifecycle_test.go`

**Modify**

- `internal/sqlite.go`, `internal/sqlite_test.go` — v21 inventory columns and activation transaction.
- `internal/knowledge/loader.go`, `internal/knowledge/loader_test.go` — compatible engine versions.
- `internal/engine/scanner.go`, `internal/engine/engine_test.go` — asset/origin/version evidence.
- `internal/engine/puller.go`, `internal/engine/binary.go`, `internal/engine/engine_test.go` — strict digest failure.
- `cmd/aima/main.go`, `cmd/aima/tooldeps_engine.go`, `cmd/aima/tooldeps_engine_test.go` — lifecycle wiring and deletion ownership.
- `internal/mcp/tools_deps.go`, `internal/mcp/tools_engine.go`, `internal/mcp/mcp_test.go` — ensure/rollback tools.
- `internal/cli/engine.go`, `internal/cli/cli_test.go` — thin CLI.
- `cmd/aima/adapters.go`, `cmd/aima/main_test.go` — Agent approval/blocking.
- `docs/engine.md`, `docs/mcp.md`, `docs/cli.md` — lifecycle contract.

---

### Task 1: SQLite v21 Engine Inventory

**Files:**
- Modify: `internal/sqlite.go:87-100`
- Modify: `internal/sqlite.go:368-465`
- Modify: `internal/sqlite.go` after `migrateV20`
- Modify: `internal/sqlite.go:1890-1980`
- Test: `internal/sqlite_test.go`

**Interfaces:**
- Produces extended `state.Engine` and DB methods `ListEngineVersions`, `ActivateEngineVersion`, `RollbackEngineVersion`, `EngineHasReferences`.
- Consumed by: Tasks 2, 4, and 5.

- [ ] **Step 1: Write failing v21 migration test**

```go
func TestMigrateV21ExtendsEngineInventory(t *testing.T) {
    db, err := Open(context.Background(), ":memory:")
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = db.Close() })
    var version int
    if err := db.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil { t.Fatal(err) }
    if version != 21 { t.Fatalf("version=%d want 21", version) }
    for _, col := range []string{"asset_name", "version", "catalog_version", "origin", "content_digest", "location", "active", "lifecycle_status", "verification_status", "previous_engine_id"} {
        var count int
        err := db.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('engines') WHERE name = ?`, col).Scan(&count)
        if err != nil { t.Fatal(err) }
        if count != 1 { t.Errorf("missing %s", col) }
    }
}
```

- [ ] **Step 2: Add migration and Engine fields**

Add columns with backward-safe defaults: `origin='legacy'`, `active=0`, `lifecycle_status='discovered'`, `verification_status='unverified'`; copy existing `binary_path` or `image:tag` into `location`. Add a unique partial index ensuring at most one `active=1` row per `(asset_name, platform, runtime_type)` when `asset_name <> ''`.

Extend `Engine` with matching JSON fields and update every INSERT/UPSERT/SELECT scan list.

- [ ] **Step 3: Write failing activation/rollback transaction tests**

```go
func TestActivateEngineVersionIsAtomic(t *testing.T) {
    // Insert verified v1 active and verified v2 inactive for same asset/platform.
    // Activate v2; assert v2 active, v1 inactive, v2.previous_engine_id=v1.ID.
}

func TestActivateRejectsUnverifiedVersion(t *testing.T) {
    // Insert staged/unverified candidate and assert activation returns an error.
}

func TestRollbackRequiresExistingVerifiedPrevious(t *testing.T) {
    // Remove or mark previous unavailable and assert active remains unchanged.
}
```

- [ ] **Step 4: Implement transaction methods**

Use one SQL transaction per activation/rollback. Re-read the current active row inside the transaction. Reject candidates whose `lifecycle_status` is not `verified|active`, whose `verification_status` is not `verified`, or whose platform/runtime/asset differs.

- [ ] **Step 5: Run DB tests**

Run: `go test ./internal -run 'Test(MigrateV21|ActivateEngineVersion|RollbackEngineVersion)' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/sqlite.go internal/sqlite_test.go
git commit -m "feat: add versioned engine inventory"
```

---

### Task 2: Generic Scan Origin and Version Evidence

**Files:**
- Modify: `internal/knowledge/loader.go` `EngineMetadata`
- Test: `internal/knowledge/loader_test.go`
- Modify: `internal/engine/scanner.go:21-57`
- Modify: `internal/engine/scanner.go:125-283`
- Test: `internal/engine/engine_test.go`
- Modify: `cmd/aima/main.go:1136-1225`

**Interfaces:**
- Consumes: Task 1 Engine fields.
- Produces: `EngineImage.AssetName`, `Origin`, `CatalogVersion`, `DetectedVersion`, `VersionMatch`, `ContentDigest`.
- Produces: `knowledge.EngineMetadata.CompatibleVersions []string`.
- Consumed by: Task 4 planning.

- [ ] **Step 1: Write failing version comparison tests**

```go
func TestCompareDetectedVersion(t *testing.T) {
    compatible := []string{"1.2.2", "1.2.x"}
    cases := []struct{detected, catalog string; want string}{
        {"1.2.3", "1.2.3", "exact"},
        {"1.2.2", "1.2.3", "compatible"},
        {"2.0.0", "1.2.3", "mismatch"},
        {"", "1.2.3", "unknown"},
    }
    // Call compareDetectedVersion with compatible only for the second row.
}
```

Compatibility is exact string membership plus a Catalog-declared trailing `.x` prefix rule; do not implement general semantic-version guessing.

- [ ] **Step 2: Extend Catalog metadata and clone/merge behavior**

```go
CompatibleVersions []string `yaml:"compatible_versions,omitempty" json:"compatible_versions,omitempty"`
```

Deep-copy it. Concrete asset value replaces an inherited empty value using the existing metadata merge pattern.

- [ ] **Step 3: Write failing scan origin tests**

Cover these paths:

- explicit `source.probe.paths` hit -> `origin=preinstalled` and correct AssetName;
- binary under configured AIMA dist root -> `origin=managed`;
- binary in PATH/extra external directory -> `origin=preinstalled`;
- image discovered before AIMA ownership evidence -> `origin=preinstalled`;
- detected version mismatch -> `VersionMatch=mismatch`, never `exact`.

- [ ] **Step 4: Extend scan descriptors without partner branches**

Pass catalog descriptors into the scanner:

```go
type AssetDescriptor struct {
    AssetName string
    Type string
    CatalogVersion string
    CompatibleVersions []string
    Patterns []string
    Probe *knowledge.EngineSourceProbe
}
```

Replace the lossy `PreinstalledProbes map[string]*...` path with `[]AssetDescriptor`. Keep `AssetPatterns` temporarily only if another call site still needs it, then remove it in the same task once all call sites compile.

- [ ] **Step 5: Persist scan evidence**

In `scanEnginesCore`, map all EngineImage evidence into `state.Engine`. A later successful `engine.pull` or `engine.import` may upgrade origin to `managed` or `imported`; a generic scan must never downgrade those owned origins back to `preinstalled`.

- [ ] **Step 6: Run scan/Catalog tests**

Run: `go test ./internal/knowledge ./internal/engine ./cmd/aima -run 'Test(CompareDetectedVersion|Scan.*Origin|EngineCompatibleVersions)' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/knowledge/loader.go internal/knowledge/loader_test.go internal/engine/scanner.go internal/engine/engine_test.go cmd/aima/main.go
git commit -m "feat: record generic engine origin and version evidence"
```

---

### Task 3: Strict Verification and Versioned Native Staging

**Files:**
- Modify: `internal/engine/puller.go:50-205`
- Modify: `internal/engine/binary.go:60-200`
- Test: `internal/engine/engine_test.go`
- Create: `internal/engine/lifecycle.go`
- Create: `internal/engine/lifecycle_test.go`

**Interfaces:**
- Produces: `VerifyImageDigest(...) error`, strict BinaryManager checksum behavior, `ManagedVersionDir`, `NewStagingDir`, `PromoteStagedDir`.
- Consumed by: Task 4.

- [ ] **Step 1: Write failing strict digest tests**

```go
func TestPullFailsOnDigestMismatch(t *testing.T) {
    // Mock successful pull and inspect returning sha256:actual.
    // PullOptions.ExpectedDigest is sha256:expected.
    // Assert error contains "digest mismatch".
}

func TestBinaryDownloadFailsOnSHA256Mismatch(t *testing.T) {
    // httptest server serves fixed bytes; expected checksum differs.
    // Assert Download returns error and destination is not treated as ready.
}
```

- [ ] **Step 2: Make verification return errors**

Change advisory `verifyDigest` into an error-returning function. If expected digest is non-empty and neither Docker nor crictl reports a match, return a contextual error. In BinaryManager, return an error immediately on expected checksum mismatch; never emit `complete`.

- [ ] **Step 3: Write failing safe-layout tests**

```go
func TestManagedVersionDirRejectsTraversal(t *testing.T) {
    _, err := ManagedVersionDir("/data", "windows-amd64", "../bad", "1.0")
    if err == nil { t.Fatal("expected traversal rejection") }
}

func TestPromoteStagedDirDoesNotReplaceExisting(t *testing.T) {
    // Existing verified destination remains byte-for-byte unchanged.
}
```

- [ ] **Step 4: Implement versioned path and atomic promotion helpers**

Allow path components matching `^[A-Za-z0-9._-]+$` after trimming. Create staging with `os.MkdirTemp(<data>/staging/engines, <asset>-*)`. Promote with `os.Rename` only after verification; if the destination already exists, compare evidence and reuse it rather than overwriting.

- [ ] **Step 5: Run Engine tests**

Run: `go test ./internal/engine -run 'Test(PullFailsOnDigestMismatch|BinaryDownloadFailsOnSHA256Mismatch|ManagedVersionDir|PromoteStagedDir)' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/engine/puller.go internal/engine/binary.go internal/engine/engine_test.go internal/engine/lifecycle.go internal/engine/lifecycle_test.go
git commit -m "feat: stage and strictly verify engine artifacts"
```

---

### Task 4: Engine Ensure Plan and Apply

**Files:**
- Modify: `internal/engine/lifecycle.go`
- Modify: `internal/engine/lifecycle_test.go`
- Create: `cmd/aima/engine_lifecycle.go`
- Create: `cmd/aima/engine_lifecycle_test.go`
- Modify: `cmd/aima/tooldeps_engine.go`
- Modify: `internal/mcp/tools_deps.go`

**Interfaces:**
- Consumes: inventory methods, scan evidence, strict staging helpers.
- Produces: `engine.EnsureRequest`, `engine.EnsurePlan`, `engine.BuildEnsurePlan`.
- Produces ToolDep: `EnsureEngine(ctx, name, version string, apply bool) (json.RawMessage, error)`.
- Consumed by: Task 6 MCP/CLI.

- [ ] **Step 1: Write failing pure planning tests**

```go
func TestBuildEnsurePlanPrefersExactPreinstalled(t *testing.T) {
    // Inventory contains preinstalled exact version; plan.Action="reuse", NetworkRequired=false.
}

func TestBuildEnsurePlanBlocksNetworkInstallWithoutDigest(t *testing.T) {
    // No local version, Catalog has URL but no checksum; plan.Blocked=true with a checksum reason.
}

func TestBuildEnsurePlanDoesNotSilentlyReplaceUnverifiedActive(t *testing.T) {
    // Active preinstalled unverified v1 and candidate v2: apply requires verified staged candidate.
}
```

- [ ] **Step 2: Define plan contract**

```go
type EnsureRequest struct { Name, Version, Platform string; Apply bool }
type EnsurePlan struct {
    AssetName, RequestedVersion, CurrentEngineID, CandidateEngineID string
    Action string // reuse, install, upgrade, activate, noop
    Origin string
    Source string
    NetworkRequired bool
    Blocked bool
    BlockReason string
    AffectedDeployments []string
}
```

Planning is side-effect free and stable for identical Catalog/inventory inputs.

- [ ] **Step 3: Write failing orchestration tests**

Use fake inventory and downloader callbacks to assert:

1. `apply=false` performs no download/DB mutation.
2. exact preinstall is reused without download or file move.
3. managed Native install downloads only into staging, verifies, promotes, upserts `verified`, then activates.
4. container install passes `Image.Digest` into PullOptions.
5. any failure leaves the old active row unchanged and removes staging.
6. successful activation does not call deploy or Runtime functions.

- [ ] **Step 4: Implement lifecycle service at the consumer boundary**

In `cmd/aima/engine_lifecycle.go`, define the narrow inventory/downloader interfaces used by the orchestration. Resolve the concrete Engine Asset with existing hardware-aware Catalog methods. Native destination uses Task 3 layout. Container candidate identity uses immutable digest when present. Call Task 1 activation only after strict verification.

- [ ] **Step 5: Wire `EnsureEngine` ToolDep**

Construct the lifecycle service in `buildEngineDeps`. The returned JSON must always include the plan plus `applied`, `active_engine_id`, and `previous_engine_id` when applicable.

- [ ] **Step 6: Run lifecycle tests**

Run: `go test ./internal/engine ./cmd/aima -run 'Test(BuildEnsurePlan|EngineEnsure)' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/engine/lifecycle.go internal/engine/lifecycle_test.go cmd/aima/engine_lifecycle.go cmd/aima/engine_lifecycle_test.go cmd/aima/tooldeps_engine.go internal/mcp/tools_deps.go
git commit -m "feat: add transactional engine ensure lifecycle"
```

---

### Task 5: Rollback and Physical Delete Protection

**Files:**
- Modify: `cmd/aima/engine_lifecycle.go`
- Test: `cmd/aima/engine_lifecycle_test.go`
- Modify: `cmd/aima/tooldeps_engine.go:285-335`
- Test: `cmd/aima/tooldeps_engine_test.go`
- Modify: `internal/mcp/tools_deps.go`

**Interfaces:**
- Produces ToolDep: `RollbackEngine(ctx, name string, confirm bool) (json.RawMessage, error)`.
- Hardens existing `RemoveEngine`.
- Consumed by: Task 6.

- [ ] **Step 1: Write failing rollback tests**

```go
func TestEngineRollbackActivatesVerifiedPrevious(t *testing.T) {
    // Active v2 points to verified v1; confirm=true switches active to v1.
}

func TestEngineRollbackDoesNotRestartDeployments(t *testing.T) {
    // Runtime/deploy callback is a panic function and must not be invoked.
}

func TestEngineRollbackRequiresConfirm(t *testing.T) {
    // confirm=false returns structured refusal and no mutation.
}
```

- [ ] **Step 2: Implement rollback orchestration**

Resolve the active inventory row by Asset name, validate previous availability/verification/platform/runtime, then call the Task 1 transaction. Return both old and new active IDs.

- [ ] **Step 3: Write failing delete-protection tests**

Cover:

- preinstalled with `delete_files=true` -> error, file remains;
- legacy with `delete_files=true` -> error;
- managed path outside canonical AIMA data directory -> error;
- managed asset referenced by active version, previous rollback link, or deployment intent -> error;
- unreferenced managed asset inside data directory -> file removed and DB row deleted.

- [ ] **Step 4: Implement ownership/reference guards**

Use `filepath.Abs`, `filepath.EvalSymlinks` for existing paths, and `filepath.Rel` to prove containment. Reject `rel == ".."` or strings beginning with `..` plus a separator. Query `EngineHasReferences` before removal. Save rollback snapshot only after authorization checks but before mutation.

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/aima -run 'TestEngine(Rollback|Remove.*Protect|Remove.*Managed)' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/aima/engine_lifecycle.go cmd/aima/engine_lifecycle_test.go cmd/aima/tooldeps_engine.go cmd/aima/tooldeps_engine_test.go internal/mcp/tools_deps.go
git commit -m "feat: protect and rollback engine versions"
```

---

### Task 6: MCP, CLI, and Agent Guardrails

**Files:**
- Modify: `internal/mcp/tools_engine.go`
- Test: `internal/mcp/mcp_test.go`
- Modify: `internal/cli/engine.go`
- Test: `internal/cli/cli_test.go`
- Modify: `cmd/aima/adapters.go`
- Test: `cmd/aima/main_test.go`

**Interfaces:**
- Consumes: `EnsureEngine`, `RollbackEngine` from Tasks 4-5.
- Produces MCP tools `engine.ensure`, `engine.rollback` and matching CLI commands.

- [ ] **Step 1: Write failing MCP contract tests**

Call:

```json
{"name":"llamacpp-hip-windows","apply":false}
```

and assert `engine.ensure` returns the fake plan. Call `engine.rollback` with `confirm=false` and assert no mutation. Verify missing `name` returns a tool error.

- [ ] **Step 2: Register MCP tools**

`engine.ensure` schema: required `name`, optional `version`, optional `apply` default false. `engine.rollback` schema: required `name`, required `confirm=true` for mutation. Descriptions must state that running deployments are not restarted.

- [ ] **Step 3: Write failing CLI tests**

Assert these Cobra invocations call the correct ToolDeps exactly once and print JSON:

```text
aima engine ensure llamacpp-hip-windows
aima engine ensure llamacpp-hip-windows --version b9330 --apply
aima engine rollback llamacpp-hip-windows --confirm
```

- [ ] **Step 4: Add thin CLI commands**

Add `newEngineEnsureCmd` and `newEngineRollbackCmd` to `newEngineCmd`. No Catalog, filesystem, or DB logic may appear in `internal/cli/engine.go`.

- [ ] **Step 5: Add Agent safety tests and rules**

Add `engine.ensure` to `confirmableTools` with a dry-run argument transform that forces `apply=false`. Add `engine.rollback` to `blockedAgentTools` because it is destructive state mutation; human CLI/MCP callers remain allowed. Test that an Agent ensure call produces `NEEDS_APPROVAL` containing the dry-run plan and rollback produces `BLOCKED`.

- [ ] **Step 6: Run contract tests**

Run: `go test ./internal/mcp ./internal/cli ./cmd/aima -run 'Test.*Engine(Ensure|Rollback)' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/mcp/tools_engine.go internal/mcp/mcp_test.go internal/cli/engine.go internal/cli/cli_test.go cmd/aima/adapters.go cmd/aima/main_test.go
git commit -m "feat: expose safe engine lifecycle tools"
```

---

### Task 7: Engine Lifecycle Documentation and Integration Tests

**Files:**
- Modify: `docs/engine.md`
- Modify: `docs/mcp.md`
- Modify: `docs/cli.md`
- Modify: `cmd/aima/tooldeps_engine_test.go`

**Interfaces:**
- Verifies the public lifecycle workflow end to end with fakes/local files.

- [ ] **Step 1: Add an offline lifecycle integration test**

Using temporary directories and an in-process HTTP server only where a network source is explicitly tested, cover:

1. scan an external preinstalled executable;
2. `engine.ensure apply=false` plans reuse;
3. import a local archive as `origin=imported` into a versioned directory;
4. activate imported v2 while preserving v1;
5. rollback to v1;
6. reject physical deletion of the external preinstalled file.

Assert no command contains a partner or engine-specific branch; use generic fixture names `engine-a` and `engine-b`.

- [ ] **Step 2: Document lifecycle contracts**

Document inventory fields, origin semantics, versioned path layout, strict digest behavior, ensure plan/apply examples, rollback, preinstalled deletion protection, offline import, and the fact that running deployments stay pinned.

- [ ] **Step 3: Run integration and package tests**

Run: `go test ./internal/engine ./internal/mcp ./internal/cli ./cmd/aima -count=1`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add docs/engine.md docs/mcp.md docs/cli.md cmd/aima/tooldeps_engine_test.go
git commit -m "docs: define generic engine lifecycle workflow"
```

---

### Task 8: Engine Lifecycle Verification Gate

**Files:**
- Modify only files required by failures found in this task.

**Interfaces:**
- Verifies Tasks 1-7 and the dependency on deployment recovery.

- [ ] **Step 1: Run focused race tests**

Run: `go test -race ./internal/engine ./internal/mcp ./cmd/aima`

Expected: PASS with no race reports.

- [ ] **Step 2: Run full tests and vet**

Run: `go test ./...`

Expected: PASS.

Run: `go vet ./...`

Expected: exit 0.

- [ ] **Step 3: Cross-compile all release targets**

```bash
GOOS=windows GOARCH=amd64 go build -o /tmp/aima-engine-windows-amd64.exe ./cmd/aima
GOOS=darwin GOARCH=arm64 go build -o /tmp/aima-engine-darwin-arm64 ./cmd/aima
GOOS=linux GOARCH=amd64 go build -o /tmp/aima-engine-linux-amd64 ./cmd/aima
GOOS=linux GOARCH=arm64 go build -o /tmp/aima-engine-linux-arm64 ./cmd/aima
```

Expected: all commands exit 0.

- [ ] **Step 4: Verify the change is generic**

Run: `git diff develop...HEAD -- '*.go' | rg -n '(partner|合作伙伴|baidu|百应|claw)'`

Expected: no output.

Run: `git diff develop...HEAD -- '*.go' | rg -n 'if .*?(vllm|sglang|llamacpp|nvidia|amd|musa|ascend)'`

Expected: no new engine/vendor selection branch in the diff. Review false positives from tests or error text manually.

- [ ] **Step 5: Validate online, offline, and preinstalled modes as one matrix**

Collect results for each available target before changing code again:

| Mode | Native | Docker | K3S |
|---|---|---|---|
| preinstalled reuse | PASS/FAIL/UNREACHABLE | PASS/FAIL/UNREACHABLE | PASS/FAIL/UNREACHABLE |
| offline import + activate | PASS/FAIL/UNREACHABLE | PASS/FAIL/UNREACHABLE | PASS/FAIL/UNREACHABLE |
| verified upgrade + rollback | PASS/FAIL/UNREACHABLE | PASS/FAIL/UNREACHABLE | PASS/FAIL/UNREACHABLE |
| checksum mismatch preserves active | PASS/FAIL/UNREACHABLE | PASS/FAIL/UNREACHABLE | PASS/FAIL/UNREACHABLE |

Do not claim full UAT while any required target is `UNREACHABLE`.

- [ ] **Step 6: Commit verification-only fixes, if any**

```bash
git add -u
git commit -m "test: complete engine lifecycle verification"
```

If Step 1-5 required no file changes, do not create an empty commit.
