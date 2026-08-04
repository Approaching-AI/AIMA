# AMD395 Qwen3.6 Native Engine Adapter Implementation Plan

> **For Codex:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Follow superpowers:test-driven-development for every behavior change and superpowers:verification-before-completion before commits or completion claims.

**Goal:** Make AIMA's `amd395-linux` branch install and launch the published v1.4.0 AMD395 Qwen3.6 engine and transparently adapt ordinary OpenAI chat requests to the engine's exact static-context admission contract.

**Architecture:** Add generic `.tar.zst` installation with strict digest verification, then describe the engine and its exact-context adapter entirely in catalog YAML. The proxy invokes the engine's tokenizer probe, pads short requests to the selected static shape, holds a per-backend lease through the complete HTTP/SSE exchange, and reuses cached prefixes only after token-ID proof.

**Tech Stack:** Go 1.25+, `net/http`, `os/exec`, `archive/tar`, `github.com/klauspost/compress/zstd`, YAML catalog assets, OpenAI-compatible HTTP/SSE.

---

## Preconditions

- Work only in `/Users/katechi/AIMA/.worktrees/amd395-qwen36-native-engine` on `feat/amd395-qwen36-native-engine`.
- Keep `origin/amd395-linux` as the target base.
- Do not modify the user's unrelated `/Users/katechi/AIMA` working tree.
- Preserve zero-CGO builds; use the pure-Go Zstandard decoder.
- Never special-case the AMD395 engine name or Qwen model name in Go. All selection and adapter behavior must come from catalog data.

### Task 1: Add safe `.tar.zst` extraction

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `internal/engine/binary.go`
- Modify: `internal/engine/engine_test.go`

**Step 1: Write failing archive tests**

Add table-driven helpers to create tar archives compressed with Zstandard and tests covering:

```go
func TestBinaryManagerEnsureInstallsLocalTarZstBundle(t *testing.T)
func TestExtractTarZstStripsCommonPrefixAndPreservesMode(t *testing.T)
func TestExtractTarZstCreatesSafeRelativeSymlink(t *testing.T)
func TestExtractTarZstRejectsTraversal(t *testing.T)
func TestExtractTarZstRejectsEscapingSymlink(t *testing.T)
func TestExtractTarZstRejectsCorruption(t *testing.T)
```

Use a nested executable named `bin/aima-engine` in the local-bundle test and assert `Ensure` resolves `<dist>/bin/aima-engine` with executable mode.

Run:

```bash
go test ./internal/engine -run 'Test(BinaryManagerEnsureInstallsLocalTarZstBundle|ExtractTarZst)' -count=1
```

Expected: FAIL because `.tar.zst` is treated as a plain binary and no Zstandard extractor exists.

**Step 2: Add the pure-Go dependency**

Run:

```bash
go get github.com/klauspost/compress/zstd@v1.19.1
```

Expected: `go.mod` and `go.sum` record the dependency without CGO additions.

**Step 3: Refactor tar extraction around an opener**

In `internal/engine/binary.go`, retain the two-pass common-prefix behavior but share it between gzip and Zstandard:

```go
type tarReaderOpenFunc func() (io.ReadCloser, error)

func extractTarArchive(destDir string, open tarReaderOpenFunc) error
func openTarGz(path string) (io.ReadCloser, error)
func openTarZst(path string) (io.ReadCloser, error)
func extractTarZst(archivePath, destDir string) error
```

The common extractor must:

- normalize names with slash semantics before joining to the host path;
- reject absolute names and any cleaned name equal to `..` or beginning `../`;
- strip exactly one common top-level directory when all entries share it;
- create directories and regular files with archive modes;
- permit symlinks only when their resolved target remains inside `destDir`;
- reject hard links and unsupported special entries with an explicit error;
- close each output before processing the next header.

Recognize both `.tar.zst` and `.tzst` in `installLocalBundle` and downloaded URL detection.

**Step 4: Run the focused tests**

```bash
go test ./internal/engine -run 'Test(BinaryManagerEnsureInstallsLocalTarZstBundle|ExtractTarZst)' -count=1
```

Expected: PASS.

**Step 5: Commit the archive behavior**

```bash
git add go.mod go.sum internal/engine/binary.go internal/engine/engine_test.go
git commit -m "feat(engine): install portable tar.zst bundles"
```

### Task 2: Make published digests enforceable

**Files:**
- Modify: `internal/engine/binary.go`
- Modify: `internal/engine/engine_test.go`

**Step 1: Write failing checksum tests**

Add HTTP test servers and tests:

```go
func TestBinaryManagerDownloadRejectsSHA256Mismatch(t *testing.T)
func TestBinaryManagerDownloadFallsBackAfterSHA256Mismatch(t *testing.T)
func TestBinaryManagerLocalBundleRejectsSHA256Mismatch(t *testing.T)
```

Assertions:

- a mismatching first source is never extracted into the final dist directory;
- a matching later source succeeds;
- all mismatches return an error containing both `sha256 mismatch` and the source URL/path;
- local bundles with a configured platform digest obey the same pin.

Run:

```bash
go test ./internal/engine -run 'TestBinaryManager(Download|LocalBundle).*SHA256' -count=1
```

Expected: FAIL because digest mismatches currently only log a warning.

**Step 2: Verify before extraction**

Change the internal download flow so the temporary file is downloaded and hashed first, the expected digest is checked with case-insensitive hex normalization, and only then is it installed:

```go
func downloadToTemp(ctx context.Context, rawURL, destDir string, onProgress func(ProgressEvent)) (path, sha string, err error)
func installDownloaded(path, rawURL, destDir, binaryName string, onProgress func(ProgressEvent)) error
func verifySHA256(expected, actual, source string) error
```

Do not leave extracted files from a rejected source. Install each attempt into a temporary staging directory under the same parent and merge/rename it into `destDir` only after digest and extraction succeed.

For local bundles, hash regular archive/binary candidates before `ImportBundle`; directory imports remain allowed only when no SHA pin is configured because a catalog archive digest cannot identify a directory tree.

**Step 3: Run focused and package tests**

```bash
go test ./internal/engine -run 'TestBinaryManager(Download|LocalBundle).*SHA256' -count=1
go test ./internal/engine -count=1
```

Expected: PASS.

**Step 4: Commit strict verification**

```bash
git add internal/engine/binary.go internal/engine/engine_test.go
git commit -m "fix(engine): enforce native bundle checksums"
```

### Task 3: Define the generic exact-context adapter schema

**Files:**
- Modify: `internal/knowledge/loader.go`
- Modify: `internal/knowledge/loader_test.go`
- Modify: `cmd/aima/adapters.go`
- Modify: `internal/inferencehttp/types.go`

**Step 1: Write failing schema and clone tests**

Add catalog fixtures for one valid adapter, an unknown adapter kind, and missing required fields. The valid fixture should round-trip all fields and survive profile/asset cloning. Add a `catalogAdapter.RequestAdapter(engineName)` mapping test in `cmd/aima/main_test.go` or a focused adapter test file.

Use this public shape:

```go
type EngineRequestAdapter struct {
    Kind             string `yaml:"kind,omitempty" json:"kind,omitempty"`
    Path             string `yaml:"path,omitempty" json:"path,omitempty"`
    ContextConfigKey string `yaml:"context_config_key,omitempty" json:"context_config_key,omitempty"`
    ProbeSubcommand  string `yaml:"probe_subcommand,omitempty" json:"probe_subcommand,omitempty"`
    DisableThinking  bool   `yaml:"disable_thinking,omitempty" json:"disable_thinking,omitempty"`
    PaddingRole      string `yaml:"padding_role,omitempty" json:"padding_role,omitempty"`
    PaddingPrefix    string `yaml:"padding_prefix,omitempty" json:"padding_prefix,omitempty"`
    PaddingUnit      string `yaml:"padding_unit,omitempty" json:"padding_unit,omitempty"`
    UpstreamModel    string `yaml:"upstream_model,omitempty" json:"upstream_model,omitempty"`
    MaxAttempts      int    `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
}
```

Add `RequestAdapter *EngineRequestAdapter` to `EngineAPI` and clone it deeply.

Run:

```bash
go test ./internal/knowledge ./cmd/aima -run 'Test.*RequestAdapter' -count=1
```

Expected: FAIL because the schema and adapter mapping do not exist.

**Step 2: Validate catalog data at load time**

After engine profiles are finalized, validate each non-nil adapter. Accept only `exact_context`; require:

- path `/v1/chat/completions` or another absolute API path;
- non-empty context key and probe subcommand;
- padding role `system`;
- non-empty prefix, unit, and upstream model;
- max attempts in `1..32`, defaulting zero to 8.

Return an error naming the engine and invalid field. Engines without an adapter remain unchanged.

**Step 3: Expose an inference-only copy**

Add a neutral `inferencehttp.RequestAdapter` type and `RequestAdapter(engineName string) *RequestAdapter` to `CatalogReader`. Implement it in `catalogAdapter` by exact engine asset name and copy every field; do not return knowledge package pointers.

**Step 4: Run tests**

```bash
go test ./internal/knowledge ./cmd/aima -run 'Test.*RequestAdapter' -count=1
```

Expected: PASS.

**Step 5: Commit the schema**

```bash
git add internal/knowledge/loader.go internal/knowledge/loader_test.go internal/inferencehttp/types.go cmd/aima/adapters.go cmd/aima/*test.go
git commit -m "feat(catalog): describe exact-context request adapters"
```

### Task 4: Persist private native adapter context

**Files:**
- Modify: `internal/runtime/runtime.go`
- Modify: `internal/runtime/native.go`
- Modify: `internal/runtime/native_test.go`

**Step 1: Write failing persistence tests**

Add tests proving that a native deployment stores the resolved command and model path, that a fresh `NativeRuntime` reconstructs both through `Status`, and that JSON marshaling of `DeploymentStatus` contains neither value under the private fields.

Define the expected private fields:

```go
AdapterCommand   []string `json:"-"`
AdapterModelPath string   `json:"-"`
```

Run:

```bash
go test ./internal/runtime -run 'TestNative.*AdapterContext' -count=1
```

Expected: FAIL because `ModelPath` is not persisted and status does not expose internal adapter context.

**Step 2: Persist and reconstruct**

Add `ModelPath` to `deploymentMeta`, populate it from `DeployRequest.ModelPath`, and copy `meta.Command` plus `meta.ModelPath` into private `DeploymentStatus` fields in `metaToStatus` and in-memory status enrichment. Deep-copy command slices.

The executable must be the resolved absolute command saved after binary resolution, not the catalog's bare command.

**Step 3: Run tests and commit**

```bash
go test ./internal/runtime -run 'TestNative.*AdapterContext' -count=1
go test ./internal/runtime -count=1
git add internal/runtime/runtime.go internal/runtime/native.go internal/runtime/native_test.go
git commit -m "feat(runtime): retain private native adapter context"
```

### Task 5: Implement probe-backed exact-context preparation

**Files:**
- Create: `internal/inferencehttp/exact_context.go`
- Create: `internal/inferencehttp/exact_context_test.go`
- Modify: `internal/inferencehttp/types.go`
- Modify: `internal/inferencehttp/routes.go`

**Step 1: Write the fake-probe tests first**

Use an injected command runner rather than a real model. Cover:

```go
func TestExactContextPadsShortRequest(t *testing.T)
func TestExactContextMergesExistingSystemMessage(t *testing.T)
func TestExactContextReusesVerifiedPrefix(t *testing.T)
func TestExactContextColdRepadsOnPrefixMismatch(t *testing.T)
func TestExactContextPreservesToolsAndRequestFields(t *testing.T)
func TestExactContextSerializesLeaseUntilFinish(t *testing.T)
func TestExactContextDoesNotCommitStateOnFailedExchange(t *testing.T)
func TestExactContextRejectsMalformedJSON(t *testing.T)
func TestExactContextRejectsOverlongColdPrompt(t *testing.T)
func TestExactContextReportsProbeFailures(t *testing.T)
func TestExactContextStopsAfterConfiguredAttempts(t *testing.T)
```

The fake probe returns the same JSON contract as v1.4.0:

```json
{"schema":"aima-amd395-qwen36/native-tokenizer-probe/v1","complete":true,"token_ids":[1,2,3]}
```

Run:

```bash
go test ./internal/inferencehttp -run 'TestExactContext' -count=1
```

Expected: FAIL because the adapter does not exist.

**Step 2: Define request preparation contracts**

Add:

```go
type AdapterContext struct {
    Command       []string
    ModelPath     string
    Config        map[string]any
}

type AdapterContextResolver func(ctx context.Context, model string) (AdapterContext, error)
type ProbeRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

type PreparedRequest struct {
    Body   []byte
    Finish func(success bool)
}
```

`RequestBodyPreparer` returns a function taking request context, canonical model, upstream model, engine type, content type, path, and body, and returning `PreparedRequest, error`.

Define typed errors carrying HTTP intent:

```go
type AdapterError struct {
    Status int
    Type   string
    Err    error
}
```

Client input errors use 400/`invalid_request`; probe/runtime errors use 502/`backend_adapter_error`.

**Step 3: Implement the engine probe**

Invoke only the resolved executable (`context.Command[0]`):

```text
<binary> <probe_subcommand> --model-dir <path> --request-json <json> --disable-thinking
```

Use `exec.CommandContext`, capture stdout and a bounded stderr tail, require `complete: true`, and require a non-empty integer token array. Never log or include the request JSON in an error.

**Step 4: Implement padding and prefix proof**

For one backend state keyed by canonical model:

1. Acquire its mutex and return a `Finish` closure that commits candidate token IDs only for a successful upstream exchange, then unlocks. The lease spans the full HTTP/SSE response.
2. Rewrite `model` to adapter `upstream_model` before the first probe.
3. Probe the unpadded request.
4. If prior padding exists, apply exactly that padding and reuse only if prior committed token IDs are an exact prefix of the new token IDs.
5. Otherwise build a cold prompt of exactly the configured context.

When a request already has an initial system message, prepend the transport padding plus a delimiter to that message's content. Otherwise insert the synthetic system message at index zero. Never create two system messages.

Start marker count at `contextTokens - unpaddedTokens`. On every attempt, probe and adjust by `contextTokens - actualTokens`; require a positive marker count and stop after `max_attempts`. An unpadded cold prompt at or above the static context is valid only when it is the exact context or a proven extension of committed IDs.

Store padding metadata separately from client messages; never return it in responses or logs.

**Step 5: Keep existing static patches and cleanup**

Run model HTTP request patches first, then the exact-context adapter, then `stripOrphanedToolChoice`. Non-JSON requests and engines without an adapter retain existing behavior byte-for-byte.

**Step 6: Run focused tests**

```bash
go test ./internal/inferencehttp -run 'Test(ExactContext|RequestBody)' -count=1
```

Expected: PASS.

**Step 7: Commit adapter logic**

```bash
git add internal/inferencehttp/exact_context.go internal/inferencehttp/exact_context_test.go internal/inferencehttp/types.go internal/inferencehttp/routes.go internal/inferencehttp/routes_test.go
git commit -m "feat(proxy): adapt exact-context chat engines"
```

### Task 6: Hold adapter leases through proxy responses

**Files:**
- Modify: `internal/proxy/server.go`
- Modify: `internal/proxy/server_test.go`
- Modify: `internal/proxy/sync.go`
- Modify: `internal/proxy/sync_test.go`
- Modify: `cmd/aima/main.go`
- Modify: `cmd/aima/main_test.go`
- Modify: `cmd/aima/tooldeps_deploy.go`

**Step 1: Write failing proxy lifecycle tests**

Add tests that assert:

- preparation errors become OpenAI-shaped 400 or 502 responses and never reach the backend;
- upstream model rewrite is visible to preparation;
- direct registration and periodic synchronization preserve the private deployment name used for runtime lookup;
- `Finish(true)` is called after a 2xx JSON response;
- `Finish(true)` is held until an SSE stream ends;
- `Finish(false)` is called on upstream 4xx/5xx and transport failure;
- a nil preparer preserves current behavior.

Run:

```bash
go test ./internal/proxy -run 'Test.*RequestPrepar' -count=1
```

Expected: FAIL because the proxy hook cannot return errors or lifecycle callbacks.

**Step 2: Replace the rewrite hook with a preparer**

Use a proxy-local neutral contract (or an interface implemented by the inference package without creating an import cycle):

```go
type PreparedRequest struct {
    Body   []byte
    Finish func(bool)
}

type RequestPreparer func(ctx context.Context, path, contentType, model, upstreamModel, engineType, deploymentName string, body []byte) (PreparedRequest, error)
```

After routing and readiness checks, call the preparer. Defer `Finish(success)` until `ReverseProxy.ServeHTTP` returns. Set `success` from `ModifyResponse` when the upstream status is 2xx/3xx; leave false for preparation, transport, or backend errors. Preserve immediate SSE flushing.

Map typed adapter errors without importing `inferencehttp` by an interface exposing `HTTPStatus()`, `ErrorType()`, and `Error()`.

Add this private field to `proxy.Backend`, deep-copy it with the other route metadata, populate it from `DeploymentInfo.Name` in `SyncBackends`, preserve it across not-ready transitions, and set it in both direct registration paths in `cmd/aima/tooldeps_deploy.go`:

```go
DeploymentName string `json:"-"`
```

Pass the private deployment name to the preparer; never infer it from the canonical model.

**Step 3: Wire runtime resolution in main**

Construct `RequestBodyPreparer` with:

- catalog adapter metadata;
- a resolver that calls the native runtime's `Status(ctx, deploymentName)` and copies private adapter command/model path/config;
- the real `os/exec` probe runner.

Resolve by `Backend.DeploymentName`, not by canonical model name, because deployment names may contain variant/slot suffixes. If the selected deployment is not native, return no adapter context and do not invoke an exact-context adapter.

**Step 4: Run tests and commit**

```bash
go test ./internal/proxy ./internal/inferencehttp ./cmd/aima -run 'Test.*(RequestPrepar|DeploymentName|ExactContext|RequestBody)' -count=1
go test ./internal/proxy ./internal/inferencehttp ./cmd/aima -count=1
git add internal/proxy/server.go internal/proxy/server_test.go internal/proxy/sync.go internal/proxy/sync_test.go cmd/aima/main.go cmd/aima/main_test.go cmd/aima/tooldeps_deploy.go
git commit -m "feat(proxy): span adapter state across inference exchanges"
```

### Task 7: Add the AMD395 engine and Qwen variant catalog data

**Files:**
- Create: `catalog/engines/aima-amd395-qwen36-native.yaml`
- Modify: `catalog/models/qwen3.6-35b-a3b.yaml`
- Modify: `internal/knowledge/loader_test.go`
- Modify: `internal/knowledge/resolver_test.go`
- Modify: `cmd/aima/tooldeps_deploy_test.go`
- Modify: `cmd/aima/tooldeps_deploy.go`

**Step 1: Write failing catalog selection tests**

Load the embedded catalog and assert:

- engine exact name, version `v1.4.0`, `linux/amd64`, RDNA3.5, native runtime;
- binary `bin/aima-engine`;
- source URL ends in `aima-engine-native-portable-a15b2774e3ab.tar.zst`;
- SHA-256 is `749a2acb8b8d49b3979e1dbb9785ce3a305bb24129175747fef6330579d2f0f2`;
- the Qwen BF16 Safetensors variant selects this exact engine on gfx1151/RDNA3.5 with sufficient unified memory;
- config emits `--context-tokens 8192` and a cache capacity larger than context;
- warmup is disabled and health path is `/health`;
- context window reporting recognizes `context_tokens` as well as existing `ctx_size`.

Run:

```bash
go test ./internal/knowledge ./cmd/aima -run 'Test.*AMD395.*Qwen36|TestContextWindow.*ContextTokens' -count=1
```

Expected: FAIL because the assets are absent.

**Step 2: Add the engine asset**

Use this catalog contract, filling the standard amplifier/time/power fields consistently with nearby native assets:

```yaml
kind: engine_asset
metadata:
  name: aima-amd395-qwen36-native
  type: aima-native
  version: v1.4.0
  supported_formats: [safetensors]
  supported_model_types: [llm]
hardware:
  gpu_arch: RDNA3.5
  vram_min_mib: 0
runtime:
  default: native
  platform_recommendations:
    linux/amd64: native
source:
  binary: bin/aima-engine
  platforms: [linux/amd64]
  # The Approaching-AI fork has no v1.4.0 release asset (verified 2026-08-04);
  # the published artifact currently lives on the release-producing fork.
  url_template: https://github.com/skyguan92/AIMA-AMD395-Qwen36-35B-Linux-Engine/releases/download/{version}/{platform_file}
  platform_files:
    linux/amd64: aima-engine-native-portable-a15b2774e3ab.tar.zst
  sha256:
    linux/amd64: 749a2acb8b8d49b3979e1dbb9785ce3a305bb24129175747fef6330579d2f0f2
  mirror_templates:
    - https://ghfast.top/{url}
    - https://cf.ghproxy.cc/{url}
    - https://gh-proxy.com/{url}
startup:
  command: [aima-engine, serve, --model-dir, "{{.ModelPath}}", --host, 127.0.0.1]
  ports:
    - {name: http, flag: --port, config_key: port, primary: true}
  default_args:
    context_tokens: 8192
    cache_capacity: 9216
  health_check: {path: /health, timeout_s: 900}
  warmup: {enabled: false}
api:
  protocol: openai
  base_path: /v1
  request_adapter:
    kind: exact_context
    path: /v1/chat/completions
    context_config_key: context_tokens
    probe_subcommand: chat-template-probe
    disable_thinking: true
    padding_role: system
    padding_prefix: "Ignore the following transport padding markers."
    padding_unit: " ·"
    upstream_model: aima-amd395-qwen36-35b
    max_attempts: 8
```

Confirm the actual release owner/URL remains reachable before committing; retain the pinned hash even when mirror templates are present.

**Step 3: Add the model variant before the universal fallback**

Add a variant named `qwen3.6-35b-a3b-amd395-native-bf16` with exact engine `aima-amd395-qwen36-native`, format `safetensors`, RDNA3.5 unified-memory requirements matching the target's 128 GiB RAM/96 GiB GTT, and default `context_tokens: 8192`, `cache_capacity: 9216`.

Do not change the existing universal llama.cpp variant or other hardware paths.

**Step 4: Generalize context reporting**

Update `contextWindowFromResolvedConfig` to try data-driven known keys in priority order:

```go
for _, key := range []string{"ctx_size", "max_model_len", "context_tokens"} { ... }
```

Reuse one numeric conversion helper and preserve current behavior.

**Step 5: Run tests and commit**

```bash
go test ./internal/knowledge ./cmd/aima -run 'Test.*AMD395.*Qwen36|TestContextWindow.*ContextTokens' -count=1
go test ./internal/knowledge ./cmd/aima -count=1
git add catalog/engines/aima-amd395-qwen36-native.yaml catalog/models/qwen3.6-35b-a3b.yaml internal/knowledge/loader_test.go internal/knowledge/resolver_test.go cmd/aima/tooldeps_deploy.go cmd/aima/tooldeps_deploy_test.go
git commit -m "feat(catalog): add AMD395 Qwen3.6 native engine"
```

### Task 8: Full local regression and static checks

**Files:**
- Modify only if a test exposes a defect in files already in scope.

**Step 1: Format and vet**

```bash
gofmt -w internal/engine/binary.go internal/engine/engine_test.go internal/knowledge/loader.go internal/knowledge/loader_test.go internal/inferencehttp/*.go internal/proxy/server.go internal/proxy/server_test.go internal/runtime/runtime.go internal/runtime/native.go internal/runtime/native_test.go cmd/aima/adapters.go cmd/aima/main.go cmd/aima/main_test.go cmd/aima/tooldeps_deploy.go cmd/aima/tooldeps_deploy_test.go
go vet ./...
```

Expected: PASS.

**Step 2: Run the race-sensitive packages**

```bash
go test -race ./internal/inferencehttp ./internal/proxy ./internal/runtime -count=1
```

Expected: PASS, especially the per-backend lease tests.

**Step 3: Run the complete suite**

```bash
go test ./... -count=1
```

Expected: PASS.

**Step 4: Inspect the diff**

```bash
git diff --check
git status --short
git diff origin/amd395-linux...HEAD --stat
```

Expected: no whitespace errors and only planned files.

Do not make a completion claim yet; hardware acceptance remains.

### Task 9: Build Linux artifact and validate archive installation on AMD395

**Files:**
- No source changes expected.

**Step 1: Build an amd64 Linux AIMA binary**

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o build/aima-linux-amd64 ./cmd/aima
shasum -a 256 build/aima-linux-amd64
```

Expected: a zero-CGO Linux executable.

**Step 2: Copy only the test artifact to the target**

Use `scp` to a uniquely named directory under `/home/baiying-algorithm-public/apps/`; do not overwrite existing AIMA installations. Use a fresh target data directory such as `/home/baiying-algorithm-public/.aima-amd395-qwen36-uat`.

**Step 3: Verify the release archive and import path**

On the target:

```bash
curl -L --fail -o /tmp/aima-engine-native-portable-a15b2774e3ab.tar.zst <catalog-release-url>
sha256sum /tmp/aima-engine-native-portable-a15b2774e3ab.tar.zst
<test-aima> engine pull aima-amd395-qwen36-native
<installed-bin>/bin/aima-engine --version
<installed-bin>/bin/aima-engine doctor --model-dir /home/baiying-algorithm-public/models/Qwen3.6-35B-A3B --json
```

Expected: the published digest, engine `1.4.0-native`, doctor success, and intact nested bundle layout.

### Task 10: AMD395 deployment and ordinary chat acceptance

**Files:**
- Record test evidence in a new `docs/validation/` markdown file only after results are captured.

**Step 1: Reconfirm idle hardware and permissions**

On `192.168.120.178`, record `rocminfo`, `/dev/kfd`, `/dev/dri/renderD128`, RAM/GTT, active listeners, and engine processes. Stop only deployments created by this task.

**Step 2: Dry-run catalog selection**

Run AIMA hardware detect/model scan and deployment dry-run against:

```text
/home/baiying-algorithm-public/models/Qwen3.6-35B-A3B
```

Expected: `aima-amd395-qwen36-native`, native runtime, BF16 Safetensors, context 8192, cache 9216, and a command using the nested installed binary.

**Step 3: Deploy once and wait for readiness**

Deploy on a free port with the fresh data directory. Tail logs until `/health` reports ready. Do not launch a second model copy while the first is resident.

**Step 4: Start AIMA proxy and test non-streaming chat**

Send at least:

- a short English greeting;
- a short Chinese question;
- a two-turn `user -> assistant -> user` conversation with complete history.

Require HTTP 200, non-empty coherent assistant content, canonical model routing, and no exact-context 400 from the engine.

**Step 5: Test SSE multi-turn chat**

Run another ordinary two-turn conversation with `stream: true`. Require a valid sequence of `data:` events, `[DONE]`, non-empty content, and no interleaving when a concurrent request is queued behind the adapter lease.

**Step 6: Inspect adapter evidence**

Confirm logs report only token counts and phases. Verify:

- short first turns were padded to exactly 8192;
- current v1.4 ordinary second turns may use cold repadding;
- unrelated conversations remain correct;
- no prompt text appears in logs.

**Step 7: Clean up task-owned processes**

Undeploy through AIMA, stop the test proxy, confirm its port is free, confirm no task-owned engine process remains, and record returned idle GTT/RAM. Keep downloaded artifacts and evidence unless the user requests removal.

### Task 11: Document evidence, final verification, and branch handoff

**Files:**
- Create: `docs/validation/2026-08-04-amd395-qwen36-native-engine.md`
- Modify: `docs/superpowers/specs/2026-08-04-amd395-qwen36-native-engine-adapter-design.md` only if implementation deliberately differs from approved design.

**Step 1: Write evidence**

Record exact commits, binary/archive hashes, host facts, commands, health output, redacted request/response summaries, token-count adapter decisions, streaming results, and cleanup results. Do not record the SSH password.

**Step 2: Run final verification fresh**

```bash
go test ./... -count=1
go test -race ./internal/inferencehttp ./internal/proxy ./internal/runtime -count=1
go vet ./...
git diff --check
git status --short
```

Expected: all pass; only the validation document may be uncommitted.

**Step 3: Commit validation evidence**

```bash
git add docs/validation/2026-08-04-amd395-qwen36-native-engine.md
git commit -m "docs: validate AMD395 Qwen3.6 native chat"
```

**Step 4: Review branch history**

```bash
git log --oneline --decorate origin/amd395-linux..HEAD
git status --short
```

Expected: clean branch with small, reviewable commits and no unrelated user changes.

**Step 5: Apply the branch-finishing workflow**

Use `superpowers:requesting-code-review` followed by `superpowers:finishing-a-development-branch`. Push or open a PR only with the user's existing authorization and valid GitHub credentials; target `Approaching-AI/AIMA:amd395-linux`.
