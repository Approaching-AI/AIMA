# OpenClaw MCP Integration

> Version: v2.0  
> Date: 2026-03-31  
> Status: implemented

## 1. Goal

Keep the existing AIMA → OpenClaw provider integration, and add the reverse
direction so OpenClaw can operate AIMA through MCP.

That means two separate planes:

- data plane: OpenClaw uses AIMA proxy endpoints for chat, ASR, TTS, and image generation
- control plane: OpenClaw uses AIMA MCP tools for deploy/model/engine/system operations

## 2. Constraints

### 2.1 OpenClaw currently consumes `mcp.servers` through stdio

For the OpenClaw path, the working MCP registration is:

```json
{
  "mcp": {
    "servers": {
      "aima": {
        "command": "/absolute/path/to/aima",
        "args": ["mcp", "--profile", "operator"]
      }
    }
  }
}
```

AIMA still supports HTTP MCP through `aima serve --mcp`, but that is not the
OpenClaw integration path.

### 2.2 `profile` is discovery-only

In AIMA today:

- `tools/list` is filtered by profile
- `tools/call` can still invoke any registered tool name

So `--profile operator` reduces tool discovery noise, but it is not an ACL.

### 2.3 Control plane must not depend on ready backends

`mcp.servers.aima` must stay available even when:

- no local models are deployed
- all backends are down
- OpenClaw needs to recover or redeploy the device

Provider sync still depends on ready local backends. MCP registration does not.

### 2.4 OpenClaw config may contain JSON5 syntax

`openclaw.json` may include comments and trailing commas. AIMA must read that
format without failing before it can merge its own config.

## 3. Implemented Design

### 3.1 New stdio entrypoint: `aima mcp`

Add:

```bash
aima mcp
aima mcp --profile operator
```

Behavior:

- reuse the normal AIMA initialization path
- build the full MCP registry
- serve JSON-RPC over stdin/stdout
- do not start the HTTP proxy server

This keeps `INV-5` intact: the CLI is just a transport wrapper for the existing
MCP server.

### 3.2 `openclaw sync` now manages the local AIMA MCP server entry

`openclaw sync` already exports provider config. It now also ensures:

```json
{
  "mcp": {
    "servers": {
      "aima": {
        "command": "/path/to/aima",
        "args": ["mcp", "--profile", "operator"]
      }
    }
  }
}
```

The command path comes from:

1. `os.Executable()` when available
2. fallback to `"aima"`

### 3.3 Ownership uses `ManagedState`, not profile filtering

`develop` already has explicit OpenClaw ownership tracking through
`aima-openclaw-managed.json`. This integration extends that model instead of
guessing ownership from visible config.

`ManagedState` now records:

- provider ownership
- media ownership
- TTS ownership
- image-generation ownership
- MCP server ownership (`mcp_server_name`)

Rules:

1. If `mcp.servers.aima` is absent, AIMA creates it and records ownership.
2. If `ManagedState` says AIMA owns it, AIMA updates it in place.
3. If `mcp.servers.aima` exists but is not AIMA-owned, AIMA preserves it and
   reports `preserved_unmanaged`.

This matches the existing `claim`/managed-state design already present on
`develop`.

### 3.4 `openclaw status` includes MCP registration state

`openclaw status` now reports:

- whether the config file exists
- expected provider summary
- configured provider summary
- MCP server registration state
- drift / missing MCP server issues

`SyncReady` is now true only when:

- provider state matches expected AIMA state
- required AIMA MCP registration is present

### 3.5 Auto-sync also covers MCP registration

`StartSyncLoop` previously focused on provider drift. It now also converges the
MCP server registration, including the case where there are zero ready
backends but the control plane entry is still missing.

### 3.6 Added `aima-control` skill

AIMA now deploys an OpenClaw skill that steers the agent toward MCP tools for
device operations such as:

- `system.status`
- `hardware.detect`
- `model.list`
- `deploy.*`
- `knowledge.resolve`
- `benchmark.run`
- `openclaw` (`action=status`)

## 4. Config Semantics

### 4.1 Provider sections

AIMA continues to manage:

- `models.providers.aima`
- `tools.media.audio`
- `tools.media.image`
- `messages.tts`
- `agents.defaults.imageGenerationModel`
- image-generation provider wiring

These remain tied to ready local backends and explicit ownership.

### 4.2 MCP section

AIMA additionally manages:

- `mcp.servers.aima`

This is not tied to ready backends. It is tied to whether AIMA can provide a
local stdio MCP command.

## 5. Operational Examples

### 5.1 Manual setup

```bash
aima openclaw sync
openclaw mcp show aima
```

### 5.2 Local MCP launch

OpenClaw launches:

```bash
/path/to/aima mcp --profile operator
```

### 5.3 Inspect drift

```bash
aima openclaw status
```

Example MCP section:

```json
{
  "mcp_server": {
    "name": "aima",
    "command": "/usr/local/bin/aima",
    "args": ["mcp", "--profile", "operator"],
    "registered": true,
    "managed": true,
    "action": "managed"
  }
}
```

## 6. Limitations

- `--profile operator` is not a permission boundary.
- HTTP MCP is still separate from the OpenClaw integration path.
- Reading accepts JSON5-like config, but writing normalizes it back to standard JSON.

## 7. Files

Primary implementation files:

- `internal/cli/mcp.go`
- `internal/cli/root.go`
- `internal/cli/serve.go`
- `cmd/aima/main.go`
- `internal/openclaw/openclaw.go`
- `internal/openclaw/managed.go`
- `internal/openclaw/config.go`
- `internal/openclaw/sync.go`
- `internal/openclaw/status.go`
- `internal/openclaw/loop.go`
- `internal/openclaw/skills/aima-control/SKILL.md`

## 8. Validation

Covered by tests for:

- `aima mcp` CLI registration
- JSON5-like OpenClaw config parsing
- MCP server registration on sync
- unmanaged MCP entry preservation
- MCP registration persistence with zero ready backends
- status inspection including MCP registration
