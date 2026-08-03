# Generic Deployment Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add persistent, bounded, vendor-neutral deployment recovery that restarts failed Native inference services while observing and quarantining Docker/K3S crash loops.

**Architecture:** Store declarative deployment intent in SQLite and evaluate it with a pure recovery state machine. A long-running reconciler starts with `aima serve`, reuses the existing deploy business path, and delegates actual container restarts to Docker/K3S. Explicit user deletion writes `desired_state=stopped` before touching the runtime so it wins every race with recovery.

**Tech Stack:** Go, SQLite (`modernc.org/sqlite`, zero CGO), existing Runtime interface, YAML Engine Assets, MCP JSON-RPC, Cobra.

## Global Constraints

- Do not add partner, product, engine-type, model-type, or GPU-vendor branches.
- K3S/Docker lifecycle remains native to those runtimes; AIMA may only apply, status/get, delete, and read logs.
- Native recovery is guaranteed only while `aima serve` is running; AIMA restart must resume persisted intent.
- Default policy is exactly: check every 5s, require 3 consecutive health failures, at most 3 attempts in 600s, backoff `[2, 10, 30]` seconds, reset after 600s stable health.
- Public MCP callers cannot forge the internal reconciler call source.
- Never persist expanded secrets or sensitive environment variables in deployment intent.
- All core recovery behavior must work offline.
- Use standard library packages and existing project dependencies only; keep zero CGO.
- The supplied source snapshot has no `.git`; execute commit steps only in a real clone with `.git`, branched from `develop`.

---

## File Structure

**Create**

- `internal/recovery/policy.go` — validated policy defaults and overlay resolution.
- `internal/recovery/intent.go` — intent/state types, trusted reconciler context marker, redaction.
- `internal/recovery/state_machine.go` — pure observation-to-decision transitions.
- `internal/recovery/controller.go` — periodic reconciliation and side-effect orchestration.
- `internal/recovery/policy_test.go`
- `internal/recovery/state_machine_test.go`
- `internal/recovery/controller_test.go`

**Modify**

- `internal/knowledge/loader.go` and `internal/knowledge/loader_test.go` — parse/merge `startup.recovery`.
- `internal/sqlite.go` and `internal/sqlite_test.go` — v20 migration and intent CRUD/CAS.
- `internal/runtime/runtime.go` — additive recovery output fields.
- `internal/runtime/docker.go`, `internal/runtime/docker_test.go` — Docker restart count.
- `internal/runtime/native.go`, `internal/runtime/native_test.go` — ongoing HTTP health truth.
- `cmd/aima/tooldeps_deploy.go`, `cmd/aima/tooldeps_deploy_test.go` — persist/enrich intent around the existing deploy path.
- `internal/mcp/tools_deps.go`, `internal/mcp/tools_deploy.go`, `internal/mcp/tools_deploy_test.go` — recovery policy input.
- `internal/cli/root.go`, `internal/cli/serve.go`, `internal/cli/serve_test.go` — serve lifecycle hook.
- `cmd/aima/main.go`, `cmd/aima/main_test.go` — construct/start controller.
- `internal/agent/patrol.go`, `internal/agent/agent_test.go` — quarantined alert dedup and no duplicate healing.
- `docs/runtime.md`, `docs/mcp.md`, `docs/cli.md` — operator contract.

---

### Task 1: Recovery Policy and Catalog Schema

**Files:**
- Create: `internal/recovery/policy.go`
- Create: `internal/recovery/policy_test.go`
- Modify: `internal/knowledge/loader.go:230-285`
- Modify: `internal/knowledge/loader.go:820-860`
- Test: `internal/knowledge/loader_test.go`

**Interfaces:**
- Produces: `recovery.Policy`, `recovery.PolicyPatch`, `recovery.DefaultPolicy() Policy`, `recovery.ResolvePolicy(base Policy, patches ...PolicyPatch) (Policy, error)`.
- Produces: `knowledge.RecoveryPolicy` at `EngineStartup.Recovery`.
- Consumed by: Tasks 2, 3, 5, and 6.

- [ ] **Step 1: Write failing policy tests**

```go
func TestDefaultPolicyMatchesProductContract(t *testing.T) {
    got := DefaultPolicy()
    want := Policy{
        Enabled: true, CheckIntervalS: 5, ConsecutiveFailures: 3,
        MaxAttempts: 3, WindowS: 600, BackoffS: []int{2, 10, 30},
        StableResetS: 600,
    }
    if !reflect.DeepEqual(got, want) {
        t.Fatalf("got=%+v want=%+v", got, want)
    }
}

func TestResolvePolicyRejectsInvalidBounds(t *testing.T) {
    zero := 0
    _, err := ResolvePolicy(DefaultPolicy(), PolicyPatch{MaxAttempts: &zero})
    if err == nil || !strings.Contains(err.Error(), "max_attempts") {
        t.Fatalf("ResolvePolicy error = %v", err)
    }
}

func TestPolicyBackoffRepeatsLastValue(t *testing.T) {
    p := DefaultPolicy()
    p.BackoffS = []int{4}
    if got := p.Backoff(3); got != 4*time.Second { t.Fatalf("got %s", got) }
}
```

- [ ] **Step 2: Run policy tests and verify they fail**

Run: `go test ./internal/recovery -run 'Test(DefaultPolicy|ResolvePolicy|PolicyBackoff)' -count=1`

Expected: FAIL because `internal/recovery` and its types do not exist.

- [ ] **Step 3: Implement validated policy types**

```go
type Policy struct {
    Enabled             bool  `json:"enabled"`
    CheckIntervalS      int   `json:"check_interval_s"`
    ConsecutiveFailures int   `json:"consecutive_failures"`
    MaxAttempts         int   `json:"max_attempts"`
    WindowS             int   `json:"window_s"`
    BackoffS            []int `json:"backoff_s"`
    StableResetS        int   `json:"stable_reset_s"`
}

type PolicyPatch struct {
    Enabled             *bool `json:"enabled,omitempty"`
    CheckIntervalS      *int  `json:"check_interval_s,omitempty"`
    ConsecutiveFailures *int  `json:"consecutive_failures,omitempty"`
    MaxAttempts         *int  `json:"max_attempts,omitempty"`
    WindowS             *int  `json:"window_s,omitempty"`
    BackoffS            []int `json:"backoff_s,omitempty"`
    StableResetS        *int  `json:"stable_reset_s,omitempty"`
}
```

Validate `CheckIntervalS` in `1..300`, `ConsecutiveFailures` in `1..20`, `MaxAttempts` in `1..20`, `WindowS` and `StableResetS` in `1..86400`, and every backoff in `1..3600`. Copy slices on input/output.

- [ ] **Step 4: Write failing Catalog parse/merge test**

```go
func TestEngineStartupRecoveryParsesAndMerges(t *testing.T) {
    // Base profile supplies all values; concrete asset overrides max_attempts.
    // Assert the final EngineAsset keeps base backoff and uses max_attempts=5.
}
```

Use the existing YAML fixture pattern in `internal/knowledge/loader_test.go`; include `startup.recovery.enabled`, `max_attempts`, `window_s`, `backoff_s`, and `stable_reset_s`.

- [ ] **Step 5: Add `knowledge.RecoveryPolicy` and merge/clone logic**

```go
type RecoveryPolicy struct {
    Enabled             *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
    CheckIntervalS      *int  `yaml:"check_interval_s,omitempty" json:"check_interval_s,omitempty"`
    ConsecutiveFailures *int  `yaml:"consecutive_failures,omitempty" json:"consecutive_failures,omitempty"`
    MaxAttempts         *int  `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
    WindowS             *int  `yaml:"window_s,omitempty" json:"window_s,omitempty"`
    BackoffS            []int `yaml:"backoff_s,omitempty" json:"backoff_s,omitempty"`
    StableResetS        *int  `yaml:"stable_reset_s,omitempty" json:"stable_reset_s,omitempty"`
}
```

Add `Recovery RecoveryPolicy` to `EngineStartup`. Merge pointer fields only when destination is nil; copy `BackoffS` when destination is empty. Deep-copy `BackoffS` in `cloneEngineAsset`.

- [ ] **Step 6: Run focused tests**

Run: `go test ./internal/recovery ./internal/knowledge -run 'Test(DefaultPolicy|ResolvePolicy|PolicyBackoff|EngineStartupRecovery)' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/recovery/policy.go internal/recovery/policy_test.go internal/knowledge/loader.go internal/knowledge/loader_test.go
git commit -m "feat: add catalog-driven deployment recovery policy"
```

---

### Task 2: SQLite v20 Deployment Intent Store

**Files:**
- Create: `internal/recovery/intent.go`
- Modify: `internal/sqlite.go:87-100`
- Modify: `internal/sqlite.go:368-460`
- Modify: `internal/sqlite.go` after `migrateV19`
- Modify: `internal/sqlite.go` in the CRUD section
- Test: `internal/sqlite_test.go`

**Interfaces:**
- Consumes: `recovery.Policy` from Task 1.
- Produces: `recovery.Intent`, `recovery.DesiredRunning`, `recovery.DesiredStopped`, recovery-state constants.
- Produces DB methods: `UpsertDeploymentIntent`, `GetDeploymentIntent`, `ListRunnableDeploymentIntents`, `CompareAndSwapDeploymentIntent`, `StopDeploymentIntent`.
- Consumed by: Tasks 3, 5, and 6.

- [ ] **Step 1: Write failing v20 migration test**

```go
func TestMigrateV20CreatesDeploymentIntents(t *testing.T) {
    db, err := Open(context.Background(), ":memory:")
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = db.Close() })
    var version int
    if err := db.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil { t.Fatal(err) }
    if version != 20 { t.Fatalf("user_version=%d want 20", version) }
    for _, col := range []string{"name", "revision", "desired_state", "recovery_state", "recovery_policy_json", "observed_restart_count"} {
        var count int
        err := db.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('deployment_intents') WHERE name = ?`, col).Scan(&count)
        if err != nil { t.Fatal(err) }
        if count != 1 { t.Errorf("missing %s", col) }
    }
}
```

- [ ] **Step 2: Run migration test and verify failure**

Run: `go test ./internal -run TestMigrateV20CreatesDeploymentIntents -count=1`

Expected: FAIL with `user_version=19` or missing table.

- [ ] **Step 3: Add v20 migration**

Create `deployment_intents` with the exact design fields, storing config and policy as JSON, timestamps as UTC RFC3339 strings, and nullable exit code. Add indexes on `(desired_state, recovery_state)` and `next_attempt_at`. Call `migrateV20` after v19 and set `PRAGMA user_version = 20`.

- [ ] **Step 4: Write failing CRUD/CAS/redaction tests**

```go
func TestDeploymentIntentCASRejectsStaleRevision(t *testing.T) {
    db, err := Open(context.Background(), ":memory:")
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = db.Close() })
    in := recovery.Intent{Name: "m", Revision: 1, DesiredState: recovery.DesiredRunning, Policy: recovery.DefaultPolicy()}
    if err := db.UpsertDeploymentIntent(context.Background(), &in); err != nil { t.Fatal(err) }
    in.RecoveryState = recovery.StateWaiting
    ok, err := db.CompareAndSwapDeploymentIntent(context.Background(), &in, 99)
    if err != nil { t.Fatal(err) }
    if ok { t.Fatal("stale revision updated row") }
}

func TestIntentConfigRedactsSensitiveKeys(t *testing.T) {
    got := recovery.SanitizeConfig(map[string]any{"ctx_size": 8192, "api_key": "secret", "nested": map[string]any{"token": "x"}})
    if got["ctx_size"] != 8192 || got["api_key"] != "[REDACTED]" { t.Fatalf("got %#v", got) }
}
```

- [ ] **Step 5: Implement intent types and DB methods**

```go
type Intent struct {
    Name, Model, EngineAsset, EngineVersion, Slot, Runtime string
    Revision int64
    Config map[string]any
    DesiredState, RecoveryState string
    Policy Policy
    AttemptCount, ConsecutiveFailureCount, ObservedRestartCount int
    WindowStartedAt, NextAttemptAt, HealthySince time.Time
    LastExitCode *int
    LastError string
    CreatedAt, UpdatedAt time.Time
}
```

`UpsertDeploymentIntent` must preserve recovery counters when called from a reconciler and reset them for explicit apply; implement that distinction in Task 5 rather than hiding it in SQL. `StopDeploymentIntent` sets `desired_state=stopped`, clears `next_attempt_at`, increments revision, and succeeds even when the runtime object is already absent.

- [ ] **Step 6: Run DB tests**

Run: `go test ./internal -run 'Test(MigrateV20|DeploymentIntent|IntentConfig)' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/recovery/intent.go internal/sqlite.go internal/sqlite_test.go
git commit -m "feat: persist deployment recovery intent"
```

---

### Task 3: Pure Recovery State Machine

**Files:**
- Create: `internal/recovery/state_machine.go`
- Create: `internal/recovery/state_machine_test.go`

**Interfaces:**
- Consumes: `recovery.Intent` and `recovery.Policy`.
- Produces: `Observation`, `Decision`, `ActionNone`, `ActionRecover`, `ActionQuarantine`, `Evaluate(Intent, Observation, time.Time) Decision`.
- Consumed by: Task 6.

- [ ] **Step 1: Write table-driven failing state tests**

```go
func TestEvaluateRecoveryTransitions(t *testing.T) {
    base := Intent{Name: "m", DesiredState: DesiredRunning, RecoveryState: StateHealthy, Policy: DefaultPolicy()}
    cases := []struct{name string; mutate func(*Intent); obs Observation; now time.Time; action Action; state string}{
        {name: "stopped never recovers", mutate: func(i *Intent){ i.DesiredState = DesiredStopped }, obs: Observation{Exists:false}, action: ActionNone, state: StateHealthy},
        {name: "first failed probe only counts", obs: Observation{Exists:true, Ready:false, Phase:"running"}, action: ActionNone, state: StateHealthy},
        {name: "third failed probe waits", mutate: func(i *Intent){ i.ConsecutiveFailureCount=2 }, obs: Observation{Exists:true, Ready:false}, action: ActionNone, state: StateWaiting},
        {name: "due native wait recovers", mutate: func(i *Intent){ i.Runtime="native"; i.RecoveryState=StateWaiting; i.NextAttemptAt=time.Unix(9,0) }, obs: Observation{Exists:false}, now:time.Unix(10,0), action:ActionRecover, state:StateRecovering},
        {name: "attempt limit quarantines", mutate: func(i *Intent){ i.AttemptCount=3; i.WindowStartedAt=time.Unix(1,0) }, obs: Observation{Exists:false}, now:time.Unix(10,0), action:ActionQuarantine, state:StateQuarantined},
    }
    // Clone base per row, apply mutation, call Evaluate, assert action/state.
}
```

Add separate tests for restart-count delta, stable reset at exactly 600 seconds, window expiration, and backoff selection 2/10/30.

- [ ] **Step 2: Run tests and verify failure**

Run: `go test ./internal/recovery -run TestEvaluate -count=1`

Expected: FAIL because `Evaluate` is undefined.

- [ ] **Step 3: Implement pure transition function**

```go
type Observation struct {
    Exists bool
    Ready bool
    Phase string
    Restarts int
    ExitCode *int
    Error string
}

type Decision struct { Intent Intent; Action Action }
```

Rules must be deterministic and side-effect free. Count a Docker/K3S restart only when `Observation.Restarts > Intent.ObservedRestartCount`. Increment Native attempt count when the controller commits `ActionRecover`, not on every failed poll. A quarantined or stopped intent always returns `ActionNone`.

- [ ] **Step 4: Run state tests**

Run: `go test ./internal/recovery -run 'TestEvaluate' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/recovery/state_machine.go internal/recovery/state_machine_test.go
git commit -m "feat: add bounded recovery state machine"
```

---

### Task 4: Runtime Health and Restart Observability

**Files:**
- Modify: `internal/runtime/docker.go:406-416`
- Modify: `internal/runtime/docker.go:576-635`
- Test: `internal/runtime/docker_test.go`
- Modify: `internal/runtime/native.go` (`nativeProcess`, deploy initialization, `procToStatus`)
- Test: `internal/runtime/native_test.go`

**Interfaces:**
- Produces accurate `DeploymentStatus.Restarts`, `Ready`, `Phase`, and `ExitCode`.
- Consumed by: Task 6 observation adapter.

- [ ] **Step 1: Write failing Docker restart-count test**

```go
func TestDockerInspectToStatusIncludesRestartCount(t *testing.T) {
    rt := &DockerRuntime{}
    got := rt.inspectToStatus(dockerInspect{RestartCount: 4 /* set running state and labels */})
    if got.Restarts != 4 { t.Fatalf("Restarts=%d want 4", got.Restarts) }
}
```

- [ ] **Step 2: Add top-level Docker inspect field and mapping**

```go
type dockerInspect struct {
    RestartCount int `json:"RestartCount"`
    // existing fields
}
```

Set `DeploymentStatus.Restarts = di.RestartCount`.

- [ ] **Step 3: Write failing Native post-ready health test**

Create an `httptest.Server` that returns 200 once and then 503. Deploy or construct the existing `nativeProcess` test fixture with the server port and `/health`; assert a later `Status` reports `Ready=false` after the health endpoint fails.

- [ ] **Step 4: Track health path in memory and re-check it in status**

Add `healthCheckPath string` to `nativeProcess`, set it from `req.HealthCheck.Path`, and in `procToStatus` require `httpHealthy(proc.port, proc.healthCheckPath)` when the path is non-empty. Do not mark an exited process ready even if another process owns the port.

- [ ] **Step 5: Run Runtime tests**

Run: `go test ./internal/runtime -run 'Test(DockerInspectToStatusIncludesRestartCount|Native.*Health)' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/docker.go internal/runtime/docker_test.go internal/runtime/native.go internal/runtime/native_test.go
git commit -m "feat: expose runtime restart and live health state"
```

---

### Task 5: Deploy MCP Path Persists and Enriches Intent

**Files:**
- Modify: `internal/runtime/runtime.go:55-85`
- Modify: `internal/mcp/tools_deps.go:30-45`
- Modify: `internal/mcp/tools_deploy.go` deploy.apply schema/handler
- Test: `internal/mcp/tools_deploy_test.go`
- Modify: `cmd/aima/tooldeps_deploy.go:47-350`
- Modify: `cmd/aima/tooldeps_deploy.go:530-663`
- Modify: `cmd/aima/tooldeps_deploy.go` `deploymentOverview`
- Test: `cmd/aima/tooldeps_deploy_test.go`

**Interfaces:**
- Consumes: Task 1 policy, Task 2 DB methods.
- Produces: public `deploy.apply.recovery_policy`, trusted reconciler context marker, intent-enriched status/list responses.
- Consumed by: Task 6 controller.

- [ ] **Step 1: Write failing MCP input test**

Register deploy tools with a fake `DeployApply` and call `deploy.apply` using:

```json
{"model":"qwen3-4b","recovery_policy":{"max_attempts":5,"backoff_s":[1,3,9]}}
```

Assert the fake receives a `recovery.PolicyPatch` with those values. Add a negative test for `max_attempts=0` returning a tool error.

- [ ] **Step 2: Change ToolDeps signature and handler**

```go
DeployApply func(ctx context.Context, engine, model, slot string,
    configOverrides map[string]any, noPull bool,
    recoveryPolicy recovery.PolicyPatch) (json.RawMessage, error)
```

Update all in-repo call sites to pass `recovery.PolicyPatch{}`. Parse public JSON directly into the patch and call `ResolvePolicy` in the deployment business closure.

- [ ] **Step 3: Write failing explicit-apply and delete-race tests**

Test these observable results with a temporary DB and fake Runtime:

1. Successful explicit apply writes `desired_state=running`, resets counters, and stores the resolved Engine Asset/version/runtime.
2. Reconciler-marked apply preserves counters.
3. Delete writes `desired_state=stopped` before fake `Runtime.Delete` is entered.
4. Deleting a quarantined intent succeeds even when no Runtime deployment exists.

- [ ] **Step 4: Implement trusted call source and intent writes**

```go
type reconcilerSourceKey struct{}
func WithReconcilerSource(ctx context.Context) context.Context
func IsReconcilerSource(ctx context.Context) bool
```

Keep the key type unexported inside `internal/recovery`; expose only the two functions. The MCP JSON schema must not contain a source field.

After resolve/runtime selection and before `Runtime.Deploy`, persist the sanitized declarative intent. Use `resolved.EngineAssetName` to look up the selected Catalog `EngineAsset`, convert `asset.Startup.Recovery` into `recovery.PolicyPatch`, and resolve policy in this order: defaults, selected Engine Asset patch, public request patch. On explicit apply reset recovery fields; on reconciler apply preserve them. On reusable deployment, persist healthy intent.

For delete, resolve matching intents as well as Runtime objects. Stop every matching intent before calling Runtime.Delete. If only a quarantined intent exists, stop it and return success.

- [ ] **Step 5: Enrich status/list output**

Add these additive JSON fields to `DeploymentStatus` and `deploymentOverview`:

```go
DesiredState       string `json:"desired_state,omitempty"`
RecoveryState      string `json:"recovery_state,omitempty"`
RecoveryAttempts   int    `json:"recovery_attempts,omitempty"`
NextRecoveryAt     string `json:"next_recovery_at,omitempty"`
QuarantineReason   string `json:"quarantine_reason,omitempty"`
```

Join actual statuses with intents by exact deployment name. Include quarantined `desired=running` intents in `deploy.list` even when the runtime object has been deleted; do not include ordinary stopped intents. `deploy.status` must return a synthesized quarantined status when no runtime object exists.

- [ ] **Step 6: Run focused tests**

Run: `go test ./internal/mcp ./cmd/aima -run 'Test.*(RecoveryPolicy|DeploymentIntent|Quarantined|Delete.*Intent)' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/runtime/runtime.go internal/mcp/tools_deps.go internal/mcp/tools_deploy.go internal/mcp/tools_deploy_test.go cmd/aima/tooldeps_deploy.go cmd/aima/tooldeps_deploy_test.go internal/recovery/intent.go
git commit -m "feat: connect deploy tools to desired state"
```

---

### Task 6: Recovery Controller and Serve Lifecycle

**Files:**
- Create: `internal/recovery/controller.go`
- Create: `internal/recovery/controller_test.go`
- Modify: `internal/cli/root.go:17-35`
- Modify: `internal/cli/serve.go:25-80`
- Test: `internal/cli/serve_test.go`
- Modify: `cmd/aima/main.go:156-200`
- Modify: `cmd/aima/main.go:430-500`
- Test: `cmd/aima/main_test.go`

**Interfaces:**
- Consumes: Task 2 Store, Task 3 `Evaluate`, Task 5 trusted deploy path.
- Produces: `recovery.NewController`, `(*Controller).RunOnce`, `(*Controller).Start`.

- [ ] **Step 1: Write failing controller side-effect tests**

```go
func TestControllerRecoversDueNativeIntent(t *testing.T) {
    // Fake store returns one due native intent; Observe reports missing.
    // RunOnce must CAS to recovering, call Apply once with IsReconcilerSource(ctx)=true,
    // and persist attempt_count=1 without resetting the window.
}

func TestControllerQuarantinesContainerBeforeDelete(t *testing.T) {
    // Fake store/runtime reports restart count at limit.
    // Assert store records quarantined before Delete is called.
}

func TestControllerNeverActsOnStoppedIntent(t *testing.T) {
    // Observe missing; Apply/Delete call counts remain zero.
}
```

- [ ] **Step 2: Implement controller with narrow consumer interfaces**

```go
type Store interface {
    ListRunnableDeploymentIntents(context.Context) ([]*Intent, error)
    CompareAndSwapDeploymentIntent(context.Context, *Intent, int64) (bool, error)
}
type ObserveFunc func(context.Context, Intent) (Observation, error)
type ApplyFunc func(context.Context, Intent) error
type DeleteFunc func(context.Context, Intent) error

type Controller struct {
    store Store
    observe ObserveFunc
    apply ApplyFunc
    delete DeleteFunc
    now func() time.Time
}
```

`RunOnce` evaluates every runnable intent independently, logs per-intent errors, and continues. Before every side effect, CAS the decision using the prior revision. For Native `ActionRecover`, call apply with `WithReconcilerSource`. For Docker/K3S `ActionQuarantine`, persist quarantine before delete. `Start` uses the minimum enabled policy interval, bounded to one second minimum, and stops on context cancellation.

- [ ] **Step 3: Write failing serve lifecycle hook test**

Add to `cli.App`:

```go
ServeBackground func(context.Context)
```

The test constructs an App whose hook closes a channel, starts `serve` with a cancellable context and loopback ephemeral addresses, and asserts the hook receives a context that is cancelled when serve exits.

- [ ] **Step 4: Invoke hook exactly once from `serve`**

Call `go app.ServeBackground(ctx)` after validation/configuration and before waiting on servers. Do not use `context.Background()`. Keep the hook nil-safe.

- [ ] **Step 5: Wire controller in main**

Build adapters that:

- find actual status across `rt`, `nativeRt`, `dockerRt`, and `k3sRt`;
- convert `runtime.DeploymentStatus` into `recovery.Observation`;
- call the existing `deps.DeployApply` with the Intent inputs, `noPull=true`, and reconciler context;
- delete via the concrete matched runtime;
- assign `app.ServeBackground = controller.Start`.

Do not invoke the HTTP/MCP transport to recover; invoke the same dependency function used by the MCP handler.

- [ ] **Step 6: Run controller and serve tests**

Run: `go test ./internal/recovery ./internal/cli ./cmd/aima -run 'Test(Controller|Serve.*Background|RecoveryController)' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/recovery/controller.go internal/recovery/controller_test.go internal/cli/root.go internal/cli/serve.go internal/cli/serve_test.go cmd/aima/main.go cmd/aima/main_test.go
git commit -m "feat: reconcile failed deployments while serving"
```

---

### Task 7: Patrol Ownership, Contracts, and Documentation

**Files:**
- Modify: `internal/agent/patrol.go`
- Test: `internal/agent/agent_test.go`
- Modify: `docs/runtime.md`
- Modify: `docs/mcp.md`
- Modify: `docs/cli.md`

**Interfaces:**
- Consumes: enriched `deploy.list` output from Task 5.
- Produces: one alert for a quarantined failure episode and no Healer action for quarantine.

- [ ] **Step 1: Write failing Patrol quarantine test**

Return this fake `deploy.list` entry twice across two `RunOnce` calls:

```json
[{"name":"qwen","phase":"failed","recovery_state":"quarantined","quarantine_reason":"3 attempts exhausted"}]
```

Assert there is one unresolved `deploy_crash` alert and zero Healer calls. Keep the existing OOM/temperature tests unchanged.

- [ ] **Step 2: Add recovery fields to Patrol parsing and dedup**

When `recovery_state=quarantined`, create a stable alert identity based on deployment name plus state, not current Unix time. Record a notify action only; never call `Healer.Heal`. Existing non-quarantined failure diagnosis remains intact.

- [ ] **Step 3: Update operator documentation**

Document:

- exact default recovery policy;
- explicit delete versus external process kill;
- `quarantined` meaning and explicit `deploy.apply` recovery;
- Docker/K3S delegation versus Native reconciliation;
- new MCP input/output fields;
- guarantee applies only while `aima serve` runs.

- [ ] **Step 4: Run package tests**

Run: `go test ./internal/agent ./internal/mcp ./cmd/aima -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/patrol.go internal/agent/agent_test.go docs/runtime.md docs/mcp.md docs/cli.md
git commit -m "docs: define bounded deployment recovery contract"
```

---

### Task 8: Recovery Verification Gate

**Files:**
- Modify only files required by failures found in this task.

**Interfaces:**
- Verifies every interface produced by Tasks 1-7.

- [ ] **Step 1: Run focused race tests**

Run: `go test -race ./internal/recovery ./internal/runtime ./internal/mcp ./cmd/aima`

Expected: PASS with no race reports.

- [ ] **Step 2: Run full tests and vet**

Run: `go test ./...`

Expected: PASS.

Run: `go vet ./...`

Expected: exit 0 with no findings.

- [ ] **Step 3: Cross-compile all release targets**

```bash
GOOS=windows GOARCH=amd64 go build -o /tmp/aima-recovery-windows-amd64.exe ./cmd/aima
GOOS=darwin GOARCH=arm64 go build -o /tmp/aima-recovery-darwin-arm64 ./cmd/aima
GOOS=linux GOARCH=amd64 go build -o /tmp/aima-recovery-linux-amd64 ./cmd/aima
GOOS=linux GOARCH=arm64 go build -o /tmp/aima-recovery-linux-arm64 ./cmd/aima
```

Expected: all four commands exit 0.

- [ ] **Step 4: Verify no new partner-specific branches**

Run: `git diff develop...HEAD -- '*.go' | rg -n '(partner|合作伙伴|baidu|百应|claw)'`

Expected: no output. Existing unrelated source text outside the diff does not fail this gate.

- [ ] **Step 5: Record UAT status honestly**

If authorized hardware is available, run the same kill/recover/delete/quarantine scenario on Windows Native, Linux Native, Docker, and K3S; collect all results before changing code. If a target is unavailable, record it as `UNREACHABLE` in the release evidence and do not claim full UAT.

- [ ] **Step 6: Commit verification-only fixes, if any**

```bash
git add -u
git commit -m "test: complete deployment recovery verification"
```

If Step 1-5 required no file changes, do not create an empty commit.
