# AMD395 Qwen3.6 Native Engine Adapter Design

## Goal

Add reusable support on AIMA's `amd395-linux` branch for the published AMD395
Qwen3.6 native engine, so AIMA can download and launch the engine with the local
BF16 Safetensors checkpoint and accept ordinary short OpenAI chat requests.
The target validation host is the Ryzen AI Max+ 395 machine at
`192.168.120.178` with `gfx1151`, a 96 GiB GTT pool, and the 26-shard
`Qwen3.6-35B-A3B` checkpoint.

## Confirmed Constraints

The engine's portable v1.4.0 package is a `.tar.zst` archive whose executable is
`bin/aima-engine`. AIMA currently extracts only `.tar.gz` and `.zip` native
engine archives and only probes for a binary directly under the distribution
directory.

The engine deliberately runs one qualified static prefill shape. A cold HTTP
request must tokenize to exactly `--context-tokens`; a longer request is
accepted only when it extends the engine's one cached token prefix. This makes
an ordinary short request fail even though the HTTP surface is OpenAI-shaped.

Issue
https://github.com/Approaching-AI/AIMA-AMD395-Qwen36-35B-Linux-Engine/issues/1
also demonstrates a separate engine-side template inconsistency: a historical
assistant turn rendered before a new user turn does not contain the same
disabled-thinking preamble as the original generation prompt. Consequently,
an ordinary `user -> assistant -> user` request does not extend the cached
token sequence even when the client sends complete history.

## Scope

This change has two independently reviewable deliveries:

1. An AIMA feature branch based on `amd395-linux` that supports the current
   published engine and makes short and ordinary multi-turn requests correct.
2. An engine pull request that aligns historical-assistant rendering with the
   disabled-thinking generation preamble, restoring prefix reuse when the
   resulting template is token-prefix compatible.

The AIMA change must remain correct if the engine pull request is not merged.
It may perform a new fixed-shape cold prefill for a turn that cannot reuse the
cached prefix.

## Non-goals

- Do not add an AMD/Qwen engine-specific `if` or `switch` in AIMA Go code.
- Do not replace or generalize the engine's captured variable-shape kernels.
- Do not silently truncate user conversation history.
- Do not publish a new engine release or tag as part of this work.
- Do not change the engine's strict admission behavior unless its new behavior
  is explicitly enabled by a future engine release.

## Chosen Approach

Use a data-driven AIMA request adapter declared in engine catalog YAML. The
adapter asks the engine's bundled `chat-template-probe` to render and tokenize
the exact request. It then injects a synthetic leading system message until the
rendered prompt has the engine's configured static token count.

This approach works with the existing v1.4.0 artifact. It was selected over an
AIMA-side tokenizer implementation because the engine probe is the source of
truth for Qwen chat/tool rendering. It was selected over waiting for an engine
release because AIMA must work on the target host now.

## AIMA Architecture

### Portable Native Archive Installation

`internal/engine/binary.go` will recognize `.tar.zst` and `.tzst` archives for
both network downloads and local bundle imports. Decompression will use a pure
Go Zstandard reader, preserving AIMA's zero-CGO build. The existing safe tar
extraction rules remain in force: strip one common top-level directory, reject
path traversal, preserve executable modes, and accept only in-tree symlinks.

The catalog source names the executable as `bin/aima-engine`, so existing
binary resolution can return the nested launcher without flattening the
portable bundle. The bundle must remain intact because the launcher resolves
its bundled loader and shared libraries relative to `/proc/self/exe`.

When a catalog source supplies SHA-256, a mismatch is a failed source rather
than an advisory warning. AIMA tries the next configured mirror and fails the
pull if no source matches the pinned digest.

### Catalog Assets

Add a native Linux engine asset for the AMD395 Qwen3.6 engine with:

- hardware `RDNA3.5`, platform `linux/amd64`, runtime `native`;
- engine version `v1.4.0` and the upstream portable release asset;
- executable `bin/aima-engine` and the published archive SHA-256;
- startup command `aima-engine serve --model-dir {{.ModelPath}} --host 127.0.0.1`;
- a catalog-managed `--port` binding and `/health` readiness check;
- no direct runtime warmup, because an unadapted short warmup request would be
  rejected before the AIMA proxy request adapter runs;
- OpenAI base path `/v1` and the exact-context request adapter declaration.

Add a Qwen3.6 model variant that matches this engine by exact asset name, uses
the BF16 Safetensors source, and defaults to a static context of 8192 tokens.
Users may select another qualified static shape with
`--config context_tokens=<N>`.

The AIMA route remains the canonical model name `qwen3.6-35b-a3b`. Before
tokenization and forwarding, the generic upstream-model rewrite changes the
request model to the engine's required `aima-amd395-qwen36-35b` identifier.

### Request Adapter Metadata

Extend engine API metadata with an optional request adapter containing these
fields:

- `kind: exact_context`;
- `path: /v1/chat/completions`;
- `context_config_key: context_tokens`;
- `probe_subcommand: chat-template-probe`;
- `disable_thinking: true`;
- `padding_role: system`;
- `padding_prefix`: an instruction that tells the model to ignore transport
  padding;
- `padding_unit: " ·"`, a tokenizer-verified one-token marker on the target
  checkpoint.

Unknown adapter kinds fail catalog validation. Engines without an adapter keep
the current proxy behavior unchanged.

### Runtime Adapter Context

The native deployment metadata will persist the resolved executable command
and model directory. These values are copied into an internal, non-JSON proxy
backend context; they are not exposed by public status responses. The proxy
uses them to execute:

```text
<resolved bin/aima-engine> chat-template-probe
  --model-dir <resolved model directory>
  --request-json <rewritten request JSON>
  --disable-thinking
```

The probe runs without loading model weights or allocating the inference
workspace. Only its tokenizer metadata is read.

### Request Rewrite Algorithm

For each configured backend, serialize adaptation with a mutex because the
engine itself accepts one request at a time and owns one prefix cache.

1. Parse the JSON request and perform AIMA's canonical-to-upstream model
   rewrite.
2. Probe the unpadded request and read its exact token IDs.
3. If a previously padded request exists, probe the current request with the
   same synthetic padding. Reuse it only when the previous padded token IDs are
   an exact prefix of the current padded token IDs.
4. Otherwise, when the unpadded prompt is shorter than the configured static
   context, insert a leading synthetic system message. Start with one padding
   marker per missing token, probe again, and adjust by the measured token-count
   difference. Stop after eight attempts.
5. Forward only when the result is exactly the static context or is a verified
   extension of the adapter's prior padded token IDs.
6. Cache the forwarded padded token IDs and padding message after successful
   adaptation. A later unrelated short conversation is re-padded to the exact
   static context and becomes a valid cold cache miss.

With the current v1.4.0 engine, an ordinary next-user turn normally takes the
cold fallback because of the historical-assistant template mismatch. After the
engine PR, the same AIMA adapter can reuse the original padding and forward the
turn as a prefix extension without further AIMA changes.

### Proxy Error Behavior

The request rewrite hook will return `([]byte, error)` instead of silently
returning an unchanged body on adapter failure. The proxy maps failures as
follows:

- malformed client JSON or an unpadded prompt already beyond the static
  context without a reusable prefix: HTTP 400 `invalid_request`;
- missing executable/model path, probe launch failure, invalid probe JSON, or
  failure to reach the exact context in eight attempts: HTTP 502
  `backend_adapter_error`;
- a backend HTTP or SSE failure after adaptation: existing proxy behavior.

Logs include token counts, adapter phase, and executable path, but never the
request body or model content.

## Engine Pull Request

The engine change is separate from AIMA correctness. It will make the native
chat renderer include the same disabled-thinking assistant preamble for a
historical assistant message that the renderer emitted at the original
generation boundary. The change must preserve byte/token parity for plain,
tool-call, assistant-tool-history, streaming, and non-streaming fixtures.

Regression coverage will reproduce issue #1:

- exact static first request;
- assistant response followed by a new user message;
- equality of the cached token prefix through the original generation
  boundary;
- prefix-extension admission for the second turn;
- tool-result history and stream/non-stream parity.

If reference Transformers rendering intentionally differs, the engine PR will
document and test an explicit compatibility mode rather than silently changing
the qualified default template.

## Data Flow

```text
OpenAI client
  -> AIMA :6188 canonical model request
  -> catalog-selected exact-context adapter
  -> engine chat-template-probe (CPU tokenizer only)
  -> exact padding or verified cached-prefix reuse
  -> engine :<deployment-port> /v1/chat/completions
  -> unchanged JSON or live SSE response through AIMA
```

## Testing Strategy

Follow red-green-refactor for each behavior.

### Automated AIMA Tests

- `.tar.zst` network extraction with common-prefix stripping, modes, symlinks,
  corruption, and traversal attempts;
- `.tar.zst` local bundle import and nested binary resolution;
- pinned SHA-256 success, mirror fallback, and all-source mismatch failure;
- catalog loading and validation of the new engine/model variant;
- AMD395 resolver selection, startup flags, runtime, model format, and port;
- exact-context adapter metadata validation;
- short request padding to exactly 8192 tokens through a fake probe;
- prior-padding prefix reuse, unrelated-conversation cold fallback, tools, and
  concurrent serialization;
- malformed JSON, probe failure, over-context request, and SSE passthrough;
- existing proxy rewrite and non-adapted engine regression suite.

### Automated Engine Tests

- disabled-thinking generation/historical-assistant token-prefix parity;
- normal next-user-turn prefix reuse;
- tools and stream/non-stream fixtures remain identical;
- existing strict cold-context tests remain green.

### AMD395 End-to-End Acceptance

Use a fresh `AIMA_DATA_DIR` on `192.168.120.178` and the existing BF16 model at
`/home/baiying-algorithm-public/models/Qwen3.6-35B-A3B`.

1. Verify the engine archive checksum, import/pull it through AIMA, and run the
   bundled `doctor` against the model.
2. Confirm `hal detect` reports `gfx1151`, unified memory, KFD/render access,
   and the 96 GiB GTT pool.
3. Verify `deploy --dry-run` selects the new native engine and emits the exact
   command/config.
4. Deploy and wait for `/health` readiness with one resident model load.
5. Start `aima serve`, send arbitrary short Chinese and English prompts through
   `127.0.0.1:6188`, and require non-empty coherent responses.
6. Complete at least two ordinary `user -> assistant -> user` conversations,
   one non-streaming and one SSE streaming, using full message history.
7. Record whether each second turn used cold fallback or prefix extension; both
   are correct for AIMA, while the patched engine must use prefix extension for
   the dedicated parity fixture.
8. Undeploy and confirm no engine process or listening port remains and GPU/GTT
   use returns to the idle baseline.

## Delivery

- AIMA branch: `feat/amd395-qwen36-native-engine`, targeting
  `Approaching-AI/AIMA:amd395-linux`.
- Engine branch: `fix/ordinary-multiturn-prefix`, targeting
  `Approaching-AI/AIMA-AMD395-Qwen36-35B-Linux-Engine:main`.
- Keep the two commits/PRs independent: AIMA works with published v1.4.0; the
  engine PR improves multi-turn prefix reuse when a later engine build is used.
