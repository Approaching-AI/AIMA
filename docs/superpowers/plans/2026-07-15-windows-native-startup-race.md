# Windows Native Startup Race Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop Windows scheduled-task native deployments from being falsely failed and killed before their port binds, preserve logs after cleanup, and publish a traceable AMD395 Windows build after real-machine validation.

**Architecture:** Add a platform-neutral metadata process-state model that separates PID liveness from port ownership, then use it in native status reconciliation. Make scheduled-task PID discovery select only a newly created process, keep in-memory startup state authoritative until the process exits, and add a safe deterministic log fallback. Keep the existing launcher, health timeout, catalog parameters, and non-Windows lifecycle behavior.

**Tech Stack:** Go 1.26, standard library process/network APIs, Windows `schtasks`/`tasklist`/`netstat`, shell packaging scripts, PowerShell, GitHub Actions.

---

### Task 1: Model Windows process identity without treating an unbound port as death

**Files:**
- Modify: `internal/runtime/native_process.go:19-53`
- Modify: `internal/runtime/native.go:788-860`
- Test: `internal/runtime/native_test.go`

- [ ] **Step 1: Write the failing pure-state test**

Add a table-driven test for a wished-for pure helper:

```go
func TestClassifyWindowsProcessMetaState(t *testing.T) {
	tests := []struct {
		name        string
		recordedPID int
		listenerPID int
		pidAlive    bool
		want        processMetaState
	}{
		{name: "alive before port bind", recordedPID: 101, pidAlive: true, want: processMetaStarting},
		{name: "owns listener", recordedPID: 101, listenerPID: 101, pidAlive: true, want: processMetaMatching},
		{name: "different listener", recordedPID: 101, listenerPID: 202, pidAlive: true, want: processMetaStale},
		{name: "exited before port bind", recordedPID: 101, pidAlive: false, want: processMetaExited},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyWindowsProcessMetaState(tt.recordedPID, tt.listenerPID, tt.pidAlive); got != tt.want {
				t.Fatalf("state = %v, want %v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./internal/runtime -run TestClassifyWindowsProcessMetaState -count=1
```

Expected: build failure because the state type and helper do not exist.

- [ ] **Step 3: Add the minimal state model**

Add:

```go
type processMetaState uint8

const (
	processMetaMatching processMetaState = iota
	processMetaStarting
	processMetaStale
	processMetaExited
)

func classifyWindowsProcessMetaState(recordedPID, listenerPID int, recordedAlive bool) processMetaState {
	switch {
	case listenerPID > 0 && listenerPID == recordedPID:
		return processMetaMatching
	case listenerPID > 0:
		return processMetaStale
	case recordedAlive:
		return processMetaStarting
	default:
		return processMetaExited
	}
}
```

Refactor metadata identity evaluation to return `processMetaState`. On Windows with a port, use `findProcessPIDByPort(meta.Port)` and `pidAlive(meta.PID)`. Keep Linux/macOS command matching. Retain `processMatchesMeta` as a compatibility wrapper returning true for matching or starting, so delete can stop a valid deployment still starting.

- [ ] **Step 4: Add a failing phase test**

Extract phase calculation into a pure helper and test that `processMetaStarting`, no listener, a recent start time, and timeout 60 produces `starting`, while stale/exited produce `failed`.

```go
func metaPhaseForProcessState(state processMetaState, portBound bool, startedAt time.Time, timeoutS int) string {
	if state == processMetaStale || state == processMetaExited {
		return "failed"
	}
	if portBound {
		return "running"
	}
	if timeoutS <= 0 {
		timeoutS = 60
	}
	if time.Since(startedAt) < time.Duration(timeoutS)*time.Second {
		return "starting"
	}
	return "failed"
}
```

`metaToStatus` may refine `running` to `starting` when HTTP health is not ready.

- [ ] **Step 5: Verify GREEN**

```bash
go test ./internal/runtime -run 'TestClassifyWindowsProcessMetaState|TestMetaPhaseForProcessState|TestMetaToStatusMarksMissingProcessFailed|TestMetaToStatusMarksStalePortReuseFailed' -count=1
```

Expected: all selected tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/native_process.go internal/runtime/native.go internal/runtime/native_test.go
git commit -m "fix: keep Windows native startup alive before port bind"
```

### Task 2: Keep live in-memory startup state authoritative

**Files:**
- Modify: `internal/runtime/native.go:520-554`
- Test: `internal/runtime/native_test.go`

- [ ] **Step 1: Write a failing reconciliation test**

Create an in-memory `nativeProcess` that has not exited plus metadata with a non-matching PID. Assert `Status` stays `starting` and does not inherit `process exited before readiness`.

```go
func TestStatusDoesNotOverrideLiveStartingProcessWithPersistedFailure(t *testing.T) {
	rt := newTestRuntime(t)
	proc := &nativeProcess{
		name: "slow-start", port: freeTCPPort(t),
		startTime: time.Now(), logPath: filepath.Join(t.TempDir(), "slow-start.log"),
	}
	rt.processes[proc.name] = proc
	if err := rt.saveMeta(&deploymentMeta{
		Name: proc.name, PID: 999999, Port: proc.port,
		Command: []string{"llama-server", "--port", strconv.Itoa(proc.port)},
		StartTime: proc.startTime,
	}); err != nil {
		t.Fatal(err)
	}
	status, err := rt.Status(context.Background(), proc.name)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != "starting" {
		t.Fatalf("phase = %q, want starting", status.Phase)
	}
	if status.Message == "process exited before readiness" {
		t.Fatalf("live startup inherited false persisted failure: %#v", status)
	}
}
```

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/runtime -run TestStatusDoesNotOverrideLiveStartingProcessWithPersistedFailure -count=1
```

Expected: current code reports `failed`.

- [ ] **Step 3: Implement minimal precedence change**

Calculate:

```go
ignorePersistedFailure := persisted.Phase == "failed" && status.Phase != "failed" && !exited
```

When true, do not return persisted failure and do not copy its failure message. Continue merging config and useful startup progress. Log-pattern errors remain effective because `procToStatus` independently marks the in-memory phase failed.

- [ ] **Step 4: Verify precedence tests and commit**

```bash
go test ./internal/runtime -run 'TestStatusDoesNotOverrideLiveStartingProcessWithPersistedFailure|TestStatusPrefersPersistedFailureOverInMemoryProcess|TestListPrefersPersistedFailureOverInMemoryProcess' -count=1
git add internal/runtime/native.go internal/runtime/native_test.go
git commit -m "fix: preserve live native startup status"
```

Update the two old precedence fixtures to mark the in-memory process exited; persisted failure should only override completed in-memory state.

### Task 3: Select only the newly launched scheduled-task PID

**Files:**
- Modify: `internal/runtime/native_process.go`
- Modify: `internal/runtime/native_windows.go:34-130,159-181`
- Test: `internal/runtime/native_test.go`

- [ ] **Step 1: Write failing PID-selection tests**

```go
func TestSelectNewProcessPID(t *testing.T) {
	tests := []struct {
		name string
		before, current []int
		want int
	}{
		{name: "one new process", before: []int{10, 20}, current: []int{10, 20, 30}, want: 30},
		{name: "reject pre-existing", before: []int{10}, current: []int{10}, want: 0},
		{name: "reject ambiguity", before: []int{10}, current: []int{10, 20, 30}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectNewProcessPID(tt.before, tt.current); got != tt.want {
				t.Fatalf("pid = %d, want %d", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/runtime -run TestSelectNewProcessPID -count=1
```

Expected: build failure because the selector does not exist.

- [ ] **Step 3: Implement the pure selector**

Build a set from `before`, return the sole positive PID in `current` absent from that set, and return 0 if there are zero or multiple new PIDs.

```go
func selectNewProcessPID(before, current []int) int {
	seen := make(map[int]struct{}, len(before))
	for _, pid := range before {
		seen[pid] = struct{}{}
	}
	candidate := 0
	for _, pid := range current {
		if pid <= 0 {
			continue
		}
		if _, exists := seen[pid]; exists {
			continue
		}
		if candidate != 0 && candidate != pid {
			return 0
		}
		candidate = pid
	}
	return candidate
}
```

- [ ] **Step 4: Replace ambiguous Windows lookup**

Change tasklist parsing to return all matching PIDs. Capture the pre-launch PID list immediately before `schtasks /run`. During discovery, prefer the target-port listener, otherwise call `selectNewProcessPID(beforePIDs, currentPIDs)`. If no unique new PID appears, return the existing discovery error so `Deploy` uses its direct-launch fallback. Never accept a pre-existing same-name process.

- [ ] **Step 5: Verify host behavior and Windows compilation**

```bash
go test ./internal/runtime -run TestSelectNewProcessPID -count=1
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/runtime -o /tmp/aima-runtime-windows.test.exe
```

Expected: the test passes and Windows test binary is produced.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/native_process.go internal/runtime/native_windows.go internal/runtime/native_test.go
git commit -m "fix: bind scheduled task to newly launched PID"
```

### Task 4: Preserve log access after failed cleanup

**Files:**
- Modify: `internal/runtime/native.go:561-575`
- Test: `internal/runtime/native_test.go`

- [ ] **Step 1: Write failing tests**

Write a log at `filepath.Join(rt.logDir, "failed-model.log")` with no metadata and assert `rt.Logs(..., "failed-model", 20)` returns it. Add a traversal test asserting names containing `/` or `\`, plus `.` and `..`, are rejected.

```go
func TestLogsFallsBackToDeterministicPathAfterMetadataRemoval(t *testing.T) {
	rt := newTestRuntime(t)
	if err := os.MkdirAll(rt.logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rt.logDir, "failed-model.log"), []byte("engine root cause\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := rt.Logs(context.Background(), "failed-model", 20)
	if err != nil {
		t.Fatal(err)
	}
	if got != "engine root cause" {
		t.Fatalf("logs = %q", got)
	}
}

func TestLogsFallbackRejectsPathTraversal(t *testing.T) {
	rt := newTestRuntime(t)
	for _, name := range []string{"", ".", "..", "../secret", `..\secret`} {
		if _, err := rt.Logs(context.Background(), name, 20); err == nil {
			t.Fatalf("unsafe name %q was accepted", name)
		}
	}
}
```

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/runtime -run 'TestLogsFallsBackToDeterministicPathAfterMetadataRemoval|TestLogsFallbackRejectsPathTraversal' -count=1
```

Expected: deterministic fallback fails because metadata is missing.

- [ ] **Step 3: Implement safe fallback**

After metadata lookup fails, reject empty names, `.`, `..`, and names containing either path separator. Otherwise read `filepath.Join(r.logDir, name+".log")`. Return the metadata error only when the safe fallback file cannot be read.

```go
func nativeFallbackLogPath(logDir, name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return ""
	}
	return filepath.Join(logDir, name+".log")
}
```

- [ ] **Step 4: Verify GREEN and commit**

```bash
go test ./internal/runtime -run 'TestLogsFallsBackToDeterministicPathAfterMetadataRemoval|TestLogsFallbackRejectsPathTraversal' -count=1
git add internal/runtime/native.go internal/runtime/native_test.go
git commit -m "fix: retain failed native deployment log access"
```

### Task 5: Stop promoting normal HIP startup information to an error

**Files:**
- Modify: `cmd/aima/deploy_failure.go:321-330`
- Test: `cmd/aima/main_test.go:740-840`

- [ ] **Step 1: Add a failing summarizer case**

```go
{
	name:       "ignore normal HIP library path",
	message:    "process exited before readiness",
	errorLines: `HIP Library Path: C:\Windows\SYSTEM32\amdhip64_7.dll`,
	want:       "process exited before readiness",
},
```

- [ ] **Step 2: Verify RED**

```bash
go test ./cmd/aima -run TestSummarizeDeploymentFailure/ignore_normal_HIP_library_path -count=1
```

Expected: summary incorrectly equals the HIP path.

- [ ] **Step 3: Mark the line as low signal**

Add `strings.HasPrefix(lower, "hip library path:")` to `isLowSignalErrorLine`.

- [ ] **Step 4: Verify GREEN and commit**

```bash
go test ./cmd/aima -run 'TestSummarizeDeploymentFailure|TestClassifyDeploymentFailure' -count=1
git add cmd/aima/deploy_failure.go cmd/aima/main_test.go
git commit -m "fix: ignore normal HIP startup path in failure summary"
```

Expected: generic early exit still classifies as `ENGINE_START_FAILED`.

### Task 6: Verify source and record evidence

**Files:**
- Create: `docs/amd395-windows-startup-race-validation-20260715.md`

- [ ] **Step 1: Format and inspect scope**

```bash
gofmt -w internal/runtime/native_process.go internal/runtime/native.go internal/runtime/native_windows.go internal/runtime/native_test.go cmd/aima/deploy_failure.go cmd/aima/main_test.go
git diff --check
git status --short
git diff origin/amd395-win...HEAD -- internal/runtime cmd/aima docs/superpowers
```

Expected: no whitespace errors or unrelated files.

- [ ] **Step 2: Run complete checks**

```bash
go test ./...
go test -race ./internal/runtime ./cmd/aima
go vet ./...
make amd395-build-test
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/aima-windows-amd64-startup-race.exe ./cmd/aima
```

Expected: every command exits 0.

- [ ] **Step 3: Prove RED-GREEN after implementation**

Save the current production files, restore the pre-fix versions from commit `2962cc8`, run the focused regression tests and require intended failures, then restore current files and require passes. Do not commit the temporary revert.

- [ ] **Step 4: Create the validation record**

Record exact source commit, commands, exit results, and a clearly pending AMD395 section. Do not claim real-machine success before the Windows commands run.

- [ ] **Step 5: Commit**

```bash
git add docs/amd395-windows-startup-race-validation-20260715.md
git commit -m "docs: record Windows startup race source validation"
```

### Task 7: Build and validate on the AMD395 Windows host

**Files:**
- Modify: `docs/amd395-windows-startup-race-validation-20260715.md`

- [ ] **Step 1: Build a clean candidate**

```bash
test -z "$(git status --porcelain)"
SOURCE_COMMIT="$(git rev-parse HEAD)"
BUILD_TIME="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
GIT_COMMIT="$SOURCE_COMMIT" BUILD_TIME="$BUILD_TIME" OUTPUT_DIR="$PWD/dist/amd395-windows" bash scripts/package-amd395-windows.sh
(cd dist/amd395-windows && shasum -a 256 -c checksums.txt)
```

Expected: clean build with passing checksum.

- [ ] **Step 2: Verify embedded identity**

Run `go version -m` on the EXE named by `build-metadata.json`. Require custom `GitCommit` and Go `vcs.revision` to equal `SOURCE_COMMIT`, and `vcs.modified=false`.

- [ ] **Step 3: Copy to AMD395**

```bash
ssh amd395 'powershell.exe -NoProfile -NonInteractive -Command "New-Item -ItemType Directory -Force C:\AIMA-UAT\20260715 | Out-Null"'
scp dist/amd395-windows/*.exe dist/amd395-windows/checksums.txt dist/amd395-windows/build-metadata.json amd395:'C:/AIMA-UAT/20260715/'
```

Expected: all artifacts exist on the test host. If the test-network host is offline, stop here and report the connectivity blocker; do not publish.

- [ ] **Step 4: Run Windows smoke checks**

Use `C:\AIMA-UAT\20260715\data` as a clean `AIMA_DATA_DIR` and `C:\ProgramData\Lenovo\baiying-llm\engine\llama.cpp` as `AIMA_ENGINE_DIR`. Verify `version`, `hal detect`, `catalog validate`, and dry runs for both reported models.

- [ ] **Step 5: Validate Embedding three cold starts**

For each cycle: deploy `Qwen3-Embedding-4B-Q4_K_M`, require exit 0 and ready status, send a non-empty `/v1/embeddings` response, undeploy, and confirm its PID/listener disappears.

- [ ] **Step 6: Validate Qwen3.6-35B 256K**

Deploy `Qwen3.6-35B-A3B-UD-Q4_K_M` with factory config, observe `starting` while loading, require ready, and complete an eight-token chat request. Preserve the full native log.

- [ ] **Step 7: Validate PID isolation and logs**

Start an unrelated installed `llama-server.exe` on another port and record its PID. Deploy and undeploy the AIMA model; require the unrelated PID remains alive. Induce a safe invalid-model failure and require `deploy logs` still reads the raw log after cleanup.

- [ ] **Step 8: Commit real-machine evidence**

Append timestamps, machine/GPU/driver identity, commands, exit codes, PID/port snapshots, response summaries, log paths, commit and checksum.

```bash
git add docs/amd395-windows-startup-race-validation-20260715.md
git commit -m "docs: record AMD395 startup race validation"
```

### Task 8: Review, package dated EXE, and publish

**Files:**
- Create: `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260715.exe`
- Modify: `dist/README.md`
- Modify: `docs/amd395-windows-startup-race-validation-20260715.md`

- [ ] **Step 1: Request code review**

Review `origin/amd395-win..HEAD` against the approved design and this plan. Resolve all Critical and Important findings and rerun affected checks.

- [ ] **Step 2: Run fresh final verification**

```bash
go test ./...
go test -race ./internal/runtime ./cmd/aima
go vet ./...
make amd395-build-test
git diff --check
```

Expected: every command exits 0 immediately before packaging.

- [ ] **Step 3: Build from the final clean source commit**

Package again with `GIT_COMMIT=$(git rev-parse HEAD)`, verify checksum and embedded identity, then copy the candidate EXE to the dated dist path. Update `dist/README.md` and the validation note with exact commit, build time, SHA-256, and real-machine result.

- [ ] **Step 4: Commit immutable artifact**

```bash
git add dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260715.exe dist/README.md docs/amd395-windows-startup-race-validation-20260715.md
git commit -m "build: package Windows native startup race fix"
```

The embedded source commit is the preceding clean source commit; the packaging commit only adds the immutable artifact and record. Document both IDs.

- [ ] **Step 5: Push without force**

```bash
git fetch origin amd395-win
git merge-base --is-ancestor origin/amd395-win HEAD
git push origin HEAD:amd395-win
gh run list --workflow "AMD395 Windows Build" --branch amd395-win --limit 1
```

Expected: normal fast-forward push succeeds and the workflow starts. Wait for workflow success before reporting publication complete.
