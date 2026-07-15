# Windows Native Startup Race Fix Design

## Goal

Prevent AIMA from falsely marking a Windows native deployment as failed while
the scheduled-task-launched engine process is alive but has not bound its HTTP
port yet. Preserve useful logs after automatic cleanup, prevent ambiguous PID
selection when multiple `llama-server.exe` processes exist, and produce a
traceable Windows artifact for AMD395 validation.

## Root Cause

The Windows native runtime currently uses the target port as both a readiness
signal and a process-identity signal. During model loading, the process can be
alive for tens of seconds before the port appears in `netstat`. In that window,
`processMatchesMeta` reports false, `metaToStatus` converts the deployment to
`failed`, and persisted status overrides the in-memory `starting` state. The
deploy orchestration then kills the process as failed cleanup.

The scheduled-task launcher also falls back from port lookup to the first PID
with the same image name. When another llama.cpp deployment already exists,
that fallback can associate the new deployment with an unrelated process.

Finally, automatic cleanup removes deployment metadata. The raw log file stays
on disk, but later `deploy logs` calls cannot discover it. If the only captured
line is `HIP Library Path: ...`, the failure summarizer promotes that normal HIP
startup line to an `UNKNOWN_ERROR` message.

## Selected Approach

Use a narrow lifecycle fix rather than changing timeouts or replacing the
Windows launcher architecture.

### Process state

Represent Windows metadata checks with enough information to distinguish:

- the recorded PID is alive and the target port is not bound yet;
- the target port is bound by the recorded PID;
- the target port is bound by another PID;
- the recorded PID is no longer alive.

An alive PID with an unbound port is a valid startup state until the existing
health-check timeout expires. A different listener PID is stale metadata. A
dead recorded PID is an exited process.

Non-Windows command-line identity checks retain their current behavior.

### In-memory status authority

When a process launched by the current AIMA invocation has not exited, its
in-memory `starting` state remains authoritative. Persisted failure state must
not override it solely because the target port is still unbound. Real startup
errors detected from engine logs continue to make the in-memory status fail.

### Scheduled-task PID discovery

Before running the scheduled task, capture the set of PIDs for the target image
name. During discovery:

1. prefer a listener on the requested port;
2. otherwise accept only a PID that appeared after the launch;
3. never select a pre-existing same-name PID as the new deployment.

If no unambiguous PID appears within the existing discovery window, fall back
to direct process launch instead of tracking an unrelated process.

### Failure logs and classification

If deployment metadata has already been removed, native log lookup may fall
back to `<logDir>/<deployment-name>.log` for a safe, single-path deployment
name. This preserves `deploy logs` after failed-deployment cleanup without
keeping stale deployment state.

`HIP Library Path:` is classified as low-signal startup information. It cannot
replace a generic engine-start failure as the reported root cause.

## Error and Cleanup Semantics

- A live, unbound startup remains `starting` until the health timeout.
- A dead process before readiness becomes `ENGINE_START_FAILED`.
- A port owned by another PID becomes a stale/port-conflict failure.
- Cleanup kills only the PID unambiguously assigned to this deployment.
- Cleanup removes deployment metadata but keeps the engine log file readable.
- Swap warnings and context-size settings do not alter lifecycle state.

## Testing

### Automated regression tests

Add test-first coverage for:

- alive PID plus unbound port remains `starting`;
- in-memory `starting` status is not overridden by a persisted false failure;
- dead PID before readiness remains failed;
- a port bound by another PID remains stale metadata;
- PID selection rejects PIDs present before scheduled-task launch;
- PID selection accepts a newly appeared same-name PID;
- log lookup works after metadata removal;
- `HIP Library Path:` is ignored by failure summarization.

Run the complete Go suite, race-enabled tests for the runtime and command
packages, `go vet`, formatting checks, AMD395 workflow contract tests, and a
Windows cross-build.

### AMD395 Windows validation

Use the configured `amd395` Windows test host and a dedicated `AIMA_DATA_DIR`.
Validate:

1. Qwen3-Embedding-4B cold deployment repeatedly reaches ready;
2. Qwen3.6-35B-A3B with the catalog 262144 context reaches ready;
3. a second llama.cpp process does not cause PID misassociation;
4. failed cleanup does not remove access to the raw log;
5. the service completes a minimal embedding or chat request after readiness;
6. no unrelated llama.cpp process is killed.

Preserve the complete command output, native logs, deployment metadata, process
list, and port ownership evidence for the validation record.

## Packaging and Publication

After source tests and AMD395 validation pass:

1. commit the source and regression tests;
2. build the Windows EXE from a clean source commit;
3. verify the custom `GitCommit`, Go `vcs.revision`, build metadata commit, and
   source commit all identify the same revision;
4. calculate and record SHA-256;
5. add the dated `20260715` EXE and validation note in a separate packaging
   commit;
6. push the verified commits to `amd395-win` without force-pushing.

## Non-Goals

- Replacing scheduled tasks with Windows Job Objects in this release.
- Changing model context defaults, swap policy, or HIP/COMGR tuning.
- Refactoring non-Windows native lifecycle management.
- Treating the normal `HIP Library Path` line as evidence of a driver failure.

## Acceptance Criteria

- The new regression tests fail on the current branch and pass with the fix.
- Existing deployment lifecycle tests continue to pass.
- AMD395 repeated cold-start validation passes for both reported model paths.
- Multi-process validation shows the deployment tracks and cleans up only its
  own process.
- Logs remain retrievable after automatic failed-deployment cleanup.
- The produced EXE has consistent source revision metadata and a recorded
  SHA-256 checksum.
- Only the scoped source, tests, design/validation documentation, and new EXE
  are published to `amd395-win`.
