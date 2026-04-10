# MCP 工具整合重构实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 AIMA MCP 工具从 101 个精简至 56 个，Profile 真正过滤 Agent LLM 可见工具集，消除认知负担。

**Architecture:** 只改注册/暴露层（`tools_*.go`、`tools.go`、`server.go`），ToolDeps 内部字段保持细粒度不变。合并工具通过 `action` 参数分发到已有 ToolDeps 函数。Profile 新增 `ListToolsForProfile()` 供 Agent 使用。

**Tech Stack:** Go, MCP (JSON-RPC 2.0), internal/mcp, internal/agent, cmd/aima

**Spec:** `docs/superpowers/specs/2026-04-08-mcp-tool-consolidation-design.md`

---

## File Structure

### Files to Create
- `internal/mcp/tools_catalog.go` — catalog.list, catalog.override, catalog.validate (3 tools)
- `internal/mcp/tools_central.go` — central.sync, central.advise, central.scenario (3 tools)
- `internal/mcp/tools_data.go` — data.export, data.import (2 tools)
- `internal/mcp/tools_automation.go` — patrol, explore, tuning, explorer (4 tools)
- `internal/mcp/tools_fleet.go` — fleet.info, fleet.exec (2 tools)
- `internal/mcp/tools_scenario.go` — scenario.show, scenario.apply (2 tools)
- `internal/mcp/tools_openclaw.go` — openclaw (1 tool)
- `internal/mcp/tools_stack.go` — stack (1 tool)

### Files to Rewrite
- `internal/mcp/tools_knowledge.go` — 25 tools → 6 tools
- `internal/mcp/tools_agent.go` — 18 tools → 4 tools (agent 3 + support 1)
- `internal/mcp/tools_system.go` — 10 tools → 2 tools (system only)
- `internal/mcp/tools.go` — profile 定义更新 + RegisterAllTools 更新
- `internal/agent/prompt.md` — 完全重写匹配 ProfileOperator 39 工具

### Files to Modify
- `internal/mcp/server.go` — 新增 `ListToolsForProfile()`
- `internal/mcp/tools_deps.go` — 删除废弃字段
- `internal/mcp/tools_model.go` — 删除 `download.list`
- `internal/mcp/tools_engine.go` — 删除 `engine.plan`
- `internal/mcp/tools_deploy.go` — `deploy.dry_run` 新增 `output` 参数吸收 `generate_pod`
- `internal/agent/agent.go` — 新增 `WithProfile` + `ListToolsForProfile` 调用
- `cmd/aima/adapters.go` — `mcpToolAdapter.ListTools()` 支持 profile 过滤
- `cmd/aima/main.go` — Agent/Explorer 创建时传 Profile

### Files to Delete
- `internal/mcp/tools_integration.go` — 内容拆散到 fleet/scenario/openclaw 等文件
- `internal/mcp/tools_explorer.go` — 合并进 tools_automation.go
- `internal/mcp/tools_scenario_central.go` — 合并进 tools_central.go

### CLI Files to Update
- `internal/cli/knowledge.go` — 更新命令名匹配新工具名
- `internal/cli/scenario.go` — 移除 list 子命令（已合并入 catalog.list）
- `internal/cli/app.go` — 删除（app.* 被删除）
- `internal/cli/discover.go` — 删除（discover.lan 被删除）
- `internal/cli/explorer.go` — 更新匹配合并后的 explorer 工具
- `internal/cli/fleet.go` — 更新匹配合并后的 fleet.info/fleet.exec
- `internal/cli/root.go` — 删除废弃的子命令注册

---

### Task 1: 创建 refactor 分支并更新 ToolDeps

**Files:**
- Modify: `internal/mcp/tools_deps.go`

- [ ] **Step 1: 创建分支**

```bash
git checkout develop
git pull origin develop
git checkout -b refactor/mcp-tool-consolidation
```

- [ ] **Step 2: 删除废弃的 ToolDeps 字段**

从 `tools_deps.go` 删除以下字段（对应被删除的工具，业务代码不引用这些字段）：

```go
// 删除:
EnginePlan    func(ctx context.Context) (json.RawMessage, error)
ListDownloads func(ctx context.Context) (json.RawMessage, error)
DiscoverLAN   func(ctx context.Context, timeoutS int) (json.RawMessage, error)
AgentGuide    func(ctx context.Context) (json.RawMessage, error)
PowerHistory  func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
PowerMode     func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
AppRegister   func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
AppProvision  func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
AppList       func(ctx context.Context) (json.RawMessage, error)
ExecShell     func(ctx context.Context, command string) (json.RawMessage, error)
```

保留所有其他字段不变——合并工具仍然调用原有的细粒度函数。

- [ ] **Step 3: 验证编译**

```bash
go build ./...
```

Expected: 编译失败——引用了被删字段的 `tooldeps_*.go` 和 `tools_*.go` 需要同步清理。暂不修这些，Task 2-8 会覆盖。

- [ ] **Step 4: 提交 ToolDeps 变更**

```bash
git add internal/mcp/tools_deps.go
git commit -m "refactor(mcp): remove deprecated ToolDeps fields for deleted tools"
```

---

### Task 2: 重写 tools_knowledge.go (25 → 6)

这是最大的单个文件变更。原文件 593 行 25 个工具 → ~250 行 6 个工具。

**Files:**
- Rewrite: `internal/mcp/tools_knowledge.go`

- [ ] **Step 1: 写测试确认旧工具数**

```bash
go test ./internal/mcp/... -run TestKnowledge -count=1 -v
```

记录当前测试状态，后续需对齐。

- [ ] **Step 2: 重写 tools_knowledge.go**

完整替换文件内容为 6 个工具：

```go
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerKnowledgeTools(s *Server, deps *ToolDeps) {
	// knowledge.resolve — 不变
	s.RegisterTool(&Tool{
		Name:        "knowledge.resolve",
		Description: "Find the optimal engine and configuration for deploying a model on this hardware. Merges YAML defaults, golden configs, and user overrides into a final resolved config.",
		InputSchema: schema(
			`"model":{"type":"string","description":"Model name to resolve, e.g. 'qwen3-0.6b'. Call model.list or catalog.list(kind=models) to see available names."},`+
				`"engine":{"type":"string","description":"Engine type, e.g. 'vllm', 'llamacpp'. Omit to auto-select the best engine."},`+
				`"overrides":{"type":"object","description":"Config overrides to apply on top of resolved defaults, e.g. {\"gpu_memory_utilization\": 0.85}"}`,
			"model"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.ResolveConfig == nil {
				return ErrorResult("knowledge.resolve not implemented"), nil
			}
			var p struct {
				Model     string         `json:"model"`
				Engine    string         `json:"engine"`
				Overrides map[string]any `json:"overrides"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Model == "" {
				return ErrorResult("model is required"), nil
			}
			data, err := deps.ResolveConfig(ctx, p.Model, p.Engine, p.Overrides)
			if err != nil {
				return nil, fmt.Errorf("resolve config for %s: %w", p.Model, err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.search — 合并 search + search_configs
	s.RegisterTool(&Tool{
		Name:        "knowledge.search",
		Description: "Search knowledge: notes (agent exploration records) or configs (tested Configuration records with benchmark data). Default scope is 'configs'.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"scope":{"type":"string","enum":["configs","notes","all"],"description":"Search scope: configs (default), notes, or all"},
			"hardware":{"type":"string","description":"Filter by hardware profile"},
			"model":{"type":"string","description":"Filter by model name"},
			"engine":{"type":"string","description":"Filter by engine type"},
			"engine_features":{"type":"array","items":{"type":"string"},"description":"Required engine features (configs scope)"},
			"constraints":{"type":"object","properties":{"ttft_ms_p95_max":{"type":"number"},"throughput_tps_min":{"type":"number"},"vram_mib_max":{"type":"integer"},"power_watts_max":{"type":"number"}},"description":"Performance constraints (configs scope)"},
			"concurrency":{"type":"integer","description":"Filter by concurrency level (configs scope)"},
			"status":{"type":"string","enum":["golden","experiment","archived"],"description":"Filter by config status (configs scope)"},
			"sort_by":{"type":"string","enum":["throughput","latency","vram","power","created"],"description":"Sort field (configs scope)"},
			"sort_order":{"type":"string","enum":["asc","desc"],"description":"Sort direction (configs scope)"},
			"limit":{"type":"integer","description":"Max results (configs scope)"}
		}}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Scope string `json:"scope"`
			}
			if len(params) > 0 {
				_ = json.Unmarshal(params, &p)
			}
			if p.Scope == "" {
				p.Scope = "configs"
			}
			switch p.Scope {
			case "notes":
				if deps.SearchKnowledge == nil {
					return ErrorResult("knowledge.search(notes) not implemented"), nil
				}
				var f struct {
					Hardware string `json:"hardware"`
					Model    string `json:"model"`
					Engine   string `json:"engine"`
				}
				if len(params) > 0 {
					_ = json.Unmarshal(params, &f)
				}
				filter := make(map[string]string)
				if f.Hardware != "" {
					filter["hardware"] = f.Hardware
				}
				if f.Model != "" {
					filter["model"] = f.Model
				}
				if f.Engine != "" {
					filter["engine"] = f.Engine
				}
				data, err := deps.SearchKnowledge(ctx, filter)
				if err != nil {
					return nil, fmt.Errorf("search knowledge notes: %w", err)
				}
				return TextResult(string(data)), nil
			case "configs":
				if deps.SearchConfigs == nil {
					return ErrorResult("knowledge.search(configs) not implemented"), nil
				}
				data, err := deps.SearchConfigs(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("search configs: %w", err)
				}
				return TextResult(string(data)), nil
			case "all":
				// Combine notes + configs results
				var results []json.RawMessage
				if deps.SearchKnowledge != nil {
					var f struct {
						Hardware string `json:"hardware"`
						Model    string `json:"model"`
						Engine   string `json:"engine"`
					}
					if len(params) > 0 {
						_ = json.Unmarshal(params, &f)
					}
					filter := make(map[string]string)
					if f.Hardware != "" {
						filter["hardware"] = f.Hardware
					}
					if f.Model != "" {
						filter["model"] = f.Model
					}
					if f.Engine != "" {
						filter["engine"] = f.Engine
					}
					data, err := deps.SearchKnowledge(ctx, filter)
					if err == nil {
						results = append(results, json.RawMessage(fmt.Sprintf(`{"scope":"notes","data":%s}`, data)))
					}
				}
				if deps.SearchConfigs != nil {
					data, err := deps.SearchConfigs(ctx, params)
					if err == nil {
						results = append(results, json.RawMessage(fmt.Sprintf(`{"scope":"configs","data":%s}`, data)))
					}
				}
				combined, _ := json.Marshal(results)
				return TextResult(string(combined)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown scope %q: use configs, notes, or all", p.Scope)), nil
			}
		},
	})

	// knowledge.analytics — 合并 compare + similar + lineage + gaps + aggregate
	s.RegisterTool(&Tool{
		Name:        "knowledge.analytics",
		Description: "Run knowledge analytics queries: compare configs, find similar configs, trace lineage, identify gaps, or aggregate statistics.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"query":{"type":"string","enum":["compare","similar","lineage","gaps","aggregate"],"description":"Analytics query type"},
			"config_ids":{"type":"array","items":{"type":"string"},"description":"Config IDs to compare (compare query)"},
			"config_id":{"type":"string","description":"Reference config ID (similar/lineage query)"},
			"metrics":{"type":"array","items":{"type":"string"},"description":"Metrics to compare (compare query)"},
			"concurrency":{"type":"integer","description":"Concurrency filter (compare query)"},
			"weights":{"type":"object","description":"Custom metric weights (similar query)"},
			"filter_hardware":{"type":"string","description":"Limit search to specific hardware (similar query)"},
			"exclude_same_config":{"type":"boolean","description":"Exclude self from results (similar query, default true)"},
			"hardware":{"type":"string","description":"Hardware filter (gaps/aggregate query)"},
			"model":{"type":"string","description":"Model filter (aggregate query)"},
			"min_benchmarks":{"type":"integer","description":"Gap threshold (gaps query, default 3)"},
			"group_by":{"type":"string","enum":["engine","hardware","model"],"description":"Grouping dimension (aggregate query)"},
			"limit":{"type":"integer","description":"Max results"}
		},"required":["query"]}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Query    string `json:"query"`
				ConfigID string `json:"config_id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			switch p.Query {
			case "compare":
				if deps.CompareConfigs == nil {
					return ErrorResult("knowledge.analytics(compare) not implemented"), nil
				}
				data, err := deps.CompareConfigs(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("compare configs: %w", err)
				}
				return TextResult(string(data)), nil
			case "similar":
				if deps.SimilarConfigs == nil {
					return ErrorResult("knowledge.analytics(similar) not implemented"), nil
				}
				data, err := deps.SimilarConfigs(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("similar configs: %w", err)
				}
				return TextResult(string(data)), nil
			case "lineage":
				if deps.LineageConfigs == nil {
					return ErrorResult("knowledge.analytics(lineage) not implemented"), nil
				}
				if p.ConfigID == "" {
					return ErrorResult("config_id is required for lineage query"), nil
				}
				data, err := deps.LineageConfigs(ctx, p.ConfigID)
				if err != nil {
					return nil, fmt.Errorf("lineage %s: %w", p.ConfigID, err)
				}
				return TextResult(string(data)), nil
			case "gaps":
				if deps.GapsKnowledge == nil {
					return ErrorResult("knowledge.analytics(gaps) not implemented"), nil
				}
				data, err := deps.GapsKnowledge(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("knowledge gaps: %w", err)
				}
				return TextResult(string(data)), nil
			case "aggregate":
				if deps.AggregateKnowledge == nil {
					return ErrorResult("knowledge.analytics(aggregate) not implemented"), nil
				}
				data, err := deps.AggregateKnowledge(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("knowledge aggregate: %w", err)
				}
				return TextResult(string(data)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown query %q: use compare, similar, lineage, gaps, or aggregate", p.Query)), nil
			}
		},
	})

	// knowledge.promote — 不变
	s.RegisterTool(&Tool{
		Name:        "knowledge.promote",
		Description: "Change a Configuration's status to 'experiment', 'golden' (auto-injected as L2 defaults), or 'archived'.",
		InputSchema: schema(
			`"config_id":{"type":"string","description":"Configuration ID to promote"},`+
				`"status":{"type":"string","enum":["golden","experiment","archived"],"description":"Target status"}`,
			"config_id", "status"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.PromoteConfig == nil {
				return ErrorResult("knowledge.promote not implemented"), nil
			}
			var p struct {
				ConfigID string `json:"config_id"`
				Status   string `json:"status"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.ConfigID == "" || p.Status == "" {
				return ErrorResult("config_id and status are required"), nil
			}
			data, err := deps.PromoteConfig(ctx, p.ConfigID, p.Status)
			if err != nil {
				return nil, fmt.Errorf("promote config: %w", err)
			}
			return TextResult(string(data)), nil
		},
	})

	// knowledge.save — 不变
	s.RegisterTool(&Tool{
		Name:        "knowledge.save",
		Description: "Save a knowledge note recording exploration results, experiment findings, or recommendations.",
		InputSchema: schema(
			`"note":{"type":"object","description":"Knowledge note to save","properties":{`+
				`"title":{"type":"string","description":"Short descriptive title for the note"},`+
				`"content":{"type":"string","description":"Full text content of the note (findings, observations, recommendations)"},`+
				`"hardware_profile":{"type":"string","description":"Hardware profile name, e.g. 'nvidia-rtx4090-x86'"},`+
				`"model":{"type":"string","description":"Model name, e.g. 'glm-4.7-flash'"},`+
				`"engine":{"type":"string","description":"Engine type, e.g. 'sglang-kt'"},`+
				`"tags":{"type":"array","items":{"type":"string"},"description":"Tags for categorization"},`+
				`"confidence":{"type":"string","description":"Confidence level: high, medium, low"}`+
				`},"required":["title","content"]}`, "note"),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			if deps.SaveKnowledge == nil {
				return ErrorResult("knowledge.save not implemented"), nil
			}
			var p struct {
				Note json.RawMessage `json:"note"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if len(p.Note) == 0 {
				return ErrorResult("note is required"), nil
			}
			if err := deps.SaveKnowledge(ctx, p.Note); err != nil {
				return nil, fmt.Errorf("save knowledge: %w", err)
			}
			return TextResult("knowledge note saved"), nil
		},
	})

	// knowledge.evaluate — 合并 validate + engine_switch_cost + open_questions
	s.RegisterTool(&Tool{
		Name:        "knowledge.evaluate",
		Description: "Evaluate knowledge quality: validate predictions, assess engine switch cost, or manage open questions.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"action":{"type":"string","enum":["validate","switch_cost","open_questions"],"description":"Evaluation type"},
			"hardware":{"type":"string","description":"Hardware profile or GPU architecture"},
			"engine":{"type":"string","description":"Engine type"},
			"model":{"type":"string","description":"Model name"},
			"current_engine":{"type":"string","description":"Current engine (switch_cost action)"},
			"target_engine":{"type":"string","description":"Target engine (switch_cost action)"},
			"question_action":{"type":"string","enum":["list","resolve","run","validate"],"description":"Open question sub-action (open_questions action)"},
			"status":{"type":"string","description":"Filter by question status (open_questions action)"},
			"id":{"type":"string","description":"Question ID for resolve (open_questions action)"},
			"result":{"type":"string","description":"Test result for resolve (open_questions action)"},
			"endpoint":{"type":"string","description":"Inference endpoint (open_questions action)"},
			"requested_by":{"type":"string","description":"Who requested the run (open_questions action)"},
			"concurrency":{"type":"integer","description":"Benchmark concurrency (open_questions action)"},
			"rounds":{"type":"integer","description":"Benchmark rounds (open_questions action)"}
		},"required":["action"]}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*ToolResult, error) {
			var p struct {
				Action string `json:"action"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			switch p.Action {
			case "validate":
				if deps.ValidateKnowledge == nil {
					return ErrorResult("validation not available"), nil
				}
				data, err := deps.ValidateKnowledge(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("knowledge.evaluate(validate): %w", err)
				}
				return TextResult(string(data)), nil
			case "switch_cost":
				if deps.EngineSwitchCost == nil {
					return ErrorResult("engine switch cost not available"), nil
				}
				data, err := deps.EngineSwitchCost(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("knowledge.evaluate(switch_cost): %w", err)
				}
				return TextResult(string(data)), nil
			case "open_questions":
				if deps.OpenQuestions == nil {
					return ErrorResult("open questions not available"), nil
				}
				data, err := deps.OpenQuestions(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("knowledge.evaluate(open_questions): %w", err)
				}
				return TextResult(string(data)), nil
			default:
				return ErrorResult(fmt.Sprintf("unknown action %q: use validate, switch_cost, or open_questions", p.Action)), nil
			}
		},
	})
}
```

- [ ] **Step 3: 验证编译**

```bash
go build ./internal/mcp/...
```

Expected: 可能因 `tools.go` 中 `RegisterAllTools` 仍引用旧函数名而报错，但 `tools_knowledge.go` 本身应通过 vet。

- [ ] **Step 4: 提交**

```bash
git add internal/mcp/tools_knowledge.go
git commit -m "refactor(mcp): knowledge tools 25→6 (resolve, search, analytics, promote, save, evaluate)"
```

---

### Task 3: 创建新工具文件 (catalog, central, data, automation, fleet, scenario, openclaw, stack)

从 `tools_system.go`、`tools_integration.go`、`tools_agent.go`、`tools_explorer.go`、`tools_scenario_central.go` 中拆出工具到新文件。

**Files:**
- Create: `internal/mcp/tools_catalog.go`
- Create: `internal/mcp/tools_central.go`
- Create: `internal/mcp/tools_data.go`
- Create: `internal/mcp/tools_automation.go`
- Create: `internal/mcp/tools_fleet.go`
- Create: `internal/mcp/tools_scenario.go`
- Create: `internal/mcp/tools_openclaw.go`
- Create: `internal/mcp/tools_stack.go`

- [ ] **Step 1: 创建 tools_catalog.go**

3 个工具: `catalog.list` (合并 knowledge.list + list_profiles + list_engines + list_models + catalog.status + scenario.list), `catalog.override`, `catalog.validate`

`catalog.list` handler 通过 `kind` 参数分发：
- `kind=profiles` → `deps.ListProfiles(ctx)`
- `kind=engines` → `deps.ListEngineAssets(ctx)`
- `kind=models` → `deps.ListModelAssets(ctx)`
- `kind=scenarios` → `deps.ScenarioList(ctx)`
- `kind=summary` → `deps.ListKnowledgeSummary(ctx)`
- `kind=status` → `deps.CatalogStatus(ctx)`
- `kind=all` → 聚合所有

`catalog.override` 和 `catalog.validate` 直接从 `tools_system.go` 搬运，handler 不变。

- [ ] **Step 2: 创建 tools_central.go**

3 个工具：
- `central.sync(action=push|pull|status)` — 分发到 `deps.SyncPush/SyncPull/SyncStatus`
- `central.advise(action=request|feedback)` — 分发到 `deps.RequestAdvise/AdvisoryFeedback`
- `central.scenario(action=generate|list)` — 分发到 `deps.RequestScenario/ListCentralScenarios`

- [ ] **Step 3: 创建 tools_data.go**

2 个工具：`data.export` 和 `data.import`，从 `tools_knowledge.go` 搬运 handler 不变，只改工具名。

- [ ] **Step 4: 创建 tools_automation.go**

4 个合并工具：
- `patrol(action=status|alerts|config|actions)` — 分发到 `deps.PatrolStatus/PatrolAlerts/PatrolConfig/PatrolActions`
- `explore(action=start|status|stop|result)` — 分发到 `deps.ExploreStart/ExploreStatus/ExploreStop/ExploreResult`
- `tuning(action=start|status|stop|results)` — 分发到 `deps.TuningStart/TuningStatus/TuningStop/TuningResults`
- `explorer(action=status|config|trigger)` — 分发到 `deps.ExplorerStatus/ExplorerConfig/ExplorerTrigger`

每个工具的 `action` 是 required 参数。子参数按原工具定义保留。

- [ ] **Step 5: 创建 tools_fleet.go**

2 个工具：
- `fleet.info` — 无 `device_id` 调用 `deps.FleetListDevices()`，有 `device_id` 调用 `deps.FleetDeviceInfo()` + `deps.FleetDeviceTools()` 合并返回
- `fleet.exec` — 从 `fleet.exec_tool` 简化名称，handler 不变

- [ ] **Step 6: 创建 tools_scenario.go**

2 个工具：`scenario.show` 和 `scenario.apply`，直接从 `tools_integration.go` 搬运。

- [ ] **Step 7: 创建 tools_openclaw.go**

1 个工具：`openclaw(action=sync|status|claim)` — 分发到 `deps.OpenClawSync/OpenClawStatus/OpenClawClaim`。

- [ ] **Step 8: 创建 tools_stack.go**

1 个工具：`stack(action=status|preflight|init)` — 分发到 `deps.StackStatus/StackPreflight/StackInit`。

- [ ] **Step 9: 提交**

```bash
git add internal/mcp/tools_catalog.go internal/mcp/tools_central.go internal/mcp/tools_data.go \
        internal/mcp/tools_automation.go internal/mcp/tools_fleet.go internal/mcp/tools_scenario.go \
        internal/mcp/tools_openclaw.go internal/mcp/tools_stack.go
git commit -m "refactor(mcp): create 8 new tool files for consolidated namespace layout"
```

---

### Task 4: 重写 tools_agent.go (18 → 4) 并更新剩余工具文件

**Files:**
- Rewrite: `internal/mcp/tools_agent.go` — 仅保留 agent.ask, agent.status, agent.rollback (合并 rollback_list + rollback), support
- Modify: `internal/mcp/tools_model.go` — 删除 `download.list`
- Modify: `internal/mcp/tools_engine.go` — 删除 `engine.plan`
- Modify: `internal/mcp/tools_deploy.go` — `deploy.dry_run` 新增 `output` 参数

- [ ] **Step 1: 重写 tools_agent.go**

保留 4 个工具：
- `support` — 原 `support.askforhelp`，简化名称，handler 不变
- `agent.ask` — 不变
- `agent.status` — 不变
- `agent.rollback(action=list|restore)` — 合并 `rollback_list` + `rollback`

删除：`agent.guide` (静态文本移入 prompt)

patrol/explore/tuning 相关已移到 `tools_automation.go`。

- [ ] **Step 2: tools_model.go 删除 download.list**

删除 `download.list` 工具注册（约 150-166 行）。

- [ ] **Step 3: tools_engine.go 删除 engine.plan**

删除 `engine.plan` 工具注册（约 166-182 行）。

- [ ] **Step 4: tools_deploy.go 更新 deploy.dry_run**

给 `deploy.dry_run` 新增 `output` 参数：

```go
"output":{"type":"string","enum":["config","pod_yaml"],"description":"Output format: config (default) or pod_yaml (K3S Pod YAML manifest)"}
```

Handler 内：当 `output == "pod_yaml"` 时调用 `deps.GeneratePod()` 而非 `deps.DeployDryRun()`。

- [ ] **Step 5: 提交**

```bash
git add internal/mcp/tools_agent.go internal/mcp/tools_model.go internal/mcp/tools_engine.go internal/mcp/tools_deploy.go
git commit -m "refactor(mcp): rewrite tools_agent (18→4), delete download.list/engine.plan, dry_run absorbs generate_pod"
```

---

### Task 5: 重写 tools_system.go 并删除旧文件

**Files:**
- Rewrite: `internal/mcp/tools_system.go` — 只保留 system.status + system.config
- Delete: `internal/mcp/tools_integration.go`
- Delete: `internal/mcp/tools_explorer.go`
- Delete: `internal/mcp/tools_scenario_central.go`

- [ ] **Step 1: 重写 tools_system.go**

只保留 `system.status` 和 `system.config`（约 80 行）。删除 shell.exec, discover.lan, catalog.*, stack.* 的注册（已迁移到各自文件）。

保留 `isCommandAllowed()` 和 `hasAnySafePrefix()` 辅助函数在 `tools.go` 中（若有其他消费者），否则连同删除。

- [ ] **Step 2: 删除旧文件**

```bash
git rm internal/mcp/tools_integration.go internal/mcp/tools_explorer.go internal/mcp/tools_scenario_central.go
```

这些文件的工具已全部迁移到新文件中。

- [ ] **Step 3: 提交**

```bash
git add internal/mcp/tools_system.go
git commit -m "refactor(mcp): slim tools_system to 2 tools, delete 3 obsolete tool files"
```

---

### Task 6: 更新 tools.go — Profile 定义 + RegisterAllTools

**Files:**
- Modify: `internal/mcp/tools.go`

- [ ] **Step 1: 更新 profileIncludes**

替换为新 Profile 定义（spec §4.3）：

```go
var profileIncludes = map[Profile][]string{
	ProfileOperator: {
		"hardware.", "model.", "engine.", "deploy.",
		"system.", "fleet.", "scenario.",
		"catalog.list",
		"benchmark.run", "benchmark.list",
		"knowledge.resolve", "knowledge.search", "knowledge.promote",
		"agent.ask", "agent.status", "agent.rollback",
		"openclaw", "support",
	},
	ProfilePatrol: {
		"hardware.metrics",
		"deploy.list", "deploy.status", "deploy.logs", "deploy.apply",
		"deploy.approve", "deploy.dry_run",
		"knowledge.resolve",
		"benchmark.run",
		"patrol",
	},
	ProfileExplorer: {
		"hardware.detect", "hardware.metrics",
		"deploy.apply", "deploy.approve", "deploy.dry_run", "deploy.status",
		"deploy.list", "deploy.logs", "deploy.delete",
		"benchmark.run", "benchmark.record", "benchmark.list",
		"knowledge.resolve", "knowledge.search", "knowledge.promote", "knowledge.save",
		"explore", "tuning", "explorer",
		"central.advise",
	},
}
```

- [ ] **Step 2: 更新 RegisterAllTools**

```go
func RegisterAllTools(s *Server, deps *ToolDeps) {
	registerHardwareTools(s, deps)
	registerModelTools(s, deps)
	registerEngineTools(s, deps)
	registerDeployTools(s, deps)
	registerKnowledgeTools(s, deps)
	registerBenchmarkTools(s, deps)
	registerSystemTools(s, deps)
	registerCatalogTools(s, deps)
	registerCentralTools(s, deps)
	registerDataTools(s, deps)
	registerAgentTools(s, deps)
	registerAutomationTools(s, deps)
	registerFleetTools(s, deps)
	registerScenarioTools(s, deps)
	registerOpenClawTools(s, deps)
	registerStackTools(s, deps)
}
```

- [ ] **Step 3: 删除 isCommandAllowed 和 hasAnySafePrefix**

这两个函数原为 `shell.exec` 使用，该工具已被删除。从 `tools.go` 中删除（约 134-221 行）。

- [ ] **Step 4: 验证编译**

```bash
go build ./internal/mcp/...
```

Expected: PASS（所有注册函数都有对应文件）。

- [ ] **Step 5: 提交**

```bash
git add internal/mcp/tools.go
git commit -m "refactor(mcp): update profiles for 56-tool set, rewire RegisterAllTools"
```

---

### Task 7: server.go 新增 ListToolsForProfile

**Files:**
- Modify: `internal/mcp/server.go`

- [ ] **Step 1: 新增 ListToolsForProfile 方法**

在 `ListTools()` 方法后添加：

```go
// ListToolsForProfile returns tool definitions filtered by the given profile.
// Used by Agent to limit which tools the LLM sees.
func (s *Server) ListToolsForProfile(p Profile) []ToolDefinition {
	s.mu.RLock()
	defer s.mu.RUnlock()

	defs := make([]ToolDefinition, 0, len(s.tools))
	for _, t := range s.tools {
		if !ProfileMatches(p, t.Name) {
			continue
		}
		defs = append(defs, ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return defs
}
```

- [ ] **Step 2: 写测试**

在 `internal/mcp/mcp_test.go` 中添加：

```go
func TestListToolsForProfile(t *testing.T) {
	s := NewServer()
	deps := &ToolDeps{}
	// Register a minimal set to test profile filtering
	s.RegisterTool(&Tool{
		Name: "hardware.detect", Description: "test", InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, p json.RawMessage) (*ToolResult, error) { return TextResult("ok"), nil },
	})
	s.RegisterTool(&Tool{
		Name: "patrol", Description: "test", InputSchema: noParamsSchema(),
		Handler: func(ctx context.Context, p json.RawMessage) (*ToolResult, error) { return TextResult("ok"), nil },
	})
	_ = deps // suppress unused

	// ProfileFull should return all
	all := s.ListToolsForProfile(ProfileFull)
	if len(all) != 2 {
		t.Fatalf("ProfileFull: got %d, want 2", len(all))
	}

	// ProfilePatrol should include hardware.metrics but not hardware.detect
	// (hardware.detect is not in patrol profile)
	patrol := s.ListToolsForProfile(ProfilePatrol)
	for _, d := range patrol {
		if d.Name == "hardware.detect" {
			t.Error("ProfilePatrol should not include hardware.detect")
		}
	}
	// patrol tool should be included
	found := false
	for _, d := range patrol {
		if d.Name == "patrol" {
			found = true
		}
	}
	if !found {
		t.Error("ProfilePatrol should include patrol")
	}
}
```

- [ ] **Step 3: 运行测试**

```bash
go test ./internal/mcp/... -run TestListToolsForProfile -v
```

Expected: PASS

- [ ] **Step 4: 提交**

```bash
git add internal/mcp/server.go internal/mcp/mcp_test.go
git commit -m "refactor(mcp): add ListToolsForProfile for profile-aware tool discovery"
```

---

### Task 8: agent.go 新增 WithProfile + adapter 更新

**Files:**
- Modify: `internal/agent/agent.go`
- Modify: `cmd/aima/adapters.go`
- Modify: `cmd/aima/main.go` (where Agent is created)

- [ ] **Step 1: 扩展 ToolExecutor 接口**

在 `internal/agent/agent.go` 的 `ToolExecutor` 接口中添加可选的 profile 支持：

```go
// ToolExecutor executes MCP tools (provided by mcp.Server).
type ToolExecutor interface {
	ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error)
	ListTools() []ToolDefinition
}

// ProfiledToolExecutor extends ToolExecutor with profile-aware listing.
type ProfiledToolExecutor interface {
	ToolExecutor
	ListToolsForProfile(profile string) []ToolDefinition
}
```

- [ ] **Step 2: 给 Agent 添加 WithProfile option**

```go
// Agent struct 新增字段
type Agent struct {
	llm      LLMClient
	tools    ToolExecutor
	maxTurns int
	sessions *SessionStore
	profile  string // tool visibility profile

	mu             sync.RWMutex
	mode           toolMode
	modeDetectedAt time.Time
}

// WithProfile sets the tool visibility profile.
func WithProfile(p string) AgentOption {
	return func(a *Agent) {
		a.profile = p
	}
}
```

- [ ] **Step 3: 修改 AskStream 使用 profile 过滤**

在 `AskStream()` 中，将 `allTools := a.tools.ListTools()` 改为：

```go
var allTools []ToolDefinition
if a.profile != "" {
	if pt, ok := a.tools.(ProfiledToolExecutor); ok {
		allTools = pt.ListToolsForProfile(a.profile)
	} else {
		allTools = a.tools.ListTools()
	}
} else {
	allTools = a.tools.ListTools()
}
```

- [ ] **Step 4: 更新 mcpToolAdapter**

在 `cmd/aima/adapters.go` 中：

```go
func (a *mcpToolAdapter) ListToolsForProfile(profile string) []agent.ToolDefinition {
	mcpDefs := a.server.ListToolsForProfile(mcp.Profile(profile))
	defs := make([]agent.ToolDefinition, len(mcpDefs))
	for i, d := range mcpDefs {
		defs[i] = agent.ToolDefinition{
			Name:        d.Name,
			Description: d.Description,
			InputSchema: d.InputSchema,
		}
	}
	return defs
}

func (a *automationToolAdapter) ListToolsForProfile(profile string) []agent.ToolDefinition {
	return a.base.ListToolsForProfile(profile)
}
```

- [ ] **Step 5: main.go 传 Profile**

在创建 Agent 的地方（`cmd/aima/main.go`），给用户 Agent 传 `ProfileOperator`，给 Explorer Agent 传 `ProfileExplorer`：

```go
goAgent := agent.NewAgent(llm, toolAdapter, agent.WithProfile("operator"), agent.WithSessions(sessions))
// ...
explorerAgent := agent.NewAgent(llm, automationAdapter, agent.WithProfile("explorer"))
```

- [ ] **Step 6: 运行测试**

```bash
go test ./internal/agent/... -v
go test ./cmd/aima/... -v
```

Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add internal/agent/agent.go cmd/aima/adapters.go cmd/aima/main.go
git commit -m "refactor(agent): add WithProfile, Agent LLM now sees profile-filtered tools"
```

---

### Task 9: 重写 Agent system prompt

**Files:**
- Rewrite: `internal/agent/prompt.md`

- [ ] **Step 1: 替换 prompt.md 内容**

用设计文档 §5.2 的新 prompt 替换整个文件（spec 中已有完整内容）。确保覆盖 ProfileOperator 全部 39 个工具，按用户意图组织。

- [ ] **Step 2: 提交**

```bash
git add internal/agent/prompt.md
git commit -m "refactor(agent): rewrite system prompt to cover all 39 ProfileOperator tools"
```

---

### Task 10: 更新 tooldeps 布线文件

合并工具的新注册函数需要 ToolDeps 中的函数，而部分被删工具的布线也要清理。

**Files:**
- Modify: `cmd/aima/tooldeps_knowledge.go` — 删除废弃布线 (GeneratePod, ListKnowledgeSummary 等已移入新工具)
- Modify: `cmd/aima/tooldeps_agent.go` — 删除 AgentGuide 布线
- Modify: `cmd/aima/tooldeps_system.go` — 删除 ExecShell, DiscoverLAN 布线
- Modify: `cmd/aima/tooldeps_integration.go` — 删除 AppRegister/Provision/List, PowerHistory, PowerMode 布线
- Modify: `cmd/aima/tooldeps_fleet.go` — 保持不变

- [ ] **Step 1: 清理各 tooldeps 文件中被删 ToolDeps 字段的赋值**

搜索所有 `deps.EnginePlan =`、`deps.ListDownloads =`、`deps.DiscoverLAN =`、`deps.AgentGuide =`、`deps.PowerHistory =`、`deps.PowerMode =`、`deps.AppRegister =`、`deps.AppProvision =`、`deps.AppList =`、`deps.ExecShell =` 并删除。

- [ ] **Step 2: 验证编译**

```bash
go build ./cmd/aima/...
```

Expected: PASS

- [ ] **Step 3: 运行全量测试**

```bash
go test ./... -count=1
```

Expected: 部分测试因工具名变更失败（Task 11 处理）。

- [ ] **Step 4: 提交**

```bash
git add cmd/aima/tooldeps_*.go
git commit -m "refactor(mcp): clean up tooldeps wiring for deleted tools"
```

---

### Task 11: 更新测试

**Files:**
- Modify: `internal/mcp/mcp_test.go` — 更新工具名引用
- Modify: `cmd/aima/*_test.go` — 更新工具名引用
- Modify: `internal/agent/agent_test.go` — 如有工具名硬编码则更新
- Modify: `cmd/aima/v040_release_gate_test.go` — 更新工具数量断言

- [ ] **Step 1: 搜索旧工具名引用**

```bash
grep -rn 'knowledge\.list_profiles\|knowledge\.list_engines\|knowledge\.list_models\|knowledge\.search_configs\|knowledge\.compare\|knowledge\.similar\|knowledge\.lineage\|knowledge\.gaps\|knowledge\.aggregate\|knowledge\.generate_pod\|knowledge\.export\|knowledge\.import\|knowledge\.validate\|knowledge\.engine_switch_cost\|knowledge\.open_questions\|knowledge\.sync_push\|knowledge\.sync_pull\|knowledge\.sync_status\|knowledge\.advise\|knowledge\.advisory_feedback\|knowledge\.list[^_]\|agent\.guide\|agent\.patrol_status\|agent\.alerts\|agent\.patrol_config\|agent\.patrol_actions\|agent\.rollback_list\|explore\.start\|explore\.status\|explore\.stop\|explore\.result\|tuning\.start\|tuning\.status\|tuning\.stop\|tuning\.results\|explorer\.status\|explorer\.config\|explorer\.trigger\|fleet\.list_devices\|fleet\.device_info\|fleet\.device_tools\|fleet\.exec_tool\|scenario\.list\|scenario\.generate\|scenario\.list_central\|support\.askforhelp\|shell\.exec\|discover\.lan\|download\.list\|engine\.plan\|device\.power_history\|device\.power_mode\|app\.register\|app\.provision\|app\.list\|openclaw\.sync\|openclaw\.status\|openclaw\.claim\|stack\.preflight\|stack\.init\|stack\.status\|catalog\.status' --include='*_test.go' .
```

- [ ] **Step 2: 更新所有测试文件中的旧工具名**

按映射表批量替换。对于合并工具，更新测试调用方式（增加 action 参数）。

- [ ] **Step 3: 更新 v040_release_gate_test.go 中的工具数断言**

如果有断言 `len(tools) == 101`，改为 `len(tools) == 56`。

- [ ] **Step 4: 运行全量测试**

```bash
go test ./... -count=1
```

Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add -A '*_test.go'
git commit -m "refactor(mcp): update all tests for 56-tool naming"
```

---

### Task 12: 更新 CLI 命令

**Files:**
- Modify: `internal/cli/knowledge.go` — 更新工具调用名
- Modify: `internal/cli/scenario.go` — 删除 list 子命令
- Modify: `internal/cli/fleet.go` — 更新工具名
- Modify: `internal/cli/explorer.go` — 更新工具调用
- Delete: `internal/cli/app.go` — app.* 已删除
- Delete: `internal/cli/discover.go` — discover.lan 已删除
- Modify: `internal/cli/root.go` — 删除废弃子命令注册

- [ ] **Step 1: 更新 knowledge CLI**

`aima knowledge list-profiles` → 调用 `catalog.list(kind=profiles)`
`aima knowledge list-engines` → 调用 `catalog.list(kind=engines)`
`aima knowledge list-models` → 调用 `catalog.list(kind=models)`
`aima knowledge search-configs` → 调用 `knowledge.search(scope=configs)`
`aima knowledge export` → 调用 `data.export`
`aima knowledge import` → 调用 `data.import`
`aima knowledge sync push` → 调用 `central.sync(action=push)`
`aima knowledge sync pull` → 调用 `central.sync(action=pull)`
`aima knowledge sync status` → 调用 `central.sync(action=status)`

- [ ] **Step 2: 更新 fleet CLI**

`fleet list` → `fleet.info`（无 device_id）
`fleet info <id>` → `fleet.info(device_id=<id>)`
`fleet exec <id> <tool>` → `fleet.exec`

- [ ] **Step 3: 更新 explorer CLI**

调用合并后的 `explorer(action=status|config|trigger)` 工具。

- [ ] **Step 4: 删除废弃 CLI 文件**

```bash
git rm internal/cli/app.go internal/cli/discover.go
```

- [ ] **Step 5: 更新 root.go**

从根命令中删除 app 和 discover 子命令注册。

- [ ] **Step 6: 验证编译和测试**

```bash
go build ./cmd/aima/...
go test ./internal/cli/... -count=1
```

Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add internal/cli/
git commit -m "refactor(cli): update commands for 56-tool naming, delete app/discover"
```

---

### Task 13: 更新文档和内存

**Files:**
- Modify: `CLAUDE.md` — 更新工具数（94→56）
- Modify: memory files

- [ ] **Step 1: 更新 CLAUDE.md 工具数**

搜索 "94 MCP tools" 替换为 "56 MCP tools"。搜索 "101" 替换相关描述。

- [ ] **Step 2: 全量验证**

```bash
go build ./...
go test ./... -count=1
go vet ./...
```

Expected: ALL PASS

- [ ] **Step 3: 提交**

```bash
git add CLAUDE.md
git commit -m "docs: update tool count to 56 after MCP consolidation"
```

---

### Task 14: 最终验证和清理

- [ ] **Step 1: 验证工具数**

```bash
go test -run TestToolCount ./internal/mcp/... -v
```

或者在测试中写一个断言：注册的工具数 == 56。

- [ ] **Step 2: 验证 Profile 过滤**

手动或通过测试验证：
- `ListToolsForProfile(ProfileOperator)` 返回 39 个工具
- `ListToolsForProfile(ProfilePatrol)` 返回 10 个工具
- `ListToolsForProfile(ProfileExplorer)` 返回 20 个工具
- `ListToolsForProfile(ProfileFull)` 返回 56 个工具

- [ ] **Step 3: go vet + race detector**

```bash
go vet ./...
go test -race ./... -count=1
```

Expected: PASS

- [ ] **Step 4: 提交最终清理**

```bash
git add -A
git commit -m "refactor(mcp): final cleanup — MCP tool consolidation complete (101→56)"
```
