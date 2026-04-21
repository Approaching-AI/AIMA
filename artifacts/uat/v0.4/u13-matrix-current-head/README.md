# U13 Smoke Matrix — current HEAD refresh

- Date: 2026-04-21
- AIMA repo: `HEAD=7dc8718`
- Build method: `make all`
- Injected build version: `v0.4-dev`
- Injected build time: `2026-04-21T07:38:25Z`

## Verdict

`KNOWN ISSUE`

The smoke command set is still healthy on the hosts that were reachable in this round, but U13 remains open because the full 10-device matrix was not refreshed at current `HEAD`.

## What Was Verified

1. A current cross-built binary from repo `HEAD=7dc8718` was used for this refresh.
   - `dev-mac`: `build/aima-darwin-arm64`
   - `gb10` / `gb10-4t`: `build/aima-linux-arm64`
   - `linux-1` / `amd395` / `w7900d`: `build/aima-linux-amd64`

2. The same smoke command set was executed again on the currently reachable devices:
   - `aima version`
   - `aima hal detect`
   - `aima engine list`
   - `aima model list`
   - `aima deploy list`
   - `aima device status`

3. Six hosts were refreshed successfully at current `HEAD`:
   - `dev-mac`
   - `gb10`
   - `gb10-4t`
   - `linux-1`
   - `amd395`
   - `w7900d`

4. All refreshed hosts reported the expected current build metadata:
   - `aima v0.4-dev`
   - `build: 2026-04-21T07:38:25Z`
   - `commit: 7dc8718`

5. No refreshed host showed a crash or missing-command symptom in the smoke command set.
   - The outputs in `smoke.txt` complete the six-command sequence on every refreshed remote host
   - `dev-mac` also completed the same six commands into separate output files

## Remaining Gap

U13 still cannot be marked `PASS` because the current-head refresh is incomplete:

- `test-win` was not refreshed in this round
- `aibook`, `m1000`, and `metax-n260` remain unreachable

So the matrix is healthier and more current than before, but still short of the 10-device acceptance bar.

## Evidence

- `dev-mac/01-version.txt`
- `dev-mac/02-hal-detect.json`
- `dev-mac/03-engine-list.json`
- `dev-mac/04-model-list.json`
- `dev-mac/05-deploy-list.json`
- `dev-mac/06-device-status.json`
- `gb10/smoke.txt`
- `gb10-4t/smoke.txt`
- `linux-1/smoke.txt`
- `amd395/smoke.txt`
- `w7900d/smoke.txt`
