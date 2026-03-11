# AIMA ZeroClaw Exploration Runner 设计稿

> 基于 develop 分支代码审计 (2026-03-10)
> 目标: 将 ZeroClaw 纳入“自主探索 -> 结构化知识 -> 后续复用”的闭环
> 状态: 提案

---

## 1. 问题定义

当前代码已经具备自主探索所需的大部分原子能力:

- `knowledge.gaps` / `knowledge.search_configs` / `knowledge.open_questions`
- `deploy.dry_run` / `deploy.apply`
- `benchmark.run` / `benchmark.matrix`
- `knowledge.promote`
- Patrol / Heal / Tuning scaffolding

但这些能力尚未被组织成一个稳定、可审计、可恢复、可跨会话复用的探索工作流。

### 1.1 当前断点

1. `L3a`/`L3b` 目前是问答式 tool loop，不是长任务执行器。
2. `tuning`、`heal` 直接调用 `deploy.apply`，默认会命中审批门控，导致“拿到 plan 但未真正执行”的假闭环。
3. `tuning_sessions`、`validation_results`、`perf_vectors` 等表存在，但未形成稳定写入链路。
4. `knowledge_note`、`configuration`、`benchmark_result`、`validation_result` 的职责边界不清。
5. `ZeroClaw` 已有 lifecycle manager，但尚未成为探索系统中的一等执行参与者。

### 1.2 设计目标

1. 让 ZeroClaw 成为 L3b 规划与长期记忆层。
2. 让 AIMA Go 仍然是唯一控制面、审计面、知识落库面。
3. 让探索任务成为持久化对象，而不是一次性对话上下文。
4. 让探索结果稳定沉淀为 `Configuration + BenchmarkResult + ValidationResult + KnowledgeNote`。
5. 不违反现有架构不变量:
   - MCP tools are the source of truth
   - 策略在 Agent，基础设施在 Go
   - 探索即知识
   - 离线可用

---

## 2. 核心决策

### 2.1 ZeroClaw 的定位

ZeroClaw 是 **L3b planner + memory sidecar**，不是普通 `app.register/app.provision` 体系里的业务应用。

它负责:

- 选择探索目标
- 生成探索计划
- 基于历史记忆调整搜索空间
- 解释实验结果
- 生成结构化 Knowledge Note

它不负责:

- 直接写 `aima.db`
- 直接修改 YAML catalog
- 直接决定某配置是否进入 resolve 链
- 绕过 AIMA 的审批、审计、回滚和安全护栏

### 2.2 AIMA Go 的定位

AIMA Go 仍然是系统唯一控制面，负责:

- ExplorationRun 状态机
- MCP 工具暴露
- deploy / benchmark / validate 的真实执行
- 知识表写入
- golden config 注入 resolve 链
- 审批和审计

### 2.3 新的一等对象: ExplorationRun

探索任务不再由 Agent 临时串 MCP 工具完成，而是作为持久化对象存在:

```text
ExplorationRun
  ├── Goal           (为什么探索)
  ├── Plan           (准备做什么)
  ├── Executor       (local-go | zeroclaw)
  ├── Steps          (deploy / benchmark / validate / note)
  ├── Artifacts      (config_id / benchmark_id / validation_id / note_id)
  └── Final Decision (promote / archive / noop)
```

### 2.4 执行模式

探索执行使用两层模式:

- **Plan mode**
  - ZeroClaw 生成计划
  - AIMA 校验计划是否合法、是否需要审批
- **Run mode**
  - AIMA 驱动状态机逐步执行
  - ZeroClaw 可在 step 间读取结果并给出下一个建议

ZeroClaw 不直接替代执行器，只驱动执行器。

---

## 3. 总体架构

```text
User / External Agent
        |
        v
   ZeroClaw (L3b)
   - planning
   - memory
   - reasoning
        |
        |  MCP: explore.start / explore.status / explore.result
        v
 AIMA Exploration Runner
   - run state machine
   - approval gate
   - audit
   - retries / rollback
        |
        +--> deploy.dry_run / deploy.apply
        +--> benchmark.run / benchmark.matrix
        +--> knowledge.search_configs / gaps / open_questions
        +--> validation writer
        +--> knowledge.promote
        |
        v
     aima.db
   - exploration_runs
   - exploration_events
   - configurations
   - benchmark_results
   - validation_results
   - knowledge_notes
```

---

## 4. 数据模型

### 4.1 新表: exploration_runs

```sql
CREATE TABLE exploration_runs (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,              -- tune | validate | open_question | fill_gap | patrol_recovery
    goal TEXT NOT NULL,
    requested_by TEXT NOT NULL,      -- user | l3a | zeroclaw | external
    executor TEXT NOT NULL,          -- local_go | zeroclaw
    planner TEXT NOT NULL,           -- none | l3a | zeroclaw
    status TEXT NOT NULL,            -- planning | needs_approval | queued | running | completed | failed | cancelled
    hardware_id TEXT,
    engine_id TEXT,
    model_id TEXT,
    source_ref TEXT,                 -- gap_id | open_question_id | alert_id | app_id ...
    approval_mode TEXT NOT NULL DEFAULT 'run', -- run | none
    approved_at DATETIME,
    started_at DATETIME,
    completed_at DATETIME,
    error TEXT,
    plan_json TEXT NOT NULL,
    summary_json TEXT
);
CREATE INDEX idx_er_status ON exploration_runs(status);
CREATE INDEX idx_er_kind ON exploration_runs(kind);
CREATE INDEX idx_er_lookup ON exploration_runs(hardware_id, engine_id, model_id);
```

### 4.2 新表: exploration_events

```sql
CREATE TABLE exploration_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL REFERENCES exploration_runs(id),
    step_index INTEGER NOT NULL,
    step_kind TEXT NOT NULL,         -- resolve | deploy | benchmark | validate | note | promote
    status TEXT NOT NULL,            -- queued | running | completed | failed | skipped
    tool_name TEXT,
    request_json TEXT,
    response_json TEXT,
    artifact_type TEXT,              -- configuration | benchmark | validation | note
    artifact_id TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_ee_run ON exploration_events(run_id, step_index);
```

### 4.3 可选扩展: exploration_locks

用于限制同一 `(hardware, engine, model)` 上同时只有一个探索任务跑，避免互相污染 benchmark。

---

## 5. Plan Schema

ZeroClaw 生成的计划必须是结构化 JSON，不能是自由文本命令。

### 5.1 ExplorationPlan

```json
{
  "kind": "tune",
  "goal": "find best throughput config for qwen3-8b on nvidia-gb10-arm64 with vllm under ttft <= 800ms",
  "target": {
    "hardware": "nvidia-gb10-arm64",
    "model": "qwen3-8b",
    "engine": "vllm"
  },
  "constraints": {
    "ttft_ms_p95_max": 800,
    "power_watts_max": 100,
    "max_candidates": 8,
    "max_duration_min": 30
  },
  "search_space": {
    "gpu_memory_utilization": [0.72, 0.76, 0.80, 0.84],
    "max_model_len": [32768, 65536]
  },
  "benchmark_profile": {
    "concurrency_levels": [1, 4],
    "input_token_levels": [128, 4096],
    "max_token_levels": [128],
    "rounds": 2,
    "requests_per_combo": 5
  },
  "stop_conditions": {
    "min_improvement_pct": 5,
    "max_failures": 2
  }
}
```

### 5.2 设计原则

- Plan 中不得出现原始 shell 命令
- Plan 中只允许白名单字段
- AIMA 在执行前必须做 schema 校验
- AIMA 必须对 search_space 做上限裁剪，避免大规模爆炸

---

## 6. MCP 工具设计

### 6.1 新增工具

#### `explore.start`

用途:

- 创建新的 ExplorationRun
- 可由用户、L3a、ZeroClaw、外部 MCP client 调用

输入:

```json
{
  "kind": "tune",
  "goal": "optimize qwen3-8b on gb10",
  "target": {
    "hardware": "nvidia-gb10-arm64",
    "model": "qwen3-8b",
    "engine": "vllm"
  },
  "planner": "zeroclaw",
  "executor": "local_go",
  "dangerously_skip_permissions": false
}
```

行为:

- 若 `planner=zeroclaw`，AIMA 先调用 ZeroClaw 生成 `plan_json`
- 计划生成后进入:
  - `needs_approval`，如果 run 包含部署/变更
  - `queued`，如果是只读验证任务

#### `explore.status`

用途:

- 查询 run 当前状态、当前 step、已产生产物

#### `explore.stop`

用途:

- 取消仍在运行的 run

#### `explore.result`

用途:

- 返回 run 汇总、步骤日志、产物 ID、推荐结论

### 6.2 扩展已有工具

#### `agent.ask`

新增建议行为:

- 当用户说“自动探索”“自己找最优”“验证 open questions”时
  - L3a 简单场景可直接建议调用 `explore.start`
  - L3b/ZeroClaw 优先转为 run-based 工作流，而不是自己串工具

#### `agent.status`

返回字段新增:

```json
{
  "zeroclaw_available": true,
  "zeroclaw_healthy": true,
  "active_exploration_runs": 1
}
```

---

## 7. Run 状态机

### 7.1 状态

```text
planning
  -> needs_approval
  -> queued
queued
  -> running
running
  -> completed
  -> failed
  -> cancelled
needs_approval
  -> queued
  -> cancelled
```

### 7.2 step 类型

```text
resolve
search_baseline
deploy_candidate
benchmark_candidate
validate_candidate
write_note
promote_candidate
resolve_open_question
rollback
```

### 7.3 关键规则

1. 一个 run 的审批是 **run 级审批**，不是每个 `deploy.apply` 单独审批。
2. run 一旦批准，AIMA 在内部受控上下文中执行 confirmable tool。
3. 所有 step 都进入 `exploration_events`。
4. 任一步失败，Runner 根据 policy 决定:
   - retry
   - skip candidate
   - rollback
   - fail run

---

## 8. ZeroClaw 与 Runner 的职责边界

### 8.1 ZeroClaw 负责什么

- 查询历史
- 生成 plan
- 根据每轮 benchmark 结果缩窄搜索空间
- 生成最终 `KnowledgeNote`
- 解释为什么 promote / 不 promote

### 8.2 Runner 负责什么

- 校验 plan
- 枚举 candidate
- 调 `deploy.apply`
- 调 `benchmark.run`
- 写 `configurations`
- 写 `benchmark_results`
- 写 `validation_results`
- 刷新 `perf_vectors`
- 执行 promote / rollback

### 8.3 禁止 ZeroClaw 直接做什么

- 直接连接 SQLite 写表
- 直接写 `~/.aima/catalog/*.yaml`
- 直接调用 shell
- 在未获 run approval 的情况下执行部署

---

## 9. 知识资产职责重构

### 9.1 Configuration

语义:

- 一个可执行配置实例
- 是后续复用的主对象

规则:

- 只有 `status = golden` 的 config 才进入 resolve 链

### 9.2 BenchmarkResult

语义:

- 某配置在特定 load profile 下的测量结果

规则:

- 每次 benchmark 保存后立即刷新 `perf_vectors`
- 每次 benchmark 保存后都可触发 validation 写入

### 9.3 ValidationResult

语义:

- “预测 vs 实测” 的可信度记录

来源:

- YAML `expected_performance`
- 现有 golden config 的参考结果
- 本次 benchmark 实测

### 9.4 KnowledgeNote

语义:

- 叙事型知识
- 给人读，也给 L3b 记忆检索

不再承担:

- 直接参与 resolve 的 config 注入

### 9.5 OpenQuestion

语义:

- 待验证假设队列

规则:

- 不用 `shell test_command` 作为主执行路径
- 建议逐步迁移为结构化验证计划:

```json
{
  "method": "benchmark",
  "target": {"model":"qwen3-8b","engine":"vllm"},
  "expected": {"metric":"throughput_tps","min":20}
}
```

---

## 10. 关键执行流

### 10.1 Tune Flow

```text
ZeroClaw:
  1. 查 baseline
  2. 生成 plan

AIMA Runner:
  3. 创建 run
  4. 审批
  5. 对每个 candidate:
       - deploy
       - benchmark
       - validate
       - append event
  6. 选 winner
  7. promote? (policy)
  8. 写 note
  9. 完成 run
```

### 10.2 Fill Gap Flow

```text
1. knowledge.gaps 找到 HW×Engine×Model 空白
2. ZeroClaw 选择最有价值的 gap
3. Runner 用默认/近邻 config 建首个 candidate
4. benchmark
5. 若通过阈值，生成 Configuration + Note
6. 可选 promote 为 golden
```

### 10.3 Open Question Flow

```text
1. knowledge.open_questions(action=list,status=untested)
2. ZeroClaw 选择一个问题
3. 生成验证计划
4. Runner 执行 benchmark / deploy / inspect
5. 写 validation + note
6. knowledge.open_questions(action=resolve)
```

### 10.4 Patrol Recovery Flow

```text
1. Patrol 发现复杂故障
2. 简单模式: Healer 直接处理
3. 复杂模式: 创建 patrol_recovery run
4. ZeroClaw 基于历史和日志给恢复计划
5. Runner 执行受控恢复步骤
```

---

## 11. 审批模型

### 11.1 当前问题

当前 confirmable tool 是按 tool 调用审批，适合问答，不适合长任务。

### 11.2 新模型

引入 **run-scoped approval**:

- `explore.start` 先生成完整计划摘要
- 用户批准一次
- Runner 拿到临时 `run_execution_token`
- token 只允许该 run 内的指定 step 执行 confirmable tool

### 11.3 token 约束

- 仅绑定一个 `run_id`
- 有效期有限
- 只能调用 plan 中声明的 confirmable 操作
- 完成/失败/取消后立即失效

---

## 12. 与现有模块的兼容策略

### 12.1 Tuning

现有 `internal/agent/tuner.go` 不再作为最终自治方案。

建议:

- 短期: 修成可用的本地搜索器
- 中期: 让 `tuning.start` 内部直接转为 `explore.start(kind=tune)`

### 12.2 Patrol / Heal

保留现有轻量自愈:

- OOM -> 降 gmu
- image_pull -> retry

复杂故障升级到:

- `explore.start(kind=patrol_recovery, planner=zeroclaw)`

### 12.3 Knowledge Validate

`knowledge.validate` 从“只读查询”升级为:

- 读取已有 validation 结果
- 可选触发新验证 run

### 12.4 App

`app.register/app.provision` 仍用于业务应用依赖声明。

ZeroClaw 不是普通 app。

如需“系统应用”登记，可新增:

- `apps.kind = system | user`

但 ZeroClaw lifecycle 仍应走 `internal/zeroclaw/manager.go`。

---

## 13. 实施顺序

### Phase 1: 修正确性

1. 修 `tuning` 结果解析、空 search_space、审批断链
2. 修 `heal` 对 confirmable deploy 的处理
3. benchmark 保存后刷新 `perf_vectors`
4. 打通 `validation_results` 写入链
5. 禁用或重做危险的 perf overlay 写法

### Phase 2: 建立 Runner

1. 新增 `exploration_runs` / `exploration_events`
2. 新增 `explore.start/status/stop/result`
3. 实现本地 Go Runner

### Phase 3: 接入 ZeroClaw

1. 定义 ZeroClaw plan prompt
2. 定义 plan schema
3. 让 `planner=zeroclaw` 可用
4. 让复杂 ask 场景自动转 run-based 工作流

### Phase 4: 覆盖场景

1. tune
2. validate
3. open_question
4. fill_gap
5. patrol_recovery

---

## 14. 验收标准

### 功能

- 用户可一句话触发探索:
  - “帮我把 qwen3-8b 在 GB10 上调到最优”
  - “把未验证的 open questions 跑一遍”
  - “补齐这个设备上的知识空白”

- ZeroClaw 可基于历史给出计划
- AIMA 可将计划落为 run 并持久执行
- Run 结束后产生:
  - `config_id`
  - `benchmark_id`
  - `validation_id`
  - `note_id`

### 数据

- 每个 run 都可追溯每一步
- 每个 benchmark 保存后都刷新 `perf_vectors`
- 每个验证任务都写 `validation_results`
- 只有 `golden` config 影响 `Resolve()`

### 安全

- ZeroClaw 不能绕过审批
- 零网络环境下仍可运行本地 runner
- 失败可取消、回滚、重试

---

## 15. 最小结论

最可行方案不是“让 ZeroClaw 直接替代执行器”，而是:

1. ZeroClaw 负责 **规划 + 记忆 + 总结**
2. AIMA 负责 **执行 + 审批 + 审计 + 落库**
3. 用 `ExplorationRun` 把自主探索变成一等对象

这样既能发挥 ZeroClaw 的 L3b 优势，又不破坏 AIMA 当前的架构不变量。
