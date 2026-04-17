# Explorer Coverage/Tuner 修复计划

## 目标

按 `docs/superpowers/specs/2026-04-16-explorer-coverage-and-tuner-design.md` 收口三个根因：

1. combo-complete 误当作 work-complete
2. validate 长上下文 coverage 缺失
3. tune 没有真实 search-space 契约

## 范围

仅修改现有 explorer / exploration / tuner / workspace 链路：

- `internal/agent/explorer_planner.go`
- `internal/agent/explorer.go`
- `internal/agent/explorer_agent_planner.go`
- `internal/agent/explorer_workspace.go`
- `internal/agent/exploration.go`
- 相关测试

不新增表，不做 schema migration。

## 分阶段实施

### Phase 1: 派生 PendingWork

目标：从 durable local facts 推导“还缺什么工作”。

改动：

- 在 `PlanInput` 增加 `PendingWork`
- 在 `buildPlanInput()` 中：
  - 读取本地 `configurations`
  - 读取对应 `benchmark_results`
  - 读取 `exploration_runs`
  - 推导每个 Ready combo 的 pending work

验收：

- 一个已有 baseline、但没有 long-context evidence 的 combo，会得到 `validate_long_context`
- 一个已有 baseline 且有 tunable params、但没有 tune history 的 combo，会得到 `tune`

### Phase 2: frontier 和 dedup 改成 pending-work aware

目标：completed combo 只在 work 全部完成时才退出 frontier。

改动：

- 去掉“只要 completed 就 dedup”的硬逻辑
- dedup / taskAllowed 依据改为：
  - structural blocker
  - fail-count blocker
  - 没有 pending work 的 completed combo

同时更新：

- `available-combos.md`
- `knowledge-base.md`
- `Frontier Coverage`

验收：

- combo 首次完成后，如果仍有 pending work，仍出现在 Ready frontier
- `knowledge-base.md` 出现 `## Pending Work`

### Phase 3: validate 覆盖补长上下文 anchor

目标：validate 默认 profile 不再天然止步于 8K。

改动：

- 用任务参数或 `LocalModel.MaxContextLen` 推导有效 context ceiling
- 改造 benchmark profile enrichment：
  - 保留现有短/中 profile
  - 在 ceiling 足够大时补一个长上下文 anchor

验收：

- 对长上下文模型，默认 validate profile 至少出现一个 `>8192` 的 input point（若 ceiling 允许）
- 不把矩阵膨胀成完整 ladder

### Phase 4: tune search-space 契约

目标：`kind=tune` 真正代表参数搜索。

改动：

- `TaskSpec` 新增 `search_space`
- plan parser / workspace 文档 / planner prompt 同步更新
- Explorer 执行 `tune` 时：
  - 优先使用 `search_space`
  - 空时再 fallback 到 `Tuner.defaultParameters()`

验收：

- planner 生成的 `tune` task 能带多值 `search_space`
- `ExplorationManager.executeTune()` 收到的参数列表不是单值伪 search space

### Phase 5: RulePlanner/Tier1 一并收口

目标：不仅 Tier2 生效，Tier1 规则规划也能继续工作。

改动：

- RulePlanner 在 `gaps/open_question/advisory` 之外，优先吃 `PendingWork`
- baseline / long-context 用 validate
- tune debt 用 tune

验收：

- 无 LLM 时，Tier1 也不会在 combo 首次成功后过早退出

## 测试计划

### 单元测试

- `buildPlanInput()`：
  - completed combo + pending long-context → 不被 skip
  - completed combo + no pending work → 被 skip
  - baseline success + tunables + no tune history → 生成 `tune`

- `adaptBenchmarkProfiles()`：
  - `MaxContextLen=32768` 时补出 `>8192` anchor
  - 小模型不额外膨胀

- `TaskSpec`：
  - 解析/序列化 `search_space`

- `workspace`：
  - `available-combos.md` 显示 pending work
  - `knowledge-base.md` 生成 `## Pending Work`

### 集成测试

- Tier2 planner：
  - 长上下文 debt 可继续生成 validate task
  - tune debt 可生成带 `search_space` 的 tune task

- execute path：
  - tune task 的多值 search space 进入 `ExplorationManager/Tuner`

## 风险

1. 长上下文 anchor 可能增加单次 validate 成本
   - 约束：只补一个最高 anchor，不展开完整 ladder

2. pending work 推导如果过宽，会导致 frontier 过于保守地不收口
   - 约束：仅做 baseline / long-context / tune 三种 debt，不泛化

3. 旧 plan.md 样例中的 `tune + engine_params` 语义会变化
   - 处理：保留 fallback，但 prompt 和文档改成优先写 `search_space`

## 完成定义

- 文档更新完成
- 代码实现完成
- 定向测试通过
- `go test ./...` 通过
- 最终说明中明确列出 residual risk（如果有）
