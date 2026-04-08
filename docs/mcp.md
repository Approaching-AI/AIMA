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

## MCP 工具列表 (56 个)

所有工具统一由 `internal/mcp/tools.go` 的 `RegisterAllTools()` 注册，按领域拆分在 `internal/mcp/tools_*.go` 中实现。下列分组反映当前分支的完整工具前缀集合；具体参数与返回值以各工具的 `inputSchema` 和实现为准。

### 核心运维

- Hardware (2): `hardware.detect`, `hardware.metrics`
- Model (6): `model.scan`, `model.list`, `model.pull`, `model.import`, `model.info`, `model.remove`
- Engine (6): `engine.scan`, `engine.info`, `engine.list`, `engine.pull`, `engine.import`, `engine.remove`
- Deploy (8): `deploy.apply`, `deploy.approve`, `deploy.dry_run`, `deploy.run`, `deploy.delete`, `deploy.status`, `deploy.list`, `deploy.logs`
- Stack (1): `stack`
- System (2): `system.status`, `system.config`

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
- Fleet (2): `fleet.info`, `fleet.exec`
- OpenClaw (1): `openclaw`
- Support (1): `support`

Profile filtering is advisory. `tools/list` uses the server profile for discovery, and `ListToolsForProfile()` feeds the Go Agent's `agent.ask` path. The Explorer planner is a pure-text planner and does not request a filtered tool list.

---

## 工具定义示例

### deploy.apply

部署前自动执行硬件适配性检查（`CheckFit`）：
- 根据实时 GPU 显存占用自动调低 `gpu_memory_utilization`
- GPU 空闲显存不足时拒绝部署并返回原因
- 采集失败时不阻止部署（graceful degradation）

```go
{
    "name": "deploy.apply",
    "description": "Deploy a model inference service",
    "inputSchema": {
        "type": "object",
        "properties": {
            "engine": {"type": "string", "description": "Engine type (vllm, llamacpp, ...)"},
            "model": {"type": "string", "description": "Model name"},
            "slot": {"type": "string", "description": "Partition slot name (primary, secondary)"}
        },
        "required": ["model"]
    }
}
```

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

*最后更新：2026-04-08 (工具集收敛到 56 个，profile 仅用于 discovery/agent.ask)*
