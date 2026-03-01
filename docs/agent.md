# Agent Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的双皮层 Agent 架构 (L3a: Go Agent + L3b: ZeroClaw)。

## 核心命题

**远程强 LLM+Agent 框架 与 本地轻量 Agent 并不冲突，可以共存。**

AIMA 实现两级本地 Agent：
- **L3a: Go Agent** — 内置于 AIMA 二进制，无状态/会话级，处理简单查询
- **L3b: ZeroClaw Sidecar** — 可选的独立进程，持久记忆+跨会话学习，处理复杂任务

两者共享同一套 MCP 工具，对外部 Agent（Claude Code / GPT 等）完全透明。

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

- ~400 行 Go 代码，无外部依赖
- 无持久记忆 (每次对话独立)
- 单一 LLM 后端 (最近一次检测到的可用模型)
- ~0 额外内存开销
- 适合：简单查询、一次性操作、快速响应

### LLM Provider 检测优先级

```
1. AIMA 自身部署的本地模型 (localhost:6188/v1)  → 零网络依赖
2. 用户配置的 API Key (Anthropic/OpenAI/...)    → 需联网
3. 不可用 → 降级到 L2 知识解析 (无 Agent)
```

---

## L3b: ZeroClaw Sidecar (可选增强)

ZeroClaw 是 Rust 实现的轻量 AI Agent 运行时 (~8.8 MB 二进制, <5 MB RAM, <10ms 冷启动)。
它用 **trait 驱动架构** 定义了 Agent 系统的 8 个核心可替换组件:

| ZeroClaw 组件 | 职责 | AIMA 如何利用 |
|--------------|------|-------------|
| **Provider** | LLM 后端 | 指向 AIMA 部署的本地模型 or 云端 API |
| **Channel** | 通信接入 | stdio pipe (AIMA 发任务给 ZeroClaw) |
| **Tool** | 能力执行 | MCP Client 连回 AIMA MCP Server |
| **Memory** | 持久记忆 | SQLite + FTS5 全文搜索 + 向量相似度 |
| **Security** | 访问控制 | 配对认证 + 沙箱 + 白名单 |
| **Tunnel** | 网络暴露 | 可选 (Cloudflare / Tailscale) |
| **Identity** | Agent 人格 | AIMA 设备大脑身份定义 |
| **Observability** | 监控 | 日志 + 指标 |

### ZeroClaw 弥补 Go Agent 缺失的关键能力

| 能力 | Go Agent (L3a) | ZeroClaw (L3b) |
|------|---------------|----------------|
| 持久记忆 | 无 (文件级对话存储) | SQLite + FTS5 + 向量相似度 + 混合排序 |
| 跨会话学习 | 无 | 完整记忆系统，跨对话积累经验 |
| 安全沙箱 | 工具白名单 | 配对认证 + 沙箱 + 白名单 + 工作区限制 |
| 多 LLM Provider | 单一后端 | 22+ 模型服务开箱即用 |
| Agent 人格 | 无 | Markdown 格式 Identity 角色定义 |

---

## Sidecar 通信架构

```
AIMA Go Binary
  │
  ├── ZeroClaw Lifecycle Manager (start/stop/health)
  │     │
  │     └── 启动 ZeroClaw 二进制:
  │           --channel stdio              (接收任务)
  │           --provider openai            (LLM 后端)
  │           --provider-base-url localhost:6188/v1  (AIMA 自己的推理)
  │           --tool-mcp stdio:aima        (连回 AIMA MCP Server)
  │           --memory-path ~/.aima/zeroclaw.db
  │           --identity ~/.aima/zeroclaw-identity.md
  │
  └── 两条通信通道:
        1. stdio pipe (AIMA → ZeroClaw): 发送任务请求
        2. MCP client (ZeroClaw → AIMA): 调用 54 个 MCP 工具
```

**优雅之处：ZeroClaw 本身就是 MCP Client，AIMA 本身就是 MCP Server。
协议已经存在，无需发明新接口。**

---

## Agent Dispatcher — 任务路由

`aima ask` 命令的 Agent Dispatcher 根据任务复杂度自动路由：

```bash
aima ask "..."                # 自动路由 (简单→L3a, 复杂→L3b)
aima ask --local "..."        # 强制 L3a (Go Agent, 快速, 无状态)
aima ask --deep "..."         # 强制 L3b (ZeroClaw, 持久记忆)
aima ask --session abc "..."  # 继续 ZeroClaw 会话 (跨对话记忆)
```

### 路由启发式

| 信号 | 路由目标 |
|------|---------|
| 单步操作 ("有什么 GPU?", "部署 qwen3-8b") | L3a |
| 多步推理 ("为什么模型慢?", "优化所有配置") | L3b |
| 需要历史上下文 ("上次调优结果如何?") | L3b |
| 需要规划 ("为5个模型规划GPU分配") | L3b |
| ZeroClaw 不可用 | L3a (降级) |
| 无可用 LLM | L2 (知识解析, 无 Agent) |

### 任务路由决策矩阵

| 任务示例 | L3a (Go Agent) | L3b (ZeroClaw) |
|---------|---------------|----------------|
| `aima ask "我有什么GPU?"` | **首选** | 过度 |
| `aima ask "部署 qwen3-8b"` | **首选** | 过度 |
| `aima ask "为什么我的模型慢?"` | 可用 | **更好** |
| `aima ask "优化所有模型配置"` | 不足 | **首选** |
| `aima ask "分析上周性能趋势"` | 不能(无记忆) | **首选** |
| `aima ask "为5个模型规划GPU分配"` | 勉强 | **首选** |

---

## 相关文件

- `internal/agent/agent.go` - Go Agent Loop (L3a)
- `internal/agent/dispatcher.go` - L3a/L3b 路由决策
- `internal/zeroclaw/manager.go` - ZeroClaw Lifecycle Manager
- `internal/zeroclaw/installer.go` - ZeroClaw 下载安装

---

*最后更新：2026-02-28*
