# U13 Smoke Matrix

Historical note: this README captures the original 2026-04-20 matrix run at `HEAD=44bc4c7e362d`.
For the later current-HEAD refresh on the subset of devices that remained reachable, also see:

- `../u13-matrix-current-head/README.md`

- Date: 2026-04-20
- AIMA repo: `HEAD=44bc4c7e362d`
- Build method: `make all`
- Injected build version: `v0.4-dev`
- Injected build time: `2026-04-20T10:58:58Z`

## Verdict

`KNOWN ISSUE`

The smoke command set itself is stable on all devices that were reachable in
this round, but the U13 acceptance bar is still unmet:

- only 7/10 target devices were reachable over SSH in this round.

## What Was Verified

1. A current cross-built binary from repo `HEAD=44bc4c7e362d` was distributed to
   the reachable target devices in isolated paths (`~/aima-smoke-u13/`).

2. The same smoke command set was executed per device:
   - `aima version`
   - `aima hal detect`
   - `aima engine list`
   - `aima model list`
   - `aima deploy list`
   - `aima device status`

3. Seven devices were reachable and all six commands completed with `exit=0`:
   - `dev-mac`
   - `test-win`
   - `gb10`
   - `gb10-4t`
   - `linux-1`
   - `amd395`
   - `w7900d`

4. Three devices were unreachable and timed out at SSH connect:
   - `aibook`
   - `m1000`
   - `metax-n260`

5. No reachable device crashed on the smoke command set.
   - `hal detect`, `engine list`, `model list`, `deploy list`, and `device status`
     all returned successfully on every reachable device.

6. The observed version strings are consistent with the current pre-tag smoke rule.
   - All seven reachable devices reported `aima v0.4-dev`
   - This matches the current development-line build rule for non-tagged `HEAD`
   - U13 remains open because full 10-device coverage was not achieved, not because of the `v0.4-dev` string itself

## Evidence

- `90-matrix-summary.md`
- `dev-mac/`
- `test-win/`
- `gb10/`
- `gb10-4t/`
- `linux-1/`
- `amd395/`
- `w7900d/`
- `aibook/`
- `m1000/`
- `metax-n260/`
