# Explorer E2E 三轮 PDCA 重跑 v3（2026-04-17，gb10-4t，Kimi tier-2，commit a64b901）

## 目的

验证 [rerun v2 复盘](../../../design/experiments/2026-04-17-explorer-rerun-v2-gb10-4t.md) 后在 `a64b901` 提交的 **3 项收口修复** 是否在真实 PDCA 链路里生效：

- **Fix #1 — tune timeout narrative**：`internal/agent/explorer.go:1963-1984` 在 timeout 路径也跑 `parseExplorationResult`，保留 `MatrixCells/SuccessCells`，避免 `experiment-facts.md` 里撒 cells=0/0 的谎
- **Fix #2 — engine-wide blocker propagation**：`confirmedBlockerMatches`（`internal/agent/explorer_workspace.go`）读懂 agent 自由写的 `scope: sglang on GB10` 文本，当 scope 提到 engine 但没提 model 时按整个 engine 降级
- **Fix #3 — structured decision-trace log**：把 `explorer: plan generated` 这一行扩成单条结构化 slog，带 `task_list / llm_tokens / proposed_tasks / dedup_dropped / ready_combos_seen / blocked_combos_seen / knowledge_gaps / reasoning`

环境：`qujing@100.91.39.109`（gb10-4t，NVIDIA GB10 ARM64，120 GiB 统一内存）。
模式：`mode=budget` + `max_rounds=3` + `max_tasks=2`，MCP `explorer.trigger` 三轮全部手动触发（本机 `GapScanInterval=24h`，budget 模式不会在几十分钟内自动衔接）。

## 一句话结论

**Fix #3 在三轮中 3/3 条 plan-generated 日志上直接验证通过；Fix #2 在 `available-combos.md` 上把 Ready Combos 彻底收敛为 `_None_`（零 sglang / 零 vllm-standard 泄漏）强力验证通过；Fix #1 的诚实-零分支被 experiment 019 验证通过，但 preserve-partial-cells 分支在本轮未被触发（round 1 tune 5/5 cells 全在 benchmark 前就失败，没有 cell 可保留），需要后续再跑一次有部分成功的 tune 来补齐。**

本轮无新 bug、无前一轮 fix 回归。

## 三轮 PDCA 时间线

| Round | 触发时间 (UTC) | 任务 1 | 任务 2 | 备注 |
|-------|---------------|--------|--------|------|
| 1 | 09:32:11 | tune GLM-4.5-Air-nvfp4 × vllm-nightly ❌ **30m timeout，5/5 configs 在 benchmark 前失败** | validate GLM-4.5-Air-nvfp4 × vllm-nightly ⏭️ 因 "prior deploy failure" 被规避 | 验证 Fix #1 诚实-零；Fix #2 首轮 Ready=3 |
| 2 | 10:31:09 | — | — | **空 plan**（tasks=0）。Kimi 从 Ready=2 的 2 个候选里选择不提案 |
| 3 | 11:12:39 | — | — | **空 plan**（tasks=0）。同上，`budget exhausted rounds_used=3 max_rounds=3` |

总任务数 2（round 1），0 成功；LLM token 合计 276,948（94,846 + 120,739 + 61,363）；DB 计数：`configurations 67→67, benchmark_results 72→72, exploration_runs 42→43, tuning_sessions 3→4, knowledge_notes 11→11`。

## 修复逐项验证

### Fix #1：tune timeout narrative — 诚实-零分支 ✅ / 部分保留分支 ⏸️

**证据（诚实-零）**：
- `artifacts/uat/explorer/2026-04-17-explorer-rerun-v3-gb10-4t/workspace-final/explorer/experiments/019-GLM-4.5-Air-nvfp4-vllm-nightly.md`：
  ```yaml
  status: failed
  started_at: "2026-04-17T09:36:06Z"
  duration_s: 1800.657888713
  error: exploration 52e4d30ff71cf1fd timed out after 30m0s
  ```
  后面 Benchmark Matrix 段恰如其分地写 `_No benchmark data_`，Agent Notes 里也说 "no cells were measured, no throughput or latency data can be inferred"。
- `experiment-facts.md` 里 019 的行是 `0 | 0.0 | 0/0`，和 DB 实际状况一致。

**没被触发的分支**：`executeTask` 在 `parseExplorationResult(status)` 拿到非零 `SuccessCells` 时会跨过 `err != nil` 把这部分数据保留给后续 harvester / experiment-facts，属于 Fix #1 的"保留部分成功 cell"能力。本轮 round 1 tune 5/5 configs 全部在 benchmark 前就失败（2 次 `deploy result missing ready endpoint`、1 次 `benchmark has no throughput/QPS signal`、1 次 `deploy context canceled`、1 次未完成即触发 30m 硬 cap），所以此分支无数据可保留。下一轮需要刻意跑一次能部分成功的 tune（比如单参数 sweep、缩短 max_model_len）来补齐证据。

### Fix #2：engine-wide blocker propagation ✅（强力验证）

**证据**：

- `summary.md` 里 agent 继续写出原样的自由 scope 文本：
  ```yaml
  - family: deploy_timeout
    scope: sglang on GB10
    model: GLM-4.6V-Flash-FP4
    engine: sglang
    confidence: confirmed
  - family: deploy_timeout
    scope: vllm (standard) on GB10 for Qwen models
    model: Qwen2.5-Coder-3B-Instruct
    engine: vllm
    confidence: confirmed
  ```
- `available-combos.md`（本轮 regenerate 后的快照）`## Ready Combos` **整段是 `_None_`**。既没有 sglang combo、也没有 vllm-standard combo 漏进来；round 1 跑炸的 GLM-4.5-Air-nvfp4+vllm-nightly 也被 history 规则收到 `Already Explored` 里，不占 Ready 名额。
- 三条 `explorer: plan generated` 里的 `task_list` 没有一个 task 落在 sglang 或 vllm 上：
  ```
  round 1: [tune:GLM-4.5-Air-nvfp4/vllm-nightly validate:GLM-4.5-Air-nvfp4/vllm-nightly]
  round 2: []
  round 3: []
  ```

这是比任何 whitebox test 都硬的证据：planner **压根没机会**提案到 sglang，因为 frontier 在它之前就过滤掉了。

### Fix #3：structured decision-trace log ✅

**证据**：`serve.log` 里所有三轮的 `explorer: plan generated` 行：

```
09:36:06 id=11282994 tier=2 reasoning=agent-planned tasks=2
         task_list="[tune:GLM-4.5-Air-nvfp4/vllm-nightly validate:GLM-4.5-Air-nvfp4/vllm-nightly]"
         llm_tokens=94846 proposed_tasks=2 dedup_dropped=0
         ready_combos_seen=3 blocked_combos_seen=109 knowledge_gaps=46
10:33:01 id=fafd13a7 tier=2 reasoning=agent-planned tasks=0
         task_list=[] llm_tokens=120739 proposed_tasks=0 dedup_dropped=0
         ready_combos_seen=2 blocked_combos_seen=104 knowledge_gaps=46
11:13:26 id=c42ce726 tier=2 reasoning=agent-planned tasks=0
         task_list=[] llm_tokens=61363 proposed_tasks=0 dedup_dropped=0
         ready_combos_seen=2 blocked_combos_seen=104 knowledge_gaps=46
```

- 所有 8 个结构化 key 都在（`id / tier / reasoning / tasks / task_list / llm_tokens / proposed_tasks / dedup_dropped / ready_combos_seen / blocked_combos_seen / knowledge_gaps`）
- **空 plan 分支也不崩**：round 2 / 3 的 `tasks=0 task_list=[]` 按空切片序列化为空 `[]`，没有 nil 解引用
- `dedup_dropped=0` 三轮都成立（proposed 和 final 一致，说明没有任务被 DB dedup 层挡掉）

## 前一轮 fix 全部未回归

- rerun v2 的 12 项 fix（S1/S2/S3/M1-M4/L1/L3/L4/X2/cleanup）在本轮 fact-doc header、summary.md Confirmed Blockers 段、experiment-facts.md 写入结构上继续表现一致，无一项回归（没有看到带模板残值的 label、没有 redundant teardown WARN、没有 "recently torn down" 误判）。

## 被顺带暴露的既有执行面脆弱

Round 1 tune 5/5 configs 全部在 benchmark 前失败，模式和 v2 里 experiments 014（Qwen2.5-Coder-7B）/ 018（GLM-4.1V-9B-Thinking）的 30m timeout 同构：

- 2 次 `tuning: deploy result missing ready endpoint, skipping config` — deploy 起来但 warmup readiness 未兑现
- 1 次 `tuning: benchmark failed, skipping config error="benchmark run: benchmark has no throughput/QPS signal — not saved"` — 容器活但推理没走通
- 1 次 `tuning: deploy failed, skipping config error="deploy run GLM-4.5-Air-nvfp4: context canceled"` — 30m 硬 cap 到期

这不是本次 fix 的回归，而是上一轮（commit `6fa91f6` "Honor warmup readiness and normalize served model labels"）修的问题又以略微不同的形态复现：warmup 路径在某些 tune 循环里仍然太早/太脆。Agent 在 round 2 / 3 看到仅剩 2 个 Ready combo 时直接放弃提案，这本身就是一次 agent-layer 的诚实反应。

下一步 tune-harness 深挖方向：

1. 在 `ExplorationManager.executeTune` 的 deploy 轮次里把 `cold_start_s` + warmup 目标 TPS 作为 ready gate（让 native runtime 的 warmup 语义覆盖 docker）
2. 对 vllm-nightly image 加 per-config deploy timeout 线，和 run-level 30m 硬 cap 解耦
3. 跑一次刻意小的 search space（`[0.7, 0.8]`）看能不能落 2 个 cell，同时验证 Fix #1 的 preserve-partial-cells 分支

## 工作产物

- `workspace-final/explorer/` — 三轮结束后的 fact-doc 快照（含 20 份 experiments/*.md）
- `serve.log` — 完整 `~/aima-serve.log`（09:29:04 – 11:13:26，约 1h44m 窗口，60 条日志）
- `aima.db.post-v3` — 三轮结束后的完整 SQLite 快照

## 结论与建议

- Fix #2 和 Fix #3 在真 Agent 驱动的 PDCA 闭环里直接验证通过。Fix #1 的诚实-零分支验证通过，部分保留分支需要后续补测。
- 下一个 rerun 的重点应该是：(a) 通过刻意缩小 search space 触发一次能落 cell 的 tune，补齐 Fix #1 partial-preserve 证据；(b) 顺便做 tune-harness 的 warmup/timeout 深挖。
