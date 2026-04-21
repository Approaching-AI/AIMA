# U6 on `light-salt` (`test-win`)

- Initial run: 2026-04-20
- Current rerun: 2026-04-21
- Host: `jguan@100.114.25.35` (`Light-Salt`, Windows 11, RTX 4060 8GB)
- Latest binary: `aima v0.4-dev` (`af9ba09`)
- Latest isolation: `C:\Users\jguan\aima-uat-rerun\u6-pass\data`

## Verdict

`PASS`

The 2026-04-20 failure is superseded by the rerun on `af9ba09`.

## What Was Verified

1. Unregistered bootstrap remains offline-first.
   - `device status` started as `registered=false`, `registration_state=unregistered`.
   - `knowledge sync --push` failed cleanly with `device not registered with aima-service`.

2. Windows native `llamacpp` deploy now works before registration.
   - `deploy Qwen3-0.6B-Q8_0 --engine llamacpp` returned `0`.
   - Returned config only contained `ctx_size`, `n_gpu_layers`, `port`.
   - No leaked `--gpu-memory-utilization` appeared in the deploy payload.
   - Poll 1 showed `startup_phase=loading_model`, `ready=false`; poll 2 showed `ready=true`.
   - Final `deploy list` reported the deployment as `runtime=native`, `engine=llamacpp-universal`, `ready=true`.

3. Explicit registration writes canonical + mirrored keys consistently.
   - `device register --invite-code U6-INVITE-20260421` succeeded against the mock support service.
   - `device status` ended as `registered=true`, `registration_state=registered`, `device_id=dev-u6-light-salt`.
   - `device.id` / `device.token` / `device.recovery_code` matched their `support.state.*` mirrors.

## Evidence

- `10-rerun-af9ba09.json`: full rerun summary on current HEAD
- `03-device-status-before.txt`: original pre-serve `unregistered` capture
- `04-sync-push-before-serve.txt`: original graceful rejection before registration
- `08-identity-mirror-check.txt`: original mirror-key capture from the first run

## Notes

- The rerun JSON was captured through Python `subprocess(..., text=True)` on Windows; one background reader thread logged a benign decode error, but the script completed and emitted the full summary.
