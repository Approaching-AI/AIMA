# AIMA — AMD Strix Halo (Win11) preview build

AIMA for the **AMD Ryzen AI Max+ 395 / Radeon 8060S "Strix Halo"** on Windows 11.
Built from `develop` + the platform fixes below. Preview/handoff build — **not an official release**.

- Binary: `dist/aima-windows-amd64.exe` (`aima version` → `v0.5-dev-amd-strix-halo`)
- Launcher: `dist/serve.bat`

## Fixes included (vs develop) — all in PRs #78–#83

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

## Run

1. Put a **ROCm/HIP** build of llama.cpp somewhere and set `AIMA_ENGINE_DIR` to it in
   `serve.bat` (the CUDA build will not start on a no-NVIDIA box).
2. Run `serve.bat`.
3. Open `http://localhost:6188/ui/`.

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
