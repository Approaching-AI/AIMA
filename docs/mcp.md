# MCP Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的 MCP (Model Context Protocol) 服务器和工具定义。

## 协议概述

MCP 是 Anthropic 发起、Linux Foundation 托管的开放协议，
用 JSON-RPC 2.0 标准化 LLM 应用与外部工具/数据源的集成。

### 架构

```
Host (Claude Code / IDE / 自定义应用)
  │
  └── MCP Client ──── stdio/SSE ────→ MCP Server (AIMA)
                                          │
Go Agent (内置) ── 直接调用 ──────→ MCP Tools (内部)       [同一逻辑]
                                          │
                                          ├── Tools   (Agent 可调用的操作)
                                          ├── Resources (可读取的数据)
                                          └── Prompts  (预定义的工作流模板)
```

**两种 Agent 走同一代码路径**——外部 Agent (MCP over stdio/SSE)、
Go Agent (直接调用)，保证行为一致。

### 三种服务器原语

| 原语 | 控制方 | 用途 | AIMA 示例 |
|------|--------|------|----------|
| **Tools** | LLM 驱动 | Agent 可调用的函数 | deploy.apply, knowledge.resolve |
| **Resources** | 应用驱动 | 可读取的上下文数据 | 硬件状态, 部署列表, 知识索引 |
| **Prompts** | 用户驱动 | 预定义的操作模板 | 模型部署向导, 故障排查流程 |

### 传输协议

- **stdio** — 本地 Agent (Host 启动 AIMA 作为子进程)
- **SSE (Server-Sent Events)** — 远程 Agent (HTTP 长连接)
- **Streamable HTTP** — 2025-11-25 规范新增的通用传输

---

## MCP 工具列表

所有工具统一由 `internal/mcp/tools.go` 的 `RegisterAllTools()` 注册，按领域拆分在 `internal/mcp/tools_*.go` 中实现。下列分组反映当前分支的完整工具前缀集合；具体参数与返回值以各工具的 `inputSchema` 和实现为准。

### 核心运维

- Hardware (2): `hardware.detect`, `hardware.metrics`
- Model (6): `model.scan`, `model.list`, `model.pull`, `model.import`, `model.info`, `model.remove`
- Engine (8): `engine.scan`, `engine.info`, `engine.list`, `engine.ensure`, `engine.rollback`, `engine.pull`, `engine.import`, `engine.remove`
- Deploy (8): `deploy.apply`, `deploy.approve`, `deploy.dry_run`, `deploy.run`, `deploy.delete`, `deploy.status`, `deploy.list`, `deploy.logs`
- Stack (1): `stack`
- System (3): `system.status`, `system.config`, `system.diagnostics`

#### Deploy 返回契约

- `deploy.list` 是 overview 接口。
  返回当前设备上的部署摘要，顶层字段以 `name`、`model`、`engine`、`slot`、`phase`、`status`、`ready`、`address`、`runtime` 为主。
  启动/失败摘要字段如 `startup_phase`、`startup_progress`、`startup_message`、`message`、`error_lines` 也可能出现。
  供 proxy 路由使用的 `served_model`、`parameter_count`、`context_window_tokens` 也是顶层字段。
- `deploy.status` 是 detail 接口。
  返回单个部署的完整状态，包含上述 overview 字段，以及 `config`、`labels`、`restarts`、`exit_code`、启动时间戳等 detail 字段。
- `deploy.list` 与 `deploy.status` 都可能返回恢复字段：
  `desired_state`、`recovery_state`、`recovery_attempts`、`next_recovery_at`、`quarantine_reason`。
  当 Runtime 对象已被删除但持久化意图仍为 `desired_state=running`、`recovery_state=quarantined` 时，AIMA 会返回一条合成的隔离状态，而不是让该部署从列表中消失。
- 不要依赖 `deploy.list` 提供原始 `config` 或 label map。
  如果自动化流程需要精确运行配置或原始 labels，应调用 `deploy.status`。

#### Engine 生命周期契约

- `engine.ensure` 必填 `name`，可选 `version`、`apply`。`apply` 默认 `false`，返回无副作用计划；只有 `apply=true` 才安装或激活。
- `engine.rollback` 必填 `name`、`runtime_type`（`container` 或 `native`）和 `confirm`。`confirm=false` 返回结构化拒绝且不修改库存；`confirm=true` 只允许切换到 verified、available、匹配当前 asset/platform/runtime 的前一版本。
- 两个工具都只改变版本库存，不调用 deploy/Runtime，不重启或重新绑定已运行部署。
- `engine.import` 的 Native 路径要求版本化本地包，先暂存、校验、原子提升并登记为 inactive `imported/verified`；激活必须另行调用 `engine.ensure`。
- `engine.remove(delete_files=true)` 不会删除 preinstalled/legacy、受引用版本、越出 `AIMA_DATA_DIR` 的路径或容器镜像层。
- Go Agent 调用 `engine.ensure` 时先强制 `apply=false` 生成 `NEEDS_APPROVAL` 计划；`engine.rollback` 对 Agent 直接返回 `BLOCKED`。人类 MCP/CLI 调用不受这两个 Agent 适配器策略限制。

示例：

```json
{"name":"engine-a","version":"2.0.0","apply":false}
```

```json
{"name":"engine-a","confirm":true}
```

### 知识与调优

- Knowledge (6): `knowledge.resolve`, `knowledge.search`, `knowledge.analytics`, `knowledge.promote`, `knowledge.save`, `knowledge.evaluate`
- Benchmark (4): `benchmark.run`, `benchmark.matrix`, `benchmark.record`, `benchmark.list`
- Agent (3): `agent.ask`, `agent.status`, `agent.rollback`
- Automation (4): `patrol`, `explore`, `tuning`, `explorer`
- Scenario (2): `scenario.show`, `scenario.apply`

### 协同与集成

- Catalog (3): `catalog.list`, `catalog.override`, `catalog.validate`
- Central (3): `central.sync`, `central.advise`, `central.scenario`
- Data (2): `data.export`, `data.import`
- Device (4): `device.register`, `device.status`, `device.renew`, `device.reset`
- Fleet (2): `fleet.info`, `fleet.exec`
- OpenClaw (1): `openclaw`
- Onboarding (1): `onboarding`
- Support (1): `support`

Profile filtering is advisory. `tools/list` uses the server profile for discovery, and `ListToolsForProfile()` feeds the Go Agent's `agent.ask` path. The Explorer uses `ExplorerAgentPlanner` with its own `ExplorerToolExecutor` (7 document-workspace tools: cat/ls/write/append/grep/query/done), not the MCP profile tool list.

`support.askforhelp` 默认连接 `https://aimaserver.com`，AIMA 会在运行时自动补齐 `/api/v1`。
如需覆盖默认地址，可传入 `endpoint` 参数，或提前配置 `support.endpoint` / `AIMA_SUPPORT_ENDPOINT`。

---

## 工具定义示例

### deploy.apply

部署前自动执行硬件适配性检查（`CheckFit`）：
- 根据实时 GPU 显存占用自动调低 `gpu_memory_utilization`
- GPU 空闲显存不足时拒绝部署并返回原因
- 采集失败时不阻止部署（graceful degradation）

`deploy.apply` 可选输入 `recovery_policy` 对当前部署执行字段级覆盖。未提供的字段继续使用“内置默认值 → 已选 Engine Asset `startup.recovery`”的解析结果：

| 字段 | 默认值 | 合法范围 |
|------|--------|----------|
| `enabled` | `true` | boolean |
| `check_interval_s` | `5` | 1–300 |
| `consecutive_failures` | `3` | 1–20 |
| `max_attempts` | `3` | 1–20 |
| `window_s` | `600` | 1–86400 |
| `backoff_s` | `[2, 10, 30]` | 每项 1–3600 |
| `stable_reset_s` | `600` | 1–86400 |

显式 `deploy.apply` 会将部署重新置为健康意图并重置恢复计数，因此也是解除 `quarantined` 的受支持入口。可信的后台 reconciler 身份只通过 AIMA 进程内 context 传递，MCP 不提供可由调用方伪造的 `source` 或 claim 字段。

```go
{
    "name": "deploy.apply",
    "description": "Deploy a model inference service",
    "inputSchema": {
        "type": "object",
        "properties": {
            "engine": {"type": "string", "description": "Engine type (vllm, llamacpp, ...)"},
            "model": {"type": "string", "description": "Model name"},
            "slot": {"type": "string", "description": "Partition slot name (primary, secondary)"},
            "recovery_policy": {
                "type": "object",
                "properties": {
                    "enabled": {"type": "boolean"},
                    "check_interval_s": {"type": "integer", "minimum": 1, "maximum": 300},
                    "consecutive_failures": {"type": "integer", "minimum": 1, "maximum": 20},
                    "max_attempts": {"type": "integer", "minimum": 1, "maximum": 20},
                    "window_s": {"type": "integer", "minimum": 1, "maximum": 86400},
                    "backoff_s": {"type": "array", "items": {"type": "integer", "minimum": 1, "maximum": 3600}},
                    "stable_reset_s": {"type": "integer", "minimum": 1, "maximum": 86400}
                }
            }
        },
        "required": ["model"]
    }
}
```

恢复 Controller 只随 `aima serve` 运行。显式 `deploy.delete` 会先把意图置为 `stopped`，因此不会被后台恢复；在操作系统或容器平台外部结束工作负载不会改变 desired state，仍可能按策略恢复。后台恢复与显式部署操作的锁只协调同一 AIMA 进程；不支持把多个写进程指向同一 SQLite 数据库并依赖该锁协调 Runtime 副作用。

### knowledge.resolve

Variant 选择阶段会根据 `HardwareInfo` 中的显存和统一显存信息过滤不可行方案：
- `vram_min_mib` > 硬件显存 → 跳过该 variant
- `unified_memory` 不匹配 → 跳过该 variant

```go
{
    "name": "knowledge.resolve",
    "description": "Resolve optimal configuration (L0→L3 multi-layer merge, VRAM-aware variant filtering)",
    "inputSchema": {
        "type": "object",
        "properties": {
            "model": {"type": "string"},
            "engine": {"type": "string"},
            "slot": {"type": "string"},
            "config": {"type": "object", "description": "L1 user overrides"}
        }
    }
}
```

---

## "往 Agent 沉淀" 的含义

以下能力在传统方案中由代码实现，AIMA 架构中由 Agent 通过 MCP 工具组合完成:

| 能力 | 传统方案 (代码实现) | Agent-centric (MCP 工具组合) |
|------|-------------------|---------------------------|
| 调优 | 编码搜索策略 + 基准测试框架 | Agent: deploy → inference × N → knowledge.save |
| 基准测试 | 专用测试框架 + 报告生成 | Agent: HTTP /v1/chat/completions × N + benchmark.record |
| 故障恢复 | 告警规则 + 重试逻辑 | Agent: hardware.metrics → LLM 诊断 → deploy |
| 工作流编排 | DSL 解析器 + 执行引擎 | Agent: 自行编排 MCP 工具调用序列 |
| 资源规划 | 资源调度算法 | Agent: 读 Partition Strategy + LLM 推理 |
| 模型选择 | 格式→引擎映射规则 | Agent: knowledge.resolve + LLM 泛化能力 |

---

## Agent 决策循环

```
┌──────────────────────────────────────────────────────┐
│                                                        │
│  ┌──────────┐    ┌──────────┐    ┌──────────┐         │
│  │ Perceive │───→│  Reason  │───→│   Act    │         │
│  │ 感知      │    │  推理     │    │  行动    │         │
│  │           │    │          │    │          │         │
│  │ hardware. │    │ knowledge│    │ deploy.  │         │
│  │ detect    │    │ .resolve │    │ apply    │         │
│  │ model.scan│    │ + LLM    │    │ model.   │         │
│  │ engine.   │    │ 推理能力  │    │ pull     │         │
│  │ scan      │    │          │    │ engine.  │         │
│  │ hardware. │    │          │    │ pull     │         │
│  │ metrics   │    │          │    │          │         │
│  └──────────┘    └──────────┘    └──────────┘         │
│       ↑                               │                │
│       │          ┌──────────┐         │                │
│       └──────────│  Learn   │←────────┘                │
│                  │  学习     │                           │
│                  │ knowledge│                           │
│                  │ .save    │                           │
│                  └──────────┘                           │
└──────────────────────────────────────────────────────┘
```

每一步对应具体的 MCP 工具调用。Agent 不需要理解 AIMA 内部实现，
只需要理解工具的 inputSchema 和返回格式。

---

## 相关文件

- `internal/mcp/server.go` - MCP 服务器实现
- `internal/mcp/tools.go` - 注册入口、共享 schema helper、profile 过滤
- `internal/mcp/tools_*.go` - 各领域 MCP 工具定义
- `cmd/aima/tooldeps_*.go` - 工具依赖的具体装配与业务接线

---

*最后更新：2026-08-02（增加 Engine ensure/rollback 生命周期合同与 Agent 护栏）*
