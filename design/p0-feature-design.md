# AIMA P0 Feature Design — 三项核心功能详细设计

> 基于 develop 分支代码审计 (2026-03-06, 80 MCP tools)
> 目标: 让优化模型的 L0→L2c 配置链和 Agent 自治巡检真正闭环运转
> **状态: ✅ 全部实现并验证 (2026-03-06)** — 7/8 设备通过真机验证 (hygon 不可达)

---

## 目录

1. [Feature A: L2c Golden 配置注入 Resolve 链](#feature-a-l2c-golden-配置注入-resolve-链)
2. [Feature B: 时间约束 + 放大器纳入引擎选择](#feature-b-时间约束--放大器纳入引擎选择)
3. [Feature C: Agent 巡检循环 (A2)](#feature-c-agent-巡检循环-a2)
4. [跨功能依赖关系](#跨功能依赖关系)
5. [实施顺序与验收标准](#实施顺序与验收标准)

---

## Feature A: L2c Golden 配置注入 Resolve 链

### A.1 问题陈述

PRD §3 定义了 5 层配置解析: L0(默认值) → L2a(Engine Asset) → L2b(Model Variant) → L2c(调优最优) → L1(用户覆盖)。

当前 `Catalog.Resolve()` (`resolver.go:107`) 只实现了 L0 → L2b → L1:

```
当前:  L0(engine default_args) → L0(variant default_config) → L1(user overrides)
目标:  L0(engine default_args) → L0(variant default_config) → L2c(golden config) → L1(user overrides)
```

auto-promote 机制已存在 (`maybeAutoPromote()` in `main.go`)，benchmark 后吞吐量超过当前 golden 5% 会自动晋升为 golden。但 golden 配置只存在 SQLite 中，**从未被 Resolve() 读取**。调优的最优结果永远不会影响下一次部署。

### A.2 当前数据流

```
benchmark.run → InsertBenchmarkResult() → maybeAutoPromote()
                                              ↓
                                    configurations.status = 'golden'
                                              ↓
                                         (死数据)
                                              ↓ ← 这里断了
Resolve() → L0 → L2b → L1 → ResolvedConfig
            ↑ 没有读 golden config
```

### A.3 目标数据流

```
benchmark.run → InsertBenchmarkResult() → maybeAutoPromote()
                                              ↓
                                    configurations.status = 'golden'
                                              ↓
Resolve() → L0 → L2b → L2c(golden) → L1 → ResolvedConfig
                         ↑
                   FindGoldenConfig(hw, engine, model)
                   从 configurations 表读取 golden 记录的 config JSON
```

### A.4 详细设计

#### A.4.1 新增接口: GoldenConfigFunc

Resolve() 当前是 Catalog 的方法，Catalog 是纯 YAML 数据结构，不持有 DB 引用。
不应让 Catalog 直接依赖 `state.DB`，这违反了知识层与状态层的分离。

方案: 通过 `ResolveOption` 注入 golden config 查找函数。

```go
// resolver.go — 新增

// GoldenConfigFunc returns the golden (L2c) config overrides for a hardware/engine/model triple.
// Returns nil map if no golden config exists (graceful degradation).
type GoldenConfigFunc func(hardware, engine, model string) map[string]any

// WithGoldenConfig injects L2c lookup into the resolve chain.
func WithGoldenConfig(fn GoldenConfigFunc) ResolveOption {
    return func(o *resolveOpts) { o.GoldenConfig = fn }
}
```

扩展 `resolveOpts`:

```go
type resolveOpts struct {
    MaxColdStartS int
    GoldenConfig  GoldenConfigFunc // L2c lookup (nil = skip)
}
```

#### A.4.2 修改 Resolve() — 在 L2b 和 L1 之间插入 L2c

`resolver.go:150-174` 当前的配置合并顺序:

```go
// 当前:
// L0: Engine default_args
for k, v := range engine.Startup.DefaultArgs { ... provenance[k] = "L0" }
// L0 (model variant): variant default_config
if variant != nil { for k, v := range variant.DefaultConfig { ... provenance[k] = "L0" } }
// L1: User overrides
for k, v := range userOverrides { ... provenance[k] = "L1" }
```

修改为:

```go
// L0: Engine default_args
for k, v := range engine.Startup.DefaultArgs {
    config[k] = v
    provenance[k] = "L0"
}

// L0 (model variant): variant default_config
if variant != nil {
    for k, v := range variant.DefaultConfig {
        config[k] = v
        provenance[k] = "L0"
    }
}

// -- 新增: L2c Golden config (from benchmark-promoted optimal) --
if ropts.GoldenConfig != nil {
    hwID := hw.GPUArch // 与 configurations.hardware_id 对齐
    goldenOverrides := ropts.GoldenConfig(hwID, engineType, modelName)
    for k, v := range goldenOverrides {
        config[k] = v
        provenance[k] = "L2c"
    }
}

// L1: User overrides (highest non-Agent priority)
for k, v := range userOverrides {
    if k == "model_path" || k == "partition" || k == "slot" {
        continue
    }
    config[k] = v
    provenance[k] = "L1"
}
```

**关键语义**: L1 (用户显式指定) 仍然覆盖 L2c，确保人类始终能 override Agent 的推荐。

#### A.4.3 Resolve() 需要解析 ResolveOption

当前 `Resolve()` 签名已有 `opts ...ResolveOption`，但函数体内只用于 `InferEngineType()` 的传递。
需要在 `Resolve()` 函数体开头也解析 opts:

```go
func (c *Catalog) Resolve(hw HardwareInfo, modelName, engineType string, userOverrides map[string]any, opts ...ResolveOption) (*ResolvedConfig, error) {
    // 解析 options (新增)
    var ropts resolveOpts
    for _, o := range opts {
        o(&ropts)
    }

    // Auto-detect engine (已有, opts 已传递)
    if engineType == "" {
        inferred, err := c.InferEngineType(modelName, hw, opts...)
        ...
    }
    ...
    // 在 config 合并阶段使用 ropts.GoldenConfig
}
```

#### A.4.4 在 main.go 中接入 GoldenConfig

`cmd/aima/main.go` 的 `buildToolDeps()` 闭包中构造 Resolve 调用。需要:

1. 从 `state.DB` 构造 `GoldenConfigFunc`
2. 在所有调用 `catalog.Resolve()` 的地方传入 `WithGoldenConfig(...)`

```go
// main.go — buildToolDeps() 内部

goldenConfigFn := func(hardware, engine, model string) map[string]any {
    cfg, _, err := db.FindGoldenBenchmark(context.Background(), hardware, engine, model)
    if err != nil || cfg == nil {
        return nil
    }
    var configMap map[string]any
    if err := json.Unmarshal([]byte(cfg.Config), &configMap); err != nil {
        slog.Debug("L2c: failed to parse golden config", "error", err)
        return nil
    }
    return configMap
}

// 然后在所有 catalog.Resolve() 调用点添加:
resolved, err := catalog.Resolve(hwInfo, model, engine, overrides,
    knowledge.WithGoldenConfig(goldenConfigFn),
    // ...existing opts...
)
```

Resolve() 在 main.go 中的调用点 (需逐一添加 option):
- `deploy.apply` handler
- `deploy.dry_run` handler (dry-run 也应反映 L2c)
- 其他通过 catalog.Resolve 的路径

#### A.4.5 hardware_id 对齐问题

`FindGoldenBenchmark` 按 `hardware_id, engine_id, model_id` 查询。当前 `configurations` 表的 `hardware_id` 是 benchmark 录入时的自由文本。需要确保:

- benchmark.record 录入时 `hardware_id` = `hw.GPUArch` (当前由调用方传入)
- Resolve 查询时用同一个 key: `hw.GPUArch`

如果存在不一致，需要做标准化映射。最简方案: 查询时同时尝试 GPUArch 和 HardwareProfile name。
但首版保持简单——约定 `hardware_id` 统一使用 GPUArch 字符串 (如 "Ada", "GB10", "MUSA")。

#### A.4.6 安全考量

- **Golden config 可能包含过时参数**: 如果引擎升级后某参数名变了，golden config 会注入无效参数。
  缓解: L1 用户覆盖仍然生效; golden 参数如果不在引擎已知参数列表中，可以 warn 但不阻止。
- **Golden config 可能来自不同硬件实例**: GPUArch = "Ada" 匹配 RTX 4060 和 4090，
  但 4060 的 golden gmu=0.5 可能对 4090 太保守。
  缓解: 首版接受此限制。后续可扩展为 `hardware_id + gpu_model` 联合匹配。

#### A.4.7 影响面

| 文件 | 变更 |
|------|------|
| `internal/knowledge/resolver.go` | 新增 `GoldenConfigFunc` 类型, `WithGoldenConfig()` option, `resolveOpts.GoldenConfig` 字段, Resolve() 解析 opts + 插入 L2c 合并 |
| `cmd/aima/main.go` | 构造 `goldenConfigFn` 闭包, 所有 `catalog.Resolve()` 调用点添加 option |
| (无新文件) | |

代码量估计: ~40 行新增, ~10 行修改。

#### A.4.8 测试策略

```go
// resolver_test.go — 新增 test case

func TestResolve_L2cGoldenOverride(t *testing.T) {
    catalog := buildTestCatalog() // 已有 helper

    // Simulate golden config: gmu=0.7 (YAML default is 0.9)
    golden := func(hw, engine, model string) map[string]any {
        if hw == "Ada" && model == "qwen3-8b" {
            return map[string]any{"gpu_memory_utilization": 0.7}
        }
        return nil
    }

    // Case 1: L2c overrides L0
    rc, err := catalog.Resolve(
        HardwareInfo{GPUArch: "Ada", GPUVRAMMiB: 24576},
        "qwen3-8b", "vllm", nil,
        WithGoldenConfig(golden),
    )
    // gmu should be 0.7 (from golden), not 0.9 (from YAML default)
    assert(rc.Config["gpu_memory_utilization"] == 0.7)
    assert(rc.Provenance["gpu_memory_utilization"] == "L2c")

    // Case 2: L1 overrides L2c
    rc2, _ := catalog.Resolve(
        HardwareInfo{GPUArch: "Ada", GPUVRAMMiB: 24576},
        "qwen3-8b", "vllm",
        map[string]any{"gpu_memory_utilization": 0.5},
        WithGoldenConfig(golden),
    )
    assert(rc2.Config["gpu_memory_utilization"] == 0.5)
    assert(rc2.Provenance["gpu_memory_utilization"] == "L1")

    // Case 3: No golden config -> graceful degradation
    noGolden := func(hw, engine, model string) map[string]any { return nil }
    rc3, _ := catalog.Resolve(
        HardwareInfo{GPUArch: "Ada", GPUVRAMMiB: 24576},
        "qwen3-8b", "vllm", nil,
        WithGoldenConfig(noGolden),
    )
    assert(rc3.Config["gpu_memory_utilization"] == 0.9) // L0 default
    assert(rc3.Provenance["gpu_memory_utilization"] == "L0")

    // Case 4: nil GoldenConfig option (not provided) -> skip L2c entirely
    rc4, _ := catalog.Resolve(
        HardwareInfo{GPUArch: "Ada", GPUVRAMMiB: 24576},
        "qwen3-8b", "vllm", nil,
    )
    assert(rc4.Config["gpu_memory_utilization"] == 0.9)
}
```

---

## Feature B: 时间约束 + 放大器纳入引擎选择

### B.1 问题陈述

PRD §3 形式化定义:
> (4) 对每个 di: startup_time(pj) <= max_switch_time(di) -- 时间约束

当前 `InferEngineType()` (`resolver.go:304`) 已有:
- `engineCandidate.coldStartS` 字段 -- 已解析
- `WithMaxColdStart(s)` option -- 已定义
- cold start 过滤逻辑 -- 已实现 (`resolver.go:372-383`)
- amplifier 排序 -- 按 `performance_multiplier` 降序 (`resolver.go:387-402`)

**但这些能力没有被实际使用:**

1. **deploy pipeline 从未传入 `WithMaxColdStart`** -- `main.go` 中的 `deploy.apply` handler 调用 `catalog.Resolve()` 时不传时间约束
2. **App 的 `max_cold_start_s` 不参与引擎选择** -- `app.provision` 只检查已有部署，不影响新部署的引擎选择
3. **放大器排序虽然存在，但缺少实际 benchmark 数据反哺** -- `performance_multiplier` 是 YAML 静态值，不随实测数据更新

### B.2 目标

三个子目标:

**B.2.1** deploy pipeline 支持时间约束过滤

```
用户: aima deploy qwen3-8b --max-cold-start 30
      -> InferEngineType 排除 cold_start > 30s 的引擎
      -> vllm (cold_start 30-60s) 被过滤, llamacpp (3-10s) 胜出
```

**B.2.2** App provision 的时间约束传递到引擎选择

```
app.register: max_cold_start_s: 30
app.provision: -> deploy.apply with WithMaxColdStart(30)
              -> 引擎选择自动排除启动慢的引擎
```

**B.2.3** 放大器评估基于实测数据增强 (P1 级别, 此处只做架构预留)

### B.3 详细设计

#### B.3.1 deploy.apply 支持 max_cold_start_s 参数

**修改文件**: `internal/mcp/tools.go` -- deploy.apply schema

当前 deploy.apply schema 的 `inputSchema.properties`:
```json
{
  "model": {"type": "string"},
  "engine": {"type": "string"},
  "config_overrides": {"type": "object"},
  "slot": {"type": "string"},
  ...
}
```

新增:
```json
"max_cold_start_s": {
  "type": "integer",
  "description": "Maximum acceptable cold start time in seconds. Engines exceeding this threshold are excluded from auto-selection. 0 or omitted means no constraint."
}
```

**修改文件**: `cmd/aima/main.go` -- deploy.apply handler

```go
// 在 deploy.apply handler 中提取时间约束
var resolveOpts []knowledge.ResolveOption

if maxCS, ok := args["max_cold_start_s"].(float64); ok && maxCS > 0 {
    resolveOpts = append(resolveOpts, knowledge.WithMaxColdStart(int(maxCS)))
}

// 加上 L2c (Feature A)
resolveOpts = append(resolveOpts, knowledge.WithGoldenConfig(goldenConfigFn))

resolved, err := catalog.Resolve(hwInfo, model, engine, overrides, resolveOpts...)
```

同样修改 `deploy.dry_run` handler，使 dry-run 输出也反映时间约束过滤结果。

**修改文件**: `internal/cli/root.go` -- deploy 命令新增 CLI flag

```go
var maxColdStartS int
deployCmd.Flags().IntVar(&maxColdStartS, "max-cold-start", 0,
    "Max acceptable cold start time in seconds (0=no constraint)")
```

CLI handler 将 flag 值传入 MCP 工具参数:
```go
if maxColdStartS > 0 {
    args["max_cold_start_s"] = maxColdStartS
}
```

#### B.3.2 deploy.dry_run 输出增强

dry-run 响应已包含 `cold_start_s` 和 `startup_time_s`。新增:
- `engine_selection_reason`: 说明为何选择了该引擎
- `filtered_engines`: 被时间约束过滤掉的引擎列表

这需要 `InferEngineType()` 返回更多信息。方案: 新增 `InferEngineTypeDetailed()` 方法。

```go
// resolver.go -- 新增

// EngineSelection describes the engine selection result with reasoning.
type EngineSelection struct {
    EngineType       string            `json:"engine_type"`
    Multiplier       float64           `json:"performance_multiplier"`
    ColdStartS       int               `json:"cold_start_s_max"`
    Offload          bool              `json:"offload_path"`
    FilteredEngines  []FilteredEngine  `json:"filtered_engines,omitempty"`
}

// FilteredEngine records an engine that was considered but excluded.
type FilteredEngine struct {
    EngineType string `json:"engine_type"`
    Reason     string `json:"reason"` // "cold_start_exceeded", "vram_insufficient", "format_incompatible"
    ColdStartS int    `json:"cold_start_s,omitempty"`
}

// InferEngineTypeDetailed is like InferEngineType but returns selection reasoning.
func (c *Catalog) InferEngineTypeDetailed(modelName string, hw HardwareInfo, opts ...ResolveOption) (*EngineSelection, error) {
    // 复用 InferEngineType 的核心逻辑, 但收集 filtered 信息
    // (具体实现: 在 candidate 过滤阶段记录被排除的原因)
    ...
}
```

dry-run handler 使用 `InferEngineTypeDetailed()`:
```go
// main.go -- deploy.dry_run handler
if engine == "" {
    selection, err := catalog.InferEngineTypeDetailed(model, hwInfo, resolveOpts...)
    if err == nil {
        engine = selection.EngineType
        dryRunResult["engine_selection"] = selection
    }
}
```

#### B.3.3 App provision 传递时间约束

**修改文件**: `cmd/aima/main.go` -- `app.provision` handler

当前 provision 只检查已有部署是否满足 app 依赖。新增 `auto_deploy` 参数:

MCP schema 新增:
```json
"auto_deploy": {
  "type": "boolean",
  "description": "If true, automatically deploy missing inference services. Default false."
}
```

Handler 逻辑:
```go
// app.provision handler
autoDeploy, _ := args["auto_deploy"].(bool)

for _, dep := range app.Dependencies {
    if dep.Satisfied {
        continue
    }

    if autoDeploy {
        var deployOpts []knowledge.ResolveOption
        // 从 app 的 time_constraints 传递到引擎选择
        if app.TimeConstraints.MaxColdStartS > 0 {
            deployOpts = append(deployOpts, knowledge.WithMaxColdStart(app.TimeConstraints.MaxColdStartS))
        }
        deployOpts = append(deployOpts, knowledge.WithGoldenConfig(goldenConfigFn))

        resolved, err := catalog.Resolve(hwInfo, dep.ModelType, "", nil, deployOpts...)
        if err != nil {
            // 记录失败, 继续下一个依赖
            continue
        }
        // 调用 deploy pipeline
        // ...
        dep.Satisfied = true
    }
}
```

> 注意: 完整的 auto-deploy (包括模型下载、存储检查等) 是 D4 完整实现的范畴。
> 此处 P0 只做时间约束传递的管道，auto_deploy=true 时尝试 Resolve+Deploy，
> 失败不阻塞，保持 graceful degradation。

#### B.3.4 引擎选择决策流程 (完整版, 标注 P0 改动点)

```
InferEngineType(modelName, hw, opts...)
  |
  +-- 1. 收集候选引擎                              [已有, 不改]
  |     for each model variant:
  |       +-- gpu_arch 匹配?
  |       +-- format 兼容?
  |       +-- VRAM 够?  -- 不够 -> 检查 effective_R (offload)
  |       +-- 通过 -> 加入 candidates[]
  |
  +-- 2. 时间约束过滤                              [已有, 不改]
  |     if MaxColdStartS > 0:                      [P0: 调用方开始传入此值]
  |       filter candidates where coldStartS > MaxColdStartS
  |       if all filtered out -> keep all (graceful degradation)
  |
  +-- 3. 排序                                     [已有, 不改]
  |     exact_arch > wildcard
  |     then: highest performance_multiplier
  |     then: non-offload > offload
  |     then: lowest cold_start (tiebreaker)
  |
  +-- 4. 返回 best.engineType
```

P0 的改动点:
- **步骤 2 已实现**, 只需要在调用方传入 `WithMaxColdStart`
- **步骤 3 已实现**, `performance_multiplier` 来自 YAML (后续 P1 可被 calibration overlay 增强)
- P0 **不改** `InferEngineType()` 内部逻辑, 只改调用方 + 新增 `InferEngineTypeDetailed()`

#### B.3.5 放大器校准 (P1 架构预留)

P0 不实现。此处记录设计方向:

放大器 `performance_multiplier` 校准的思路是:
1. 新增 MCP 工具 `knowledge.calibrate_multipliers`
2. 查询所有 golden benchmarks，按 `(hardware, engine)` 分组
3. 计算各引擎在该 hardware 上的平均 throughput
4. 以最低 throughput 引擎为 baseline，计算 `calibrated_multiplier`
5. 写入 `~/.aima/catalog/engines/<engine>-calibrated.yaml` overlay
6. 下次加载 Catalog 时，overlay 自动覆盖 embedded YAML 的 `performance_multiplier`

当前 Catalog 已支持 `~/.aima/catalog/` overlay 机制，无需新增基础设施。

### B.4 影响面

| 文件 | 变更 | 类型 |
|------|------|------|
| `internal/mcp/tools.go` | deploy.apply + deploy.dry_run schema 新增 `max_cold_start_s`; app.provision schema 新增 `auto_deploy` | schema |
| `internal/knowledge/resolver.go` | 新增 `EngineSelection`, `FilteredEngine` 类型, `InferEngineTypeDetailed()` 方法 | 新增 |
| `cmd/aima/main.go` | deploy.apply/dry_run handler 提取 `max_cold_start_s`, 构造 `WithMaxColdStart`; app.provision handler 处理 `auto_deploy` + 时间约束传递 | handler |
| `internal/cli/root.go` | deploy 命令新增 `--max-cold-start` flag | CLI |

代码量估计: ~80 行新增 (含 `InferEngineTypeDetailed`), ~20 行修改。

### B.5 测试策略

```go
// resolver_test.go

func TestInferEngine_MaxColdStartFiltersVLLM(t *testing.T) {
    catalog := buildTestCatalog()
    // vllm-ada: cold_start_s [30, 60], multiplier 2.5
    // llamacpp-universal: cold_start_s [3, 10], multiplier 1.0
    hw := HardwareInfo{GPUArch: "Ada", GPUVRAMMiB: 24576}

    // Without constraint: vllm wins (higher multiplier)
    engine1, _ := catalog.InferEngineType("qwen3-8b", hw)
    assert(engine1 == "vllm")

    // With 20s constraint: vllm filtered (cold_start_max=60 > 20), llamacpp wins
    engine2, _ := catalog.InferEngineType("qwen3-8b", hw, WithMaxColdStart(20))
    assert(engine2 == "llamacpp")

    // With 100s constraint: vllm still wins (60 < 100)
    engine3, _ := catalog.InferEngineType("qwen3-8b", hw, WithMaxColdStart(100))
    assert(engine3 == "vllm")

    // Edge case: all engines filtered -> graceful degradation, keep all
    engine4, _ := catalog.InferEngineType("qwen3-8b", hw, WithMaxColdStart(1))
    // Both filtered (vllm 60 > 1, llamacpp 10 > 1) -> keep all -> vllm wins
    assert(engine4 == "vllm")
}

func TestInferEngineTypeDetailed_ReturnsFilteredEngines(t *testing.T) {
    catalog := buildTestCatalog()
    hw := HardwareInfo{GPUArch: "Ada", GPUVRAMMiB: 24576}

    sel, _ := catalog.InferEngineTypeDetailed("qwen3-8b", hw, WithMaxColdStart(20))
    assert(sel.EngineType == "llamacpp")
    assert(len(sel.FilteredEngines) > 0)
    assert(sel.FilteredEngines[0].EngineType == "vllm")
    assert(sel.FilteredEngines[0].Reason == "cold_start_exceeded")
}
```

CLI 端到端测试:

```bash
# 在 dev-mac 上测试
./aima deploy qwen3-8b --dry-run --max-cold-start 20
# 期望: engine=llamacpp (not vllm), engine_selection 显示 vllm 被过滤

./aima deploy qwen3-8b --dry-run --max-cold-start 0
# 期望: engine=vllm (no constraint, higher multiplier wins)

./aima deploy qwen3-8b --dry-run
# 期望: engine=vllm (默认无时间约束)
```

---

## Feature C: Agent 巡检循环 (A2)

### C.1 问题陈述

PRD J3:
> Agent 定时检查 metrics -> 发现性能退化 -> 触发调优 -> 应用最优配置

当前状态:
- `Patrol` 结构体已实现 (`internal/agent/patrol.go`) -- 5 分钟间隔的 goroutine
- `RunOnce()` 调用 `checkMetrics()` + `checkDeployments()` 产出 `Alert`
- `Healer` 已实现 (`internal/agent/heal.go`) -- 可诊断 OOM, image_pull 等故障并恢复
- `Tuner` 已实现 (`internal/agent/tuner.go`) -- 可执行参数网格搜索循环

**缺失的是**: Patrol 产出 alert 后，没有自动触发 Healer/Tuner。alert 只是存入内存和数据库，
等待外部查询。这是一个"有感知无行动"的巡检系统。

### C.2 目标

```
Patrol.RunOnce()
  +-- checkMetrics()     -> alerts
  +-- checkDeployments() -> alerts
  |
  +-- * NEW: reactToAlerts(alerts)
        +-- deploy_crash (critical) -> Healer.Diagnose() -> Healer.Heal()
        +-- gpu_temp (warning)      -> 记录趋势, 超阈值告警
        +-- gpu_idle (info)         -> 记录, Agent 决策时参考
        +-- vram_opportunity (info) -> 记录, Agent 决策时参考
```

### C.3 详细设计

#### C.3.1 修改 Patrol 结构体: 持有 Healer + Action 记录

```go
// patrol.go -- 修改

// PatrolAction records an automated response to an alert.
type PatrolAction struct {
    AlertID   string    `json:"alert_id"`
    Type      string    `json:"type"`      // "heal", "scale_down", "notify"
    Detail    string    `json:"detail"`
    Success   bool      `json:"success"`
    Timestamp time.Time `json:"timestamp"`
}

// PatrolOption configures optional Patrol dependencies.
type PatrolOption func(*Patrol)

// WithHealer enables automated self-healing in response to critical alerts.
func WithHealer(h *Healer) PatrolOption {
    return func(p *Patrol) { p.healer = h }
}

// WithActionCallback registers a function called after each automated action.
func WithActionCallback(fn func(ctx context.Context, action PatrolAction)) PatrolOption {
    return func(p *Patrol) { p.onAction = fn }
}

type Patrol struct {
    config   PatrolConfig
    tools    ToolExecutor
    persist  AlertPersister
    healer   *Healer                                              // NEW
    onAction func(ctx context.Context, action PatrolAction)       // NEW
    mu       sync.RWMutex
    alerts   []Alert
    actions  []PatrolAction                                       // NEW
    lastRun  time.Time
    running  bool
    cancel   context.CancelFunc
}
```

修改构造函数:

```go
// NewPatrol creates a patrol loop. persist may be nil (alerts only kept in memory).
func NewPatrol(config PatrolConfig, tools ToolExecutor, persist AlertPersister, opts ...PatrolOption) *Patrol {
    p := &Patrol{
        config:  config,
        tools:   tools,
        persist: persist,
    }
    for _, o := range opts {
        o(p)
    }
    return p
}
```

> 注意: NewPatrol 签名从 3 参数变为 3+variadic，已有调用方不需要修改 (Go variadic 兼容)。

#### C.3.2 修改 RunOnce(): 在告警持久化后触发自动响应

```go
func (p *Patrol) RunOnce(ctx context.Context) []Alert {
    var newAlerts []Alert

    // 1. Device metrics check (已有)
    metricsAlerts := p.checkMetrics(ctx)
    newAlerts = append(newAlerts, metricsAlerts...)

    // 2. Deployment health check (已有)
    deployAlerts := p.checkDeployments(ctx)
    newAlerts = append(newAlerts, deployAlerts...)

    // 3. Persist and track (已有)
    p.mu.Lock()
    p.alerts = append(p.alerts, newAlerts...)
    p.lastRun = time.Now()
    p.mu.Unlock()

    if p.persist != nil {
        for _, alert := range newAlerts {
            if err := p.persist(ctx, alert.ID, alert.Severity, alert.Type, alert.Message); err != nil {
                slog.Warn("patrol: failed to persist alert", "error", err)
            }
        }
    }

    // 4. * NEW: Automated response to alerts
    if p.config.SelfHealEnabled {
        p.reactToAlerts(ctx, newAlerts)
    }

    return newAlerts
}
```

#### C.3.3 reactToAlerts() -- 巡检动作分发器

```go
func (p *Patrol) reactToAlerts(ctx context.Context, alerts []Alert) {
    for _, alert := range alerts {
        switch {
        case alert.Type == "deploy_crash" && alert.Severity == "critical":
            p.handleCrash(ctx, alert)
        case alert.Type == "gpu_temp" && alert.Severity == "warning":
            p.handleOverheat(ctx, alert)
        // gpu_idle 和 vram_opportunity 是 info 级别, 只记录不自动行动
        // 这些信息供 Agent (L3a) 在主动模式下决策参考
        }
    }
}
```

#### C.3.4 handleCrash() -- 自动自愈 (核心逻辑)

```go
func (p *Patrol) handleCrash(ctx context.Context, alert Alert) {
    if p.healer == nil {
        p.recordAction(ctx, alert.ID, "notify", "healer not configured, alert only", false)
        return
    }

    // 从 alert message 提取 deploy name
    deployName := extractDeployName(alert.Message)
    if deployName == "" {
        p.recordAction(ctx, alert.ID, "notify", "could not extract deploy name from alert", false)
        return
    }

    // 诊断
    diag, err := p.healer.Diagnose(ctx, deployName)
    if err != nil {
        p.recordAction(ctx, alert.ID, "heal",
            fmt.Sprintf("diagnosis failed: %v", err), false)
        return
    }

    // 不可恢复的故障类型 -> 只告警不自愈
    if diag.Remedy == "escalate" {
        p.recordAction(ctx, alert.ID, "notify",
            fmt.Sprintf("diagnosis: %s (%s), requires human intervention", diag.Type, diag.Cause), false)
        return
    }

    // 执行自愈
    action, err := p.healer.Heal(ctx, deployName, diag)
    success := err == nil && action != nil && action.Success

    detail := fmt.Sprintf("diagnosis=%s, action=%s", diag.Type, diag.Remedy)
    if action != nil {
        detail = fmt.Sprintf("diagnosis=%s, action=%s, attempt=%d", diag.Type, action.Action, action.Attempt)
    }
    p.recordAction(ctx, alert.ID, "heal", detail, success)

    // 标记 alert 为已解决
    if success {
        p.resolveAlert(alert.ID)
    }
}
```

#### C.3.5 handleOverheat() -- GPU 过热响应

```go
func (p *Patrol) handleOverheat(ctx context.Context, alert Alert) {
    // 策略: 记录告警, 首版不自动降负载 (需要连续过热趋势才安全执行)
    // 后续可扩展: 连续 N 次过热 -> 降 gmu 或减并发
    p.recordAction(ctx, alert.ID, "notify",
        "GPU overheating detected, monitoring", true)
}
```

#### C.3.6 辅助方法

```go
func (p *Patrol) recordAction(ctx context.Context, alertID, typ, detail string, success bool) {
    action := PatrolAction{
        AlertID:   alertID,
        Type:      typ,
        Detail:    detail,
        Success:   success,
        Timestamp: time.Now(),
    }

    p.mu.Lock()
    p.actions = append(p.actions, action)
    p.mu.Unlock()

    if p.onAction != nil {
        p.onAction(ctx, action)
    }

    slog.Info("patrol action",
        "alert", alertID, "type", typ, "success", success, "detail", detail)
}

func (p *Patrol) resolveAlert(id string) {
    p.mu.Lock()
    defer p.mu.Unlock()
    now := time.Now()
    for i := range p.alerts {
        if p.alerts[i].ID == id {
            p.alerts[i].Resolved = true
            p.alerts[i].ResolvedAt = &now
            break
        }
    }
}

// extractDeployName parses "Deployment <name> is in <status> state"
func extractDeployName(message string) string {
    const prefix = "Deployment "
    const suffix = " is in "
    i := strings.Index(message, prefix)
    if i < 0 {
        return ""
    }
    rest := message[i+len(prefix):]
    j := strings.Index(rest, suffix)
    if j < 0 {
        return ""
    }
    return rest[:j]
}

// RecentActions returns the most recent N patrol actions.
func (p *Patrol) RecentActions(limit int) []PatrolAction {
    p.mu.RLock()
    defer p.mu.RUnlock()
    if limit <= 0 || limit > len(p.actions) {
        limit = len(p.actions)
    }
    start := len(p.actions) - limit
    if start < 0 {
        start = 0
    }
    result := make([]PatrolAction, len(p.actions[start:]))
    copy(result, p.actions[start:])
    return result
}
```

#### C.3.7 增强 PatrolStatus

```go
func (p *Patrol) Status() PatrolStatus {
    p.mu.RLock()
    defer p.mu.RUnlock()
    healCount := 0
    for _, a := range p.actions {
        if a.Type == "heal" && a.Success {
            healCount++
        }
    }
    s := PatrolStatus{
        Running:     p.running,
        LastRun:     p.lastRun,
        AlertCount:  len(p.alerts),
        ActionCount: len(p.actions),    // NEW
        HealCount:   healCount,         // NEW
        Interval:    p.config.Interval.String(),
    }
    if p.running && !p.lastRun.IsZero() {
        s.NextRun = p.lastRun.Add(p.config.Interval)
    }
    return s
}
```

PatrolStatus 新增字段:
```go
type PatrolStatus struct {
    Running     bool      `json:"running"`
    LastRun     time.Time `json:"last_run,omitempty"`
    NextRun     time.Time `json:"next_run,omitempty"`
    AlertCount  int       `json:"alert_count"`
    ActionCount int       `json:"action_count"`   // NEW: 总动作数
    HealCount   int       `json:"heal_count"`     // NEW: 成功自愈数
    Interval    string    `json:"interval"`
}
```

#### C.3.8 新增 MCP 工具: agent.patrol_actions

```json
{
  "name": "agent.patrol_actions",
  "description": "List automated actions taken by the patrol loop (self-healing, notifications)",
  "inputSchema": {
    "type": "object",
    "properties": {
      "limit": {
        "type": "integer",
        "description": "Maximum number of actions to return (default 50)"
      }
    }
  }
}
```

Handler in main.go:
```go
"agent.patrol_actions": func(args map[string]any) (string, error) {
    limit := 50
    if l, ok := args["limit"].(float64); ok && l > 0 {
        limit = int(l)
    }
    actions := patrol.RecentActions(limit)
    return formatJSON(actions)
},
```

#### C.3.9 在 main.go 中连接 Patrol + Healer

```go
// main.go -- 在 Patrol 构造处修改

healer := agent.NewHealer(mcpServer) // mcpServer implements ToolExecutor

patrol := agent.NewPatrol(
    agent.DefaultPatrolConfig(),
    mcpServer,
    alertPersistFn,
    agent.WithHealer(healer),
    agent.WithActionCallback(func(ctx context.Context, a agent.PatrolAction) {
        slog.Info("patrol_action_audit",
            "alert_id", a.AlertID,
            "type", a.Type,
            "success", a.Success,
            "detail", a.Detail)
    }),
)
patrol.Start(ctx)
```

### C.4 完整巡检周期时序图

```
[每 5 分钟]
Patrol.RunOnce(ctx)
    |
    +-- tools.ExecuteTool("device.metrics") --> GPU metrics JSON
    |     +-- temp > 85C?  -> Alert{type:gpu_temp, severity:warning}
    |     +-- util < 10%?  -> Alert{type:gpu_idle, severity:info}
    |     +-- vram free > 50%? -> Alert{type:vram_opportunity, severity:info}
    |
    +-- tools.ExecuteTool("deploy.list") --> Deployments JSON
    |     +-- status in {CrashLoopBackOff, Error, Failed}?
    |           -> Alert{type:deploy_crash, severity:critical}
    |
    +-- persist(alerts) --> SQLite patrol_alerts
    |
    +-- reactToAlerts(alerts)   [NEW]
          |
          +-- [deploy_crash, critical]
          |     +-- healer.Diagnose(deployName)
          |     |     +-- OOM        -> healer.Heal() -> reduce gmu -> redeploy
          |     |     +-- image_pull -> healer.Heal() -> retry pull
          |     |     +-- unknown    -> recordAction(notify, escalate)
          |     +-- success? -> resolveAlert()
          |
          +-- [gpu_temp, warning]
                +-- recordAction(notify, monitoring)
```

### C.5 安全护栏

| 护栏 | 实现 |
|------|------|
| SelfHealEnabled 开关 | PatrolConfig.SelfHealEnabled 控制是否执行自愈, 可通过 agent.patrol_config 工具关闭 |
| Healer 最多重试 3 次 | `Healer.maxRetries = 3`, GMU 不低于 0.3 (heal.go:139) |
| 不可恢复故障不重试 | `diag.Remedy == "escalate"` 时只记录通知 |
| Info 级别不自动行动 | gpu_idle, vram_opportunity 只记录, 不触发自动操作 |
| 动作审计日志 | 每个 PatrolAction 通过 slog 记录, 可通过 onAction 回调持久化 |

### C.6 影响面

| 文件 | 变更 |
|------|------|
| `internal/agent/patrol.go` | 新增 `PatrolAction` 类型, `PatrolOption` + `WithHealer()` + `WithActionCallback()`, 修改 `NewPatrol()` 签名 (variadic opts), 新增 `reactToAlerts()`, `handleCrash()`, `handleOverheat()`, `recordAction()`, `resolveAlert()`, `extractDeployName()`, `RecentActions()`, 增强 `PatrolStatus` 和 `Status()` |
| `internal/mcp/tools.go` | 新增 `agent.patrol_actions` tool schema |
| `cmd/aima/main.go` | 构造 Patrol 时注入 Healer + ActionCallback; 新增 `agent.patrol_actions` handler |

代码量估计: ~120 行新增 (`patrol.go`), ~20 行新增 (`tools.go` + `main.go`)。

### C.7 测试策略

```go
// patrol_test.go

func TestPatrol_AutoHealOOM(t *testing.T) {
    mockTools := &mockToolExecutor{
        responses: map[string]string{
            "device.metrics": `{"gpu":{"temperature_celsius":60,"utilization_percent":50,"memory_used_mib":4000,"memory_total_mib":8000}}`,
            "deploy.list":    `[{"name":"qwen3-8b","status":"CrashLoopBackOff","config":{"gpu_memory_utilization":0.9},"model":"qwen3-8b","engine":"vllm"}]`,
            "deploy.logs":    `torch.cuda.OutOfMemoryError: CUDA out of memory`,
            "deploy.apply":   `{"status":"deployed"}`,
        },
    }

    healer := NewHealer(mockTools)
    patrol := NewPatrol(DefaultPatrolConfig(), mockTools, nil, WithHealer(healer))
    alerts := patrol.RunOnce(context.Background())

    // Should have deploy_crash alert
    hasCrash := false
    for _, a := range alerts {
        if a.Type == "deploy_crash" { hasCrash = true }
    }
    assert(t, hasCrash, "expected deploy_crash alert")

    // Should have successful heal action
    actions := patrol.RecentActions(10)
    assert(t, len(actions) > 0, "expected at least one action")
    found := false
    for _, a := range actions {
        if a.Type == "heal" && a.Success {
            found = true
        }
    }
    assert(t, found, "expected successful heal action")
}

func TestPatrol_WithoutHealer_NotifiesOnly(t *testing.T) {
    mockTools := &mockToolExecutor{
        responses: map[string]string{
            "device.metrics": `{"gpu":{"temperature_celsius":60}}`,
            "deploy.list":    `[{"name":"test","status":"CrashLoopBackOff"}]`,
        },
    }

    // No WithHealer -> crash alerts generate notify actions, not heal
    patrol := NewPatrol(DefaultPatrolConfig(), mockTools, nil)
    patrol.RunOnce(context.Background())

    actions := patrol.RecentActions(10)
    for _, a := range actions {
        assert(t, a.Type == "notify", "without healer, all actions should be notify")
    }
}

func TestExtractDeployName(t *testing.T) {
    tests := []struct{ msg, want string }{
        {"Deployment qwen3-8b is in CrashLoopBackOff state", "qwen3-8b"},
        {"Deployment my-model is in Error state", "my-model"},
        {"some other message", ""},
    }
    for _, tc := range tests {
        got := extractDeployName(tc.msg)
        assert(t, got == tc.want, "extractDeployName(%q) = %q, want %q", tc.msg, got, tc.want)
    }
}

func TestPatrol_SelfHealDisabled_NoReaction(t *testing.T) {
    config := DefaultPatrolConfig()
    config.SelfHealEnabled = false

    mockTools := &mockToolExecutor{...}
    healer := NewHealer(mockTools)
    patrol := NewPatrol(config, mockTools, nil, WithHealer(healer))
    patrol.RunOnce(context.Background())

    // Despite healer being configured, SelfHealEnabled=false -> no actions
    actions := patrol.RecentActions(10)
    assert(t, len(actions) == 0, "self-heal disabled, no actions expected")
}
```

远程设备验证:

```bash
# gb10 (K3S): 构造 OOM 场景, 验证巡检自动恢复
ssh qujing@100.105.58.16 './aima deploy qwen3-32b --config gpu_memory_utilization=0.99'
# 等待 5 分钟巡检周期...
ssh qujing@100.105.58.16 './aima mcp agent.patrol_status'
# 期望: heal_count > 0

ssh qujing@100.105.58.16 './aima mcp agent.patrol_actions'
# 期望: 有 "heal" 类型的 action, diagnosis=oom, success=true
```

---

## 跨功能依赖关系

```
Feature A (L2c)  <---- Feature B (时间约束) 共享 ResolveOption 机制
    |                      |
    |                      v
    |                 deploy.apply handler (同一处修改)
    |                      |
    v                      |
Resolve() <--- 两者都通过 ResolveOption 注入, 互不冲突
    |
    |         Feature C (巡检循环)
    |              |
    |              v
    |         Healer.Heal() -> deploy.apply MCP tool -> main.go handler
    |                                                       |
    |                                                       v
    |                                                  Resolve() 带上 L2c + MaxColdStart
    |
    +-- 所有功能通过 ResolveOption 组合, 互不冲突
```

**关键**: Feature C 的 Healer 调用 `deploy.apply` MCP 工具 (通过 `ToolExecutor`)，
这会走 main.go 中的 handler，自动经过 Feature A (L2c) 和 Feature B (时间约束) 的管道。
Healer 不需要自己处理这些逻辑，符合 INV-5 (MCP tools 是单一真相源)。

---

## 实施顺序与验收标准

### 实施顺序

```
Phase 1: Feature A (L2c 注入)              最小改动, 最高价值
  +-- 1a. resolver.go: GoldenConfigFunc + WithGoldenConfig + resolveOpts 扩展
  +-- 1b. resolver.go: Resolve() 解析 opts + 插入 L2c 合并逻辑
  +-- 1c. main.go: 构造 goldenConfigFn, 注入所有 Resolve 调用
  +-- 1d. 测试: resolver_test.go 4 cases
  +-- 预估: ~40 行新增, ~10 行修改

Phase 2: Feature B (时间约束)              依赖 Phase 1 的 ResolveOption 模式
  +-- 2a. tools.go: deploy.apply/dry_run schema 新增 max_cold_start_s
  +-- 2b. resolver.go: InferEngineTypeDetailed() + EngineSelection 类型
  +-- 2c. main.go: deploy handler 提取约束, app.provision 传递时间约束
  +-- 2d. cli/root.go: --max-cold-start flag
  +-- 2e. 测试: InferEngineType 过滤 + CLI dry-run
  +-- 预估: ~80 行新增, ~20 行修改

Phase 3: Feature C (巡检循环)              独立于 A/B, 但自动享受其成果
  +-- 3a. patrol.go: PatrolOption, PatrolAction, reactToAlerts 全套
  +-- 3b. tools.go + main.go: agent.patrol_actions 工具
  +-- 3c. main.go: 构造 Patrol 注入 Healer
  +-- 3d. 测试: patrol_test.go 4 cases + gb10 远程验证
  +-- 预估: ~120 行新增, ~20 行修改

Total: ~240 行新增, ~50 行修改, 0 新文件
```

### 验收标准

| Feature | 验收条件 | 验证方式 |
|---------|---------|---------|
| **A: L2c** | benchmark.record 晋升 golden 后, 下一次 Resolve 返回的 config 包含 golden 参数, `provenance="L2c"`; L1 用户覆盖仍优先 | Unit test: 4 cases |
| **A: L2c** | 无 golden config 时, Resolve 行为不变 (graceful degradation) | Unit test: case 3+4 |
| **B: 时间约束** | `--max-cold-start 20` 使 vllm (cold_start 30-60s) 被过滤, llamacpp 被选中 | Unit test + CLI dry-run |
| **B: 时间约束** | dry-run 输出包含 `engine_selection` 说明过滤原因 | CLI dry-run |
| **B: 时间约束** | 所有引擎都超时时 graceful degradation (不报错, 保留全部候选) | Unit test: edge case |
| **C: 巡检** | deploy CrashLoopBackOff -> patrol alert -> healer diagnose OOM -> reduce gmu -> redeploy -> success | Mock test + gb10 远程 |
| **C: 巡检** | 无 Healer 时只产出 notify action, 不 panic | Mock test |
| **C: 巡检** | SelfHealEnabled=false 时不执行任何自动动作 | Mock test |
| **C: 巡检** | `agent.patrol_actions` MCP 工具返回动作记录 | MCP 调用 |
| **C: 巡检** | `agent.patrol_status` 返回 `action_count` 和 `heal_count` | MCP 调用 |
