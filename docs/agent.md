# Agent Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的 Agent 架构 (L3a: Go Agent) 及 Explorer 自主探索子系统。

## 核心命题

**远程强 LLM+Agent 框架 与 本地轻量 Agent 并不冲突，可以共存。**

AIMA 内置本地 Agent：
- **L3a: Go Agent** — 内置于 AIMA 二进制，进程内会话记忆（30min TTL），处理查询和多轮对话
- **Explorer** — 自主探索引擎，PDCA 循环驱动，发现设备最优推理配置

Go Agent 与外部 Agent（Claude Code / GPT 等）共享同一套 MCP 工具，对外部 Agent 完全透明。

---

## L3a: Go Agent (内置轻量)

Go Agent 是编译进 AIMA 二进制的极简 Agent Loop：

```
用户: aima ask "我有什么 GPU?"
  │
  ▼
Go Agent Loop (最多 30 轮):
  1. 构建系统提示 (含 MCP 工具定义)
  2. 发送给 LLM Provider (本地模型 or 云端 API)
  3. 收到 tool_call → 执行 MCP 工具 → 结果追加上下文
  4. 收到 text → 返回给用户
  5. 重复 3-4 直到 LLM 不再调用工具
```

### 特性

- ~500 行 Go 代码，无外部依赖
- 进程内会话记忆 (SessionStore: 30min TTL, 50 条消息上限, 重启即清零)
- 单一 LLM 后端 (最近一次检测到的可用模型)
- ~0 额外内存开销
- 适合：简单查询、多轮追问、一次性操作、快速响应

### LLM Provider 检测优先级

```
1. AIMA 自身部署的本地模型 (localhost:6188/v1)  → 零网络依赖
2. 用户配置的 API Key (Anthropic/OpenAI/...)    → 需联网
3. 不可用 → 降级到 L2 知识解析 (无 Agent)
```

---

## Agent Dispatcher — 任务路由

`aima ask` 命令通过 Go Agent 处理：

```bash
aima ask "..."                # Go Agent 处理
aima ask --session <id> "..." # 继续会话
```

### 降级策略

| 条件 | 行为 |
|------|------|
| Go Agent 可用 | L3a 处理 |
| 无可用 LLM | L2 (知识解析, 无 Agent) |

---

## Explorer — 自主探索子系统

Explorer 是 AIMA 的自主知识发现引擎，负责在边缘设备上探索最优的 model×engine 配置。

### 架构概览

```
Explorer (协调器)
  │
  ├── Planner ─────── Tier 0/1: RulePlanner (确定性规则)
  │                   Tier 2:   ExplorerAgentPlanner (LLM PDCA)
  │
  ├── Workspace ───── ~/.aima/explorer/
  │   ├── device-profile.md      (只读 — AIMA 生成)
  │   ├── available-combos.md    (只读 — AIMA 生成)
  │   ├── knowledge-base.md      (只读 — AIMA 生成)
  │   ├── plan.md                (Agent 写入)
  │   ├── summary.md             (Agent 写入)
  │   └── experiments/           (AIMA 写入实验结果)
  │
  ├── Harvester ───── 实验结果后处理 (知识沉淀、自动晋升)
  ├── Scheduler ───── cron/interval 调度
  └── EventBus ────── 事件驱动协调
```

### PDCA 循环 (Tier 2: ExplorerAgentPlanner)

```
Plan: LLM 读取工作区文档 → 制定探索计划 (plan.md)
  │
Do:   Go 透明执行计划 → 部署引擎 → 运行 benchmark → 收获结果
  │
Check: LLM 分析实验报告 → 更新知识 (summary.md)
  │     verdict="done" → 结束
  │     verdict="continue" → 进入 Act
  │
Act:  LLM 追加实验 → 修订 plan.md → 回到 Do
```

LLM 通过 `ExplorerToolExecutor` 的 7 个 bash 风格工具操作工作区：

| 工具 | 功能 | 示例 |
|------|------|------|
| `cat` | 读取文件 | `cat("device-profile.md")` |
| `ls` | 列目录 | `ls("experiments/")` |
| `write` | 写文件 | `write("plan.md", content)` |
| `append` | 追加内容 | `append("summary.md", findings)` |
| `grep` | 搜索内容 | `grep("OOM", "experiments/")` |
| `query` | 查询知识库 | `query("search", {model: "qwen3"})` |
| `done` | 完成阶段 | `done("continue")` |

### 文档分类

| 类别 | 文件 | 生成者 | LLM 权限 |
|------|------|--------|----------|
| Fact (事实) | device-profile.md, available-combos.md, knowledge-base.md | AIMA (RefreshFactDocuments) | 只读 |
| Analysis (分析) | plan.md, summary.md | LLM Agent | 读写 |
| Experiment (实验) | experiments/*.md | AIMA (WriteExperimentResult) | 只读+可追加 Agent Notes |

### Tier 降级

| Tier | 条件 | Planner | 能力 |
|------|------|---------|------|
| 0 | 无 LLM | RulePlanner | 基于规则的组合遍历 |
| 1 | LLM 无工具能力 | RulePlanner | 同 Tier 0 |
| 2 | LLM 有工具能力 | ExplorerAgentPlanner | 文档驱动 PDCA，深度分析 |

### Engine Discovery (解耦设计)

Explorer 的 engine 发现已从 catalog 匹配解耦：列出所有本地安装的 engines，catalog 仅用于元数据增强（Features、TunableParams）。这确保新安装的引擎（即使还没有 catalog YAML）也能被 Explorer 发现和使用。

### 资源控制

- `max_cycles`: PDCA 最大迭代次数（默认 3）
- `max_tasks`: 每轮计划最大任务数（默认 5）
- `max_plan_duration`: 单轮时间预算（默认 30min）
- `max_tokens_per_day`: 日 LLM token 上限

---

## 相关文件

### Go Agent
- `internal/agent/agent.go` - Go Agent Loop (L3a)
- `internal/agent/session.go` - 会话记忆 (SessionStore, 进程内)
- `internal/agent/dispatcher.go` - Agent 路由决策
- `internal/agent/openai.go` - OpenAI 兼容 LLM 客户端

### Explorer
- `internal/agent/explorer.go` - Explorer 协调器（事件循环、计划执行、PDCA 集成）
- `internal/agent/explorer_planner.go` - Planner 接口、AnalyzablePlanner、RulePlanner、类型定义
- `internal/agent/explorer_agent_planner.go` - ExplorerAgentPlanner（LLM agent loop、三阶段系统提示）
- `internal/agent/explorer_workspace.go` - ExplorerWorkspace（文件操作、只读守卫、路径安全、YAML 解析）
- `internal/agent/explorer_tools.go` - ExplorerToolExecutor（7 工具定义与执行）
- `internal/agent/explorer_harvester.go` - Harvester（结果后处理、知识沉淀）
- `internal/agent/explorer_scheduler.go` - Scheduler（cron/interval 调度）
- `internal/agent/explorer_eventbus.go` - EventBus（事件驱动协调）

---

*最后更新：2026-04-09 (ExplorerAgentPlanner 文档驱动 PDCA 工作流)*
