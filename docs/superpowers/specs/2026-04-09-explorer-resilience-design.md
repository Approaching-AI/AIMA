# Explorer 弹性与资源控制设计

> **日期**: 2026-04-09
> **状态**: 设计完成，待实现
> **范围**: 6 个改进点 (T1-T6)，覆盖 ~14 个文件
> **前置**: v0.4 Explorer postmortem (P1-P7 已修复)

---

## 一、问题全景

| ID | 问题 | 当前行为 | 风险等级 |
|----|------|----------|----------|
| T1 | waitForReady 固定 5min 超时 | 无视引擎冷启动时间和进展信号 | High |
| T2 | Explorer 无资源控制 | 启动后永不停止，每 24h 触发 gap scan | High |
| T3 | 历史去重窗口 = 全局最近 10 条 run | 老 run 滑出窗口后重复探索 | Medium |
| T4 | RulePlanner gap 排序 = 字母序 | 每次 cycle 选同一批模型 | Medium |
| T5 | 单次 plan 无时间上限 | 30min+ 独占 GPU | Medium |
| T6 | remote LLM http.Client.Timeout = 5min | 深度推理被截断 | Low |

---

## 二、T1 — Stalled 检测（替代固定超时）

### 2.1 背景

Runtime 层已有完整的进度检测基础设施：

- `DeploymentStatus.StartupProgress` (0-100) — 从引擎日志模式匹配
- `DeploymentStatus.StartupPhase` — `initializing` / `loading_weights` / `cuda_graphs` / `ready`
- `DeploymentStatus.EstimatedTotalS` — 从 engine YAML `cold_start_s[1]`
- 三种 runtime 各有 `enrich*Progress` 函数：
  - Native: `enrichNativeProgress` (`native_log.go`) — `readTail(logPath)`
  - Docker: `enrichDockerProgress` (`docker.go`) — `r.Logs(ctx, name)`
  - K3S: `enrichStartupProgress` (`k3s.go`) — `kubectl logs` + pod conditions

但 Explorer 的 `waitForReady` 完全忽略这些字段，只看 `Ready` 和 `Phase`。

### 2.2 DeploymentStatus 新增字段

```go
type DeploymentStatus struct {
    // ... 已有字段 ...
    Stalled        bool  `json:"stalled,omitempty"`          // 进度停滞
    LastProgressAt int64 `json:"last_progress_at,omitempty"` // unix seconds
}
```

### 2.3 共享 ProgressTracker

在 `runtime/progress.go` 中新增，三种 runtime 各 embed 一份实例：

```go
type progressEntry struct {
    progress     int
    lastChangeAt time.Time
}

type ProgressTracker struct {
    mu      sync.Mutex
    entries map[string]*progressEntry // key = deployment name
}
```

核心方法 `Update(name, currentProgress, estimatedTotalS) -> (stalled bool, lastProgressAt time.Time)`：

- 首次观测 → 记录基线，返回 `stalled=false`
- `currentProgress > entry.progress` → 有进展，更新时间戳，`stalled=false`
- 否则 → 计算 stall 阈值，超过则 `stalled=true`

Stall 阈值计算：

```
stallThreshold = max(90s, min(EstimatedTotalS * 0.4, 5min))
```

- 基础 90s：大多数引擎的单阶段（如 loading_weights → cuda_graphs）不会超过 90s 无进展
- `EstimatedTotalS * 0.4`：给慢引擎更大容忍窗口（sglang-kt EstimatedTotalS=230s → 阈值 92s）
- 上限 5min：防止极大值引擎（EstimatedTotalS=600s）的阈值不合理

清理方法 `Remove(name)` 在 deploy 删除时调用。

### 2.4 三种 Runtime 接入

每种 runtime struct embed `progressTracker ProgressTracker`，constructor 中初始化。

接入点在各自 `enrich*` 函数末尾，3 种 runtime 调用模式完全对称：

```go
// 仅对 starting/not-ready 状态检测
if ds.Phase == "starting" || (ds.Phase == "running" && !ds.Ready) {
    stalled, lastAt := r.progressTracker.Update(ds.Name, ds.StartupProgress, ds.EstimatedTotalS)
    ds.Stalled = stalled
    ds.LastProgressAt = lastAt.Unix()
}
```

Delete 路径中调用 `r.progressTracker.Remove(name)`。

### 2.5 Explorer waitForReady 重写

```
循环 (poll 间隔 5s):
  status = deploy.status(model)

  Ready                                     → return 成功
  Phase in {failed, stopped, error, exited} → return 快速失败
  Stalled                                   → return "stalled at {phase} ({progress}%)"
  否则                                      → 继续等待

外层安全网 = max(EstimatedTotalS * 3, 15min)
  ↑ 防止引擎无 log_patterns 时完全没有 stall 检测
```

### 2.6 特殊情况

- **引擎没配 `log_patterns`**: `StartupProgress` 始终 0，tracker 在 stallThreshold 后标记 stalled。阈值使用 `EstimatedTotalS * 0.4`；若也无 `EstimatedTotalS`，回退到 90s。
- **K3S pre-container 阶段** (scheduling/pulling_image): `DetectK3SPhaseFromConditions` 设 progress 2/10/20。Pod 卡在 ImagePullBackOff 时 phase 变 `failed`，走快速失败，不需 stall 检测。
- **progress 回退**: 理论上不发生（`DetectStartupProgress` 取最高匹配），若发生，tracker 视为"有活动"。

### 2.7 影响范围

| 文件 | 改动 |
|------|------|
| `internal/runtime/runtime.go` | `DeploymentStatus` +2 字段 |
| `internal/runtime/progress.go` | 新增 `ProgressTracker` |
| `internal/runtime/native.go` | embed tracker, constructor |
| `internal/runtime/native_log.go` | `enrichNativeProgress` 末尾 +3 行 |
| `internal/runtime/docker.go` | embed tracker, `enrichDockerProgress` 末尾 +3 行 |
| `internal/runtime/k3s.go` | embed tracker, `enrichStartupProgress` 末尾 +3 行 |
| `internal/agent/exploration.go` | `waitForReady` 重写 |

---

## 三、T2 — 资源控制系统

### 3.1 ExplorerConfig 新增字段

```go
type ExplorerConfig struct {
    Schedule ScheduleConfig
    Enabled  bool

    Mode             string        // "continuous" | "once" | "budget"
    MaxRounds        int           // budget 模式下最多执行 N 个 plan (0=无限)
    MaxPlanDuration  time.Duration // 单次 plan 时间预算 (默认 30min)
    MaxTokensPerDay  int           // 每日 LLM token 上限 (0=无限)
}
```

### 3.2 三种模式

| 模式 | 行为 | 适用场景 |
|------|------|----------|
| `continuous` | 当前行为，按 schedule 无限触发 | 长期值守的生产设备 |
| `once` | 执行一个 plan 后自动 `Enabled=false` | 手动触发一次性测试 |
| `budget` | 按 schedule 触发，`roundsUsed >= MaxRounds` 时暂停 | 控制 GPU 时间和 LLM 花费 |

`once` 典型用法：设 mode=once → trigger → 执行一个 plan → 自动 disable。

### 3.3 运行时状态

```go
type Explorer struct {
    // ... 已有 ...
    roundsUsed      int    // 当前周期已执行 plan 数
    tokensUsedToday int    // 今日已消耗 token 数
    tokenResetDate  string // "2026-04-09"，日期变更时重置
}
```

检查点在 `handleEvent` 入口：

```
if mode == "once" && roundsUsed >= 1 → 自动 disable, skip
if mode == "budget" && roundsUsed >= maxRounds → skip
if maxTokensPerDay > 0 && tokensUsedToday >= maxTokensPerDay → skip
if 日期变更 → 重置 tokensUsedToday, roundsUsed (budget 模式)
```

### 3.4 热更新

所有新 key 通过 `explorer.config` MCP tool / CLI 热更新：
`mode`, `max_rounds`, `max_plan_duration`, `max_tokens_per_day`, `rounds_used` (可重置)

### 3.5 ExplorerStatus 扩展

```go
type ExplorerStatus struct {
    // ... 已有 ...
    Mode            string `json:"mode"`
    RoundsUsed      int    `json:"rounds_used"`
    MaxRounds       int    `json:"max_rounds"`
    TokensUsedToday int    `json:"tokens_used_today"`
    MaxTokensPerDay int    `json:"max_tokens_per_day"`
}
```

### 3.6 影响范围

| 文件 | 改动 |
|------|------|
| `internal/agent/explorer.go` | config 字段、mode/budget 检查、计数器、Status |
| `cmd/aima/main.go` | `loadExplorerConfig` 加载新 key、`explorerConfigResponse` |
| `internal/mcp/tools_automation.go` | schema 描述新增 config key |

---

## 四、T6 — Token 追踪 + Streaming 超时修复

### 4.1 Wire Type 扩展

`openai.go` 新增：

```go
type chatUsage struct {
    PromptTokens     int `json:"prompt_tokens"`
    CompletionTokens int `json:"completion_tokens"`
    TotalTokens      int `json:"total_tokens"`
}
```

`chatResponse` 和 `chatStreamResponse` 均新增 `Usage *chatUsage` 字段。

### 4.2 Response 扩展

`agent.go`：

```go
type Response struct {
    // ... 已有 ...
    PromptTokens     int `json:"prompt_tokens,omitempty"`
    CompletionTokens int `json:"completion_tokens,omitempty"`
    TotalTokens      int `json:"total_tokens,omitempty"`
}
```

### 4.3 解析逻辑

- **非 streaming** (`decodeChatResponse`): 从 `chatResp.Usage` 拷贝到 `Response`
- **Streaming** (`ChatCompletionStream`): OpenAI 兼容 API 在最后一个 chunk 返回 usage（需请求时设 `stream_options.include_usage=true`）。解析最后一个带 usage 的 chunk。
- **Provider 不返回 usage**: 字段为 0，token 追踪退化为 `estimatePromptTokens` 估算

### 4.4 Streaming 超时修复

`ChatCompletionStream` 发起请求前临时将 `httpClient.Timeout = 0`（streaming 不应有固定超时），完全依赖调用方 `ctx` cancellation（如 idle timer）。请求结束后恢复原值。

```go
func (c *OpenAIClient) ChatCompletionStream(ctx context.Context, ...) (*Response, error) {
    origTimeout := c.httpClient.Timeout
    c.httpClient.Timeout = 0
    defer func() { c.httpClient.Timeout = origTimeout }()
    // ... 其余不变 ...
}
```

### 4.5 Explorer Token 累加

LLM 调用点只有 2 处：
1. `LLMPlanner.Plan()` → 回传 `resp.TotalTokens`
2. `Harvester.generateLLMNote()` → 回传 `resp.TotalTokens`

通过 `TokenCallback func(tokens int)` option 注入 Explorer，每次 LLM 调用后 callback 累加到 `tokensUsedToday`。

### 4.6 影响范围

| 文件 | 改动 |
|------|------|
| `internal/agent/openai.go` | wire type +usage, 解析, streaming timeout fix |
| `internal/agent/agent.go` | `Response` +3 字段 |
| `internal/agent/explorer_llmplanner.go` | Plan() 后回传 token count |
| `internal/agent/explorer_harvester.go` | generateLLMNote() 后回传 token count |
| `internal/agent/explorer.go` | token callback, 累加, 日重置 |

---

## 五、T3 — 历史去重窗口修复

### 5.1 问题

`ListExplorationRuns(ctx, "", 10)` 取全局最近 10 条 run。10 条滑出窗口后，`deduplicateTasks` 无法发现已 completed 的 model+engine，导致重复探索。

### 5.2 方案

新增 2 个 DB 方法：

```sql
-- HasCompletedExploration(ctx, modelID, engineID) bool
SELECT 1 FROM exploration_runs
WHERE model_id = ? AND engine_id = ? AND status = 'completed'
LIMIT 1

-- CountFailedExplorations(ctx, modelID, engineID) int
SELECT COUNT(*) FROM exploration_runs
WHERE model_id = ? AND engine_id = ? AND status = 'failed'
```

去重逻辑从 planner 内部移到 Explorer 层（planner 不应持有 DB 访问权）：

- `RulePlanner.Plan()` 和 `LLMPlanner.Plan()` 中移除 `deduplicateTasks` 调用
- `Explorer.handleEvent()` 在 `planner.Plan()` 返回后、`runPlan()` 之前，调用新版 `deduplicateTasks`
- 新版 `deduplicateTasks` 接受 callback `func(model, engine string) (completed bool, failCount int)` 而非 history slice
- Explorer 用 `e.db.HasCompletedExploration` / `e.db.CountFailedExplorations` 实现该 callback

`buildPlanInput` 中 `ListExplorationRuns` limit 10 保留，仅用于 LLM prompt 的 history context，不再用于去重。

### 5.3 影响范围

| 文件 | 改动 |
|------|------|
| `internal/sqlite.go` | +2 方法 |
| `internal/agent/explorer_planner.go` | 移除 planner 内 `deduplicateTasks` 调用 |
| `internal/agent/explorer.go` | handleEvent 中新增去重步骤，用 DB callback |

---

## 六、T4 — RulePlanner Gap 排序

### 6.1 问题

```go
sort.Slice(localGaps, func(i, j int) bool { return localGaps[i].Model < localGaps[j].Model })
```

字母序 → 每次 cycle 选同一批前 3 个模型。

### 6.2 方案

改为：先 shuffle → 再按 BenchmarkCount 升序 stable sort。

```go
rand.Shuffle(len(localGaps), func(i, j int) {
    localGaps[i], localGaps[j] = localGaps[j], localGaps[i]
})
sort.SliceStable(localGaps, func(i, j int) bool {
    return localGaps[i].BenchmarkCount < localGaps[j].BenchmarkCount
})
```

最少被测试的 gap 优先，同 count 时每次 cycle 顺序随机化，最大化探索广度。

### 6.3 影响范围

| 文件 | 改动 |
|------|------|
| `internal/agent/explorer_planner.go` | gap 排序 ~3 行 |

---

## 七、T5 — Plan 时间预算 + 效率指标

### 7.1 Plan 时间预算

`runPlan` 用 `MaxPlanDuration` 创建超时 context：

```go
func (e *Explorer) runPlan(ctx context.Context, plan *ExplorerPlan) {
    maxDur := e.config.MaxPlanDuration
    if maxDur <= 0 {
        maxDur = 30 * time.Minute
    }
    planCtx, cancel := context.WithTimeout(ctx, maxDur)
    defer cancel()
    e.executePlan(planCtx, plan)
}
```

`executePlan` 已有 `select { case <-ctx.Done(): return }` 检查。超时后剩余 task 标记 `skipped_timeout`。

### 7.2 效率指标

Plan 完成后计算 `PlanMetrics`：

```go
type PlanMetrics struct {
    TotalTasks      int     `json:"total_tasks"`
    Completed       int     `json:"completed"`
    Failed          int     `json:"failed"`
    Skipped         int     `json:"skipped"`
    DurationS       float64 `json:"duration_s"`
    SuccessRate     float64 `json:"success_rate"`
    AvgTaskDurationS float64 `json:"avg_task_duration_s"`
    TokensUsed      int     `json:"tokens_used"`
    DiscoveryCount  int     `json:"discovery_count"`
}
```

存入 `exploration_plans.summary_json`（已有字段），`ExplorerStatus` 新增 `LastPlanMetrics *PlanMetrics`。

### 7.3 影响范围

| 文件 | 改动 |
|------|------|
| `internal/agent/explorer.go` | `runPlan` timeout, metrics 统计, Status |
| `internal/agent/explorer_planner.go` | `PlanTask.Status` 新增 `skipped_timeout` |

---

## 八、全局影响汇总

| 改动类别 | 文件数 | 核心文件 |
|----------|--------|----------|
| T1: Stalled 检测 | 7 | runtime.go, progress.go, native.go, native_log.go, docker.go, k3s.go, exploration.go |
| T2: 资源控制 | 3 | explorer.go, main.go, tools_automation.go |
| T3: 历史去重 | 3 | sqlite.go, explorer_planner.go, explorer.go |
| T4: Gap 排序 | 1 | explorer_planner.go |
| T5: Plan 预算 | 2 | explorer.go, explorer_planner.go |
| T6: Token 追踪 | 5 | openai.go, agent.go, explorer_llmplanner.go, explorer_harvester.go, explorer.go |
| **合计** | **~14** | |

无 DB migration：Stalled/LastProgressAt 是运行时字段不持久化；token 计数存 config 表（已有 `db.SetConfig/GetConfig`）；效率指标存已有 `summary_json` 字段。

---

## 九、实现顺序建议

1. **T1 (Stalled)** — 最核心，解除超时硬编码
2. **T2 (资源控制)** — 实测需要，没有这个无法安全地跑 E2E
3. **T5 (Plan 预算)** — 与 T2 紧耦合，一起做
4. **T3+T4 (去重+排序)** — 独立小改
5. **T6 (Token 追踪)** — 依赖 T2 的 budget 检查点
