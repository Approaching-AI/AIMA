# Agent Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的 Agent 架构 (L3a: Go Agent)。

## 核心命题

**远程强 LLM+Agent 框架 与 本地轻量 Agent 并不冲突，可以共存。**

AIMA 内置本地 Agent：
- **L3a: Go Agent** — 内置于 AIMA 二进制，进程内会话记忆（30min TTL），处理查询和多轮对话

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
aima ask --local "..."        # 强制 Go Agent (L3a)
aima ask --session <id> "..." # 继续会话
```

### 降级策略

| 条件 | 行为 |
|------|------|
| Go Agent 可用 | L3a 处理 |
| 无可用 LLM | L2 (知识解析, 无 Agent) |

---

## 相关文件

- `internal/agent/agent.go` - Go Agent Loop (L3a)
- `internal/agent/session.go` - 会话记忆 (SessionStore, 进程内)
- `internal/agent/dispatcher.go` - Agent 路由决策

---

*最后更新：2026-03-02*
