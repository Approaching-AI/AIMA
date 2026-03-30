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

按功能领域组织。所有工具通过 `internal/mcp/tools.go` 的 `RegisterAllTools()` 注册。

### 硬件感知 — Hardware (2)

| 工具 | 功能 |
|------|------|
| `hardware.detect` | 检测 GPU/CPU/RAM/NPU/Storage，返回能力向量 + 功耗 |
| `hardware.metrics` | 实时资源利用率 (GPU/CPU/RAM) + 功耗 + 温度 |

### 模型管理 — Model (6)

| 工具 | 功能 |
|------|------|
| `model.scan` | 扫描本地模型目录，发现并注册新模型 (GGUF/SafeTensors) |
| `model.list` | 列出所有已注册模型 |
| `model.pull` | 下载模型 (HuggingFace/ModelScope, 断点续传) |
| `model.import` | 从本地路径导入模型 |
| `model.info` | 查询模型详细信息 (格式/量化/参数量) |
| `model.remove` | 删除模型记录 (**需审计+回滚快照**) |

### 引擎管理 — Engine (6)

| 工具 | 功能 |
|------|------|
| `engine.scan` | 扫描 containerd + Docker 镜像，匹配 Engine Asset |
| `engine.list` | 列出可用引擎 (容器 + native 二进制) |
| `engine.info` | 查询引擎详细信息 |
| `engine.pull` | 拉取引擎镜像 (Docker/containerd) |
| `engine.import` | 从 OCI tar 导入镜像 |
| `engine.remove` | 删除引擎镜像 (**需审计+回滚快照**) |

### 编排 — Deploy (7)

| 工具 | 功能 |
|------|------|
| `deploy.apply` | hal detect → knowledge resolve → CheckFit → 部署 (K3S Pod / Docker 容器 / Native 进程)。**Agent 调用需审批**：返回部署计划 + approval ID，用户确认后调 `deploy.approve` 执行 |
| `deploy.approve` | 批准并执行挂起的部署 (通过 approval ID)。仅在 `deploy.apply` 返回 NEEDS_APPROVAL 后使用 |
| `deploy.dry_run` | 同 apply 但不执行，预览生成的配置和 Pod YAML |
| `deploy.delete` | 删除部署 (**需审计+回滚快照**) |
| `deploy.status` | 查询部署状态 (支持 pod 名或模型名查找, 返回 phase/ready/restarts/exit_code) |
| `deploy.list` | 列出所有活跃部署及资源使用 |
| `deploy.logs` | 查看部署日志 (支持 pod 名或模型名查找, K3S kubectl logs / Docker docker logs / Native stdout) |

### 知识核心 — Knowledge Core (8)

| 工具 | 功能 |
|------|------|
| `knowledge.resolve` | 解析最优配置 (L0→L2 多层合并 + VRAM/统一显存过滤) |
| `knowledge.list` | 知识库概览 (Engine/Model/Hardware/Partition 统计) |
| `knowledge.list_profiles` | 列出硬件 Profile (从 SQLite) |
| `knowledge.list_engines` | 列出引擎定义 (从 SQLite) |
| `knowledge.list_models` | 列出模型定义 (从 SQLite) |
| `knowledge.search` | 搜索知识笔记 (by hardware / model / engine / tags) |
| `knowledge.save` | 保存 Knowledge Note |
| `knowledge.generate_pod` | 从知识资产直接生成 Pod YAML |

### 知识查询 — Knowledge Query (7)

SQLite 关系查询驱动，支持多维分析:

| 工具 | 功能 |
|------|------|
| `knowledge.search_configs` | 多维配置搜索 (约束过滤 + 排序 + 分页) |
| `knowledge.compare` | 对比 N 个配置的多维性能 |
| `knowledge.similar` | 基于 6D 性能向量找相似配置 (跨硬件迁移推荐) |
| `knowledge.lineage` | 查询配置演化链 (WITH RECURSIVE) |
| `knowledge.gaps` | 发现知识空白: 哪些 HW×Engine×Model 组合缺少测试 |
| `knowledge.aggregate` | 分组聚合统计 (按引擎/硬件/模型维度) |
| `knowledge.promote` | 将 Configuration 提升为推荐配置 |

### 基准测试 — Benchmark (4)

| 工具 | 功能 |
|------|------|
| `benchmark.run` | 在线基准测试: 向 OpenAI 兼容 endpoint 发送流式请求, 测量 TTFT/TPOT/吞吐量. 支持多轮测量(rounds)、输出长度校验重试(min_output_ratio)、请求级重试(max_retries) |
| `benchmark.matrix` | 矩阵测试: 在多组 concurrency × input_tokens × max_tokens 组合上批量运行 benchmark.run |
| `benchmark.record` | 手动记录性能数据 (throughput/TTFT/TPOT/VRAM), 自动创建 Configuration |
| `benchmark.list` | 查询历史基准测试结果, 支持按 model/hardware/engine/config_id 过滤 |

### 基础设施 — Stack (3)

| 工具 | 功能 |
|------|------|
| `stack.preflight` | 预检: 按 tier 检查缺失文件 (tier: docker/k3s) |
| `stack.init` | 分层安装: docker 层 (Docker+CTK+aima-serve) 或 k3s 层 (+K3S+HAMi) |
| `stack.status` | 查询所有 Stack 组件状态 |

### 目录覆盖 — Catalog (2)

| 工具 | 功能 |
|------|------|
| `catalog.override` | 推送 YAML 到 `~/.aima/catalog/` 运行时覆盖层 |
| `catalog.status` | 查看目录状态 (factory vs overlay, staleness) |

### 系统 — System (3)

| 工具 | 功能 |
|------|------|
| `system.status` | 设备全景: 硬件 + 部署列表 + 实时指标 |
| `system.config` | 读写 AIMA 配置 (get/set); `api_key`/`llm.api_key` 响应脱敏; `api_key` 热更新认证, `llm.*` 热替换 Agent LLM 客户端 |
| `shell.exec` | 执行 shell 命令 (60s 超时 + 1MB 输出上限 + 审计日志) |

### 发现 — Discovery (1)

| 工具 | 功能 |
|------|------|
| `discover.lan` | mDNS 扫描局域网 AIMA 设备 (_llm._tcp) |

### Agent — Agent (5)

| 工具 | 功能 |
|------|------|
| `agent.ask` | 向 Go Agent (L3a) 提问，自动调用工具链。支持 `dangerously_skip_permissions` 跳过部署审批 |
| `agent.status` | 查询 Agent 状态 (L3a 可用性) |
| `agent.rollback_list` | 列出可回滚的操作快照 |
| `agent.rollback` | 从快照恢复 (模型/引擎/部署) |
| `agent.guide` | 获取完整 Agent 使用指南 (所有工具参数、工作流、API 详情) |

### Fleet 多设备 — Fleet (4)

| 工具 | 功能 |
|------|------|
| `fleet.list_devices` | mDNS 扫描 + 列出局域网所有 AIMA 设备 (每次调用自动发现) |
| `fleet.device_info` | 查询远程设备详情 (硬件+部署+指标, registry 为空时自动发现) |
| `fleet.device_tools` | 列出远程设备可用工具 (registry 为空时自动发现) |
| `fleet.exec_tool` | 在远程设备执行 MCP 工具 (blockedTools 安全过滤, registry 为空时自动发现) |

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
- `internal/mcp/tools.go` - 工具实现

---

*最后更新：2026-03-04 (shell.exec 60s 超时+1MB 输出上限, deploy 操作跨 Runtime fallback)*
