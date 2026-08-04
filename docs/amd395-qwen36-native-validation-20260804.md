# AMD395 Qwen3.6 Native Engine Validation (2026-08-04)

## Scope

This record covers the catalog-driven AIMA integration of the portable
`AIMA-AMD395-Qwen36-35B-Linux-Engine` v1.4.0 bundle with the BF16
`Qwen3.6-35B-A3B` checkpoint. The validation used a clean, task-specific AIMA
data directory on an AMD Ryzen AI Max+ 395 Linux host.

## Host and artifact

- OS: Ubuntu 24.04, Linux 7.0.0-28
- GPU: AMD Radeon 8060S, `gfx1151`, RDNA3.5
- ROCm: 7.2.3
- Unified GPU-addressable GTT: 98,304 MiB
- System RAM: 127,940 MiB
- Model: 26-shard BF16 safetensors checkpoint, approximately 71.9 GB
- Engine archive: `aima-engine-native-portable-a15b2774e3ab.tar.zst`
- Engine archive SHA-256:
  `749a2acb8b8d49b3979e1dbb9785ce3a305bb24129175747fef6330579d2f0f2`

The archive was downloaded through `aima engine pull`, verified against the
catalog digest before extraction, and resolved to the nested
`bin/aima-engine` entrypoint.

## AIMA verification

The following source gates passed on the feature branch:

```text
go test ./... -count=1
go vet ./...
go test -race ./internal/engine ./internal/knowledge \
  ./internal/inferencehttp ./internal/proxy ./internal/runtime ./internal/hal \
  -count=1
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -o build/aima-linux-amd64 ./cmd/aima
```

The Linux build was a static x86-64 ELF with SHA-256:

```text
ed37b548650ffbdd527f419bdd08fee0bcb95f79013f0d0afff0b59330c8af5b
```

## Target-host results

`aima hal detect` reported `gfx1151`, RDNA3.5, unified memory, and 98,304 MiB
of usable GPU memory. The resolver selected:

```text
model:   qwen3.6-35b-a3b
engine:  aima-amd395-qwen36-native
runtime: native
context_tokens: 8192
cache_capacity: 9216
fit: true
```

The deployment launched the portable entrypoint from AIMA's managed dist
directory and reached `/health` in about 22 seconds. A fresh AIMA process then
restored the deployment metadata and reported it as `running` and `ready`.

The AIMA OpenAI-compatible proxy passed these requests:

- non-streaming English instruction, returning exactly `AIMA_OK`;
- non-streaming Chinese two-turn history, recalling `海豚` correctly;
- streaming digit response, returning `1,2,3`, a stop chunk, and `[DONE]`;
- `/v1/models` and `/health` discovery/readiness checks.

Each cold request was transparently padded to the engine's exact 8,192-token
admission shape. The upstream engine reported approximately 1,670 prompt
tokens/s and 32 decode tokens/s during these checks.

## Known upstream prefix-cache limitation

Ordinary multi-turn correctness is available now through AIMA's verified cold
fallback. Engine v1.4.0 does not reproduce the disabled-thinking generation
preamble when rendering a historical assistant message, so the adapter's
token-ID proof correctly refuses prefix-extension reuse and repads the request
as a cold exact-context request. The corresponding engine fix is tracked
separately in upstream issue #1; it does not block normal conversation through
AIMA.
