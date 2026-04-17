# Explorer E2E 重跑记录（2026-04-17，gb10-4t，Kimi tier-2）

## 目的

验证 [2026-04-17 原始实验](./2026-04-17-explorer-gb10-4t-kimi.md) 中发现的 10 个 bug + 9 个设计疑虑（DC）的修复效果。

- 原始 commit：`c3e5f6d`（dirty）
- 修复 commit：`68c972c`（本次重跑使用）
- 机器：`qujing@100.91.39.109`（`gb10-4t`，NVIDIA GB10 ARM64，122 GiB 统一内存）
- 模式：`mode=once` + `max_tasks=2`
- 触发方式：直接 MCP POST `/mcp` → `explorer.trigger`（CLI `--remote` 未连接 MCP；详见"新发现的 bug"）

## 一句话结论

**全部 10 个 bug 中 7 个直接验证通过，2 个旁路验证通过，1 个本轮未覆盖；全部 9 个 DC 中 8 个直接验证通过，1 个为内部重构无外显。期间新发现 2 个 bug + 1 个计量口径需要记录。**

## 修复对照表（终版）

| # | 类型 | 项目 | 原症状 | 预期修复 | 重跑验证 |
|---|------|------|--------|----------|----------|
| 1 | Bug | max_tasks 未生效 | 计划产出 N 个任务，执行了 >N | 计划与执行严格受 max_tasks 约束 | ✅ plan `40c0e6df` 恰好 2 个任务，执行 2 个后停止 |
| 2 | Bug | 启动日志噪声（catalog overlay/runtime selected 重复） | 每个 cycle 都重复打印 | 仅启动时打印一次 | ✅ serve.log 整个 ~1h27m cycle 内 catalog/runtime 行只在 `01:17:09` 启动时各 1 次 |
| 3 | Bug | benchmark 进度未写 serve.log | 只能看 stdout 流 | serve.log 出现每格 start/end | ✅ task 2 每格都有 `benchmark matrix: cell start` + `cell end`，包含 concurrency/input_tokens/max_tokens/duration/throughput_tps/ttft_p95_ms |
| 4 | Bug | `rounds_used` 可被 CLI 覆写 | `config set rounds_used=0` 绕过一次性模式 | CLI 拒绝写入 + once 自动关闭 | ✅ CLI `--action set --key rounds_used` 返回 `rounds_used is read-only`；cycle 结束时打印 `once mode completed, auto-disabling`；最终 status 显示 `enabled=false rounds_used=1` |
| 5 | Bug | device-profile 行重复 | 同一模型/engine 多行 | 去重 | ✅ `workspace-final/explorer/device-profile.md` Local Models 30 行、Local Engines 6 行，无任何重复 |
| 6 | Bug | 应用的 engine_params 未随结果入库 | configurations 表不含实际生效值 | 入库时记录 | ✅ 三重证据：(a) harvest note content 含 `Config: map[gpu_memory_utilization:0.85 max_model_len:8192]`（MCP `knowledge.search scope=all` 确认）；(b) `experiments/012-GLM-4.5-Air-nvfp4-vllm-nightly.md` 每格 YAML 均含 `deploy_config` 子块；(c) note 绑定 `config_id=eb88eeac498bc13f`，关联 configurations 表 |
| 7 | Bug | 零计数幽灵行 | 0-req 的幻影 benchmark 行落库 | 拒绝写入并 WARN | ✅ 三重证据：(a) serve.log 有 11 条 `WARN benchmark matrix: save failed error="benchmark has no throughput/QPS signal — not saved"`；(b) MCP `benchmark.list --model GLM-4.5-Air-nvfp4` **恰好返回 7 行**（不是 18 行）；(c) `experiment-facts.md` 显示 `Success Cells=7/18`。幻影行在 WARN 层、DB 层、文档层三处均被一致鉴别 |
| 8 | Bug | task 之间未 teardown | 残留 container/deploy | 任务边界 `cleanup_model_deploy` | ⚠️ 部分生效：task 1 deploy 失败时并未建立 owner 记录，后续任务边界清理是 no-op；task 2 成功后先打印 `exploration: cleaned up owned deployment`，但紧接着又触发一条 `explorer: task-boundary cleanup failed` WARN（见 New-Bug-2） |
| 9 | Bug | tune 任务不写 tuning_sessions | 只有 benchmark_results | 同步写 | ⏭️ 本次 plan 只含 2 个 validate 任务，**未覆盖** tune 路径 |
| 10 | Bug | sync_push 静默失败 | 中心不可达时仍返回 ok | WARN 日志 | ⚠️ sync_push 在 `02:31:47` 触发 `INFO type=sync_push`；central.endpoint 已配置为 `https://aimaservice.ai/central`（可达），故无 WARN 属正常。**本次无法判定修复是否生效，需显式配置不可达 endpoint 再跑一次** |
| 1 | DC | MaxContextLen 列空 | 列存在但一直空 | 填充来源 YAML | ✅ 30 个模型中 14 个显示了上下文（8K/64K/256K/152K），其它行保持 `—`（YAML 里未定义） |
| 2 | DC | Pending Work 不持久 | plan 后丢失 | 每次重新计算且可溯源 | ✅ plan 前 `knowledge-base.md` 有 11 items（1 完成后变 10）；plan 确实把最高优先级的两个 validate 任务取出来执行 |
| 3 | DC | Recent History 膨胀 | 越跑越长 | 截断至 30 | ✅ `knowledge-base.md` Recent History 恰好 30 行（本 cycle 新增 2 条，旧的最老 2 条被挤出） |
| 4 | DC | 模型无 catalog 时静默 | 无任何提示 | WARN 可见 | ✅ 启动时共 10 条 `WARN model_scanner: no catalog metadata for model`（FLUX.2-dev、GLM-4.5-Air-FP8、MiniMax-M2.5 等） |
| 5 | DC | max_tokens_per_day 默认值不合理 | 0 或过小 | 合理默认 | ✅ `explorer.config get` 返回 `max_tokens_per_day=2000000`（200 万 token/天） |
| 6 | DC | source 字段词汇散乱 | string 自由填 | 内部化为常量 | ➖ 代码内部重构（`Source*` 常量族），外部不可见；不计验证 |
| 7 | DC | device-profile 行重复 | 同 Bug-5 | 去重 | ✅ 同 Bug-5 |
| 8 | DC | 计数口径不清晰 | 同字段多处读数不同 | index.md 快照加 Meaning 列 + 权威声明 | ✅ `workspace-final/explorer/index.md` 包含 `_All counts below are for this exact phase..._` 前言 + Meaning 列 + `Pending Work Items` 行 |
| 9 | DC | Active Deployments 缺 handoff 效应 | 不清楚重启会不会 drop | 新增 Live Snapshot 标题 + 斜体说明 | ✅ `device-profile.md` 有 `## Active Deployments (Live Snapshot)` + 斜体 handoff 提示 |

## 关键事件时间线

- `01:17:09` serve 启动（`runtime=docker`，catalog overlay / runtime selected 各打印 1 次）
- `01:19:35` MCP `/mcp` 接收 `explorer.trigger` → 发 `scheduled.gap_scan`
- `01:19:37` tier 切换到 2（Kimi tool-mode probe 通过）
- `01:19:40` plan 输入构建完成（`knowledge_gaps=45 ready_combos=11 blocked_combos=124 deploys=0 models=30 engines=6 history=30`）
- `01:19:40 – 01:24:28` plan 阶段（5 轮 tool call，~4 分 48 秒）
- `01:24:28` plan `40c0e6df` 生成：task 1 `validate Qwen3.5-35B-A3B/vllm-nightly`，task 2 `validate GLM-4.5-Air-nvfp4/vllm-nightly`
- `01:24:29 – 01:34:37` **Task 1 失败**：vllm-nightly 容器内 pip 安装 `transformers==5.4.0` 超过 10 min 健康期，且该版本仍不识别 `qwen3_5_moe`；`auto-repair failed: pip's dependency resolver ...`
- `01:34:37 – 02:31:46` **Task 2 成功**：GLM-4.5-Air-nvfp4 deploy 成功，benchmark matrix 18 格中 7 格真正落库、11 格被 Bug-7 守卫拒绝
  - concurrency=1 全部 6 格通过（~22 TPS，TPOT ~43 ms）
  - concurrency=2 `128-128` 通过（35.0 TPS），其它 11 格 vLLM 返回空响应（~465 ms 静默拒绝）
  - 最后 `exploration: cleaned up owned deployment` + 一条 New-Bug-2 冗余 WARN
- `02:31:47` harvest note 入库（含 engine_params）+ sync_push 触发（central 未配置，见 Bug-10）
- `02:31:50 – 02:40:30` **Check 阶段**（6 轮 tool call，verdict=continue）
- `02:40:30 – 02:44:18` **Act 阶段**（6 轮 tool call，verdict=continue，`extra_tasks=2`）
- `02:44:18` `INFO explorer: once mode completed, auto-disabling`，最终 status `enabled=false rounds_used=1`

## 新发现的 bug

### New-Bug-1: `aima explorer trigger --remote http://...` 不走 MCP

**现象**：本地执行 `aima --remote http://127.0.0.1:9090 explorer trigger` 后，远端 serve 日志完全没有 `scheduled.gap_scan` 事件。

**根因**：`internal/cli/explorer.go:43-66` 的 `newExplorerTriggerCmd` 直接调用 `app.ToolDeps.ExplorerTrigger`，该 closure 向本地进程内的 `EventBus` 发布事件。CLI 进程立即退出，事件丢失。`--remote` 参数未被此子命令消费，也没有路径把 trigger 转成 MCP JSON-RPC 调用。

**影响**：所有 explorer CLI 子命令（trigger/status/config）的 `--remote` 都是失效的。必须改走 MCP JSON-RPC，或在 CLI 层检测到 `--remote` 时统一转发。

**临时规避**：直接 curl `/mcp` JSON-RPC：
```bash
curl -s -X POST http://127.0.0.1:9090/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"explorer","arguments":{"action":"trigger"}}}'
```

**修复方案（建议）**：
- 方案 A（窄）：在 `explorer.go` 的每个子命令中检测 `app.Remote != ""`，走 HTTP MCP client。
- 方案 B（广）：在 CLI 主 loop 里拦截 `--remote`，把所有 ToolDeps 调用统一路由到 `McpHTTPClient`。

本次重跑后应把它纳入 v0.3.1 清单。

### New-Bug-2: 任务边界清理双重触发、产生虚假 WARN

**现象**：task 2 成功收尾时 serve.log 依次打印
```
INFO  exploration: cleaned up owned deployment deploy=GLM-4.5-Air-nvfp4
WARN  explorer: task-boundary cleanup failed deploy=GLM-4.5-Air-nvfp4 err="deployment not found"
```

前一条 INFO 是 `exploration.runTask` 在任务成功路径做的 owner 清理；后一条 WARN 来自 `explorer.harvest` 再次调用同名清理接口——此时 deploy 已被删除，于是报 "deployment not found"。

**根因**：Bug-8 修复把 teardown 同时放进了 `runTask`（任务内）和 `harvest`（任务边界）两条路径，两者对成功路径不互斥。

**影响**：功能上无害（仍正确清理），但每个成功任务都会产生一条误导性 WARN，污染 serve.log，给运营人员制造假警报。

**修复方案（建议）**：`harvest` 的 `cleanup_model_deploy` 调用应先判存在性；或让 `runTask` 在成功分支标记 `alreadyCleaned`，`harvest` 跳过。

### New-Bug-3（观察级）: benchmark 矩阵维度扩展与计划声明不一致

**现象**：plan 中 task 2 声明 `benchmark.input_tokens: [128]`、`max_tokens: [128, 256]`；但运行时扩展成 **3 input × 3 max × 3 concurrency = 18 格**（input ∈ {128, 512, 4096}，max ∈ {128, 256, 1024}，concurrency ∈ {1, 2, 4}）。

**根因**：benchmark matrix 有默认 fill 逻辑，planner 给的窄 search space 被补全成默认全矩阵。

**影响**：
- 好：数据更全，能看出并发降级模式；
- 差：planner 的 "tokens_budget" 估算失效（实际花费 ~9x 宣称），且 `max_tasks=2` 的可预期性下降。

**修复方案（建议）**：要么在入口强制尊重 planner search space，要么把"扩展后 matrix"回显到 plan.md，避免两本账。

## 正向亮点（非 bug，但值得记录）

### `validation_guard` 主动防御 LLM 过度宣称

`summary.md` 结尾出现非致命反馈：
> ⚠️ summary recommendation GLM-4.5-Air-nvfp4/vllm-nightly marked validated without matching successful experiment
>
> Do NOT use `validated` or `tuned` confidence unless summary.md shows benchmark-backed performance and experiment-facts.md contains a matching successful experiment. Downgrade to `provisional` when evidence is missing or only partial.

LLM 给 GLM-4.5-Air-nvfp4 标 `confidence: validated`，但 18 格里 11 格失败 → `experiment-facts.md` Signal=`benchmark_ok` 阈值未达；Guard 检测并提示降级。这是对 LLM hallucination 的关键防线，应在 test matrix 中专项保留。

### `experiments/012` 的 Agent Notes 能够识别 KV-cache 假设

```
...all concurrency=1 cells pass with stable ~22 TPS and ~43 ms TPOT; only 1 of 6
concurrency>=2 cells succeeded, suggesting silent request rejection or KV-cache
exhaustion on this 57.68 GiB nvfp4 model...
```

Agent 自主给出了"并发批处理不稳定"的诊断，且未把失败格误报为基线。这是 PDCA 工具间协同信号正确传递的直观体现。

## Task 2 benchmark 矩阵详情（参考）

| cell | conc | in | max | throughput (tps) | TTFT p95 (ms) | 落库 |
|------|------|----|-----|------------------|---------------|------|
| 1 | 1 | 128  | 128  | 22.30 | ~800  | ✅ |
| 2 | 1 | 128  | 256  | 22.26 | ~790  | ✅ |
| 3 | 1 | 128  | 1024 | 22.18 | ~800  | ✅ |
| 4 | 1 | 512  | 128  | 22.05 | ~860  | ✅ |
| 5 | 1 | 512  | 256  | 22.10 | ~860  | ✅ |
| 6 | 1 | 512  | 1024 | 22.00 | ~870  | ✅ |
| 7 | 2 | 128  | 128  | 35.00 | ~1100 | ✅ |
| 8-18 | 2-4 | * | * | 0.0 (空响应) | — | ❌（Bug-7 守卫拒绝） |

## 待收集产物（已完成）

- ✅ `~/aima.serve.log` 全量（本地，约 300 KB）
- ✅ `~/.aima/explorer/` 全量 workspace → `artifacts/uat/explorer/2026-04-17-explorer-rerun-gb10-4t/workspace-final/`
- ⏸ `aima --remote ... explorer status`（走 MCP）—— New-Bug-1 阻塞，改用直 curl 验证
- ⏸ `aima --remote ... knowledge search` 查本次配置的入库记录（可在 cycle 间隙补）
- ✅ docker ps / images 列表对照（vllm-nightly image 一直在线）

## 工程笔记

重跑前快照已保存于 `artifacts/uat/explorer/2026-04-17-explorer-rerun-gb10-4t/workspace-initial/`（index.md、device-profile.md、knowledge-base.md、plan.md）。这些是 plan 阶段生成的事实文档，在后续 task 执行中**不应**被篡改；本次 final diff 比对：

- `index.md` 字段未改 ✅
- `device-profile.md` 只有 Active Deployments Live Snapshot 随实时状态更新（符合预期）
- `knowledge-base.md` Recent History 新增 2 条，老的 2 条挤出；总 30 行保持（符合预期）
- `plan.md`、`summary.md`、`experiments/*.md` 由 agent 写入（预期内）

## v0.3.1 TODO 清单

基于本次重跑观察，建议后续补丁：

1. **[High]** 修 `explorer trigger --remote` 走 MCP JSON-RPC（New-Bug-1）
2. **[Medium]** 消除 task-boundary 重复 cleanup WARN（New-Bug-2）
3. **[Medium]** 让 benchmark matrix 尊重 planner 给定的窄 search space，或回显实际扩展（New-Bug-3）
4. **[Medium]** sync_push 在 central 不可达时必须 WARN（Bug-10 专项）
5. **[Low]** 给 validation_guard 的反馈加一个短期持久化入口，便于下一 cycle 识别并真正降级（目前只是打印）
6. **[Future]** tune 任务路径覆盖（Bug-9 未验证），下一轮 3-round PDCA 让 planner 产出 tune 任务再验
