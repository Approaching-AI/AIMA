# AMD395 Qwen3.6 Native Engine Validation (2026-08-04)

## Scope

This record covers the catalog-driven AIMA integration of the portable
`AIMA-AMD395-Qwen36-35B-Linux-Engine` v1.4.1 bundle with the BF16
`Qwen3.6-35B-A3B` checkpoint. The validation used a clean, task-specific AIMA
data directory on an AMD Ryzen AI Max+ 395 Linux host.

## Host and artifact

- OS: Ubuntu 24.04, Linux 7.0.0-28
- GPU: AMD Radeon 8060S, `gfx1151`, RDNA3.5
- ROCm: 7.2.3
- Unified GPU-addressable GTT: 98,304 MiB
- System RAM: 127,940 MiB
- Model: 26-shard BF16 safetensors checkpoint, approximately 71.9 GB
- Engine archive: `aima-engine-native-portable-b98b7bc698ae.tar.zst`
- Engine archive SHA-256:
  `f75562537277af8b3a0e1a92fb012761a1522b7021f3014bc1f5b8355f650d1b`
- Engine version: `1.4.1-native`
- Qualified engine source commit: `4536dbaeb6d1d013232db8150fbb6f7c3100b20a`

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
c06b7b4bd70afc5ba29eae0660e6889864d14a28bb770d4c512c900eb177cff4
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
directory and reached `/health` in about 21 seconds. A fresh AIMA process then
restored the deployment metadata and reported it as `running` and `ready`.
The persisted deployment exposes the public route as `qwen3.6-35b-a3b` and the
catalog-declared upstream identity as `aima-amd395-qwen36-35b`.

The AIMA OpenAI-compatible proxy passed these requests:

- non-streaming English instruction, returning exactly `AIMA_V141_OK`;
- non-streaming Chinese two-turn history, recalling `海豚` correctly;
- streaming digit response, returning `1,2,3`, a stop chunk, and `[DONE]`;
- a forced `get_weather` tool call with `{"city":"Paris"}` arguments;
- `/v1/models` and `/health` discovery/readiness checks.

No transport padding was applied. The English request used 26 prompt tokens;
the Chinese first and second turns used 22 and 44 prompt tokens. The engine
reported `cold-decode-fallback` for these independent cache misses and served
all of them with HTTP 200. Repeating an identical 16-token request produced an
exact cache hit, reducing measured TTFT from about 498 ms to 3.6 ms while
returning the same `CACHE_OK` content.

## Upstream resolution

Upstream issue #1 was fixed by the engine maintainer and released as v1.4.1.
The engine now treats `context_tokens` as the preferred AOT specialization and
admits any positive prompt whose prompt-plus-output length fits
`cache_capacity`; cache hits affect latency rather than correctness or
admission. AIMA therefore no longer enables the v1.4.0 `exact_context` padding
adapter for this catalog entry. The reusable `api.upstream_model` field keeps
the public AIMA model name separate from the engine's required request model.
