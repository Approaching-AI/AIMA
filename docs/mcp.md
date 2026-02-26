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
ZeroClaw Sidecar ── stdio ──────────→ MCP Server (AIMA)  [同一接口]
                                          │
Go Agent (内置) ── 直接调用 ──────→ MCP Tools (内部)       [同一逻辑]
                                          │
                                          ├── Tools   (Agent 可调用的操作)
                                          ├── Resources (可读取的数据)
                                          └── Prompts  (预定义的工作流模板)
```

**三种 Agent 走同一代码路径**——外部 Agent (MCP over stdio/SSE)、ZeroClaw (MCP over stdio)、
Go Agent (直接调用)，保证行为一致。

### 三种服务器原语

| 原语 | 控制方 | 用途 | AIMA 示例 |
|------|--------|------|----------|
| **Tools** | LLM 驱动 | Agent 可调用的函数 | deploy.apply, knowledge.resolve |
| **Resources** | 应用驱动 | 可读取的上下文数据 | 硬件状态, 部署列表, 知识索引 |
| **Prompts** | 用户驱动 | 预定义的操作模板 | 模型部署向导, 故障排查流程 |

### 传输协议

- **stdio** — 本地 Agent (Host 启动 AIMA 作为子进程) / ZeroClaw Sidecar
- **SSE (Server-Sent Events)** — 远程 Agent (HTTP 长连接)
- **Streamable HTTP** — 2025-11-25 规范新增的通用传输

---

## MCP 工具列表

按 PRD 的 Supply / Demand / Control / Feedback 组织：

### 硬件感知 (Supply)

| 工具 | 功能 |
|------|------|
| `hardware.detect` | 检测 GPU/CPU/RAM，返回能力向量 + 功耗模式 |
| `hardware.metrics` | 实时资源利用率 + 功耗 + 温度 |

### 模型管理 (Supply/Demand)

| 工具 | 功能 |
|------|------|
| `model.scan` | 扫描本地模型目录，发现并注册新模型 |
| `model.list` | 列出所有已注册模型 |
| `model.pull` | 下载模型 (断点续传) |
| `model.import` | 从本地路径导入模型 |
| `model.info` | 查询模型详细信息 |
| `model.remove` | 删除模型记录 (可选删除文件) |

### 引擎管理 (Supply/Demand)

| 工具 | 功能 |
|------|------|
| `engine.scan` | 扫描 containerd 镜像，匹配 Engine Asset |
| `engine.list` | 列出可用引擎 |
| `engine.pull` | 拉取引擎镜像 |
| `engine.import` | 从 OCI tar 导入镜像 |
| `engine.remove` | 删除引擎镜像 |

### 编排 (Supply ↔ Demand 绑定)

| 工具 | 功能 |
|------|------|
| `deploy.apply` | 生成并提交 Pod YAML 到 K3S |
| `deploy.delete` | 删除部署 |
| `deploy.status` | 查询 Pod 状态 + 容器日志 |
| `deploy.list` | 列出所有部署及资源使用 |

### 推理 (Demand, 代理到引擎)

| 工具 | 功能 |
|------|------|
| `inference.chat` | 对话补全 (代理到引擎 OpenAI API) |
| `inference.complete` | 文本补全 |
| `inference.embed` | 生成嵌入向量 |
| `inference.models` | 列出当前可用模型 |

### 知识 (Control + Feedback)

| 工具 | 功能 |
|------|------|
| `knowledge.search` | 搜索知识 (by hardware / model / engine / tags) |
| `knowledge.save` | 保存 Knowledge Note |
| `knowledge.resolve` | 解析最优配置 (L0→L2 多层合并) |
| `knowledge.list_engines` | 列出可用引擎定义 (从 SQLite 查询) |
| `knowledge.list_profiles` | 列出硬件 Profile (从 SQLite 查询) |
| `knowledge.generate_pod` | 从知识资产生成 Pod YAML |

### 知识查询 (增强，SQLite 关系查询驱动)

| 工具 | 功能 |
|------|------|
| `knowledge.search_configs` | 多维配置搜索：支持约束过滤/排序/聚合 |
| `knowledge.compare` | 对比 N 个配置的多维性能 |
| `knowledge.similar` | 基于性能向量距离找相似配置（跨硬件迁移推荐） |
| `knowledge.lineage` | 查询配置演化链（WITH RECURSIVE） |
| `knowledge.gaps` | 发现知识空白：哪些 HW×Engine×Model 组合未被充分测试 |
| `knowledge.aggregate` | 分组聚合统计（按引擎/硬件/模型维度的均值/分布） |

### 系统

| 工具 | 功能 |
|------|------|
| `shell.exec` | 执行 shell 命令 (白名单 + 审计) |
| `system.config` | 读写 AIMA 配置 |

---

## 工具定义示例

### deploy.apply

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

```go
{
    "name": "knowledge.resolve",
    "description": "Resolve optimal configuration (L0→L3 multi-layer merge)",
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
| 基准测试 | 专用测试框架 + 报告生成 | Agent: inference.chat × N + LLM 统计分析 |
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
- `internal/mcp/tools.go` - 工具实现

---

*最后更新：2026-02-27*
