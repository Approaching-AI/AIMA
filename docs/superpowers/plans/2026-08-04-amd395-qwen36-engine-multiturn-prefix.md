# Qwen3.6 Engine Ordinary Multi-turn Prefix Implementation Plan

> **For Codex:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Follow superpowers:test-driven-development and superpowers:verification-before-completion. This engine PR is independent of AIMA correctness.

**Goal:** Make disabled-thinking historical assistant messages reproduce the same token prefix emitted at their original generation boundary, so ordinary `user -> assistant -> user` histories can extend the resident cache.

**Architecture:** Fix the native Qwen chat renderer at the shared template boundary, add token-prefix regression fixtures using the real tokenizer, and keep strict fixed-context admission unchanged. Do not alter kernels, cache admission, HTTP concurrency, or release packaging.

**Tech Stack:** C++17, ICU byte-level BPE tokenizer, nlohmann JSON chat preparation, Python `unittest` contract tests, Make targets.

---

## Preconditions

- Base repository: `/Users/katechi/AIMA-AMD395-Qwen36-35B-Linux-Engine` at `main` commit `09298a7`.
- Before editing, create an isolated branch/worktree named `fix/ordinary-multiturn-prefix` using `superpowers:using-git-worktrees`; if the repository still has no worktree-location convention, ask the user to choose the location as required by that skill.
- Target PR: `Approaching-AI/AIMA-AMD395-Qwen36-35B-Linux-Engine:main`.
- Link issue `#1` in the PR, but do not close it until target-host evidence is complete.
- Do not publish or replace v1.4.0 artifacts.

### Task 1: Capture the issue #1 prefix failure in a tokenizer regression

**Files:**
- Modify: `tests/native_chat_template_parity.cpp`

**Step 1: Add a failing prefix assertion**

Add helpers:

```cpp
void require_prefix(const std::vector<std::uint32_t>& prefix,
                    const std::vector<std::uint32_t>& extension,
                    const char* name);

std::vector<std::uint32_t> encode_request(
    aima::NativeTokenizer* tokenizer,
    const Json& request,
    bool disable_thinking = true);
```

Construct:

```json
first  = {"messages":[{"role":"user","content":"Hello"}]}
second = {"messages":[
  {"role":"user","content":"Hello"},
  {"role":"assistant","content":"Hi there."},
  {"role":"user","content":"How are you?"}
]}
```

Let `firstTokens` include the disabled-thinking generation preamble. Assert it is an exact prefix of `secondTokens` through every token. Print the first mismatch index and both token IDs on failure.

Also add a case where the assistant response provides `reasoning_content` and a tool-call/tool-result history case.

Run on the AMD395 model host or any host with the same tokenizer:

```bash
AIMA_MODEL_DIR=/home/baiying-algorithm-public/models/Qwen3.6-35B-A3B make native-chat-template-parity
```

Expected: FAIL at the historical assistant boundary because the empty disabled-thinking preamble is absent.

### Task 2: Align historical assistant rendering

**Files:**
- Modify: `native/src/native_tokenizer.cpp`
- Modify: `tests/native_chat_template_parity.cpp`

**Step 1: Implement the minimum renderer change**

In the assistant branch of `NativeTokenizer::render_chat_prompt`, keep the existing `preserve_thinking` behavior, but when thinking is disabled and the assistant message occurred at or before the last user query, render the same empty-thinking prefix used by the generation boundary:

```text
<|im_start|>assistant
<think>

</think>

<assistant content><|im_end|>
```

Express the preamble through one small helper/constant so generation-boundary and historical rendering cannot drift again. The branch must distinguish:

- `disable_thinking == true`: empty thinking preamble for historical assistant content;
- `disable_thinking == false`: retain current thinking-enabled behavior;
- `preserve_thinking == true`: retain explicit historical reasoning content.

Do not alter whitespace outside these exact branches.

**Step 2: Update fixed parity hashes only from observed output**

Run `native-chat-template-parity`, copy the newly reported SHA-256/token count only for fixtures whose intentional historical rendering changed, and rerun. Do not guess hashes.

**Step 3: Verify the focused regression**

```bash
AIMA_MODEL_DIR=/home/baiying-algorithm-public/models/Qwen3.6-35B-A3B make native-chat-template-parity
```

Expected: PASS for simple, tool, history, ordinary multi-turn prefix, reasoning, and tool-result prefix fixtures.

**Step 4: Commit the renderer fix**

```bash
git add native/src/native_tokenizer.cpp tests/native_chat_template_parity.cpp
git commit -m "fix(chat): preserve disabled-thinking history prefix"
```

### Task 3: Add source-level release-contract protection

**Files:**
- Modify: `tests/test_release_contract.py`
- Modify: `docs/API.md`

**Step 1: Write a failing source-contract test**

Add a Python test that requires the shared disabled-thinking preamble helper/constant to be used by both historical-assistant and final generation rendering. This inexpensive test must run under `make check-cpu` when the full tokenizer/model fixture is unavailable.

Run:

```bash
python3 -m unittest tests.test_release_contract -v
```

Expected before the source assertion is wired: FAIL.

**Step 2: Document the compatibility guarantee**

In `docs/API.md`, state that with `--disable-thinking`, a historical assistant message is rendered with the same empty-thinking prefix emitted at generation start, allowing a complete ordinary chat history to preserve a prior prompt prefix. Clarify that this does not relax exact cold-context admission.

**Step 3: Run and commit**

```bash
python3 -m unittest tests.test_release_contract -v
git add tests/test_release_contract.py docs/API.md
git commit -m "test(chat): guard ordinary history prefix parity"
```

### Task 4: Run engine regression gates

**Files:**
- Modify only for defects directly caused by the renderer change.

**Step 1: CPU and public-tree checks**

```bash
make check-cpu
```

Expected: PASS, including Python tests, public-tree checks, generated registry checks, packaging metadata, and the engine self-verify surface.

**Step 2: Native syntax checks on the AMD395 host**

```bash
make check-native-syntax
```

Expected: PASS with ROCm and nlohmann headers.

**Step 3: Tokenizer parity with the target checkpoint**

```bash
AIMA_MODEL_DIR=/home/baiying-algorithm-public/models/Qwen3.6-35B-A3B make native-chat-template-parity
```

Expected: PASS.

**Step 4: Inspect diff**

```bash
git diff --check
git status --short
git diff main...HEAD --stat
```

Expected: only renderer, tests, and API documentation changes.

### Task 5: Build a target-host test binary without publishing a release

**Files:**
- No tracked source changes expected.

**Step 1: Build on `192.168.120.178`**

Use the repository's existing native build scripts and documented ROCm 7.2 toolchain. Build into a unique task directory; do not overwrite the installed v1.3/v1.4 bundles.

Run the new binary's:

```bash
./aima-engine --version
./aima-engine doctor --model-dir /home/baiying-algorithm-public/models/Qwen3.6-35B-A3B --json
```

Expected: build commit identifies the branch and doctor succeeds.

### Task 6: Prove real prefix reuse through HTTP

**Files:**
- Create an untracked temporary request fixture on the target; remove it after evidence capture.

**Step 1: Start one patched engine at a qualified static shape**

Start the test build with context 8192 and cache capacity 9216 on a free loopback port. Use the existing AIMA adapter or an exact-length fixture to make the first request exactly 8192 tokens.

**Step 2: Run ordinary non-streaming multi-turn**

Send first user request, append the returned assistant content to full history, then append a second user request. Before sending, use the patched `chat-template-probe` to assert the first forwarded token IDs are an exact prefix of the second. Require HTTP 200 and engine cache telemetry confirming prefix-extension admission rather than a cold fallback.

**Step 3: Run the same shape with streaming**

Repeat with `stream: true`; require valid SSE completion and the same token-prefix assertion.

**Step 4: Run tool-result history**

Use a deterministic tool-call/tool-result history and require the original generation prefix to survive in the next request. Confirm existing tool parsing and response schema are unchanged.

**Step 5: Stop the task-owned engine**

Terminate only the process started in this task, confirm its port is released, and record returned idle GPU/GTT state.

### Task 7: Prepare and submit the engine PR

**Files:**
- Create: `docs/validation/2026-08-04-ordinary-multiturn-prefix.md` if the repository's public-tree policy permits validation documents; otherwise place the evidence in the PR body only.

**Step 1: Record evidence**

Include:

- issue #1 reproduction before the fix;
- first mismatch token index before the fix;
- passing token-prefix fixture after the fix;
- check-cpu, native syntax, tokenizer parity, HTTP non-stream/SSE/tool results;
- target host and model identity without credentials or private paths beyond the documented model location;
- confirmation that cold exact-context admission is unchanged.

**Step 2: Run final verification fresh**

```bash
make check-cpu
make check-native-syntax
AIMA_MODEL_DIR=/home/baiying-algorithm-public/models/Qwen3.6-35B-A3B make native-chat-template-parity
git diff --check
git status --short
```

Expected: all pass and the worktree is clean after any evidence commit.

**Step 3: Review**

Use `superpowers:requesting-code-review`; address only evidence-backed findings and rerun affected gates.

**Step 4: Push and open the authorized PR**

Push `fix/ordinary-multiturn-prefix` and open a PR against `main`. The PR body must say:

- `Fixes` or `Addresses #1` according to whether target HTTP evidence is complete;
- AIMA v1.4 adaptation remains compatible and independent;
- no kernel/admission/release artifact changes;
- exact test commands and results.

Do not merge the PR or publish a tag without separate user authorization.
