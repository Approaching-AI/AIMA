# 知识自动化端到端集成实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Edge Explorer 和 Central Server 串联成端到端闭环：Sync v2 协议（advisory + scenario 通道）、Advisory 反馈机制、Central advisory 自动触发边缘验证，完整走通"新设备上线→中央推荐→边缘验证→反馈中央"流程。

**Architecture:** 在现有 sync push/pull 基础上扩展 advisory 和 scenario 数据通道。边缘 sync_pull 收到 advisory 后通过 EventBus 触发 Explorer 自动验证。验证结果通过 sync_push 反馈中央。

**Tech Stack:** Go 1.22+, zero CGO, existing internal/mcp + internal/agent + internal/central packages

**Design Spec:** `docs/superpowers/specs/2026-04-07-v0.4-knowledge-automation-design.md` §5

**Dependencies:** 本计划依赖 Plan 1 (Edge Explorer) 和 Plan 2 (Central Server) 完成后执行。

---

## File Structure

```
internal/mcp/
  tools_knowledge.go       # MODIFY: sync_pull 增加 advisory + scenario 拉取
  tools_deps.go            # MODIFY: 新增 advisory/scenario 相关 ToolDeps 字段

cmd/aima/
  main.go                  # MODIFY: wire sync v2 + advisory event bridge
  knowledge.go             # MODIFY: 新增 advise / scenario CLI 命令

internal/agent/
  explorer.go              # MODIFY: handleEvent 增加 central.advisory 处理
  explorer_test.go         # MODIFY: 增加 advisory 验证流程测试

tests/
  integration_test.go      # CREATE: 端到端集成测试
```

---

### Task 1: Sync v2 — Advisory 拉取

**Files:**
- Modify: `internal/mcp/tools_knowledge.go`
- Modify: `internal/mcp/tools_deps.go`

- [ ] **Step 1: 读取现有 sync_pull 实现**

读取 `internal/mcp/tools_knowledge.go` 中 `knowledge.sync_pull` 的处理逻辑，以及 `cmd/aima/main.go` 中 `buildToolDeps` 里对应的闭包。

- [ ] **Step 2: 在 ToolDeps 中新增 advisory 相关字段**

```go
// tools_deps.go — 新增字段
SyncPullAdvisories func(ctx context.Context, hardware string) ([]json.RawMessage, error)
SyncPullScenarios  func(ctx context.Context, hardware string) ([]json.RawMessage, error)
AdvisoryFeedback   func(ctx context.Context, advisoryID, status, reason string) error
RequestAdvise      func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
RequestScenario    func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
ListScenarios      func(ctx context.Context, hardware string) (json.RawMessage, error)
```

- [ ] **Step 3: 扩展 sync_pull handler 以拉取 advisories**

在 `tools_knowledge.go` 的 `sync_pull` 工具处理中，增加 advisory 和 scenario 拉取：

```go
// 在 sync_pull 处理逻辑的响应中，增加:
// 1. 调用 Central GET /api/v1/advisories?hardware=<本机>&status=pending
// 2. 调用 Central GET /api/v1/scenarios?hardware=<本机>
// 3. 将 advisories 和 scenarios 附加到 pull 响应中
// 4. 对每个收到的 advisory，通过 EventBus 发布 EventCentralAdvisory 事件

// handleSyncPull 扩展后返回:
type SyncPullResponse struct {
	Configurations []json.RawMessage `json:"configurations"`
	Benchmarks     []json.RawMessage `json:"benchmarks"`
	Notes          []json.RawMessage `json:"notes"`
	Advisories     []json.RawMessage `json:"advisories"`  // 新增
	Scenarios      []json.RawMessage `json:"scenarios"`    // 新增
}
```

- [ ] **Step 4: Run existing sync tests**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/mcp/ -v -count=1`
Expected: PASS (additive changes)

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/tools_knowledge.go internal/mcp/tools_deps.go
git commit -m "feat(sync): extend sync_pull with advisory and scenario channels"
```

---

### Task 2: Sync v2 — Advisory Feedback Push

**Files:**
- Modify: `internal/mcp/tools_knowledge.go`

- [ ] **Step 1: 扩展 sync_push 增加 advisory feedback**

在 `sync_push` 处理逻辑中，增加向 Central 发送 advisory feedback 的能力：

```go
// 在 sync_push 的请求体中增加:
type SyncPushPayload struct {
	// 现有字段...
	AdvisoryFeedback []AdvisoryFeedbackItem `json:"advisory_feedback,omitempty"` // 新增
}

type AdvisoryFeedbackItem struct {
	AdvisoryID string `json:"advisory_id"`
	Status     string `json:"status"` // validated / rejected
	Reason     string `json:"reason"`
}

// sync_push handler 扩展:
// 1. 正常 push configurations + benchmarks + notes
// 2. 如果 payload 包含 advisory_feedback，逐个调用 Central POST /api/v1/advisory/feedback
```

- [ ] **Step 2: 在 main.go 的 buildToolDeps 中 wire feedback 函数**

```go
// buildToolDeps 中:
deps.AdvisoryFeedback = func(ctx context.Context, advisoryID, status, reason string) error {
	if centralURL == "" {
		return fmt.Errorf("central not configured")
	}
	body, _ := json.Marshal(map[string]string{
		"advisory_id": advisoryID,
		"status":      status,
		"reason":      reason,
	})
	req, _ := http.NewRequestWithContext(ctx, "POST", centralURL+"/api/v1/advisory/feedback", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+centralAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("feedback status %d", resp.StatusCode)
	}
	return nil
}
```

- [ ] **Step 3: Run tests**

Run: `cd /Users/jguan/projects/AIMA && go build ./cmd/aima && go test ./internal/mcp/ -v -count=1`
Expected: BUILD + PASS

- [ ] **Step 4: Commit**

```bash
git add internal/mcp/tools_knowledge.go cmd/aima/main.go
git commit -m "feat(sync): add advisory feedback push in sync_push"
```

---

### Task 3: Central Advisory → EventBus Bridge

**Files:**
- Modify: `internal/agent/explorer.go`

- [ ] **Step 1: 读取 explorer.go 的 handleEvent 方法**

确认当前 `handleEvent` 如何处理事件，找到添加 `EventCentralAdvisory` 处理的位置。

- [ ] **Step 2: 添加 advisory 事件处理**

在 `handleEvent` 中增加对 `EventCentralAdvisory` 的处理：

```go
func (e *Explorer) handleEvent(ctx context.Context, ev ExplorerEvent) {
	// ... 现有的 tier 检测和通用处理 ...

	switch ev.Type {
	case EventCentralAdvisory:
		e.handleAdvisory(ctx, ev)
		return
	case EventCentralScenario:
		e.handleScenario(ctx, ev)
		return
	}

	// ... 现有的通用 plan → execute 流程 ...
}

func (e *Explorer) handleAdvisory(ctx context.Context, ev ExplorerEvent) {
	if len(ev.Advisory) == 0 {
		return
	}

	var adv struct {
		ID         string          `json:"id"`
		Type       string          `json:"type"`
		Engine     string          `json:"target_engine"`
		Model      string          `json:"target_model"`
		Config     json.RawMessage `json:"content"`
		Confidence string          `json:"confidence"`
	}
	if err := json.Unmarshal(ev.Advisory, &adv); err != nil {
		slog.Warn("explorer: parse advisory failed", "error", err)
		return
	}

	slog.Info("explorer: processing advisory", "id", adv.ID, "type", adv.Type, "model", adv.Model)

	// Tier 1: high confidence → apply directly, otherwise skip
	if e.tier == 1 && adv.Confidence != "high" {
		slog.Info("explorer: tier 1 skipping non-high-confidence advisory", "id", adv.ID)
		return
	}

	// Generate validation plan for this advisory
	plan := &ExplorationPlan{
		ID:   genShortID(),
		Tier: e.tier,
		Tasks: []PlanTask{{
			Kind:   "validate",
			Model:  adv.Model,
			Engine: adv.Engine,
			Params: adv.Config,
			Reason: fmt.Sprintf("validate advisory %s: %s", adv.ID, adv.Type),
		}},
		Reasoning: fmt.Sprintf("Central advisory %s validation", adv.ID),
	}

	e.mu.Lock()
	e.activePlan = plan
	e.lastRun = time.Now()
	e.mu.Unlock()

	// Execute validation
	e.executePlan(ctx, plan)

	// Send feedback based on results
	if e.advisoryFeedback != nil {
		status := "validated"
		reason := "benchmark completed successfully"
		// Check if last task failed
		if plan.Tasks[0].Result != nil && !plan.Tasks[0].Result.Success {
			status = "rejected"
			reason = plan.Tasks[0].Result.Error
		}
		_ = e.advisoryFeedback(ctx, adv.ID, status, reason)
	}
}

func (e *Explorer) handleScenario(ctx context.Context, ev ExplorerEvent) {
	// Store scenario locally for future use
	slog.Info("explorer: received scenario from central", "type", ev.Type)
}
```

- [ ] **Step 3: 添加 advisoryFeedback 函数字段到 Explorer**

```go
// explorer.go — Explorer struct 新增字段:
advisoryFeedback func(ctx context.Context, advisoryID, status, reason string) error

// NewExplorer 新增 option:
type ExplorerOption func(*Explorer)

func WithAdvisoryFeedback(fn func(ctx context.Context, advisoryID, status, reason string) error) ExplorerOption {
	return func(e *Explorer) { e.advisoryFeedback = fn }
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestExplorer -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/explorer.go
git commit -m "feat(explorer): handle central advisory events with validation + feedback"
```

---

### Task 4: Advisory 拉取时发布 EventBus 事件

**Files:**
- Modify: `cmd/aima/main.go`

- [ ] **Step 1: 在 sync_pull 成功后发布 advisory 事件**

在 `buildToolDeps` 的 sync_pull 闭包中，当从 Central 拉取到 advisories 时，通过 EventBus 发布事件：

```go
// buildToolDeps 中 sync_pull 的闭包:
deps.SyncPull = func(ctx context.Context) (json.RawMessage, error) {
	// ... 现有 pull 逻辑 ...

	// 拉取 advisories
	if centralURL != "" {
		advisories, err := pullAdvisories(ctx, centralURL, centralAPIKey, hwProfile)
		if err == nil {
			for _, adv := range advisories {
				eventBus.Publish(agent.ExplorerEvent{
					Type:     agent.EventCentralAdvisory,
					Advisory: adv,
				})
			}
			pullResp.Advisories = advisories
		}

		scenarios, err := pullScenarios(ctx, centralURL, centralAPIKey, hwProfile)
		if err == nil {
			for _, sc := range scenarios {
				eventBus.Publish(agent.ExplorerEvent{
					Type: agent.EventCentralScenario,
				})
			}
			pullResp.Scenarios = scenarios
		}
	}

	return json.Marshal(pullResp)
}

func pullAdvisories(ctx context.Context, centralURL, apiKey, hardware string) ([]json.RawMessage, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/advisories?hardware=%s&status=pending", centralURL, url.QueryEscape(hardware)), nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var advisories []json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&advisories)
	return advisories, nil
}

func pullScenarios(ctx context.Context, centralURL, apiKey, hardware string) ([]json.RawMessage, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/scenarios?hardware=%s", centralURL, url.QueryEscape(hardware)), nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var scenarios []json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&scenarios)
	return scenarios, nil
}
```

- [ ] **Step 2: Wire Explorer's advisoryFeedback in main.go**

```go
// 在 Explorer 初始化时:
explorer := agent.NewExplorer(explorerConfig, goAgent, explMgr, db, eventBus,
	agent.WithAdvisoryFeedback(deps.AdvisoryFeedback),
)
```

- [ ] **Step 3: Build verification**

Run: `cd /Users/jguan/projects/AIMA && go build ./cmd/aima`
Expected: BUILD SUCCESS

- [ ] **Step 4: Commit**

```bash
git add cmd/aima/main.go
git commit -m "feat(sync): publish EventBus events on advisory pull + wire feedback"
```

---

### Task 5: Knowledge Advise CLI 命令

**Files:**
- Modify: `cmd/aima/knowledge.go` (或创建新文件)

- [ ] **Step 1: 读取现有 knowledge CLI 命令**

确认 `aima knowledge` 命令的结构和 thin CLI 模式。

- [ ] **Step 2: 添加 advise 子命令**

```go
func newKnowledgeAdviseCmd() *cobra.Command {
	var model, engine, intent string
	cmd := &cobra.Command{
		Use:   "advise",
		Short: "Request configuration recommendation from Central",
		RunE: func(cmd *cobra.Command, args []string) error {
			params, _ := json.Marshal(map[string]string{
				"model":  model,
				"engine": engine,
				"intent": intent,
			})
			result, err := deps.RequestAdvise(cmd.Context(), params)
			if err != nil {
				return err
			}
			return formatJSON(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "Model to get recommendation for")
	cmd.Flags().StringVar(&engine, "engine", "", "Preferred engine (optional)")
	cmd.Flags().StringVar(&intent, "intent", "balanced", "Optimization intent: throughput/latency/balanced")
	_ = cmd.MarkFlagRequired("model")
	return cmd
}
```

- [ ] **Step 3: 添加 scenario generate/list 子命令**

```go
func newScenarioGenerateCmd() *cobra.Command {
	var modalities, constraints string
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Request Central to generate a deployment scenario",
		RunE: func(cmd *cobra.Command, args []string) error {
			params, _ := json.Marshal(map[string]any{
				"modalities":  strings.Split(modalities, ","),
				"constraints": constraints,
			})
			result, err := deps.RequestScenario(cmd.Context(), params)
			if err != nil {
				return err
			}
			return formatJSON(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().StringVar(&modalities, "modalities", "text", "Comma-separated modalities: text,tts,image")
	cmd.Flags().StringVar(&constraints, "constraints", "", "Additional constraints")
	return cmd
}

func newScenarioListCmd() *cobra.Command {
	var source string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List scenarios from Central",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := deps.ListScenarios(cmd.Context(), "")
			if err != nil {
				return err
			}
			return formatJSON(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "Filter by source: central/local")
	return cmd
}
```

- [ ] **Step 4: 注册到根命令**

在 `cmd/aima/main.go` 的命令注册处:

```go
knowledgeCmd.AddCommand(newKnowledgeAdviseCmd())
scenarioCmd := &cobra.Command{Use: "scenario", Short: "Manage deployment scenarios"}
scenarioCmd.AddCommand(newScenarioGenerateCmd(), newScenarioListCmd())
rootCmd.AddCommand(scenarioCmd)
```

- [ ] **Step 5: Build verification**

Run: `cd /Users/jguan/projects/AIMA && go build ./cmd/aima`
Expected: BUILD SUCCESS

- [ ] **Step 6: Commit**

```bash
git add cmd/aima/knowledge.go cmd/aima/main.go
git commit -m "feat(cli): add 'aima knowledge advise' and 'aima scenario generate/list' commands"
```

---

### Task 6: 新增 MCP 工具 — knowledge.advise / scenario.generate / scenario.list

**Files:**
- Modify: `internal/mcp/tools_knowledge.go` (或创建 `tools_scenario.go`)
- Modify: `internal/mcp/tools_deps.go`

- [ ] **Step 1: 定义 3 个新 MCP 工具**

```go
// knowledge.advise — 请求中央推荐配置
{
	Name: "knowledge.advise",
	Description: "Request configuration recommendation from Central server",
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"model":  map[string]any{"type": "string", "description": "Model name"},
			"engine": map[string]any{"type": "string", "description": "Preferred engine (optional)"},
			"intent": map[string]any{"type": "string", "enum": []string{"throughput", "latency", "balanced"}},
		},
		"required": []string{"model"},
	},
}

// scenario.generate — 请求中央生成部署方案
{
	Name: "scenario.generate",
	Description: "Request Central to generate a deployment scenario for this hardware",
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"modalities":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"constraints": map[string]any{"type": "string"},
		},
	},
}

// scenario.list — 查询中央 scenario 库
{
	Name: "scenario.list",
	Description: "List deployment scenarios from Central",
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"hardware": map[string]any{"type": "string"},
			"source":   map[string]any{"type": "string", "enum": []string{"central", "local", ""}},
		},
	},
}
```

- [ ] **Step 2: Wire handlers to ToolDeps**

每个工具的 handler 直接调用对应的 `deps.RequestAdvise` / `deps.RequestScenario` / `deps.ListScenarios`。

- [ ] **Step 3: Run tests**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/mcp/ -v -count=1`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/mcp/tools_knowledge.go internal/mcp/tools_deps.go
git commit -m "feat(mcp): add knowledge.advise, scenario.generate, scenario.list tools"
```

---

### Task 7: 端到端集成测试

**Files:**
- Create: `internal/central/integration_test.go`

- [ ] **Step 1: 写集成测试——完整 Advisory 生命周期**

```go
// integration_test.go
package central

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestAdvisoryLifecycle_EndToEnd(t *testing.T) {
	// Setup: create Central store + Advisor with mock LLM
	store := newTestSQLiteStore(t)
	defer store.Close()

	llm := &mockAdvisorLLM{
		response: `{"engine":"vllm","config":{"gpu_memory_utilization":0.78},"confidence":"high","reasoning":"test","suggested_validation":{"kind":"benchmark","params":[]}}`,
	}
	advisor := NewAdvisor(store, llm)

	ctx := context.Background()

	// Step 1: Device registers
	err := store.UpsertDevice(ctx, DeviceInfo{
		ID: "w7900d", HardwareProfile: "amd-w7900d-x86", GPUArch: "RDNA3",
	})
	if err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}

	// Step 2: Device pushes initial data (no benchmarks)
	_, _ = store.IngestConfigurations(ctx, "w7900d", []IngestConfig{{
		ID: "cfg-1", Hardware: "amd-w7900d-x86", EngineType: "vllm",
		Model: "qwen3-8b", Config: []byte(`{"gmu":0.5}`), ConfigHash: "h1", Status: "experiment",
	}})

	// Step 3: Advisor generates recommendation
	resp, err := advisor.Recommend(ctx, RecommendRequest{
		HardwareProfile: "amd-w7900d-x86",
		HardwareInfo:    HardwareSpec{GPUVRAMMiB: 49152, GPUCount: 8},
		Model:           "qwen3-8b",
		Intent:          "throughput",
	})
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	advisoryID := resp.AdvisoryID

	// Step 4: Edge pulls advisories (pending)
	advs, _ := store.ListAdvisories(ctx, AdvisoryQuery{
		Hardware: "amd-w7900d-x86", Status: "pending",
	})
	if len(advs) != 1 {
		t.Fatalf("pending advisories = %d, want 1", len(advs))
	}
	if advs[0].ID != advisoryID {
		t.Errorf("advisory ID mismatch")
	}

	// Step 5: Mark as delivered (edge received it)
	_ = store.UpdateAdvisoryStatus(ctx, advisoryID, "delivered")
	pending, _ := store.ListAdvisories(ctx, AdvisoryQuery{Status: "pending"})
	if len(pending) != 0 {
		t.Errorf("still pending after deliver: %d", len(pending))
	}

	// Step 6: Edge validates and sends feedback
	_ = store.UpdateAdvisoryStatus(ctx, advisoryID, "validated")
	all, _ := store.ListAdvisories(ctx, AdvisoryQuery{Status: "validated"})
	if len(all) != 1 {
		t.Errorf("validated = %d, want 1", len(all))
	}
}

func TestGapScan_GeneratesAdvisories(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close()

	// Seed: config without benchmark
	_ = store.UpsertDevice(context.Background(), DeviceInfo{ID: "dev-1", GPUArch: "Ada"})
	_, _ = store.IngestConfigurations(context.Background(), "dev-1", []IngestConfig{{
		ID: "cfg-1", Hardware: "nvidia-rtx4090-x86", EngineType: "vllm",
		Model: "qwen3-8b", Config: []byte(`{}`), ConfigHash: "h1", Status: "golden",
	}})

	llm := &mockAdvisorLLM{
		response: `{"patterns":[],"anomalies":[],"recommendations":[{"type":"gap_alert","target_hardware":"nvidia-rtx4090-x86","target_model":"qwen3-8b","reasoning":"missing benchmark data"}]}`,
	}
	analyzer := NewAnalyzer(store, llm, AnalyzerConfig{})

	runID, err := analyzer.RunGapScan(context.Background())
	if err != nil {
		t.Fatalf("RunGapScan: %v", err)
	}

	// Verify: analysis run created
	runs, _ := store.ListAnalysisRuns(context.Background(), 5)
	found := false
	for _, r := range runs {
		if r.ID == runID && r.Status == "completed" {
			found = true
		}
	}
	if !found {
		t.Error("expected completed analysis run")
	}

	// Verify: advisory created from gap scan
	advs, _ := store.ListAdvisories(context.Background(), AdvisoryQuery{Status: "pending"})
	if len(advs) == 0 {
		t.Error("expected gap_alert advisory")
	}
}
```

- [ ] **Step 2: Run integration tests**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/central/ -run TestAdvisoryLifecycle -v && go test ./internal/central/ -run TestGapScan_GeneratesAdvisories -v`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/central/integration_test.go
git commit -m "test(central): add end-to-end advisory lifecycle and gap scan integration tests"
```

---

### Task 8: 全链路 Build 验证

- [ ] **Step 1: Build 所有二进制**

```bash
cd /Users/jguan/projects/AIMA
go build ./cmd/aima
go build ./cmd/central
go vet ./...
```
Expected: BUILD SUCCESS, no vet warnings

- [ ] **Step 2: Run full test suite with race detector**

```bash
go test -race ./internal/agent/ -v -count=1
go test -race ./internal/central/ -v -count=1
go test -race ./internal/mcp/ -v -count=1
go test -race ./internal/state/ -v -count=1
```
Expected: PASS, no race conditions

- [ ] **Step 3: 交叉编译验证**

```bash
GOOS=linux GOARCH=amd64 go build -o /dev/null ./cmd/aima
GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/aima
GOOS=windows GOARCH=amd64 go build -o /dev/null ./cmd/aima
GOOS=linux GOARCH=amd64 go build -o /dev/null ./cmd/central
```
Expected: 全部 BUILD SUCCESS（零 CGO 保证）

- [ ] **Step 4: 验证新命令注册**

```bash
go run ./cmd/aima explorer --help
go run ./cmd/aima knowledge advise --help
go run ./cmd/aima scenario --help
```
Expected: 输出帮助信息，无 panic
