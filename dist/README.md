# AIMA — AMD Strix Halo (Win11) preview build

AIMA for the **AMD Ryzen AI Max+ 395 / Radeon 8060S "Strix Halo"** on Windows 11.
Built from `develop` + the platform fixes below. Preview/handoff build — **not an official release**.

### Builds (version-stamped filenames — newer builds do NOT overwrite older ones)

| File | `aima version` | Date | Notes |
|------|----------------|------|-------|
| `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260610.exe` | `v0.5-dev-amd-strix-halo-20260610` | 2026-06-10 | **latest** — adds out-of-box HIP engine (#85, #86). `serve.bat` uses this one. |
| `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo.exe` | `v0.5-dev-amd-strix-halo` | 2026-06-09 | prior build (#78–#83 only, **no** HIP engine auto-download). Kept for rollback. |

Both built from the same source line (commit `fc3ef41`); the file name matches the
exe's own `aima version` string. Launcher: `dist/serve.bat` (edit the exe name there to
switch builds).

> **2026-06-10 rebuild** — now includes the out-of-box AMD-HIP engine work (#85, #86):
> AIMA **auto-downloads a Strix-Halo-adapted ROCm/HIP llama.cpp** on first deploy, and a
> pre-installed engine you point `AIMA_ENGINE_DIR` at is the one that actually launches
> ("scanned ⇒ launchable"). **Manually setting `AIMA_ENGINE_DIR` is no longer required** —
> verified on the 395: empty dist + no `AIMA_ENGINE_DIR` → `aima deploy <gguf> --engine llamacpp`
> auto-fetches `llama-b9330-bin-win-hip-radeon-x64` and runs all layers on the iGPU (~33 t/s).

## Fixes included (vs develop) — PRs #78–#86

- **Windows hardware detection**: `wmic` was removed in Win11 24H2, so CPU/RAM detection
  returned empty. Now uses PowerShell **CIM** (`Get-CimInstance`), detects the **AMD iGPU**,
  reports the **true installed unified memory (128 GB)**, and reads the **real GPU-usable VRAM
  (~110 GB via ROCm)** for deploy-fit. (#78)
- **Model scan across all drives** + group split GGUF shards. (#79)
- **Engine scan off-PATH** via `AIMA_ENGINE_DIR`. (#80)
- **Strix Halo llama.cpp GGUF catalog knowledge** (aliases + verified perf). (#81)
- **Speculative draft heads (DFlash/MTP)** are no longer listed as standalone deployable
  models. (#82)
- **External services**: AIMA's own deployment backend (llama.cpp default port 8080) is not
  re-listed as importable, and dead/undeployed services drop out of the list. (#83)
- **Out-of-box AMD-HIP llama.cpp engine**: new `llamacpp-hip-windows` catalog asset
  (`go:embed`'d, pins official `b9330` `win-hip-radeon-x64`) so a no-NVIDIA Strix Halo box
  auto-downloads the *right* ROCm/HIP llama.cpp — not the CUDA `llamacpp-universal` (which
  would run CPU-only) nor the linux-only vulkan/rocm assets. (#85)
- **Scanned ⇒ launchable**: the native runtime resolves the engine binary against
  `AIMA_ENGINE_DIR` (order: dist → `AIMA_ENGINE_DIR` → auto-download → PATH), so a
  pre-installed llama.cpp of **any version** is the one that actually launches — fixes the
  partner's "deploy falls back to bare `llama-server` → command not found". (#86)

## Run

1. Run `serve.bat`. The llama.cpp engine is handled automatically:
   - **No engine on the box?** On first `aima deploy <model> --engine llamacpp` AIMA
     auto-downloads the official ROCm/HIP build (`b9330`, `win-hip-radeon-x64`) and runs
     on the iGPU. No setup needed. (#85)
   - **Already have your own llama.cpp build (any version)?** Set `AIMA_ENGINE_DIR` in
     `serve.bat` to its folder — AIMA launches *that exact binary*, so your llama.cpp
     version is supported regardless of what AIMA ships. Must be a **ROCm/HIP** build,
     not the CUDA build (CUDA won't start on a no-NVIDIA box). (#86)
2. Open `http://localhost:6188/ui/`.

**LAN access** (other machines): it binds `0.0.0.0:6188`, but Windows Firewall auto-creates an
*inbound block* rule for `aima.exe` on first listen. Remove it and add an allow rule:

```
netsh advfirewall firewall delete rule name="aima.exe"
netsh advfirewall firewall add rule name="AIMA UI 6188" dir=in action=allow protocol=TCP localport=6188 profile=any
```

Note: when opened via a non-`localhost` host the UI won't auto-fill the API key — set it once in
Settings → General → API Key = `local`.

## OpenClaw auto-connect to local models

Install OpenClaw, then deploy a model in AIMA. AIMA automatically writes an `aima` provider into
`~/.openclaw/openclaw.json` (`baseUrl http://127.0.0.1:6188/v1`, the matching `apiKey`, and the
deployed models), so OpenClaw connects to the local model with zero manual config.

## 连线灵机云 (remote assist)

In the UI, click **连线灵机云** and enter the invite/worker code (the machine must be online;
it connects to aimaserver.com). Backed by the MCP `support.askforhelp` tool — that's why
`serve.bat` runs with `--mcp`.

## Notes

- Unified memory shows the nameplate **128 GB**; deploy-fit uses the real GPU-usable **~110 GB**.
- Don't run two `aima` instances against the same data dir / ports.
