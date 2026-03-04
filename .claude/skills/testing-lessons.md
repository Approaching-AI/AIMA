# AIMA Testing Lessons

> Distilled from multi-device E2E validation, GPU deployment testing, and Agent test campaigns.

## 1. Multi-Device Test Loop (Core Methodology)

### The Cardinal Rule: ALL COLLECT, THEN ANALYZE

```
WRONG: Test device A → fix → test device B → fix → device A breaks
RIGHT: Test ALL devices → build result matrix → analyze → ONE fix → re-verify ALL
```

Never fix device-by-device. Each fix must be verified against the full device matrix.

### Standard workflow
```
[1] Build: cross-compile all targets in one batch
[2] Distribute: SCP to all remote machines
[3] Execute: same commands on EVERY device (parallel SSH)
[4] BARRIER: wait for ALL results (unreachable = record as UNREACHABLE, don't skip)
[5] Collect: build comparison matrix
[6] Analyze: which devices pass/fail, is failure pattern same or different?
[7] Fix: ONE commit covering ALL platforms
[8] Re-verify: back to [1] until matrix is all-green
```

### SCP while process running = silent failure
**Bug**: SCP binary while old process holds the file → binary not replaced (checksum mismatch).
**Fix**: Always kill the process first, then SCP, then restart.
**Pattern**: `ssh host 'pkill aima'; scp binary host:~/aima; ssh host './aima ...'`

### SSH timeout handling
- Set SSH ConnectTimeout (e.g. `-o ConnectTimeout=10`)
- Unreachable machines → record as UNREACHABLE in matrix, don't silently skip
- Tailscale IPs (100.x) preferred for reliability; LAN IPs as fallback

## 2. GPU Deployment Testing

### VRAM estimation formula
```
Model weights + KV cache + framework overhead + CUDA graphs
= total VRAM requirement (must be < physical VRAM)
```

**Real-world examples:**
- Qwen3-30B-A3B BF16: 58.42 GiB weights + 5 GiB overhead = 63 GiB > 64 GiB AMD395 VRAM → OOM
- cpu-offload-gb is useless on unified memory (same physical pool)
- gmu (GPU memory utilization) sweet spots: 0.9 (single model), 0.78 (LLM + ASR + TTS coexistence)

### Multi-model coexistence testing
```
LLM gmu=0.78 (TP=2) + ASR gmu=0.14 + TTS CUDA (no health probes)
```
- gmu=0.85 for LLM → ASR OOM (only 4.8 GiB free)
- gmu=0.75 for LLM → CUDA graph capture failure
- gmu=0.78 = sweet spot: LLM has enough + ASR fits in remainder
- TTS liveness probe (30s) on single-threaded server → exit 137 → MUST remove health_check

### Cross-vendor GPU container access
| Vendor | Method | Requirement |
|--------|--------|------------|
| NVIDIA | CDI: `--device nvidia.com/gpu=all` | `/etc/cdi/nvidia.yaml` (nvidia-ctk generated) |
| NVIDIA (legacy) | `--gpus all` | nvidia-container-toolkit distro package |
| AMD/ROCm | `--device /dev/kfd --device /dev/dri` | Correct render group GID (NOT lxd!) |
| Hygon DCU | `--device /dev/kfd --device /dev/dri` | DTK SDK container env |

**AMD GID trap**: Ubuntu render group = GID 109, but `lxd` = GID 110. Wrong GID → "no ROCm-capable device detected".

### Engine parser matching
| Model Family | vLLM Parser | SGLang Parser | Notes |
|-------------|-------------|---------------|-------|
| Qwen3.5 | `qwen3_xml` | `qwen3_coder` | hermes/qwen25 DON'T work |
| Qwen3 | `qwen3_xml` | `qwen3` | |

**Lesson**: Always check available parsers via engine error message first. Parser names change between engine versions.

### Engine flags that disappeared
- `--enable-cuda-graph`: removed in SGLang v0.5.9 (enabled by default)
- `--quantization none` on FP8 model: conflicts with model's own config
- Always test engine flag compatibility when upgrading versions

## 3. Agent (L3a) E2E Validation

### Test matrix structure (39/39 PASS)
```
Phase 1: Basic smoke (5 tests) — version, detect, list, ask
Phase 2: Config persistence (8 tests) — CLI get/set, SQLite, REST, hot-swap
Phase 3: Single-turn tools (8 tests) — each tool category
Phase 4: Multi-turn chains (3 tests) — session continuity
Phase 5: Security guardrails (6 tests) — blocked tools, param-level blocks
Phase 6: Config hot-swap (3 tests) — invalid endpoint → recovery
Phase 7: LAN proxy (3 tests) — auto-discovery, remote exec
Phase 8: Audit + edge cases (3+1 tests) — audit_log table, maxTurns
```

### vLLM tool calling setup
Two args needed in engine YAML `startup.default_args`:
```yaml
enable_auto_tool_choice: true
tool_call_parser: qwen3_xml    # must match model family
```

### Agent safety test patterns
- Destructive tools → returns "BLOCKED" (not executed, logged to audit_log)
- `system.config` write → BLOCKED; `system.config` read → ALLOWED
- `shell.exec` non-whitelisted command → ERROR (not BLOCKED)
- LLM itself may refuse dangerous commands before even calling the tool (double safety)

### GB10 deployment timing (full cold start)
- Model load: ~63s (14 shards × 4.5s)
- torch.compile: ~22s
- CUDA graph warmup: ~30s
- Health check readiness: after 10s initial delay
- **Total: ~4-5 minutes pod creation → ready:true**

## 4. Benchmark Methodology

### Data collection pattern
```bash
# Single-request throughput
curl -s http://<endpoint>/v1/chat/completions \
  -d '{"model":"<name>","messages":[{"role":"user","content":"<prompt>"}],"max_tokens":512}' \
  | jq '.usage.completion_tokens / (.usage.total_time // 1)'
```

### Key benchmark results (reference)
| Device | Model | Engine | Config | tok/s |
|--------|-------|--------|--------|-------|
| gb10 | Qwen3.5-35B-A3B | vLLM | BF16, gmu=0.9 | 30.0 |
| gb10 | Qwen3.5-35B-A3B | SGLang | +speculative decode | 45.5 (+52%) |
| linux-1 | Qwen3.5-35B-A3B | vLLM | BF16, TP=2 | 174.0 |
| linux-1 | Qwen3.5-122B | KTransformers | gpu_experts=30 | 22.8 |
| amd395 | qwen3-0.6B | llamacpp | Vulkan GGUF | 194.0 |
| mac-m4 | qwen3-0.6B | llamacpp | Metal GGUF | 120.0 |

### SGLang vs vLLM comparison (gb10, Qwen3.5-35B-A3B)
- SGLang +52% throughput at 1K context (speculative decoding: NEXTN topk=2/draft=8/steps=4)
- At 128K+ context: disable speculative decoding (overhead > benefit)
- SGLang v0.5.9 has known repetition bug (#19393) for complex reasoning

## 5. Testing Infrastructure Patterns

### K3S registries.yaml for China
Tested mirrors (reliability order): daocloud > 1ms.run > rat.dev > vvvv.ee > dockerproxy.net
Dead mirrors: nju (403), rainbond (000) — remove immediately when discovered.

### HuggingFace in China
```bash
HF_ENDPOINT=https://hf-mirror.com huggingface-cli download <model>
```
Direct HuggingFace.co is unreachable — always use mirror.

### Model file permission trap
K3S `DirectoryOrCreate` volume mounts create dirs as root. If later writing as non-root → permission denied.
Fix: `sudo chown -R <user> ~/.aima/models/<model>/` before use.

## 6. AMD-Specific Testing

### AMD395 vLLM: dead end
- FP8: `NotImplementedError: No FP8 MoE backend supports the deployment configuration` (ROCm lacks kernel)
- BF16: OOM (58.42 GiB model > 64 GiB unified VRAM, cpu-offload useless on UMA)
- **Conclusion**: AMD RDNA3.5 + vLLM = not viable for large models. Use llamacpp + GGUF (Q4_K_M)

### Engine info matching on AMD
Three-pass matching prevents wrong engine selection:
1. Exact name match
2. Type + hardware preference match
3. Image substring fallback

### Model pull variant matching
Three-pass variant matching prevents wrong download:
1. Exact gpu_arch match
2. Wildcard match
3. Engine-only match
