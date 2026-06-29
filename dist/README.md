# AIMA — AMD Strix Halo (Win11) preview build

AIMA for the **AMD Ryzen AI Max+ 395 / Radeon 8060S "Strix Halo"** on Windows 11.
Built from `develop` + the platform fixes below. Preview/handoff build — **not an official release**.

### Builds (version-stamped filenames — newer builds do NOT overwrite older ones)

| File | `aima version` | Date | Notes |
|------|----------------|------|-------|
| `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260629d.exe` | `v0.5-dev-amd-strix-halo-20260629d` | 2026-06-29 | **latest** — makes **`aima deploy` of Qwen3.6-35B work on b9196 too** (the partner's engine), without an engine upgrade or disabling the cache. Root cause: the deploy warmup sent **two identical `Hello` requests**; the 2nd hits a 100%-cached prompt → llama.cpp drops the last token via a partial seq_rm → ABORTs on b9180/b9196. Fix: the **warmup requests now send `cache_prompt:false`** so they don't reuse the slot cache; real serving still caches (full speed). Verified on the partner's exact b9196 (2× identical Hello CRASHES; with `cache_prompt:false` SURVIVES). `serve.bat` uses this one. |
| `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260629c.exe` | `v0.5-dev-amd-strix-halo-20260629c` | 2026-06-29 | Qwen3.6-35B deploy crash root cause **corrected: it's the llama.cpp ENGINE VERSION** (same-machine A/B: b9196 CRASHES, b9330 SURVIVES, identical command). Removed the `cache_prompt:false` catalog workaround (it disabled the prompt cache = slower); engine b9330+ is the clean engine-side fix. Keeps the OpenClaw 撤回同步 (exclude/include) feature. Superseded by 20260629d (warmup-level fix works on b9196). Kept for rollback. |
| `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260629b.exe` | `v0.5-dev-amd-strix-halo-20260629b` | 2026-06-29 | adds **OpenClaw 撤回同步**(`aima openclaw exclude/include <model>`, MCP `openclaw` action=exclude/include): persistent + reversible per-model opt-out so a model removed from OpenClaw is NOT re-added by the auto-sync loop, until `include`. Model keeps serving in AIMA (exclude ≠ undeploy). `status` shows `excluded_models`. ⚠ carries the `cache_prompt:false` workaround (superseded by 20260629c — prefer engine b9330+). Integration guide: `AIMA-OpenClaw撤回同步集成-20260629.md`. Kept for rollback. |
| `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260629.exe` | `v0.5-dev-amd-strix-halo-20260629` | 2026-06-29 | fixes the **Qwen3.6-35B-A3B deploy crash on llama.cpp HIP** (`common.cpp: failed to remove sequence ... p0=10`). It is a hybrid Gated-DeltaNet (recurrent) MoE; recurrent KV can't be partially truncated, so llama-server crashed when reusing a slot's cached prefix (the deploy warmup's 2nd identical "Hello" triggered it). `--cache-ram 0` was NOT enough (only disables the RAM cache, not in-slot prefix reuse). Catalog-only fix on `qwen3.6-35b-a3b`: `cache_prompt: false` → `--no-cache-prompt`, forcing a full KV clear each turn. Verified on b9180 & b9196 HIP. Kept for rollback. |
| `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260625b.exe` | `v0.5-dev-amd-strix-halo-20260625b` | 2026-06-25 | port-allocation in-flight reservation (a later deploy no longer steals a still-loading deploy's port) + **verbatim deploy model name** (served identity = the exact name passed to `aima deploy`, incl. quant suffix, e.g. `Qwen3.6-35B-A3B-UD-Q4_K_M`; `/v1/models` and routing use it). ⚠ **Reverses A1** canonicalization for the served identity. see `AIMA集成问题-补充修复-20260625b.md`. Kept for rollback. |
| `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260625.exe` | `v0.5-dev-amd-strix-halo-20260625` | 2026-06-25 | cross-process OpenClaw sync policy, Windows native stale metadata detection, and 395 long-context llama.cpp stability defaults; see `AIMA集成问题-补充排查与修复-20260625.md`. Kept for rollback. |
| `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260623.exe` | `v0.5-dev-amd-strix-halo-20260623` | 2026-06-23 | integration fixes A1 (undeploy by original name) / A5 (sync-loop switch) / A6 (undeploy disk hint); see `AIMA集成问题-解决说明-20260623.md`. Kept for rollback. |
| `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260618.exe` | `v0.5-dev-amd-strix-halo-20260618` | 2026-06-18 | runtime offline/mirror/private registry support + deploy/OpenClaw sync stabilization. Kept for rollback. |
| `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260616.exe` | `v0.5-dev-amd-strix-halo-20260616` | 2026-06-16 | GLM/Qwen3.6-35B/Embedding context windows + clamp uses iGPU pool. Kept for rollback. |
| `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260615.exe` | `v0.5-dev-amd-strix-halo-20260615` | 2026-06-15 | hardware-aware context sizing + 128K VL default. Kept for rollback. |
| `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260612.exe` | `v0.5-dev-amd-strix-halo-20260612` | 2026-06-12 | VL/OpenClaw deploy fixes (#87–#91). Kept for rollback. |
| `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260610.exe` | `v0.5-dev-amd-strix-halo-20260610` | 2026-06-10 | out-of-box HIP engine (#85, #86). Kept for rollback. |
| `dist/aima-windows-amd64-v0.5-dev-amd-strix-halo.exe` | `v0.5-dev-amd-strix-halo` | 2026-06-09 | prior build (#78–#83 only, **no** HIP engine auto-download). Kept for rollback. |

The file name matches the exe's own `aima version` string. Launcher: `dist/serve.bat`
(edit the exe name there to switch builds).

> **2026-06-25b rebuild** — **port-allocation race + verbatim model name.**
> (1) Port allocation now keeps an in-process in-flight reservation: a port that a
> still-loading deploy has chosen (llama.cpp takes 40s+ to bind) is treated as taken
> by concurrent deploys, so a later deploy no longer lands on the same port and
> bind-kills the earlier service. (2) `aima deploy <name>` now preserves the EXACT
> name as the served identity — the proxy route, `/v1/models`, `aima.dev/model`,
> `llm.model` and OpenClaw all use `Qwen3.6-35B-A3B-UD-Q4_K_M` verbatim (incl. the
> precision suffix), so requests with that name stop 404-ing. Internal catalog/path
> lookups still use the canonical id. **This reverses A1's canonicalization for the
> served identity** — coordinate before adopting as the official line. Verified
> end-to-end on Strix Halo (deploy → `/v1/models` returns the suffixed name → chat
> with it returns 200 → undeploy by the same name). See `AIMA集成问题-补充修复-20260625b.md`.
> Note on the separate `failed to remove sequence` engine abort: reproduced as
> **engine-build-specific** — AIMA's auto-downloaded **b9330** and b9180 both serve
> Qwen3.6-35B at ctx 262144 with no abort; the crash came from a custom engine set
> via `AIMA_ENGINE_DIR` (`ProgramData\Lenovo\baiying-llm`). Use the auto-downloaded
> b9330, not a custom build.

> **2026-06-25 rebuild** — **OpenClaw sync policy + 395 long-context stability.**
> `serve --no-openclaw-sync` now persists `openclaw.sync=manual`, and `aima deploy`
> honors the same policy before its ready-after OpenClaw sync, so implicit sync is
> disabled across processes while explicit `aima openclaw sync` remains available.
> Windows native deployment recovery now checks that the listening port's PID matches
> the deployment metadata PID, avoiding stale metadata/port reuse being reported as a
> ready model. Qwen3.6-35B-A3B and GLM-4.7-Flash Strix Halo GGUF defaults now use
> `--parallel 1 --cache-ram 0` to avoid llama.cpp auto-4-slot + prompt-cache ROCm
> allocation spikes at 262144/202752 context.

> **2026-06-18 rebuild** — **runtime pull resilience + OpenClaw deploy sync stability.**
> Runtime acquisition now supports offline native packages (`.zip`, `.tgz`/`.tar.gz`, folder,
> or single exe) via `AIMA_ENGINE_BUNDLE`, `AIMA_ENGINE_ARCHIVE`, `AIMA_ENGINE_OFFLINE_PACKAGE`,
> or `aima engine import <package>`. It also supports enterprise binary mirrors via
> `AIMA_ENGINE_MIRROR_BASE` / `AIMA_ENGINE_MIRROR`, `AIMA_ENGINE_MIRROR_TEMPLATE`, and
> `AIMA_ENGINE_URL_REWRITE`; plus private OCI runtime registries via `AIMA_ENGINE_REGISTRY` /
> `AIMA_ENGINE_REGISTRIES`. `aima deploy` now runs the full deploy workflow instead of returning
> immediately after apply: it waits for the model to become ready and then best-effort syncs
> OpenClaw. OpenClaw sync also falls back to the deployed backend's `model_type` for local or
> synthetic model names, and writes `agents.defaults.model` by default so deploys do not stop at
> `models.providers`. Keep `AIMA_OPENCLAW_SET_DEFAULT=false` if the user wants to preserve an
> existing OpenClaw default model. Use `aima deploy --no-pull <model>` when the runtime must come
> only from a pre-installed/offline package.

> **2026-06-16 rebuild** — **partner context windows + APU memory fix.** Catalog defaults
> now carry each model's full trained context, verified loading + serving on the Strix Halo
> iGPU: **GLM-4.7-Flash 202752**, **Qwen3.6-35B-A3B 262144 (256K)** (new llama.cpp variant +
> `-UD-Q4_K_M`/`q4_k_m` aliases), and a new **Qwen3-Embedding-4B** entry that deploys in
> `--embedding` mode and serves `/v1/embeddings` (2560-dim; deploy-only, not synced into
> OpenClaw). These large defaults stay safe via the deploy-time auto-clamp — which now sizes
> against the **iGPU memory pool** (≈110 GB on Strix Halo) instead of the OS-visible system
> RAM (≈32 GB, which Win32 under-reports on APUs), so it no longer risks shrinking contexts.
> Also adds two **partner-controllable env knobs** for `openclaw sync` (both honor CLI sync
> and the serve auto-sync loop): `AIMA_OPENCLAW_SET_DEFAULT=false` registers the provider but
> leaves the user's primary chat model untouched; `AIMA_OPENCLAW_CONFIG=<path>/openclaw.json`
> writes the config (and skills/extensions) to a custom dir, e.g. `.byClaw` instead of `.openclaw`.

> **2026-06-15 rebuild** — **hardware-aware context sizing.** On deploy, AIMA reads the
> GGUF's real architecture and clamps llama.cpp `ctx_size` to fit the detected memory
> (weights + projector + KV cache) and the model's trained context, lowering it only when
> needed. So the catalog can ship a large default (Qwen2.5-VL-3B is now **128000 / 128K** —
> big enough for OpenClaw's agent prompt, which injects all MCP tool schemas) without risking
> an out-of-memory crash on smaller machines: big boxes get the full context, constrained
> boxes auto-shrink to what fits. Override per-deploy with `--config ctx_size=N` if needed.

> **2026-06-12 rebuild** — adds the VL-model + OpenClaw deploy fixes on top of the HIP
> engine work: native deploy readiness no longer false-times-out (#87), the deploy launcher
> **no longer pops a `cmd` console window** (#88), `Qwen2.5-VL-3B-Instruct` catalog knowledge
> (#89), **zero-config vision** — llama.cpp `--mmproj` is auto-wired for VL models (#90), and
> `aima openclaw sync` now **preflight-probes the `:6188` proxy** and warns loudly when it is
> unreachable instead of letting OpenClaw fail with an opaque connection-error/timeout (#91).

> **2026-06-10 rebuild** — now includes the out-of-box AMD-HIP engine work (#85, #86):
> AIMA **auto-downloads a Strix-Halo-adapted ROCm/HIP llama.cpp** on first deploy, and a
> pre-installed engine you point `AIMA_ENGINE_DIR` at is the one that actually launches
> ("scanned ⇒ launchable"). **Manually setting `AIMA_ENGINE_DIR` is no longer required** —
> verified on the 395: empty dist + no `AIMA_ENGINE_DIR` → `aima deploy <gguf> --engine llamacpp`
> auto-fetches `llama-b9330-bin-win-hip-radeon-x64` and runs all layers on the iGPU (~33 t/s).

## Fixes included (vs develop) — PRs #78–#91 + 2026-06-18/25 partner fixes

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
- **Native deploy readiness**: `deploy.apply` returned a sanitized pod name the readiness
  poll couldn't find, so a deploy that was actually serving reported "not ready within 1m".
  It now returns the real runtime deployment name. (#87)
- **No `cmd` popup on deploy**: the Windows launcher is wrapped in a hidden VBS launcher, so
  starting an engine no longer flashes a `C:\WINDOWS\SYSTEM32\cmd.exe` console window. (#88)
- **Qwen2.5-VL-3B-Instruct catalog knowledge** (type `vlm`, scan-name aliases, verified
  Strix Halo perf) so the model is recognized, deployable, and syncs into OpenClaw. (#89)
- **Zero-config vision**: for a VL gguf, AIMA auto-detects the colocated `mmproj-*.gguf` and
  injects `--mmproj`, so vision works with no manual flag. (#90)
- **OpenClaw sync preflight**: `aima openclaw sync` probes the `:6188` data-plane proxy (direct
  + env-proxy) and emits a loud warning + `proxyReachable`/`proxyWarning` when it is not
  reachable — so "OpenClaw provider times out" is diagnosed as *serve not running* or
  *HTTP_PROXY intercepting loopback* instead of an opaque failure. (#91)
- **Hardware-aware context sizing**: on llama.cpp GGUF deploys, AIMA reads the model's GGUF
  architecture (`model.ReadKVArch`) and clamps `ctx_size` to fit detected memory
  (weights + projector + KV cache) and the trained context — lowering only when needed. Lets
  the catalog ship a large default (Qwen2.5-VL-3B → 128000) that fixes agent context-overflow
  without OOM-ing smaller machines.
- **Offline / mirror / private runtime pull**: native llama.cpp runtimes can be installed from
  local offline packages before AIMA tries the network, binary downloads can be redirected through
  enterprise mirrors or URL-rewrite rules, and container runtime pulls can prefer private OCI
  registries.
- **Deploy-driven OpenClaw sync**: `aima deploy` waits for readiness and triggers OpenClaw sync,
  so provider and `agents` config update together after a successful deploy. Non-catalog/local
  model names use the backend `model_type` fallback, avoiding missed syncs for alias names such
  as partner-local Qwen builds.
- **OpenClaw manual-sync policy is cross-process**: `serve --no-openclaw-sync` now persists
  `openclaw.sync=manual`, and `deploy` reads it before any implicit ready-after sync. Use
  `aima config set openclaw.sync auto` to re-enable implicit sync.
- **Windows native stale metadata guard**: a recovered native deployment is considered matching
  only when the listening port PID equals the persisted deployment PID, avoiding false ready
  reports from port reuse.
- **395 large-context llama.cpp stability**: Qwen3.6-35B-A3B and GLM-4.7-Flash defaults set
  `parallel=1` and `cache_ram=0`, reducing ROCm allocation spikes on ultra-long contexts.

## Run

1. Run `serve.bat`. The llama.cpp engine is handled automatically:
   - **No engine on the box?** On first `aima deploy <model> --engine llamacpp` AIMA
     auto-downloads the official ROCm/HIP build (`b9330`, `win-hip-radeon-x64`) and runs
     on the iGPU. No setup needed. (#85)
   - **Already have your own llama.cpp build (any version)?** Set `AIMA_ENGINE_DIR` in
     `serve.bat` to its folder — AIMA launches *that exact binary*, so your llama.cpp
     version is supported regardless of what AIMA ships. Must be a **ROCm/HIP** build,
     not the CUDA build (CUDA won't start on a no-NVIDIA box). (#86)
   - **Offline or enterprise network?** Put the native llama.cpp package on disk and set
     `AIMA_ENGINE_OFFLINE_PACKAGE=D:\path\llama.zip` before running `aima deploy`; or set
     `AIMA_ENGINE_MIRROR_BASE=https://repo.company.local/aima/engines` /
     `AIMA_ENGINE_REGISTRIES=registry.company.local/aima` to prefer internal repositories.
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

> **Don't hand-write the provider** — run `aima openclaw sync` (or rely on serve's auto-sync). It
> fills the correct `apiKey` and the exact model `id` from `/v1/models`; hand-writing those is the
> usual cause of 401/404. The chat data plane goes through **`aima serve` on `:6188`** (this
> `serve.bat`), which must stay running — the MCP server `aima mcp` is the *control* plane and does
> **not** open `:6188`. If OpenClaw reports a **connection error / timeout** while `curl` to `:6188`
> works, an `HTTP_PROXY`/`HTTPS_PROXY` env in the OpenClaw process is routing loopback through a
> proxy — set `NO_PROXY=127.0.0.1,localhost,::1` for it. Sync now preflight-checks all of this (#91).

## 连线灵机云 (remote assist)

In the UI, click **连线灵机云** and enter the invite/worker code (the machine must be online;
it connects to aimaserver.com). Backed by the MCP `support.askforhelp` tool — that's why
`serve.bat` runs with `--mcp`.

## Notes

- Unified memory shows the nameplate **128 GB**; deploy-fit uses the real GPU-usable **~110 GB**.
- Don't run two `aima` instances against the same data dir / ports.
