# AIMA — 系统架构文档

> AI-Inference-Managed-by-AI
> 有限硬件资源上的 AI 推理优化与调度系统

---

## 1. 设计原则

### P1: 基础设施 + 轻量 Agent 便利层

Go 二进制是**薄基础设施层**——硬件检测、知识解析、Pod 生成、请求代理。
简单一次性查询可使用**内置 Go Agent (L3a)** 以最小延迟响应。
复杂智能逻辑由**外部/Sidecar AI Agent (L3b)** 通过 MCP 工具实现。

策略 = Agent 的事。基础设施 = Go 的事。
Go 代码不包含 if-else 决策树、规则引擎、或任何 "策略" 代码。

### P2: 知识胜于代码

能力扩展的主要方式是写 YAML 知识文件，而不是写新代码。

- 支持新引擎 → 写 Engine Asset YAML
- 支持新模型 → 写 Model Asset YAML
- 支持新硬件 → 写 Hardware Profile YAML（可能加少量检测代码）
- 沉淀调优经验 → Knowledge Note (YAML / SQLite)

80% 的能力扩展不需要重新编译。

### P3: 声明式优先

用 K3S Pod YAML 描述期望状态，系统自动收敛。

- 容器的启动、重试、健康检查、生命周期 —— K3S 管
- GPU 显存和算力的切分与隔离 —— HAMi 管
- AIMA 只做：(1) 从知识生成 Pod YAML, (2) kubectl apply, (3) 查询状态

### P4: 成熟工具组合

| 职责 | 选型 | 替代方案 |
|------|------|---------|
| 容器编排 | K3S | 标准 K8s, MicroK8s |
| GPU 虚拟化 | HAMi | MIG, MPS, Flex:ai |
| 状态存储 | SQLite | PostgreSQL, etcd |
| Agent 接口 | MCP (JSON-RPC) | gRPC, REST |
| 服务发现 | mDNS | Consul, etcd |
| Sidecar Agent | ZeroClaw (可选) | 任何 MCP Client |

每个工具可独立替换，彼此解耦。

### P5: 渐进智能 L0 → L3b

```
L3b: ZeroClaw ── 完整 AgentLoop + 持久记忆 + Identity ── 最优解 (动态+持久)
  ^ 升级
L3a: Go Agent ── 简单工具调用循环 ──────────────────── 良好解 (动态+无状态)
  ^ override
L2:  知识库 ──── 确定性匹配 ────────────────────────── 已知好解 (静态)
  ^ override
L1:  人类 CLI ── 手动指定参数 ──────────────────────── 指定解
  ^ override
L0:  默认值 ──── 硬编码保守配置 ────────────────────── 可用解 (always)
```

每层独立可用。无 Agent、无网络、无知识库 → L0 仍能启动推理服务。

### P6: 探索即知识

Agent 每次探索（调优、排障、部署尝试）产出结构化的 Knowledge Note。
其他设备的 Agent 复用这些知识，跳过已知失败、从最优起点开始。

### P7: 本地优先，偶尔联网

AIMA 的所有核心功能**必须在完全离线下可用**。网络连接是"锦上添花"。

---

## 2. 系统全景

### 四层 + 双脑架构

```
┌─────────────────────────────────────────────────────────────┐
│   External Agent (远程/强力) — 可选                            │
│   Claude Code / GPT / 自定义 MCP Client                       │
├───────────────────────────────────────────────────────────────┤
│   Agent Layer (双皮层 Dual Cortex)                             │
│   L3a: Go Agent (内置轻量) │ L3b: ZeroClaw (Sidecar)         │
├───────────────────────────────────────────────────────────────┤
│   Knowledge Layer (知识层)                                     │
│   5 种知识资产 (YAML) + SQLite 关系查询引擎                    │
├───────────────────────────────────────────────────────────────┤
│   Orchestration Layer (编排层)                                 │
│   K3S (轻量 Kubernetes) + HAMi (GPU 虚拟化中间件)              │
├───────────────────────────────────────────────────────────────┤
│   Infrastructure Layer (基础设施层) — AIMA Go 二进制            │
│   ~32 个 MCP 工具 · HTTP 推理代理 · 硬件检测                    │
└─────────────────────────────────────────────────────────────┘
```

---

## 3. 按领域组织的详细文档

| 领域 | 文档 | 主要内容 |
|------|------|----------|
| 核心原则 | 本文档 | 设计原则、架构全景 |
| Model | [docs/model.md](../docs/model.md) | 模型扫描、导入、删除、元数据 |
| Engine | [docs/engine.md](../docs/engine.md) | 引擎镜像、拉取、导入、Native 二进制 |
| Runtime | [docs/runtime.md](../docs/runtime.md) | K3S Runtime、Native Runtime、Multi-Runtime 抽象 |
| Knowledge | [docs/knowledge.md](../docs/knowledge.md) | 知识库、配置解析、Pod 生成 |
| HAL | [docs/hal.md](../docs/hal.md) | 硬件检测、能力向量 |
| K3S | [docs/k3s.md](../docs/k3s.md) | K8s 集成、Pod 管理、HAMi |
| Stack | [docs/stack.md](../docs/stack.md) | 基础设施（K3S, HAMi） |
| MCP | [docs/mcp.md](../docs/mcp.md) | MCP 服务器、工具定义 |
| CLI | [docs/cli.md](../docs/cli.md) | 命令行接口 |
| Agent | [docs/agent.md](../docs/agent.md) | Go Agent、ZeroClaw Sidecar |

---

## 4. 架构不变量

不可违反的架构约束：

**INV-1: 不为引擎类型写代码。** 引擎行为定义在 YAML。添加新引擎 = 写 YAML。

**INV-2: 不为模型类型写代码。** 模型元数据在 YAML。模型类型是知识，不是代码分支。

**INV-3: 最小化运行时管理。** K3S 管 Pod 的创建、监控、重启、销毁。
Native runtime 只做极简进程管理（start/stop/logs）。

**INV-4: 职责分离的状态存储。** AIMA 系统状态在 `aima.db`，Agent 记忆在 `zeroclaw.db`。

**INV-5: MCP 工具即真相。** CLI 是 MCP 工具的包装。CLI 永不实现 MCP 工具之外的逻辑。

**INV-6: 探索即知识。** Agent 每次探索必须产出 Knowledge Note。

**INV-7: 知识对齐优化链路。** 知识资产严格对齐 PRD 优化链路。

**INV-8: 离线可用。** 所有核心功能必须在完全离线下可用。

---

*最后更新：2026-02-27*
