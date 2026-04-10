# Explorer Agent Planner 设计文档

## 概述

将 Explorer 的 LLM Planner 从单次 JSON prompt 调用重构为**文档驱动的 Agent 工作流**。
LLM 作为具备 tool calling 能力的研究员，通过读写文档工作区来规划和分析推理探索实验。
Go 侧负责机械执行（deploy/benchmark/undeploy）和持久化（SQLite/overlay YAML）。

**核心理念：LLM 写 YAML，Go 透传执行。**

## 设计决策记录

| 决策 | 选项 | 选择 | 理由 |
|------|------|------|------|
| Planner 定位 | a) 简单调度 b) 策略规划 c) 智能探索 | **b) 策略规划** | LLM 看丰富上下文做有质量的决策，但 Go 仍负责确定性过滤 |
| 引擎匹配 | a) 放宽 tag b) 发现与元数据解耦 c) 不改 | **b) 解耦** | 引擎发现和 catalog 元数据是不同关注点，耦合导致大量引擎被误过滤 |
| 文档生命周期 | a) 每轮 overwrite b) 增量 c) agent 维护 | **a+c** | 事实文档 AIMA 刷新保证准确，分析文档 agent 维护 |
| LLM 交互模式 | a) agentic tool calling b) 多阶段 c) 单次预组装 | **a) tool calling** | 按需读取、灵活、与现有 agent loop 架构一致 |
| Tier 关系 | a) 完全替代 b) 替代 T2 保留 T1 c) 新增 T3 | **b) 替代 T2** | INV-8 offline-first 要求 RulePlanner 作为离线 fallback |
| 实验历史 | a) 单文件 b) 每轮一文件 c) 详细文件+摘要 | **c) 结构化日志+摘要** | summary.md 作为工作记忆，详细数据按需读取 |
| 数据流 | a) done() 带参数 b) YAML code block c) 自由格式 | **b) YAML code block** | LLM 输出结构化 YAML，Go 透传执行，不重复内容 |

## 架构

### PDCA 探索循环

```
┌─────────────────── 一轮 Explore Round ───────────────────┐
│                                                            │
│  AIMA: 刷新事实文档（device-profile, available-combos,     │
│        knowledge-base）                                    │
│                                                            │
│  ┌─ Plan ──────────────────────────────────────────────┐  │
│  │ Agent Loop (tool calling):                           │  │
│  │   读文档 → 思考策略 → write plan.md (含 YAML tasks)  │  │
│  │   → done()                                           │  │
│  └──────────────────────────────────────────────────────┘  │
│                        ↓                                   │
│  ┌─ Do ────────────────────────────────────────────────┐  │
│  │ Go 执行（无 LLM）:                                    │  │
│  │   解析 plan.md YAML → deploy → benchmark → undeploy   │  │
│  │   → 写 experiments/*.md                              │  │
│  │ （可能耗时 5-30 分钟）                                 │  │
│  └──────────────────────────────────────────────────────┘  │
│                        ↓                                   │
│  ┌─ Check ─────────────────────────────────────────────┐  │
│  │ Agent Loop:                                          │  │
│  │   读实验结果 → 分析 → 更新 summary.md                 │  │
│  │   → done(verdict="continue"|"done")                  │  │
│  └──────────────────────────────────────────────────────┘  │
│                        ↓                                   │
│              verdict == "continue"?                         │
│              ┌── yes ──┐       ┌── no ──┐                  │
│              ↓                 ↓                            │
│  ┌─ Act ───────────┐    round 完成                         │
│  │ Agent Loop:      │                                      │
│  │ 修订 plan.md     │                                      │
│  │ → done()         │                                      │
│  └──────────────────┘                                      │
│          ↓                                                 │
│       回到 Do（最多 max_pdca_cycles 次）                    │
└────────────────────────────────────────────────────────────┘

AIMA: 提取 summary.md Recommended Configurations → SQLite + overlay YAML
```

### 数据流

```
LLM 决策层:  读文档 → 思考 → 写带 YAML 的 markdown
                          ↕
文档工作区:  plan.md ←→ experiments/*.md ←→ summary.md
                          ↕
Go 执行层:   解析 YAML → deploy/benchmark → 写结果 → 提取配置 → SQLite/overlay YAML
```

每一层职责分明：
- **LLM**：理解、决策、结构化输出（YAML）
- **文档**：中间态，人机共读
- **Go**：解析、执行、持久化

## 文档工作区

### 目录结构

```
~/.aima/explorer/
├── device-profile.md          # AIMA 生成，每轮刷新
├── available-combos.md        # AIMA 生成，每轮刷新
├── knowledge-base.md          # AIMA 生成，每轮刷新
├── plan.md                    # Agent 写入（Plan / Act phase）
├── summary.md                 # Agent 维护（Check phase）
└── experiments/               # AIMA 生成框架 + Agent 补充分析
    ├── 001-qwen3-4b-sglang-kt.md
    ├── 002-gemma-4-31B-vllm.md
    └── ...
```

### AIMA 管理的事实文档（每轮刷新，agent 只读）

#### device-profile.md

```markdown
# Device Profile
Updated: 2026-04-09T19:37:03Z

## Hardware
- GPU: 2× NVIDIA GeForce RTX 4090 (Ada, 49140 MiB each, 98280 MiB total)
- CPU: Intel Xeon Platinum 8488C (48C/96T, 2.4 GHz)
- RAM: 503 GiB (473 GiB available)
- Runtime: Docker
- Hardware Profile: nvidia-rtx4090-x86

## Models (16 available)
| Name | Format | Type | Size | Active Params | Arch | Fits VRAM |
|------|--------|------|------|---------------|------|-----------|
| qwen3-4b | safetensors | llm | 7.5G | 4B | qwen3 | ✅ |
| gemma-4-31B-it | safetensors | llm | 58.3G | 20.8B (MoE) | gemma | ✅ |
| MiniMax-M2.5 | safetensors | llm | 214.3G | — | minimax | ❌ (267G > 98G) |
| ... | | | | | | |

## Engines (4 available)
| Type | Runtime | Image:Tag | Catalog Match | Features | Tunable Params |
|------|---------|-----------|---------------|----------|----------------|
| sglang-kt | native | — | ✅ sglang-kt-ada | cpu_gpu_hybrid_moe | gmu, tp, cpu_offload_gb, ... |
| vllm | container | vllm/vllm-openai:gemma4-cu130 | ⚠ partial | flash_attention | gmu, max_model_len, ... |
| sglang | container | zhiwen-sglang:v0.5.8 | ❌ no match | — | — |
| llamacpp | container | zhiwen-llama-box:v3.3.1 | ❌ no match | — | — |

## Active Deployments
(none running)
```

#### available-combos.md

```markdown
# Available Exploration Combos
Updated: 2026-04-09T19:37:03Z

## Unexplored (N combos)
| Model | Engine | Format OK | VRAM OK | Priority Hint |
|-------|--------|-----------|---------|---------------|
| gemma-4-31B-it | vllm | ✅ | ✅ | 新模型, 未测试 |
| gemma-4-31B-it | sglang-kt | ✅ | ✅ | 新模型, MoE → kt 优势 |
| ... | | | | |

## Already Explored (N combos)
| Model | Engine | Status | Best Throughput | Key Findings |
|-------|--------|--------|----------------|--------------|
| qwen3-4b | sglang-kt | ✅ completed | 85 tok/s | baseline 良好 |
| qwen3.5-27b | sglang-kt | ❌ failed ×2 | — | OOM at gmu=0.90 |
| ... | | | | |

## Incompatible (filtered out)
- bge-m3 (pytorch, embedding) — 格式/类型不兼容
- MiniMax-M2.5 (214G) — 超出 98G VRAM
- ...
```

#### knowledge-base.md

```markdown
# Knowledge Base
Updated: 2026-04-09T19:37:03Z

## Golden Configurations (本设备已验证的最佳配置)
| Model | Engine | Key Params | Throughput | Latency P50 | Source |
|-------|--------|------------|-----------|-------------|--------|
| qwen3-30b-a3b | sglang-kt | gmu=0.85, tp=2 | 125 tok/s | 38ms | local |
| ... | | | | | |

## Benchmark History (本设备全部记录)
| Model | Engine | Date | Status | Throughput | Notes |
|-------|--------|------|--------|-----------|-------|
| qwen3.5-27b | sglang-kt | 2026-04-09 | failed ×2 | — | OOM |
| ... | | | | | |

## Cross-Device Knowledge (来自 central)
| Model | Engine | Hardware | Throughput | Confidence | Advisory |
|-------|--------|----------|-----------|------------|----------|
| qwen3-30b-a3b | vllm | nvidia-a100-x86 | 180 tok/s | high | try tp=2 |
| ... | | | | | |

## Catalog Engine Capabilities
| Engine | Supported Formats | Model Types | Cold Start | Key Features |
|--------|------------------|-------------|------------|--------------|
| sglang-kt | safetensors | llm | 30-120s | cpu_gpu_hybrid_moe |
| vllm | safetensors | llm | 30-60s | flash_attention, tensor_parallel |
| llamacpp | gguf | llm | 3-10s | cpu_offload, low_memory |
| sglang | safetensors | llm | 30-60s | flash_attention |
```

### Agent 维护的文档

#### plan.md（Plan / Act phase 写入）

```markdown
# Exploration Plan

## Strategy
vllm 从未在此设备测试，gemma-4-31B-it 是 MoE 模型适合 sglang-kt 的
cpu_gpu_hybrid，两个引擎对比测试。qwen3.5-27b 之前 OOM，降参数重试。

## Tasks
​```yaml
- kind: validate
  model: gemma-4-31B-it
  engine: vllm
  engine_params:
    gpu_memory_utilization: 0.90
    tensor_parallel_size: 2
    max_model_len: 4096
  benchmark:
    concurrency: [1, 4]
    input_tokens: [128, 512]
    max_tokens: [256]
    requests_per_combo: 3
  reason: "新模型，MoE 20.8B active，58G weights 双卡可装。vllm 首次测试。"

- kind: tune
  model: qwen3.5-27b
  engine: sglang-kt
  engine_params:
    gpu_memory_utilization: 0.70
    cpu_offload_gb: 20
  benchmark:
    concurrency: [1]
    input_tokens: [128]
    max_tokens: [256]
    requests_per_combo: 2
  reason: "gmu=0.90/0.80 均 OOM。降至 0.70 + CPU offload 20G 重试。"
​```
```

Go 解析规则：提取 `## Tasks` 下的 yaml code block → `yaml.Unmarshal` → `[]TaskSpec`

#### summary.md（Check phase 更新）

```markdown
# Exploration Summary

## Key Findings
- sglang-kt 的 cpu_gpu_hybrid 对 MoE 模型有明显加速
- qwen3.5-27b 需要 cpu_offload=20G + gmu=0.70 才能运行
- vllm 在 RTX 4090 上 TP=2 表现稳定

## Recommended Configurations
​```yaml
- model: gemma-4-31B-it
  engine: vllm
  hardware: nvidia-rtx4090-x86
  engine_params:
    gpu_memory_utilization: 0.90
    tensor_parallel_size: 2
  performance:
    throughput_tps: 95.2
    latency_p50_ms: 42
  confidence: validated
  note: "首次验证通过"

- model: qwen3.5-27b
  engine: sglang-kt
  hardware: nvidia-rtx4090-x86
  engine_params:
    gpu_memory_utilization: 0.70
    cpu_offload_gb: 20
  performance:
    throughput_tps: 65.0
    latency_p50_ms: 88
  confidence: tuned
  note: "需要 CPU offload，性能一般"
​```

## Current Strategy
重点转向引擎对比：已有 sglang-kt baseline 的模型用 vllm 重新验证。
```

Go 从 `## Recommended Configurations` 提取 YAML → 写入 SQLite configurations 表 + 生成 overlay YAML。

#### experiments/*.md（AIMA 生成框架 + Agent 补充）

```markdown
# Experiment: gemma-4-31B-it + vllm

## Task
​```yaml
kind: validate
model: gemma-4-31B-it
engine: vllm
engine_params:
  gpu_memory_utilization: 0.90
  tensor_parallel_size: 2
  max_model_len: 4096
​```

## Result
​```yaml
status: completed
started_at: 2026-04-09T20:15:03Z
duration_s: 342
cold_start_s: 45
​```

## Benchmark Matrix
​```yaml
matrix:
  - concurrency: 1
    input_tokens: 128
    max_tokens: 256
    throughput_tps: 95.2
    latency_p50_ms: 42
    latency_p99_ms: 118
  - concurrency: 4
    input_tokens: 128
    max_tokens: 256
    throughput_tps: 245.8
    latency_p50_ms: 65
    latency_p99_ms: 230
​```

## Agent Notes
<!-- Phase 2 由 agent 填充分析 -->
```

## Tool Set

6 个 tools，模拟 Linux 基础命令，跨平台归一化：

### 文件操作

| Tool | 类比 | 描述 | 参数 |
|------|------|------|------|
| `cat` | cat | 读取文件内容 | path: string |
| `ls` | ls | 列出目录 | path?: string (默认 ".") |
| `write` | tee | 写入文件（覆盖） | path: string, content: string |
| `append` | >> | 追加到文件末尾 | path: string, content: string |
| `grep` | grep | 搜索文件内容 | pattern: string, path?: string |

### 数据库查询

| Tool | 描述 | 参数 |
|------|------|------|
| `query` | 查询知识库（SQLite 只读） | type: enum, filter?: map, limit?: int |

`query` 的 type 枚举：configurations, benchmarks, advisories, exploration_runs

### 流程控制

| Tool | 描述 | 参数 |
|------|------|------|
| `done` | 通知当前 phase 完成 | verdict?: string ("continue" \| "done"，仅 Check phase) |

### 约束

- `write`/`append` 对 AIMA 管理的事实文档（device-profile.md, available-combos.md, knowledge-base.md）返回 `{ok: false, error: "read-only"}`
- `cat`/`ls`/`grep` 的 path 相对于 `~/.aima/explorer/`，不可越界
- `query` 只读，不可写入 SQLite

## System Prompts

### Phase 1: Plan

```
你是一个 AI 推理优化研究员，负责在边缘设备上探索最佳的模型+引擎配置。

你的工作环境是一个文档工作区（~/.aima/explorer/），包含：
- device-profile.md — 设备硬件、已安装模型和引擎的完整信息
- available-combos.md — 经过兼容性过滤的可行 model×engine 组合
- knowledge-base.md — 已有的知识库（golden config、历史 benchmark、跨设备知识）
- summary.md — 你之前积累的发现和策略（可能为空）
- experiments/ — 历次实验的详细报告

你的目标：制定一个探索计划，选择最有价值的实验来扩展对这台设备的推理能力认知。

工作流程：
1. 用 cat 读取关键文档，了解设备状态和已有知识
2. 用 ls/grep 按需查看实验历史
3. 用 query 深入查询知识库细节（如需）
4. 思考策略，用 write 写入 plan.md
5. plan.md 的 ## Tasks 下必须包含一个 yaml code block，定义具体任务
6. 用 done() 通知系统你已完成规划

计划原则：
- 每轮最多 {max_tasks} 个任务
- 优先覆盖未测试的 model+engine 组合（breadth first）
- 已有 baseline 的模型才可以 tune
- 考虑引擎特性选择参数（如 MoE 模型用支持 cpu_gpu_hybrid 的引擎）
- 为每个任务设计合理的 benchmark 参数（concurrency、input_tokens、max_tokens）
- reason 字段要说明为什么这个实验有价值

Task YAML 格式：
- kind: validate|tune
  model: <model name>
  engine: <engine type>
  engine_params:
    <key>: <value>
  benchmark:
    concurrency: [<int>, ...]
    input_tokens: [<int>, ...]
    max_tokens: [<int>, ...]
    requests_per_combo: <int>
  reason: "<string>"
```

### Phase 2: Check

```
你是一个 AI 推理实验分析师。刚刚完成了一轮探索实验，你需要分析结果并更新知识。

工作区状态：
- plan.md — 本轮执行的计划
- experiments/ — 新产生的实验报告（含 benchmark matrix 数据）
- summary.md — 之前的发现和策略

你的任务：
1. 用 cat 读取新产生的实验报告
2. 分析结果：哪些成功了、性能如何、有没有意外发现
3. 对比已有知识（knowledge-base.md）：新结果是否优于已知最佳配置
4. 用 write/append 更新 summary.md：
   - ## Key Findings 追加新发现
   - ## Recommended Configurations 的 yaml block 更新推荐配置
   - ## Current Strategy 更新策略
5. 可选：用 append 为实验文件补充 ## Agent Notes
6. 用 done(verdict) 通知系统：
   - verdict="done" — 本轮目标达成，无需追加实验
   - verdict="continue" — 发现需要追加/重试的实验

Recommended Configurations YAML 格式：
- model: <name>
  engine: <engine>
  hardware: <profile>
  engine_params: { ... }
  performance:
    throughput_tps: <float>
    latency_p50_ms: <float>
  confidence: validated|tuned|provisional
  note: "<string>"
```

### Phase 3: Act

```
你是一个 AI 推理实验规划师。根据上一轮实验的分析结果，你决定追加实验。

工作区中 summary.md 已更新了最新分析。请：
1. 读取 summary.md 了解分析结论
2. 读取 available-combos.md 确认可行组合
3. 修订 plan.md，在 ## Tasks 的 yaml block 中只写追加的新任务
4. 用 done() 通知系统

修订原则：
- 只追加新任务，不重复已完成的实验
- 针对具体发现做针对性调整（如降低 gmu、换引擎、调 TP）
- 最多追加 {max_tasks} 个任务
```

## 引擎发现解耦

### 当前问题

`gatherLocalEngines` 要求 installed engine 的 image:tag 精确匹配 catalog YAML。
导致自定义镜像（zhiwen-vllm, zhiwen-sglang 等）全部被过滤，explorer 只看到 1 个引擎。

### 解决方案：两步分离

```
Step 1: 发现（纯事实）
  遍历 engine scan 结果，按 type 去重
  只要 available=true 就列入
  不依赖 catalog match

Step 2: 增强（可选）
  有 catalog 条目 → 补充 features、tunable_params、internal_args
  没有 catalog 条目 → 仍然列入，元数据为空
  device-profile.md 中标注 "Catalog Match: ✅/⚠/❌"
```

Agent 看到没有 catalog 信息的引擎，仍然可以选它做实验 — 只是用默认参数。

### 代码变更

`cmd/aima/main.go` 中的 `WithGatherLocalEngines` 回调：
- 移除 `installedEnginesContainResolvedAsset` 检查
- 改为：列出所有 available engines → 按 type 去重 → 可选从 catalog 补充元数据

## Go 侧架构

### ExplorerAgentPlanner

替代现有 `LLMPlanner`，实现 `Planner` interface：

```go
type ExplorerAgentPlanner struct {
    llm          StreamingLLMClient
    workspace    *ExplorerWorkspace
    tools        []Tool              // cat, ls, write, append, grep, query, done
    maxCycles    int                 // PDCA 最大迭代（explore 参数）
    maxTasks     int                 // 单次 plan 最大任务数（explore 参数）
}

// Plan 实现 Planner interface — Phase 1
func (p *ExplorerAgentPlanner) Plan(ctx, input) (*ExplorerPlan, int, error)

// Analyze 新增 — Phase 2 + 3
func (p *ExplorerAgentPlanner) Analyze(ctx, results) (verdict string, additionalTasks []TaskSpec, error)
```

### ExplorerWorkspace

文档工作区管理：

```go
type ExplorerWorkspace struct {
    root string  // ~/.aima/explorer/
}

func (w *ExplorerWorkspace) Init()                                        // 创建目录 + 空模板
func (w *ExplorerWorkspace) RefreshFactDocuments(ctx, input PlanInput)    // 刷新三件套
func (w *ExplorerWorkspace) WriteExperimentResults(results)               // 写 experiments/*.md
func (w *ExplorerWorkspace) ExtractRecommendations() ([]RecommendedConfig, error) // 解析 summary.md YAML
func (w *ExplorerWorkspace) ParsePlan() ([]TaskSpec, error)              // 解析 plan.md YAML
```

### Agent Loop

```go
func (p *ExplorerAgentPlanner) runPhase(ctx, phase, systemPrompt string) error {
    messages := []Message{{Role: "system", Content: systemPrompt}}

    for {
        resp := p.llm.ChatCompletionStream(ctx, messages, p.tools, ...)

        if len(resp.ToolCalls) == 0 {
            break
        }

        for _, tc := range resp.ToolCalls {
            result := p.executeTool(ctx, tc)  // cat/ls/write/append/grep/query/done
            messages = append(messages, toolResultMessage(tc.ID, result))

            if tc.Name == "done" {
                return nil  // phase 结束
            }
        }
    }
    return nil
}
```

### 与 Explorer 集成

```go
func (e *Explorer) executeRound(ctx) {
    // 1. Plan
    plan, tokens, err := e.planner.Plan(ctx, input)

    // 2. Do
    results := e.executor.Execute(ctx, plan.Tasks)

    // 3. Check + Act loop
    for cycle := 0; cycle < e.config.MaxCycles; cycle++ {
        verdict, extraTasks, err := e.planner.Analyze(ctx, results)
        if verdict == "done" || len(extraTasks) == 0 {
            break
        }
        // Do again
        results = e.executor.Execute(ctx, extraTasks)
    }

    // 4. 持久化
    e.workspace.ExtractRecommendations() → SQLite + overlay YAML
}
```

## 安全约束

| 约束 | 默认值 | 配置方式 |
|------|--------|----------|
| max_pdca_cycles | 3 | explore 参数 `--max-cycles` |
| max_tasks_per_plan | 5 | explore 参数 `--max-tasks` |
| read_only_docs | device-profile.md, available-combos.md, knowledge-base.md | 硬编码 |
| max_tool_calls_per_phase | 无限制 | system config 热更新 |
| tool_call_idle_timeout | 无限制 | system config 热更新 |
| workspace_max_files | 无限制 | system config 热更新 |

## 示例完整流程

```
Round 1:
  [AIMA] 刷新事实文档 → device-profile.md, available-combos.md, knowledge-base.md

  [Plan] Agent reads:
    cat device-profile.md → "2× RTX 4090, 98G VRAM, 4 engines..."
    cat available-combos.md → "8 unexplored combos: gemma-4+vllm, gemma-4+sglang-kt..."
    cat knowledge-base.md → "golden: qwen3-30b-a3b+sglang-kt 125tok/s..."
    cat summary.md → (empty)
  Agent writes plan.md → 3 tasks: gemma-4+vllm, gemma-4+sglang-kt, qwen3.5-27b tune
  Agent calls done()

  [Do] Go executes:
    gemma-4+vllm → ✅ 95 tok/s
    gemma-4+sglang-kt → ✅ 115 tok/s
    qwen3.5-27b tune → ❌ still OOM
  Go writes experiments/001.md, 002.md, 003.md

  [Check] Agent reads:
    cat experiments/001.md → "throughput 95 tok/s"
    cat experiments/002.md → "throughput 115 tok/s, sglang-kt 20% faster"
    cat experiments/003.md → "status: failed, OOM"
  Agent updates summary.md:
    Key Findings: "sglang-kt 对 gemma-4 MoE 有 20% 加速"
    Recommended Configurations: gemma-4+sglang-kt validated
    Current Strategy: "qwen3.5-27b 需要更激进的 offload 策略"
  Agent calls done(verdict="continue")

  [Act] Agent reads summary.md
  Agent writes plan.md → 1 task: qwen3.5-27b+sglang-kt gmu=0.60 cpu_offload=30G
  Agent calls done()

  [Do] Go executes → ✅ 52 tok/s
  [Check] Agent updates summary.md, calls done(verdict="done")

  [AIMA] Extract Recommended Configurations → SQLite + overlay YAML
```
