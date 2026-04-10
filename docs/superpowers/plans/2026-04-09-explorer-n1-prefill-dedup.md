# Explorer N1 修复 + Prefill Dedup 优化 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复空计划不计入 budget 的 bug (N1)，并将已探索的 model+engine 组合作为 prefill context 注入 LLM prompt，消除 decode token 浪费

**Architecture:** 新增 DB 查询方法一次性获取所有已探索组合 → 注入 PlanInput → LLM prompt 中显示为 skip_combos 列表 → 保留 post-hoc dedup 作为安全网

**Tech Stack:** Go, SQLite, LLM prompt engineering

---

### Task 1: 修复 N1 — 空计划递增 roundsUsed

**Files:**
- Modify: `internal/agent/explorer.go:443-446`
- Modify: `internal/agent/explorer_test.go` (新增测试)

**问题:** `handleEvent` 中 dedup 过滤到 0 tasks 后在 line 443-446 处 return，但 `roundsUsed++` 在 line 466，导致空计划不消耗 budget，每 30s gap_scan 持续触发 LLM 调用。

- [ ] **Step 1: 写失败测试**

在 `explorer_test.go` 中添加 `TestExplorer_EmptyPlanCountsAsBudgetRound`：

```go
func TestExplorer_EmptyPlanCountsAsBudgetRound(t *testing.T) {
	// Planner that always returns 0 tasks
	emptyPlanner := &countingPlanner{
		plan: &ExplorerPlan{ID: "empty", Tasks: nil},
	}
	e := &Explorer{
		config: ExplorerConfig{
			Mode:      "budget",
			MaxRounds: 2,
		},
		planner: emptyPlanner,
	}
	ctx := context.Background()
	// Simulate two handleEvent calls with empty plans
	e.handleEvent(ctx, ExplorerEvent{Type: "gap_scan"})
	e.handleEvent(ctx, ExplorerEvent{Type: "gap_scan"})

	if e.roundsUsed != 2 {
		t.Errorf("roundsUsed = %d, want 2 (empty plans should count)", e.roundsUsed)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/agent/ -run TestExplorer_EmptyPlanCountsAsBudgetRound -v`
Expected: FAIL — roundsUsed = 0

- [ ] **Step 3: 修复 — 空计划处也递增 roundsUsed**

在 `explorer.go` line 443-446 处，return 前递增 roundsUsed：

```go
if len(plan.Tasks) == 0 {
    slog.Info("explorer: no tasks to execute after filtering")
    e.mu.Lock()
    e.roundsUsed++
    e.mu.Unlock()
    return
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/agent/ -run TestExplorer_EmptyPlanCountsAsBudgetRound -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/explorer.go internal/agent/explorer_test.go
git commit -m "fix(explorer): N1 — empty plans count toward budget rounds"
```

---

### Task 2: 新增 DB 方法 ListExploredCombos

**Files:**
- Modify: `internal/sqlite.go` (新增方法)

**目的:** 一次性查询所有已完成或失败的 model+engine 组合及其状态，替代逐个调用 HasCompletedExploration/CountFailedExplorations。

- [ ] **Step 1: 定义返回类型并实现方法**

在 `sqlite.go` 的 exploration 方法区域添加：

```go
// ExploredCombo summarizes the exploration status of a model+engine pair.
type ExploredCombo struct {
    Model     string
    Engine    string
    Completed bool
    FailCount int
}

// ListExploredCombos returns all model+engine pairs that have been explored,
// with their completion status and failure count.
func (d *DB) ListExploredCombos(ctx context.Context) ([]ExploredCombo, error) {
    rows, err := d.db.QueryContext(ctx, `
        SELECT model_id, engine_id,
            MAX(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) AS completed,
            SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) AS fail_count
        FROM exploration_runs
        GROUP BY model_id, engine_id`)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var combos []ExploredCombo
    for rows.Next() {
        var c ExploredCombo
        if err := rows.Scan(&c.Model, &c.Engine, &c.Completed, &c.FailCount); err != nil {
            return nil, err
        }
        combos = append(combos, c)
    }
    return combos, rows.Err()
}
```

- [ ] **Step 2: 运行编译确认**

Run: `go build ./...`
Expected: 编译成功

- [ ] **Step 3: Commit**

```bash
git add internal/sqlite.go
git commit -m "feat(db): add ListExploredCombos for bulk dedup query"
```

---

### Task 3: Prefill Dedup — 注入已探索组合到 LLM Prompt

**Files:**
- Modify: `internal/agent/explorer_planner.go` (PlanInput + prompt 构建)
- Modify: `internal/agent/explorer_llmplanner.go` (system prompt + JSON 构建)
- Modify: `internal/agent/explorer.go` (buildPlanInput 填充)

**核心思路:** 将 DB 中的全量已探索组合注入到 PlanInput，然后在 LLM prompt 中以 `skip_combos` 字段呈现。LLM 在 prefill 阶段读取这些信息（成本低），从而避免在 decode 阶段生成已完成的任务（成本高）。

- [ ] **Step 1: 在 PlanInput 中添加 SkipCombos 字段**

在 `explorer_planner.go` 的 PlanInput struct 中添加：

```go
type PlanInput struct {
    Hardware      HardwareInfo
    Gaps          []GapEntry
    ActiveDeploys []DeployStatus
    Advisories    []Advisory
    History       []ExplorationRun
    OpenQuestions []OpenQuestion
    LocalModels   []LocalModel
    LocalEngines  []LocalEngine
    Event         *ExplorerEvent
    SkipCombos    []SkipCombo // model+engine pairs to avoid (completed or failed 2+)
}

// SkipCombo is a model+engine pair the LLM should not propose.
type SkipCombo struct {
    Model  string `json:"model"`
    Engine string `json:"engine"`
    Reason string `json:"reason"` // "completed" or "failed:N"
}
```

- [ ] **Step 2: 在 buildPlanInput 中填充 SkipCombos**

在 `explorer.go` 的 `buildPlanInput` 方法中，history 查询后添加：

```go
// Prefill dedup: feed all explored combos to LLM so it doesn't waste
// decode tokens proposing tasks that would be filtered post-hoc.
if e.db != nil {
    combos, _ := e.db.ListExploredCombos(ctx)
    for _, c := range combos {
        if c.Completed {
            input.SkipCombos = append(input.SkipCombos, SkipCombo{
                Model: c.Model, Engine: c.Engine, Reason: "completed",
            })
        } else if c.FailCount >= 2 {
            input.SkipCombos = append(input.SkipCombos, SkipCombo{
                Model: c.Model, Engine: c.Engine,
                Reason: fmt.Sprintf("failed:%d", c.FailCount),
            })
        }
    }
}
```

- [ ] **Step 3: 在 buildPlannerPrompt 中序列化 skip_combos**

在 `explorer_llmplanner.go` 的 `buildPlannerPrompt` 函数中，promptData map 添加：

```go
promptData := map[string]any{
    "hardware":       input.Hardware,
    "gaps":           input.Gaps,
    "active_deploys": input.ActiveDeploys,
    "advisories":     advisories,
    "open_questions": openQuestions,
    "local_models":   localModels,
    "local_engines":  localEngines,
    "history":        history,
    "event":          input.Event,
    "skip_combos":    input.SkipCombos, // NEW: prefill dedup context
}
```

- [ ] **Step 4: 更新 LLM System Prompt**

在 `llmPlannerSystemPrompt` 中添加关于 skip_combos 的明确指令：

```
CRITICAL — DO NOT PROPOSE SKIP COMBOS:
The "skip_combos" list contains model+engine pairs that have already been explored.
- "completed": This combo has validated results. Do NOT propose a validate task for it.
- "failed:N": This combo has failed N times. Do NOT propose it at all.
This list is authoritative and complete. If a model+engine pair appears in skip_combos,
you MUST NOT include it in your plan. This saves tokens and execution time.
```

- [ ] **Step 5: filterPlanInput 保留 SkipCombos（不裁剪）**

在 `filterPlanInput` 返回的 PlanInput 中传递 SkipCombos（不需要裁剪，因为这些是压缩的 key-value 对，token 成本很低）：

```go
return PlanInput{
    // ... existing fields ...
    SkipCombos: input.SkipCombos,
}
```

- [ ] **Step 6: 运行编译和现有测试**

Run: `go build ./... && go test ./internal/agent/ -v`
Expected: 编译成功，所有测试通过

- [ ] **Step 7: Commit**

```bash
git add internal/agent/explorer_planner.go internal/agent/explorer_llmplanner.go internal/agent/explorer.go
git commit -m "feat(explorer): prefill dedup — feed explored combos to LLM prompt"
```

---

## 预期效果

**修复前（E2E 测试数据）：**
- Round 2: 19K reasoning tokens, 2/5 tasks deduped (40% 浪费)
- Round 3: ~13K reasoning tokens, 5/5 tasks deduped (100% 浪费)
- Round 4: ~14K reasoning tokens, 5/5 tasks deduped (100% 浪费)
- 总浪费: ~46K reasoning tokens

**修复后预期：**
- Round 2: LLM 看到 skip_combos 中的 4 个已完成组合 → 只提议新组合 → ~8K reasoning tokens
- Round 3: LLM 看到更多 skip_combos → 可能直接返回空计划 → ~3K reasoning tokens
- N1 修复: Round 3 空计划 → roundsUsed=3 → budget 耗尽，停止
- 总节约: ~35K+ reasoning tokens（约 75%）

**Post-hoc dedup 保留为安全网：** 如果 LLM 忽略 skip_combos 指令，DB dedup 仍然会过滤。但正常情况下不应触发。
