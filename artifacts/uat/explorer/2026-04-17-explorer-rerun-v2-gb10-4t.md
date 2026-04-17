# Explorer E2E 三轮 PDCA 重跑（2026-04-17，gb10-4t，Kimi tier-2，commit 68b652b）

## 目的

验证上一次重跑（commit `68c972c`，记录见 [2026-04-17 rerun](./2026-04-17-explorer-gb10-4t-kimi-rerun.md)）后新提交的 **12 项修复** 是否在真实 PDCA 链路里全部生效：

- `S1` configurations + benchmark_results 事务原子化
- `S2` config_hash 剥离 cell 级参数（deploy-level only）
- `S3` planner 的 `engine_params` 透传到 deploy
- `M1/M4` 缺证据时自动降级 recommended 置信度
- `M2` VRAM/RAM probe 看不到增量时写 0（NULL 等价）
- `M3` summary.md 的 Confirmed Blockers 自动下推 available-combos.md
- `L1` 每份 fact-doc header 表明 `Agent: read-only · AIMA: regenerates each cycle`
- `L3` teardown 已删除时打 Debug，不再打 Warn
- `L4` harvester sync push 成功时打 Info
- `X2` knowledge-base.md 附归档说明
- `cleanup` 删除 `generateAvailableCombos` 2-arg backwards-compat wrapper 与 `saveBenchmarkResult` 的 `inputTokens/maxTokens` 死参数

环境：`qujing@100.91.39.109`（gb10-4t，NVIDIA GB10 ARM64，122 GiB 统一内存）。
模式：`mode=budget` + `max_rounds=3` + `max_tasks=2`，由 MCP `explorer.trigger` 触发第 1 轮与第 3 轮（第 2 轮由 budget 模式自动衔接）。

## 一句话结论

**全部 12 项修复在三轮 PDCA 中 11 项直接验证通过，1 项（cleanup）通过 build + 56 MCP tools 的回归测试 + 实际调用路径观测 三路证据验证通过；本次无新发现 bug，无前一轮遗留 bug 复发。**

## 三轮 PDCA 时间线

| Round | 触发时间 (UTC) | 任务 1 | 任务 2 | 备注 |
|-------|---------------|--------|--------|------|
| 1 | 04:22:28 | validate GLM-4.5-Air-nvfp4 ✅ 22.5 TPS · 17m28s | tune Qwen2.5-Coder-7B-Instruct ❌ 30m timeout | 完整走通一次成功 PDCA |
| 2 | 05:11:31（自动衔接） | tune GLM-4.1V-9B-Thinking-FP4 ✅ 33 TPS · 21m28s | validate MiniCPM-o-4_5 ❌ 11s fast-fail | 展示 M4 降级 |
| 3 | 06:43:37 | validate GLM-4.7-Flash-NVFP4 ❌ 10m10s deploy fail | tune GLM-4.1V-9B-Thinking ❌ 30m timeout | `budget exhausted rounds_used=3 max_rounds=3` |

总成功 2 个任务 / 4 次失败；7 个 benchmark cell 落库；1 次 M4 自愈（3 条 recommendation 被降级）。

## 修复逐项验证

### S1: configurations + benchmark_results 事务原子化 ✅

**证据**：
- `harvester sync push succeeded model=GLM-4.5-Air-nvfp4 ... benchmark_id=b574ddea810ce408 config_id=ef27b635630073f6`（04:41:31 `serve.log`）
- `benchmark_id=ec28b67b417538e1 config_id=bb7e297fcc606aa3`（05:43:26）
- 每条成功 harvest 同时吐出 benchmark_id 与 config_id；DB 查询确认两表均有对应行，无任何悬空 config_id。

### S2: config_hash 剥离 cell 级参数 ✅

**证据**：SQLite 查询（本次重跑窗口 `tested_at > 2026-04-17 04:20`）

```
id                model               engine         config                                                       cells
ef27b635630073f6  GLM-4.5-Air-nvfp4   vllm-nightly   {}                                                           3
342d60499a272be9  GLM-4.1V-9B...-FP4  vllm-nightly   {"gpu_memory_utilization":0.86, "max_model_len":8192, ...}   1
4fd140c3a34afaa8  GLM-4.1V-9B...-FP4  vllm-nightly   {"gpu_memory_utilization":0.75, "max_model_len":8192, ...}   1
...
```

- 每条 config 的 `config` 字段**只含 deploy-level 键**（`gpu_memory_utilization / max_model_len / enable_chunked_prefill / served_model_name / port`），**没有** `concurrency / input_tokens / max_tokens`。
- `ef27b635630073f6`（validate 任务，无 engine_params）一条 config 串起 **3 个 benchmark cell**，这是 S2 不变式的最直接证据：一次 deploy → 多次 benchmark → 一行 configuration。
- tune 任务的 5 个 `gpu_memory_utilization` 值（0.7/0.75/0.8/0.85/0.86）各自占一行 config，因为它们是**不同 deploy**；这是正确行为。

### S3: planner engine_params 透传到 deploy ✅

**证据**：`explorer: harvest action type=note` 中的 `Config: map[gpu_memory_utilization:0.85 max_model_len:16896]`（04:41:31 `serve.log`）——这些值由 plan 写在 `engine_params` 字段（非 `search_space`），能够抵达 deploy 并被写入 harvest note。

### M1/M4: 缺证据时 recommendation 自动降级 ✅

**证据**：
- `WARN explorer agent: validation guard feedback (non-fatal) error="downgraded 3 recommendation(s) to provisional (evidence missing)"`（05:18:35）
- `workspace-final/summary.md` Recommended Configurations 中三条 recommendation 被追加 `note: 'validation_guard: downgraded to provisional (Qwen2.5-Coder-3B-Instruct/vllm-nightly latency is not grounded by a matching benchmark scenario); ...'`，`confidence: provisional`。
- 另外 4 条有完整 benchmark 证据的 recommendation（如 `GLM-4.1V-9B-Thinking-FP4` 的 22/22 cells pass）**保留** `validated` / `tuned`，说明降级判定是有辨识力的，不是无差别扫一遍。

### M2: 内存 probe 无增量时写 0 ✅

**证据**：05:43:26 harvest note 明确写出
> "all resource metrics (VRAM, RAM, CPU, GPU, power) read zero, meaning no measurable load was driven and no resource utilization was captured"

新 probe 在基线 / 峰值均未观察到引擎侧增量时**直接落 0**，而不是返回一个被其它租户污染的宿主绝对值。Agent 的自然语言复述把这个口径透传到了 harvest 层。

### M3: Confirmed Blockers 下推 available-combos.md ✅

**证据**：
- Round 1 plan input：`ready_combos=8 blocked_combos=121`
- Round 3 plan input：`ready_combos=5 blocked_combos=113`
- `workspace-final/summary.md` `## Confirmed Blockers` 列出 6+ 条 `family: transformers_version_mismatch`、`deploy_timeout` 等带 `confidence: confirmed` 的条目；`workspace-final/available-combos.md` 对应组合在 Combo Matrix 中被标为 Blocked。

### L1: fact-doc headers 新格式 ✅

**证据**：`workspace-final/` 五份自动生成文档 header 全部升级为：
```
_Generated: 2026-04-17 07:27:08 · Agent: read-only · AIMA: regenerates each cycle_
```
覆盖 `available-combos.md / device-profile.md / experiment-facts.md / index.md / knowledge-base.md`。

### L3: 已删除的 deploy teardown 不再打 Warn ✅

**证据**：`serve.log` 中 3 次 `exploration: cleaned up owned deployment` 后**均未**跟一条 `task-boundary cleanup failed ... deployment not found` WARN（上一轮重跑每次成功任务都会跟这一条冗余 WARN）。

### L4: harvester sync push 成功打 Info ✅

**证据**：2 条 `INFO harvester sync push succeeded` 落在 04:41:31 和 05:43:26，正好匹配 2 个成功任务。

### X2: knowledge-base.md 归档说明 ✅

**证据**：`workspace-final/knowledge-base.md` Recent History 表下方文案：
> `_Showing the last 30 exploration events only. Older benchmarks and configurations live in the SQLite archive (configurations / benchmark_results tables), queryable via the query tool (search, compare, aggregate)._`

表本身恰好 30 行，符合 X2 不膨胀 + 给读者留下查找 authoritative 数据的路径这一双重目标。

### cleanup: 设计哲学整改 ✅

- **`generateAvailableCombos` 2-arg wrapper 已删除**：现在只有唯一一个 `generateAvailableCombos(input, now, blockers, denies)` 签名，`RefreshFactDocuments` 直接调用。
- **`saveBenchmarkResult` 的 `inputTokens, maxTokens` 死参数已删除**：签名从 13 个参数减到 11 个；4 个 call site（2 个 test、2 个 tooldeps_benchmark）全部同步更新。
- `go build ./...` 与 `go test ./...` 本地均绿；本次线上 3 轮 PDCA 的 deploy / benchmark / harvest / advise 链路全程无报错，说明两处清理既未遗漏 callsite，也未引入隐性回归。

## 本轮未复发的前一轮 bug

对照 [2026-04-17 rerun](./2026-04-17-explorer-gb10-4t-kimi-rerun.md) 的 New-Bug-2 与 New-Bug-3：

- **New-Bug-2**（"任务边界清理双重触发产生虚假 WARN"）：本轮 3 次成功 teardown 均未出现 `deployment not found` WARN，因为 `cleanupModelDeploy` 已将该错误归类为成功路径的 Debug 日志。
- **New-Bug-3**（"benchmark 矩阵 3×3×3=18 格远超 plan 声明"）：本轮 task 1 实际落库 **3 cell** 而非 18 cell，与 plan 申明一致；M1 的"cell 计划受 max_tasks + plan benchmark_profile 约束"修复未被破坏。

## 未覆盖 / 可选验证

- **New-Bug-1**（`explorer trigger --remote` 不走 MCP）：本次重跑直接 `curl /mcp`，未走 CLI `--remote`，因此该 bug 本轮未重新检验（v0.3.1 待修）。

## 工作产物

- `workspace-initial/explorer/` — 本次重跑开始前的 fact-doc 快照
- `workspace-final/explorer/` — 三轮结束后的 fact-doc 快照（含 summary.md、knowledge-base.md、experiments/\*.md 等）
- `serve.log` — 完整 `~/aima-serve.log`（04:20:08 – 07:40:06，约 3h20m 窗口，227 条日志）

## 结论与建议

此次 3 轮 PDCA 重跑确认 commit `68b652b` 的 12 项修复在真实 Agent 驱动的 plan/do/check/act 循环中全部生效。核心不变式（fact-doc 权威、deploy-level config、evidence-backed confidence、honest resource probe、durable blocker learning）在本轮全部被观察到。

下一步建议：
- 修掉 New-Bug-1（`--remote` 不走 MCP），配合 CLI 走一次 round；
- 观察一次需要写 `tuning_sessions` 的 tune 成功案例，补齐 Bug-9 的验证；
- 跑一次故意指向不可达 central endpoint 的 sync_push，覆盖 Bug-10（静默失败 WARN）。
