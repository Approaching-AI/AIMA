# U13 Smoke Matrix — post-tag refresh

- Date: 2026-04-21
- AIMA tag: `v0.4.0`
- Tagged commit: `c185d38`
- Build method: `make all`
- Injected build version: `v0.4.0`
- Injected build time: `2026-04-21T11:14:24Z`

## Verdict

`KNOWN ISSUE`

The post-tag smoke command set is healthy on every host that was reachable in this round, including `test-win`, but U13 still cannot be marked `PASS` because three target devices remained unreachable.

## What Was Verified

1. The final tagged binaries were rebuilt after `v0.4.0` was moved to the fixed `master` tip `c185d38`.
   - `build/aima-darwin-arm64`
   - `build/aima.exe`
   - `build/aima-linux-arm64`
   - `build/aima-linux-amd64`

2. The same six-command smoke set was rerun against every reachable post-tag host:
   - `aima version`
   - `aima hal detect`
   - `aima engine list`
   - `aima model list`
   - `aima deploy list`
   - `aima device status`

3. Seven hosts completed the full post-tag smoke successfully:
   - `dev-mac`
   - `test-win`
   - `gb10`
   - `gb10-4t`
   - `linux-1`
   - `amd395`
   - `w7900d`

4. All seven successful hosts reported the same release build metadata:
   - `aima v0.4.0`
   - `build: 2026-04-21T11:14:24Z`
   - `commit: c185d38`

5. Three devices remained unreachable and were preserved as matrix gaps, not product failures:
   - `aibook`
   - `m1000`
   - `metax-n260`

## Evidence

- `dev-mac/01-version.txt`
- `dev-mac/02-hal-detect.json`
- `dev-mac/03-engine-list.json`
- `dev-mac/04-model-list.json`
- `dev-mac/05-deploy-list.json`
- `dev-mac/06-device-status.json`
- `test-win/smoke.txt`
- `gb10/smoke.txt`
- `gb10-4t/smoke.txt`
- `linux-1/smoke.txt`
- `amd395/smoke.txt`
- `w7900d/smoke.txt`
- `aibook/unreachable.txt`
- `m1000/unreachable.txt`
- `metax-n260/unreachable.txt`
