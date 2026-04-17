# Explorer Coverage/Tuner 收敛设计

## 背景

当前 Explorer 的 frontier 主要以 `model×engine combo` 为粒度。这个粒度足以回答“这个组合能不能跑”，但不足以回答下面两个真正重要的问题：

1. 这个 combo 的关键 benchmark coverage 是否已经足够？
2. 这个 combo 是否已经完成了值得做的 tuning？

这导致当前行为在实现上自洽、在产品上却不对：

- 一个 combo 只要出现一次 `completed` exploration run，就会被 Explorer 当作“已经探索过”，从 Ready frontier 里移走。
- `knowledge.gaps` 仍然按 `Hardware×Engine×Model` 的 benchmark 数量来判 gap，和 “一次 completed 就 dedup” 发生语义冲突。
- validate 默认 profile 的长上下文覆盖止步于 `8192` input；很多大上下文模型即使能支持更长 context，也不会自动得到更高点位的 probe。
- `kind=tune` 虽然存在，但 Planner/Explorer/Tuner 之间没有一个真实的 search-space 契约。当前 `engine_params` 在 tune 任务里更像单点 override，不像真正的参数搜索。

## 根因

根因不是单个 bug，而是一个抽象层级错误：

> Explorer 把 “是否还应该继续做工作” 错误地建模成了 “这个 combo 是否已经碰过一次”。

这把三个本应分开的概念混在了一起：

- **Executable combo fact**：这个 combo 当前是否可执行。
- **Coverage debt**：这个 combo 还有哪些 benchmark obligation 没补齐。
- **Tuning debt**：这个 combo 是否还有值得做的参数搜索。

只要这三者没有分开，Explorer 就会不断在“过早终止”和“盲目重试”之间摇摆。

## 目标

1. 让 Explorer 的 frontier 语义从 “combo 完成” 升级成 “combo 的 pending work 完成”。
2. 让 validate 默认覆盖不再天然止步于 `8192`，而是至少包含一个受控的长上下文探针。
3. 让 `kind=tune` 真正代表参数搜索，而不是单点 deploy 换个名字。
4. 继续遵守项目原则：
   - 不新增服务层
   - 不新增独立状态系统
   - 不让 `summary.md` 反向驱动执行
   - 尽量用现有 durable local facts (`configurations`, `benchmark_results`, `exploration_runs`)

## 非目标

- 不在这次改动中引入新的 SQLite 表。
- 不把 benchmark obligation 建成无限细粒度的全场景矩阵计划器。
- 不在 Go 里硬编码 engine/model 家族策略。

## 设计原则

### 1. Executable facts 和 pending work 分离

`ComboFacts` 继续回答“这个 combo 现在能不能执行”。

新增一个纯派生的事实层：`PendingWork`，回答“这个 combo 现在还缺什么工作”。

`PendingWork` 不落新表，只在 `buildPlanInput()` 时由 durable facts 推导：

- `configurations`
- `benchmark_results`
- `exploration_runs`
- 本地 inventory (`LocalModel`, `LocalEngine`)

### 2. combo 不再因一次 completed 就自动退出 frontier

Explorer 不应该在看到 `HasCompletedExploration(model, engine)` 后立即把 combo dedup 掉。

新的语义是：

- **无 pending work**：combo 才算 frontier complete
- **仍有 pending work**：combo 继续保留在 Ready frontier
- **结构性失败 / blocker**：combo 进入 blocked / skipped

### 3. validate 至少建立三段覆盖心智，而不是只有短上下文

validate 不追求把整个 context ladder 全打满，但至少要覆盖：

- short baseline
- existing mid-range profile
- one bounded long-context probe

长上下文 probe 的生成规则：

- 基于 `LocalModel.MaxContextLen`
- 若任务显式设置 `max_model_len/context_length/ctx_size/max_context_tokens`，则以任务值为上限
- 仅补一个最高安全 anchor，不展开成完整 context ladder

这样仍然是“少代码、受控成本”，但不再天然停在 `8192`。

### 4. tune 必须有真实 search space 契约

为 `TaskSpec` 新增显式字段：

```yaml
search_space:
  gpu_memory_utilization: [0.75, 0.8, 0.85]
  max_model_len: [8192, 16384, 32768]
```

语义：

- `engine_params`：validate 的单点参数覆盖
- `search_space`：tune 的候选参数空间

Explorer 对 `tune` 任务不再把 `engine_params` 强行翻译成单值 search space。

### 5. tune 仍然复用现有 Tuner

不新增新的 tuning executor。

链路保持：

`Explorer -> ExplorationManager(kind=tune) -> Tuner`

本次只修正两件事：

- Planner/Workspace 能表达真实 `search_space`
- Explorer 把 `search_space` 正确传给 Tuner

## 新的派生事实：PendingWork

`PendingWork` 是 buildPlanInput 时派生出的结构化事实：

```go
type PendingWork struct {
    Model       string
    Engine      string
    Kind        string // "validate_baseline" | "validate_long_context" | "tune"
    Reason      string
    Benchmark   BenchmarkSpec
    SearchSpace map[string][]any
    Priority    int
}
```

### PendingWork 推导规则

#### A. validate_baseline

满足以下条件之一时生成：

- combo 为 Ready
- 本地不存在任何有意义的 benchmark evidence

“有意义 evidence” 的判断沿用 Explorer 现有 benchmark 成功语义，而不是只看 run status。

#### B. validate_long_context

满足以下条件时生成：

- combo 为 Ready
- 已存在 baseline benchmark evidence
- 模型的有效 context ceiling 高于当前成功覆盖上界

`validate_long_context` 只建议一个长上下文 probe，不生成完整 ladder。

#### C. tune

满足以下条件时生成：

- combo 为 Ready
- 已存在 baseline benchmark evidence
- engine 暴露 tunable params
- 当前没有已完成的 tune exploration run

`tune` 的 search space 优先来自 Go 派生的建议空间；LLM 可以删减，但不能引入非 tunable key。

## Workspace 变化

### available-combos.md

`Ready Combos` 继续保留，但 Reason 不再只是“coarse local compatibility”或空字符串，而是显示 pending work 摘要：

- `pending: baseline`
- `pending: long-context`
- `pending: tune`
- `pending: baseline,long-context`

### knowledge-base.md

新增 section：

`## Pending Work`

表格字段：

- Model
- Engine
- Work Kind
- Reason
- Suggested Benchmark / Suggested Search Space

这个 section 是 Planner 的主要输入之一，比自由文本 summary 更接近 executable truth。

## Planner 契约变化

### Plan phase

Planner 继续只能从 Ready combos 里选任务，但需要优先吃掉 `Pending Work`。

新约束：

- 若某 combo 存在 `validate_baseline` 或 `validate_long_context`，优先写 validate 任务
- 若某 combo 存在 `tune`，且 baseline 已存在，可以写 tune 任务
- tune 任务必须填写 `search_space`

### Check/Act phase

`done(verdict="done")` 的条件不再是“没有新 combo”，而是：

- 没有高价值 Ready combo 的 pending work
- 或剩余 pending work 全是环境性阻塞，继续无意义

## 执行语义变化

### validate

validate 任务的 benchmark profile 仍可来自：

- 任务显式 benchmark spec
- catalog benchmark profile

但在执行前统一经过 coverage-aware enrichment：

- 过滤不可行 `input+output > effective_max_context`
- 若有效上限显著大于当前 profile 上界，则补一个长上下文 anchor

### tune

Explorer 对 `tune` 任务的执行语义改为：

- 若 `search_space` 非空：直接传给 Tuner
- 若 `search_space` 为空：退回现有 `Tuner.defaultParameters()`

这保证：

- 旧链路不被打断
- 新链路可以表达真正的多候选搜索

## 不改的部分

- `ComboFacts` 仍是执行面真相
- `summary.md` 仍只是工作记忆 / 展示产物
- `Harvester` 仍复用当前逻辑
- `Tuner` 仍负责真正的参数枚举和 benchmark loop

## 验收标准

### 1. frontier 语义

- 一个 combo 首次 validate 成功后，只要仍有 `PendingWork`，不能从 `Ready Combos` 消失
- `HasCompletedExploration` 不能再单独决定 dedup

### 2. 长上下文覆盖

- 对 `MaxContextLen > 8192` 的模型，默认 validate 不能只留下 `<=8192` 的 input coverage
- benchmark profile 必须出现至少一个高于 `8192` 的长上下文 anchor（若有效上限允许）

### 3. tune 语义

- `kind=tune` 任务必须能携带 `search_space`
- Explorer 必须把多值 search space 原样交给 Tuner，而不是降成单值 config

### 4. planner/workspace

- `knowledge-base.md` 必须出现 `Pending Work`
- `available-combos.md` 的 Ready rows 必须反映 pending work，而不是单纯“未探索/已探索”

## 为什么这符合 CLAUDE.md / ARCHITECTURE

- 没有新增服务或新状态系统
- 没有让 Markdown 反驱动执行
- 继续以 MCP/DB/executable facts 为单一真相面
- 尽量通过文档和派生事实修正策略，而不是引入新的抽象层
- `tune` 的执行仍复用现有 Tuner，不开第二套执行器
