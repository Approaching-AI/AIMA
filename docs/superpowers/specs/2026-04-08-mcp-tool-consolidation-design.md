# MCP 工具整合重构设计

> **日期**: 2026-04-08
> **状态**: Draft
> **分支**: `refactor/mcp-tool-consolidation`
> **影响版本**: v0.4.x (breaking change)

## 1. 背景与动机

### 1.1 现状

AIMA 当前暴露 **101 个 MCP 工具**，分布在 17 个命名空间。这些工具通过 11 个注册文件 (`tools_*.go`) 定义，通过 9 个布线文件 (`tooldeps_*.go`) 实现。

### 1.2 核心问题

**Agent 认知负担过重：**
- Go Agent (L3a) 通过 `ListTools()` 获取全部 101 个工具传给 LLM，无 Profile 过滤
- 每次对话浪费 ~10K-14K tokens 用于工具定义
- System prompt (`prompt.md`) 仅覆盖 ~25 个工具，剩余 ~76 个是 Agent 的盲区
- LLM 工具选择准确性在 50+ 工具时显著下降

**人类操作员认知负担过重：**
- 101 个工具作为扁平列表呈现，无有效层次
- `knowledge.*` 一个命名空间就有 25 个工具，混合了 8 个不同子领域
- ~29 个工具有隐式前置条件（需要 Central Server / 数据积累 / OpenClaw / K3S），但不向用户说明

**Profile 系统形同虚设：**
- 定义了 ProfilePatrol (10 工具)、ProfileExplorer (24 工具) 等子集
- 但只影响 `tools/list` JSON-RPC 发现，不影响 `ListTools()`（Agent 内部用的）
- 内部消费者（Agent、Patrol、Explorer）全部绕过 Profile，看到全部 101 个工具

**原子化原则偏离：**
- 项目要求 "One MCP tool = one function = one responsibility"
- 但存在大量"同一操作、不同参数"被拆为独立工具的情况（如 4 个 `knowledge.list_*`）
- CRUD 生命周期被拆为 3-4 个独立工具（explore、tuning、patrol、explorer 各一组）

### 1.3 设计目标

1. 工具数从 101 降至 ~56，减少 45%
2. `knowledge.*` 从 25 个工具降至 6 个，减少 76%
3. Profile 系统在 Agent LLM 层面真正生效
4. Agent system prompt 与 Profile 工具集完全对齐，零盲区
5. 命名空间与领域边界一致，依赖关系显式化
6. 底层业务逻辑零改动——只改注册/布线/暴露层

## 2. 设计原则

1. **命名空间 = 领域边界** — 每个 namespace 覆盖一个内聚领域
2. **操作语义原子化，查询参数不原子化** — `deploy.apply` vs `deploy.delete` 是不同工具（不同操作语义）；`catalog.list(kind=profiles)` vs `catalog.list(kind=engines)` 是同一个工具的不同参数（同一操作语义）
3. **scan 是独立操作语义** — 重新发现磁盘/容器资源 ≠ 读数据库列表，保留为独立工具
4. **CRUD 生命周期合并** — 管理同一实体的 start/status/stop/result 合为一个工具 + `action` 参数
5. **依赖显式化** — 需要 Central Server 的工具归入 `central.*`，不藏在 `knowledge.*` 里
6. **不做兼容层** — 旧工具名直接删除，不保留 alias。外部调用者（OpenClaw）同步更新。

## 3. 工具映射表

### 3.1 完整映射（101 -> 56）

#### hardware (2) — 不变

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 1 | `hardware.detect` | `hardware.detect` | — |
| 2 | `hardware.metrics` | `hardware.metrics` | — |

#### model (6) — 不变

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 3 | `model.scan` | `model.scan` | — |
| 4 | `model.list` | `model.list` | — |
| 5 | `model.pull` | `model.pull` | — |
| 6 | `model.import` | `model.import` | — |
| 7 | `model.info` | `model.info` | — |
| 8 | `model.remove` | `model.remove` | — |

#### engine (6) — 不变

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 9 | `engine.scan` | `engine.scan` | — |
| 10 | `engine.list` | `engine.list` | — |
| 11 | `engine.pull` | `engine.pull` | — |
| 12 | `engine.import` | `engine.import` | — |
| 13 | `engine.info` | `engine.info` | — |
| 14 | `engine.remove` | `engine.remove` | — |

#### deploy (8) — 基本不变，dry_run 吸收 generate_pod

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 15 | `deploy.run` | `deploy.run` | — |
| 16 | `deploy.apply` | `deploy.apply` | — |
| 17 | `deploy.dry_run` | `deploy.dry_run` + `knowledge.generate_pod` | 新增 `output` 参数: config (默认) / pod_yaml |
| 18 | `deploy.approve` | `deploy.approve` | — |
| 19 | `deploy.delete` | `deploy.delete` | — |
| 20 | `deploy.status` | `deploy.status` | — |
| 21 | `deploy.list` | `deploy.list` | — |
| 22 | `deploy.logs` | `deploy.logs` | — |

#### catalog (3) — 新命名空间

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 23 | `catalog.list` | `knowledge.list` + `list_profiles` + `list_engines` + `list_models` + `catalog.status` + `scenario.list` | 合并：`kind` 参数 (profiles / engines / models / partitions / scenarios / summary / all) |
| 24 | `catalog.override` | `catalog.override` | 从 system 文件迁入 catalog 文件 |
| 25 | `catalog.validate` | `catalog.validate` | 从 system 文件迁入 catalog 文件 |

#### knowledge (6) — 从 25 大幅瘦身

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 26 | `knowledge.resolve` | `knowledge.resolve` | — |
| 27 | `knowledge.search` | `knowledge.search` + `knowledge.search_configs` | 合并：`scope` 参数 (configs / notes / all)。configs 为默认值。两种 scope 保留各自的过滤参数。 |
| 28 | `knowledge.analytics` | `knowledge.compare` + `similar` + `lineage` + `gaps` + `aggregate` | 合并：`query` 参数分发。每种 query 保留各自的子参数（如 compare 需要 config_ids，similar 需要 config_id + weights）。 |
| 29 | `knowledge.promote` | `knowledge.promote` | — |
| 30 | `knowledge.save` | `knowledge.save` | — |
| 31 | `knowledge.evaluate` | `knowledge.validate` + `engine_switch_cost` + `open_questions` | 合并：`action` 参数 (validate / switch_cost / open_questions)。每种 action 保留各自的子参数。 |

#### benchmark (4) — 不变

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 32 | `benchmark.run` | `benchmark.run` | — |
| 33 | `benchmark.record` | `benchmark.record` | — |
| 34 | `benchmark.list` | `benchmark.list` | — |
| 35 | `benchmark.matrix` | `benchmark.matrix` | — |

#### system (2) — 瘦身

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 36 | `system.status` | `system.status` | — |
| 37 | `system.config` | `system.config` | — |

#### stack (1) — 合并

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 38 | `stack` | `stack.preflight` + `stack.init` + `stack.status` | 合并：`action` 参数 (status / preflight / init) |

#### central (3) — 新命名空间

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 39 | `central.sync` | `knowledge.sync_push` + `sync_pull` + `sync_status` | 合并：`action` 参数 (push / pull / status) |
| 40 | `central.advise` | `knowledge.advise` + `advisory_feedback` | 合并：`action` 参数 (request / feedback) |
| 41 | `central.scenario` | `scenario.generate` + `scenario.list_central` | 合并：`action` 参数 (generate / list) |

#### fleet (2) — 合并

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 42 | `fleet.info` | `fleet.list_devices` + `fleet.device_info` + `fleet.device_tools` | 合并：无 device_id = 列表，有 device_id = 详情含工具清单 |
| 43 | `fleet.exec` | `fleet.exec_tool` | 简化名称 |

#### scenario (2) — 本地场景

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 44 | `scenario.show` | `scenario.show` | — |
| 45 | `scenario.apply` | `scenario.apply` | — |

注：`scenario.list` 已合并入 `catalog.list(kind=scenarios)`。

#### data (2) — 新命名空间

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 46 | `data.export` | `knowledge.export` | 独立命名空间 |
| 47 | `data.import` | `knowledge.import` | 独立命名空间 |

#### agent (3) — 瘦身

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 48 | `agent.ask` | `agent.ask` | — |
| 49 | `agent.status` | `agent.status` | — |
| 50 | `agent.rollback` | `agent.rollback_list` + `agent.rollback` | 合并：`action` 参数 (list / restore) |

#### 自动化子系统 (4) — 各自 CRUD 合并

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 51 | `patrol` | `agent.patrol_status` + `alerts` + `patrol_config` + `patrol_actions` | 合并：`action` 参数 (status / alerts / config / actions) |
| 52 | `explore` | `explore.start` + `status` + `stop` + `result` | 合并：`action` 参数 (start / status / stop / result) |
| 53 | `tuning` | `tuning.start` + `status` + `stop` + `results` | 合并：`action` 参数 (start / status / stop / results) |
| 54 | `explorer` | `explorer.status` + `config` + `trigger` | 合并：`action` 参数 (status / config / trigger) |

#### 集成 (2) — 保留

| # | 新工具名 | 旧工具名 | 变更 |
|---|---------|---------|------|
| 55 | `openclaw` | `openclaw.sync` + `status` + `claim` | 合并：`action` 参数 (sync / status / claim) |
| 56 | `support` | `support.askforhelp` | 简化名称，保留全部参数 |

### 3.2 删除清单（12 个旧工具，无对应新工具）

| 旧工具 | 删除理由 |
|--------|---------|
| `download.list` | 极低频，通过 model.pull 进度查看 |
| `engine.plan` | 被 knowledge.resolve + deploy.dry_run 覆盖 |
| `shell.exec` | 白名单太窄（nvidia-smi/df/free/uname/kubectl），不实用 |
| `discover.lan` | 与 fleet.info 完全冗余 |
| `agent.guide` | 静态文本，内容移入 agent system prompt |
| `device.power_history` | 极低频，后续按需加回 |
| `device.power_mode` | 折入 system.config 的 power_mode key |
| `app.register` | PRD D4 功能从未实际使用 |
| `app.provision` | 同上 |
| `app.list` | 同上 |
| `knowledge.generate_pod` | 吸收进 deploy.dry_run(output=pod_yaml) |
| `knowledge.list` (单独 summary 版) | 被 catalog.list(kind=summary) 取代 |

### 3.3 数字汇总

| 维度 | 旧 | 新 | 变化 |
|------|---|---|------|
| 总工具数 | 101 | 56 | **-45%** |
| `knowledge.*` 工具数 | 25 | 6 | **-76%** |
| 命名空间数 | 17 (混乱) | 17 (内聚) | 边界重组 |
| CRUD 生命周期工具 | 22 | 6 | **-73%** |
| 需要 Central 的工具 | 7 (藏在 knowledge 里) | 3 (显式 central.*) | 依赖透明 |
| Agent LLM 可见工具 (ProfileOperator) | 101 (全量) | 39 | **-61%** |

## 4. Profile 系统重设计

### 4.1 架构决策：Advisory 而非 Enforcement

Profile 控制**哪些工具对 LLM 可见**，但不阻止 `ExecuteTool()` 的直接调用。

| 调用路径 | Profile 是否过滤 | 理由 |
|----------|----------------|------|
| `tools/list` JSON-RPC | 是 | 外部客户端发现 |
| `ListToolsForProfile(p)` | 是（新增） | Agent LLM 工具列表 |
| `tools/call` JSON-RPC | 否 | 外部客户端可能知道隐藏工具名 |
| `ExecuteTool()` 内部调用 | 否 | Patrol/Explorer 硬编码工具名 |

安全边界由已有的 agent guardrails（blocked / confirmable）保障，不是 Profile 的职责。

### 4.2 代码变更

在 `server.go` 新增：

```go
func (s *Server) ListToolsForProfile(p Profile) []ToolDefinition
```

在 `agent.go` 变更：

```go
func WithProfile(p mcp.Profile) AgentOption
// Ask() 内部使用 a.tools.ListToolsForProfile(a.profile) 替代 a.tools.ListTools()
```

### 4.3 新 Profile 定义

**ProfileOperator（39 个）— 外部 AI Agent + 人类操作员：**

```
hardware.*          (2)   model.*             (6)   engine.*            (6)
deploy.*            (8)   system.*            (2)   catalog.list        (1)
benchmark.run       (1)   benchmark.list      (1)   fleet.*             (2)
scenario.*          (2)   knowledge.resolve   (1)   knowledge.search    (1)
knowledge.promote   (1)   agent.ask           (1)   agent.status        (1)
agent.rollback      (1)   openclaw            (1)   support             (1)
```

**ProfilePatrol（10 个）— 巡逻循环：**

```
hardware.metrics                                                        (1)
deploy.list, deploy.status, deploy.logs, deploy.apply,
  deploy.approve, deploy.dry_run                                        (6)
knowledge.resolve                                                       (1)
benchmark.run                                                           (1)
patrol                                                                  (1)
```

**ProfileExplorer（20 个）— Explorer + Tuning Agent：**

```
hardware.detect, hardware.metrics                                       (2)
deploy.apply, deploy.approve, deploy.dry_run, deploy.status,
  deploy.list, deploy.logs, deploy.delete                               (7)
benchmark.run, benchmark.record, benchmark.list                         (3)
knowledge.resolve, knowledge.search, knowledge.promote, knowledge.save  (4)
explore, tuning, explorer                                               (3)
central.advise                                                          (1)
```

**ProfileFull — 全部 56 个工具。** 默认值，用于管理员和调试。

### 4.4 Agent 创建时传入 Profile

```
用户调用的 agent.ask   -> Agent 使用 ProfileOperator (39 个工具)
Explorer LLM planning  -> Agent 使用 ProfileExplorer (20 个工具)
```

## 5. Agent System Prompt 重写

### 5.1 设计原则

- 只描述 Profile 内的工具，与 LLM 看到的工具列表 100% 对齐，零盲区
- 按用户意图组织（"部署模型"、"搜索知识"），不按工具类别
- 覆盖 ProfileOperator 的全部 39 个工具

### 5.2 新 prompt 结构

```
# AIMA Agent

你是 AIMA 推理管理代理。通过 MCP 工具操作这台边缘设备上的 AI 推理服务。

## 了解设备
- hardware.detect       -> GPU/CPU/VRAM 硬件信息
- hardware.metrics      -> 实时 GPU 利用率、温度、显存
- system.status         -> 综合概览（硬件 + 部署 + 指标）
- system.config         -> 读写系统配置

## 部署模型
1. knowledge.resolve(model=...) -> 获取最优引擎和配置
2. deploy.run(model=...)        -> 一步部署（自动解析、拉取、部署、等待就绪）
   或 deploy.apply -> 返回审批计划 -> 用户确认 -> deploy.approve
3. deploy.status(name=...)      -> 确认运行状态
- deploy.dry_run  -> 预览配置和适配报告，不执行
- deploy.list     -> 所有部署
- deploy.logs     -> 部署日志
- deploy.delete   -> 删除部署

## 管理模型和引擎
- model.list / engine.list           -> 本地已有（数据库）
- model.scan / engine.scan           -> 重新扫描磁盘/容器发现新资源
- catalog.list(kind=models|engines)  -> YAML 目录支持的完整列表
- model.pull / engine.pull           -> 下载
- model.info / engine.info           -> 详情
- model.import / engine.import       -> 从本地路径导入
- model.remove / engine.remove       -> 删除

## 搜索知识库
- knowledge.search(scope=configs) -> 已测试的配置和性能数据
- knowledge.search(scope=notes)   -> Agent 探索笔记
- knowledge.promote               -> 提升配置为 golden/archived

## 基准测试
- benchmark.run  -> 对已部署模型执行基准测试
- benchmark.list -> 查看历史基准结果

## 多设备管理
- fleet.info  -> 列出局域网 AIMA 设备（或指定 device_id 查详情）
- fleet.exec  -> 在远程设备执行工具

## 场景部署
- scenario.show  -> 查看部署方案详情
- scenario.apply -> 批量部署方案内所有模型

## 集成
- openclaw(action=sync|status|claim) -> OpenClaw 集成管理
- support                            -> 连接支持平台

## 规则
- 一次调一个工具，读完结果再决定下一步
- 不要猜参数值——先调 list 类工具获取可用名称
- deploy.apply 始终需要用户审批，展示计划后等待确认
- 审批确认词：approve/yes/ok/批准/同意/确认/可以/好的/执行吧/部署吧
- 如果工具返回错误，不要用相同参数重试，换个思路
- 2-5 次工具调用后给出答案，不要无进展地持续调用

## 安全
- 被阻止的工具：model.remove, engine.remove, deploy.delete（Agent 不可直接调用）
- 需审批的工具：deploy.apply 返回审批 ID，必须用户确认后才调 deploy.approve
- 所有工具调用记录在 audit_log
```

## 6. 实现范围

### 6.1 文件影响矩阵

| 层 | 文件 | 改动量 | 说明 |
|----|------|--------|------|
| Tool 定义 | `internal/mcp/tools_*.go` | 重写 | 重组为按新命名空间一个文件 |
| Tool 依赖接口 | `internal/mcp/tools_deps.go` | 中等 | 删除废弃字段，ToolDeps 内部字段可保留细粒度 |
| Profile 机制 | `internal/mcp/tools.go` + `server.go` | 中等 | 新 profile 定义 + ListToolsForProfile() |
| 依赖布线 | `cmd/aima/tooldeps_*.go` | 中等 | 合并工具做 action 分发调用已有实现 |
| Agent 核心 | `internal/agent/agent.go` | 小 | WithProfile + Ask() 用 profile 过滤 |
| Agent Prompt | `internal/agent/prompt.md` | 重写 | 匹配 ProfileOperator 39 个工具 |
| CLI | `internal/cli/*.go` | 中等 | 匹配新工具名，删除/新增命令 |
| 主入口 | `cmd/aima/main.go` | 小 | Agent/Explorer 创建时传 Profile |
| 测试 | `*_test.go` | 中等 | 跟随改名 |
| 文档 | CLAUDE.md, MEMORY.md | 小 | 更新工具数 |

### 6.2 不变层

底层业务逻辑完全不动：

- `internal/hal/` — 硬件检测
- `internal/knowledge/` — Resolver / Store / Query
- `internal/runtime/` — K3S / Docker / Native
- `internal/state/` — SQLite schema 和 DB 方法
- `internal/benchmark/` — Runner
- `internal/agent/patrol.go` — Patrol 循环逻辑
- `internal/agent/explorer*.go` — Explorer 逻辑
- `internal/central/` — Central server
- `catalog/*.yaml` — YAML 知识库

### 6.3 合并工具的内部实现模式

合并后的 MCP 工具在 handler 内做 action 分发，调用已有的 ToolDeps 函数。ToolDeps 内部字段保留细粒度（不合并），只有外部暴露的 MCP 工具合并。

示例 — patrol 工具：

```
注册层：一个 MCP 工具 "patrol"，action 参数路由
  action=status  -> deps.PatrolStatus(ctx)
  action=alerts  -> deps.PatrolAlerts(ctx)
  action=config  -> deps.PatrolConfig(ctx, params)
  action=actions -> deps.PatrolActions(ctx, limit)
```

这种模式保证：
- `tooldeps_*.go` 中的实现函数不需要合并
- 改动范围限制在 MCP 工具注册层
- 分发逻辑简单且可测试

### 6.4 文件重组方案

```
internal/mcp/
  server.go              # 改：新增 ListToolsForProfile()
  tools.go               # 改：新 profile 定义
  tools_deps.go          # 改：删除废弃字段
  tools_hardware.go      # 不变 (2 tools)
  tools_model.go         # 小改：删 download.list (6 tools)
  tools_engine.go        # 小改：删 engine.plan (6 tools)
  tools_deploy.go        # 小改：dry_run 吸收 generate_pod (8 tools)
  tools_benchmark.go     # 不变 (4 tools)
  tools_system.go        # 重写：只保留 system 2 个
  tools_knowledge.go     # 重写：25 -> 6
  tools_catalog.go       # 新建 (3 tools)
  tools_central.go       # 新建 (3 tools)
  tools_data.go          # 新建 (2 tools)
  tools_agent.go         # 重写：agent 3 + support 1
  tools_automation.go    # 新建：patrol + explore + tuning + explorer (4 tools)
  tools_fleet.go         # 新建 (2 tools)
  tools_scenario.go      # 新建 (2 tools)
  tools_openclaw.go      # 新建 (1 tool)
  tools_stack.go         # 新建 (1 tool)
  tools_integration.go   # 删除（已拆散到上述文件）
```

### 6.5 实施阶段

| Phase | 内容 | 验收标准 |
|-------|------|---------|
| P1 | 新建/重写 `tools_*.go`，更新 ToolDeps，更新 Profile 定义 | `go build` 通过 |
| P2 | 更新 `tooldeps_*.go`，合并工具做 action 分发 | `go build` + `go test ./internal/mcp/...` 通过 |
| P3 | `server.go` 新增 `ListToolsForProfile()`，`agent.go` 新增 WithProfile | `go test ./internal/agent/...` 通过 |
| P4 | 重写 `prompt.md` | Agent e2e 验证 |
| P5 | 更新 `cli/*.go`，删除废弃命令 | CLI smoke test 通过 |
| P6 | 删除旧文件，更新测试和文档 | `go test ./...` 全绿 |

每个 Phase 结束后应能编译通过。P2 结束后 MCP 工具可用。P5 结束后 CLI 可用。
