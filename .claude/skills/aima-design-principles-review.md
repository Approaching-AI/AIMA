# AIMA 设计原则实战案例

> 记录在实际开发中违反/遵守设计原则的典型案例
> 最后更新：2026-03-01

---

## 一、已修复的 INV-1 违反案例

### 案例：`findModelFileInDir` switch engineType

**违反代码**（已在 commit `7e2ee26` 修复）：
```go
func findModelFileInDir(dir, engineType string) string {
    switch engineType {
    case "llamacpp":
        ext = ".gguf"
    default:
        return ""  // 容器引擎用目录
    }
    ...
}
```

**问题**：在 Go 代码中 branch on engine type，违反 INV-1。新增引擎时必须修改 Go 代码。

**正确写法**：用 YAML 中已有的信息（`source:` 字段）区分 native 和 container 引擎：
```go
// 调用侧：resolved.Source != nil 表示 native 引擎（engine YAML 中有 source: 字段）
// native 引擎需要单文件路径；container 引擎需要目录路径
if resolved.Source != nil {
    if fi, _ := os.Stat(modelPath); fi.IsDir() {
        if f := findModelFileInDir(modelPath); f != "" {
            modelPath = f
        }
    }
}

// 函数本身：无 engine-specific 逻辑，只扫描已知模型文件扩展名
func findModelFileInDir(dir string) string {
    for _, e := range entries {
        switch strings.ToLower(filepath.Ext(e.Name())) {
        case ".gguf", ".ggml", ".bin", ".safetensors":
            return filepath.Join(dir, e.Name())
        }
    }
    return ""
}
```

**原则**：区分"file vs directory"的判据来自 engine YAML（`source:` 是否存在），
不来自引擎名字字符串。增加新的 native 引擎 = 在 YAML 中加 `source:` 字段，零 Go 改动。

---

## 二、务实修复 vs 架构漂移的边界

### 判断框架

```
这个修复是否让未来增加新 engine/model 需要改 Go 代码？
  是 → INV-1 违反，必须修复
  否 → 可接受的务实修复（即使不完美）
```

### 可接受的务实修复（有技术债但暂不修）

| 修复 | 为什么可接受 | 技术债 |
|------|------------|--------|
| `/dev/shm` 无条件挂载 | 所有当前引擎都受益，且无害 | engine YAML 应有 `requires_shm: true` |
| `schedulerName: default-scheduler` 硬编码 | 是 K8s 默认值，普遍适用 | hardware profile YAML 应有 `scheduler_name` |
| NVIDIA env 包含 x86 路径 | 仅在 x86 NVIDIA 机器触发，已验证有效 | hardware profile YAML 应有 `container_env` |

### 必须修复的架构违反

任何以 engine 类型名称或 model 格式名称为条件的 Go `switch/if` 语句。

---

## 三、YAML-Driven 模式实战对照

### Config 值传递链（正确示例）

```
engine YAML default_args:
  gpu_memory_utilization: 0.90
  max_model_len: 8192
          ↓
cat.Resolve() → resolved.Config["gpu_memory_utilization"] = 0.90
          ↓
podgen.go / native.go → "--gpu-memory-utilization" "0.90"
          ↓
vllm 进程收到正确参数
```

修复前：`resolved.Config` 填充了但从未转换为 CLI flags，引擎始终以内置默认值运行。

### Health Check 延时链（正确示例）

```
engine YAML health_check.timeout_s: 300
          ↓
resolved.HealthCheck.TimeoutS = 300
          ↓
podData.HealthCheckInitDelaySec = 300
          ↓
pod spec: initialDelaySeconds: 300
```

修复前：硬编码 `initialDelaySeconds: 30`，模型加载超时直接杀死 Pod。

---

## 四、INV-3 实战边界（不管理容器生命周期）

**允许**：
- `kubectl apply` / `kubectl delete` / `kubectl get` / `kubectl logs`
- 发现 apply 失败（immutable 字段）→ delete + re-apply（这是 K8s 平台约束，不是 AIMA 业务逻辑）

**不允许**：
- 轮询 Pod 状态等它变成 Running（那是 K8s 的工作）
- 在 Go 中实现重启策略（`restartPolicy: Always` 已经在 pod spec 里）
- 拦截容器崩溃并自动修复（K8s 的事）

### 案例：K3S immutable 字段处理

```go
// 正确：发现 apply 失败 → delete + re-apply，AIMA 本身不做任何生命周期管理
err = r.client.Apply(ctx, podYAML)
if err != nil && (strings.Contains(err.Error(), "immutable") || ...) {
    podName := knowledge.SanitizePodName(req.Name + "-" + req.Engine)
    _ = r.client.Delete(ctx, podName)
    err = r.client.Apply(ctx, podYAML)
}
```

这是处理 K8s API 约束（QoS class 不可变），不是 AIMA 管理容器生命周期。

---

## 五、代码重复 vs 抽象的判断

### PullEngine 选引擎逻辑 vs resolver.findEngine

两者都实现了"exact gpu_arch → wildcard *"的引擎选择，但语义不同：

- `resolver.findEngine`：deploy 时用，考虑 native/container runtime 过滤
- `PullEngine` 闭包：pull 时用，按 name OR type 匹配，不过滤 runtime

目前不抽象，原因：两处逻辑差异显著，强行合并会引入不必要的参数和条件。
待两处逻辑真正收敛（3+ concrete uses）再提取。Prime Directive 适用。

---

## 六、2026-03-01 全面审计修复（commit `f18365c`）

### 审计方法

4 个 Agent 并行审计 4 个维度：INV-1/2（YAML 驱动）、INV-5（MCP 真相）、Go 规范、设计模式。
共发现 **11 P0 + 21 P1** 问题，全部在一次 commit 中修复（33 文件, +599/-376 行）。

### INV-5 违反模式及修复

**典型错误**：CLI 命令直接调用内部包而不通过 MCP ToolDeps：

| CLI 命令 | 违反方式 | 修复 |
|----------|---------|------|
| `ask` | `app.Dispatcher.Ask()` 直接调用 | 新增 `agent.ask` MCP 工具 |
| `agent status` | Agent 状态直接调用 | 新增 `agent.status` MCP 工具 |
| `status` | 组合 3 个 ToolDeps + 错误处理策略 | 新增 `system.status` MCP 工具 |
| `knowledge list` | 60 行数据变形逻辑 | 新增 `knowledge.list` MCP 工具 |

**判断标准**：CLI 函数体 > 5 行且包含非格式化逻辑（if/for/unmarshal/compose）= INV-5 违反。

**注意**：`serve` 命令（110 行编排）判定为 P1 而非 P0，因为服务器生命周期管理本质上不适合 MCP request-response 模型。

### INV-1 违反模式及修复

**模式 1：Go 代码中硬编码 format→engine 映射**

```go
// 违反（已删除）
var FormatEngineMap = map[string]string{
    "safetensors": "vllm",
    "gguf":        "llamacpp",
}

// 正确：Engine YAML 声明 supported_formats，运行时从 Catalog 动态构建
func (c *Catalog) FormatToEngine(format string) string { ... }
```

**模式 2：默认值散落在多处**

```go
// 违反：3 个文件各自写 name = "llamacpp"
// 正确：Engine YAML 声明 default: true，运行时 cat.DefaultEngine() 动态读取
```

**经验**：任何「写死在 Go 里的字符串常量如果对应 YAML 中某类资产的属性」= INV-1 违反。

### Go 规范违反模式及修复

| 模式 | 违反 | 修复 |
|------|------|------|
| `init()` + 全局变量 | `model/scanner.go` 5 个包级 var | `ScanConfig` 结构体 + 依赖注入 |
| 可变全局 map/slice | `allowedCommands` 包级 var | 移入函数内局部变量 |
| bare `return err` | 29 处无上下文错误 | `fmt.Errorf("context: %w", err)` |
| `io.ReadAll` 无限制 | 外部 HTTP 响应 | `io.LimitReader(10MB)` |
| 导出的死代码 | `FormatPort`, `Store` 接口 | 删除 |

### 审计检查清单（后续可复用）

```
□ grep -r 'switch.*engine\|if.*engine.*==' internal/ — 是否有引擎特定分支
□ grep -r 'switch.*vendor\|if.*vendor.*==' internal/ — 是否有厂商分支（HAL 层除外）
□ 对比 CLI 函数体行数：> 5 行非格式化逻辑 = 可能违反 INV-5
□ grep -r 'func init()' internal/ — 不应存在
□ grep -r 'var .* = .*{' internal/ --include='*.go' — 检查可变全局状态
□ grep -r 'io.ReadAll' internal/ — 外部输入必须有 LimitReader
□ grep -r 'return err$' internal/ cmd/ — 错误缺少上下文包装
□ 新增 MCP 工具是否需要加入 blockedAgentTools — Agent 自动调用有安全风险？
□ 新增包的接口是否定义在 consumer 侧 — 避免 provider 侧定义导致耦合
```

---

## 七、Fleet REST API 审查案例 (2026-03-02, commit `5f0d210`)

### P0: Agent 安全缺口 — fleet.exec_tool

**问题**: `fleet.exec_tool` MCP 工具允许在远程设备执行任意 MCP 工具。如果 Agent 自动调用它，
可以绕过本地的 `blockedAgentTools` 安全检查（因为远程设备不知道本地 Agent 的限制）。

**修复**: 将 `"fleet.exec_tool": "remote tool execution bypasses local guardrails"` 加入 `blockedAgentTools`。

**原则**: 每次新增 MCP 工具都必须审查 Agent 安全影响。特别是代理/转发类工具，
它们可能让 Agent 间接执行被本地 blocklist 禁止的操作。

### P1: Mirror types 消除 — json.RawMessage 优于自定义类型

**问题**: fleet.MCPExecutor 接口最初返回自定义 mirror 类型 `MCPToolResult`/`MCPToolDef`，
main.go 中需要 ~30 行逐字段转换代码。这些类型仅用于跨包传递数据，无任何方法。

**修复**: 接口改为返回 `json.RawMessage`，adapter 用 `json.Marshal()` 一行搞定。
删除 3 个 mirror 类型定义 + 全部转换代码。

**原则**: 当接口两侧已经是 JSON 序列化/反序列化场景时，`json.RawMessage` 是零成本传递方式。
只有当调用方需要类型安全访问具体字段时，才定义结构体。

### INV-5 实践: Fleet CLI → REST API，不走进程内状态

**问题**: `aima fleet devices` 最初通过 `ToolDeps.FleetListDevices` 直接调用 Registry.List()。
但 CLI 是独立进程，Registry 在 `aima serve` 进程中 → CLI 进程的 Registry 永远是空的。

**修复**: CLI 改为 HTTP 调用 `http://127.0.0.1:6188/api/v1/devices`，和远程 Agent 走同一路径。
符合 INV-5: MCP tools (通过 REST API 暴露) 是单一真相源，CLI 只是薄包装。

**教训**: CLI 命令调用的目标如果是**有状态服务**（如 mDNS discovery registry），
必须通过 IPC/REST 而非 in-process 调用。"Thin CLI" 原则在有状态场景尤为重要。

### Consumer-side interface 模式验证

fleet 包定义 `MCPExecutor` interface（consumer），main.go 实现 `fleetMCPAdapter`（provider）。

```go
// fleet/handler.go — 消费者定义接口
type MCPExecutor interface {
    ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error)
    ListToolDefs() json.RawMessage
}

// cmd/aima/main.go — 提供者实现适配器
type fleetMCPAdapter struct { server *mcp.Server }
```

这是 Go 惯用的"Interfaces at consumer, not provider"模式。
fleet 包不 import mcp 包，零耦合。CLAUDE.md 明确要求此模式。
