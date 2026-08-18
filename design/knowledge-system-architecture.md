# AIMA — 知识系统架构 (Knowledge System Architecture)

> AI-Inference-Managed-by-AI
> 知识驱动的推理优化系统的**架构约束与契约**

---

## 1. 定位

### 一句话定义

> AIMA 的知识系统是**一个管理"HW × Model × Engine → Config → Performance"因果证据的多层存储**——
> 人类先验 + 系统证据 + 决策综合三者分层解耦，由明确的 invariants 和 promotion gates 约束流动。

### 核心命题

AIMA 是知识驱动的系统。知识的质量决定推理调度的质量，知识系统的架构决定能否演进。

**三类知识混合的客观事实**：
- **人写的先验**（catalog YAML）—— 厂商手册、社区经验、工程判断
- **系统产出的证据**（benchmark、deploy config、decision trace）—— exploration / patrol / advisory 留下的事实
- **中央蒸馏的建议**（Central advisory / scenario）—— 跨设备聚合后的推理结果

如果不把三者的边界、流向、权责画清楚，这些知识会在代码里互相污染，让"知识驱动"变成"知识困扰"。

### 本文的角色

本文是**架构约束蓝图**，不是实施计划：

- 它告诉所有后续版本（v0.4, v0.5, v1.0, v2.0...）**什么是对的、什么是错的、边界在哪里**
- 它**不**规定何时实施、分几步做、谁来做——那是各版本 release plan / roadmap 的职责
- 它应当像 PRD、ARCHITECTURE.md 一样**长期稳定**，只在根本原则变化时才修订

---

## 2. 三条根本原则

### 2.1 Edge 对 Prior 层只读

Edge 永远不直接写 Prior 层 YAML。Prior 层的系统级修改**只能**由 Central Distillation Engine 产出并下发。

### 2.2 Overlay 是 Patch，不是完整 asset

`~/.aima/catalog/central/` 和 `~/.aima/catalog/user/` 下每个文件都是 **patch**，只携带相对 factory 的增量。factory (`catalog/*` 通过 go:embed) 是**唯一**的完整 asset base。

### 2.3 跨设备流动经 Central 中介

Edge 之间永不直连。跨设备知识共享只能通过：**发送方 → Central DB → (Advisor 个性化 或 Distillation Fleet 分发) → 接收方本地验证或应用**。

**三原则合力**：Edge 离线仍能完整工作；Prior 层变更可审计；跨版本兼容。

---

## 3. 知识分层

AIMA 的知识在架构上分三层。**讨论知识归属时使用这套术语**，不使用模糊的 "L0/L1/L2/L2c/L3a"（后者仅在讨论 Resolver 内部合并顺序时使用）。

### 3.1 三层定义

| 层 | 存储 | 写者 | 频率 | 特征 |
|---|---|---|---|---|
| **Prior 先验** | factory (go:embed) + `~/.aima/catalog/{central,user}/*.yaml` | 人（factory + user）；Central Distillation Engine（central） | 周/月级 | 稳定、可版本控制、是 prior 不是 truth |
| **Evidence 证据** | SQLite dynamic tables | Explorer / Benchmark / Patrol / Sync | 分钟/小时级 | 带完整证据链、可聚合、有状态机 |
| **Decision 决策** | 内存（短暂） | Resolver `Resolve()` | 每次部署一次 | 一次性、可追溯（Provenance） |

### 3.2 Prior Layer 物理结构

```
Prior
├─ factory catalog/ (go:embed)            【唯一完整 asset base】
└─ ~/.aima/catalog/
   ├─ central/  (Central push 的 patch)
   │  └─ manifest.json (distillation_id + hash 追踪)
   └─ user/     (用户手写的 patch，最高优先级)
```

**Merge 顺序**：`factory → sum(central patches) → sum(user patches) → effective asset`

同层不冲突（每 asset 一个文件）；跨层冲突：**user > central > factory**。

### 3.3 Evidence Layer 的两个子类

| 子类 | 例 | 可否直接进 Prior |
|---|---|---|
| **本地证据** | 本机 Explorer / Benchmark 产出，有完整 benchmark 证据链 | 是 Gate 3 / 4 的**主输入** |
| **候选知识** | Central advisory / scenario，跨设备推断、尚未本地验证 | **不**直接进 Prior；须本地验证成为本地证据后才行 |

**Benchmark-Backed Knowledge Contract**（已定义在 `docs/knowledge.md §3.4.1`）: 本地证据的字段完整性契约。

### 3.4 Decision Layer

Resolver 合成 `ResolvedConfig`：`effective Prior ⊕ L2c (Evidence) ⊕ L1 (UserOverride) ⊕ L3a (synthetic fallback)`。

`Provenance[key]` 必须能指出每个参数来自哪一源。

---

## 4. Edge Pipeline：六阶段契约

Edge 侧的知识产出流水线。每阶段有确定的输入、输出、产物落点、允许副作用。

| 阶段 | 输入 | 输出 | 产物落点 | 允许副作用 |
|---|---|---|---|---|
| **Plan** | Hardware, Gaps, Advisories, History, PendingWork | ExplorationPlan (TaskSpec[]) | Workspace `plan.md` | 否 |
| **Act** | TaskSpec | deploy + benchmark 原始结果 | runtime + 临时 | 改 runtime |
| **Check** | benchmark 原始结果 | 结构化事实 + 成败判定 | Workspace `experiment-facts.md` | 否 |
| **Harvest** | Check 输出 + 真实 deploy config | Configuration / BenchmarkResult / KnowledgeNote | **SQLite Evidence** | **只写 Evidence，绝不碰 Prior** |
| **Promote** | Evidence 候选 config | `status='golden'` | SQLite | 否 |
| **Sync** | Evidence 变更 + feedback + applied_overlays heartbeat | Central ingest payload | Central DB | 跨进程 |

**Pipeline 不含 Distill 阶段**。Distill 归 Central Distillation Engine。

**Workspace 与 Evidence 的关系**：workspace 是 Evidence 的 **LLM-readable 投影**——事实性内容（已验证数据、confirmed blocker、golden 推荐）必须在 SQLite 有对应行；LLM 的叙事性推理可 workspace-only。

---

## 5. Central Distillation Engine

**定位**：Central 的第三能力模块，与 Advisor / Periodic Analyzer 并列。

- **Advisor**：给某一台设备的**个性化推荐**（单设备粒度）
- **Periodic Analyzer**：跨 Fleet 的**定时扫描 + 模式发现**
- **Distillation Engine**：把跨 Fleet 稳定知识蒸馏为 **patch overlay** 并 push 到目标 Edge（Fleet 粒度）

### 5.1 职责

| 子模块 | 职责 |
|---|---|
| **Candidate Scanner** | 扫描 Central DB，产出 distillation 候选（Gate 3 / Gate 4）|
| **Agent Analyzer** | LLM 读跨设备 Evidence，**生成 patch**（非完整 asset），输出 confidence |
| **Approval Queue** | 高置信度（≥阈值 + 跨 ≥N 设备一致）自动批准；低置信度人审 |
| **Writer** | 落 `distillations` 表（证据追溯）+ 入 push queue |
| **Push Channel** | Edge `GET /api/v1/overlays` 对端；支持 `upsert` / `revoke` 两种 op |

### 5.2 晋升哲学

对齐 MLflow "**auto-to-staging, gated-to-production**"：
- 证据足够充分 + 跨设备一致 → Agent 自动签字
- 否则 → 进入人审队列

---

## 6. Overlay Patch Semantics

AIMA 自定义的 overlay 合并规则（仿 Kubernetes Strategic Merge Patch，不引入 k8s 依赖）。

### 6.1 Patch 文件识别

```yaml
kind: <原 kind>_patch         # 告诉 loader 走 patch merge
metadata:
  name: <target_asset_name>   # 锁定目标
# ... 只写要改的字段
```

Loader 读到 `kind: model_asset` 走完整 asset；读到 `kind: model_asset_patch` 走 patch merge。

### 6.2 合并规则

| 字段类型 | 规则 |
|---|---|
| 标量（string/int/bool/...）| 后者覆盖 |
| map（default_config / expected_performance / container.env / ...）| **深合并**（recursive，同 key 覆盖、不同 key 并存）|
| list of objects | 按**预定义 merge key** 匹配：命中则字段级深合并；未命中则 append |
| list of scalars（aliases / formats / ...）| **并集去重** |

### 6.3 Merge Key 白名单

**新增 list 字段必须在此表加一行**（或标注为标量 list 并集）。

| kind | 字段 | Merge Key |
|---|---|---|
| model_asset | `variants[]` | `name` |
| model_asset | `storage.sources[]` | `type` + `repo`/`path` |
| model_asset | `metadata.aliases[]` | 标量并集 |
| model_asset | `storage.formats[]` | 标量并集 |
| engine_asset | `features[]` | `name` |
| engine_asset | `hardware_compat[]` | `gpu_arch` |
| engine_asset | `supported_formats[]` | 标量并集 |
| hardware_profile | `container.volumes[]` | `name` |
| hardware_profile | `constraints.power_modes[]` | 标量并集 |
| hardware_profile | `partition.{gpu,cpu}_tools[]` | 标量并集 |
| hardware_profile | `container.security.supplemental_groups[]` | 标量并集 |
| deployment_scenario | `deployments[]` | `model` |
| deployment_scenario | `startup_order[]` | `step` |
| deployment_scenario | `post_deploy[]` | `action` |
| deployment_scenario | `alternative_configs[]` | `name` |
| deployment_scenario | `deployments[].modalities[]` | 标量并集 |
| partition_strategy | `slots[]` | `name` |
| stack_component | `sources[]` | `type` |

### 6.4 不支持显式删除

- 用户要删 → 删整个 user overlay 文件（从 factory 重新来）
- Central 要撤销 → SyncPullOverlays 响应带 `revoke` 动作，Edge 按 manifest 反查并删文件
- 未来如有需求可扩展 `$patch: delete` 语义

### 6.5 调试工具（patch 语义的必要配套）

- `aima catalog effective <kind> <name>` — 输出合并后的 effective asset
- `aima catalog diff <kind> <name>` — 展示 factory → effective 的 diff
- `aima catalog validate-patch <file>` — 离线校验 patch 合规性

---

## 7. Knowledge Flow

### 7.1 三个方向

**Intra-device（设备内）**
```
Exploration → Evidence (SQLite) → L2c (Resolver 读 golden) → Decision → Deployment
```
单机闭环，不依赖网络。Resolver 消费 Evidence 的唯一机制是 **L2c**。

**Inter-device（设备间，经 Central 中介）**
```
Device A Evidence → SyncPush → Central DB
                                     ├─→ Advisor → Advisory → Device B (个性化)
                                     └─→ Distillation Engine → patch → Fleet push
```

**Cross-layer（跨层，受 Gate 控制）**
```
Act → [Gate 1] → Evidence → [Gate 2] → Golden → [Gate 3/4] → Prior overlay patch
```

### 7.2 四个 Promotion Gates

| Gate | From → To | 位置 | 触发 | 条件 |
|---|---|---|---|---|
| **1** | Act 原始结果 → Evidence | Edge 自动 | Harvest 阶段 | Benchmark-Backed Contract 字段齐备 |
| **2** | `experiment` → `golden` | Edge auto / 手动 | auto-promote 或 `knowledge.promote` | throughput ≥ 当前 golden × 1.05 |
| **3** | 跨设备 golden → `model_asset_patch` | **Central Distillation Engine** | 显式 Agent 或人 | 跨 ≥ N 设备一致 + confidence 阈值 + approval |
| **4** | Central scenario → `deployment_scenario_patch` | **Central Distillation Engine** | 显式 Agent 或人 | schema 完整转换 + approval；允许 `verified=nil` |

**核心差异**：Gate 1/2 是 Edge 本地自动门；Gate 3/4 是 Central 的 production gate，必须经独立工具链 + audit log。

---

## 8. 架构 Invariants

这 6 条是本架构的**硬约束**。任何 PR / feature 设计违反其一都必须在 PR 描述中显式论证。

| # | 名称 | 内容 |
|---|---|---|
| **INV-K1** | **Evidence-First & Edge Read-Only for Prior** | Exploration 产出首先落 Evidence；**Edge 代码库不存在程序性写 Prior YAML 的 code path**；Prior 修改由 Central Distillation Engine 独占 |
| **INV-K2** | **Orthogonal Consumption Paths** | Resolver 消费 Evidence 的唯一机制是 **L2c**；Synthetic fallback 是**正交**路径（Prior miss 时兜底），两者不重叠；`Provenance` 必须标注来源层 |
| **INV-K3** | **Overlay Patch Discipline** | `~/.aima/catalog/{central,user}/` 所有文件必须是合规 patch：`kind: *_patch` + `metadata.name` + 遵循 §6.3 merge key 白名单；factory 是唯一完整 asset |
| **INV-K4** | **Explicit Gates with Audit** | Gate 3 / Gate 4 触发必须通过独立工具链 + `distillations` 表 audit log。"显式" 定义：不作为 Harvest 或其他自动流程的副作用；触发者可以是 Agent，但必须走独立 tool call 并留 log |
| **INV-K5** | **Decision Traceability** | Decision 层 Provenance 能标出每个参数的来源；L2c 参数可回溯到 benchmark；Central 下发的 patch 通过 manifest.json 的 distillation_id 回溯到 `distillations` 表 |
| **INV-K6** | **Device Isolation with Central Mediation + Fleet Exception** | 默认跨设备必须经 Central 中介。例外：`hardware_profile.metadata.name` 完全相同 + GPU 指纹一致时允许 Fleet push |

---

## 9. 跨 Repo 契约

Central 独立于 `aima-central-knowledge` repo。两端通过以下契约对接。

### 9.1 端点与载荷

| 方向 | 端点 | 新增（相对 v0.3.x）|
|---|---|---|
| Edge → Central | `POST /api/v1/ingest` | `applied_overlays` heartbeat |
| Edge → Central | `POST /api/v1/advisories/{id}/feedback` | 无 |
| Central → Edge | `GET /api/v1/sync` | 无 |
| Central → Edge | `GET /api/v1/advisories` | 无 |
| Central → Edge | `GET /api/v1/overlays` | **新增**；返回 patch + `op: upsert/revoke` |

### 9.2 Scenario Schema 转换（Central 侧完成）

Central 的轻量 `scenario_yaml` → Edge 的 `deployment_scenario_patch`。关键映射：

| Central | Edge patch | 说明 |
|---|---|---|
| `name` / `description` / `hardware` | `metadata.name` / `metadata.description` / `target.hardware_profile` | 直接映射，+前缀 `central-` |
| `deployments[].dependencies: [model_name]` (新契约) | `startup_order[]` | Central 提供依赖图则生成 startup_order |
| `reasoning` / `confidence` | `metadata.description` 尾部 | 追加 `; distillation_id=<x>; confidence=<y>` |
| — | `role` / `slot` / `modalities` / `post_deploy` / `integrations` / `alternative_configs` | Edge 独有；import 时取默认值 |
| — | `verified` | **不写**，表达"未本地验证" |

### 9.3 契约同步纪律

Central repo schema 变动时必须同步更新：
- `aima-central-knowledge/api/openapi.yaml`
- Edge 侧 `cmd/aima/tooldeps_integration.go` normalization 函数
- Edge 侧 §6.3 merge key 白名单（如果新增 list 字段）

---

## 10. 验证：架构能否回答典型问题

### Q1: 单设备发现的 golden config 怎么让同 arch 的其他设备受益？

两条独立路径：
- **Advisory**（快、针对性）：Central Advisor → advisory → 目标设备本地验证 → Gate 2 promote
- **Distillation**（系统性）：跨 ≥ N 设备一致 → Distillation Engine 生成 patch → Fleet push → 目标设备 `central/` 接收

### Q2: 用户手改 overlay 会不会被 Central 覆盖？

不会。user/ 永远最高优先级（`user > central > factory`）；Central push 只写 `central/`。

### Q3: Central Distillation Engine 挂了，Edge 还能工作吗？

完全能。Edge 6 阶段 Pipeline 全部本地闭环；L2c 消费 SQLite 本地 golden；已 apply 的 `central/` patch 继续可用；只是新的 distillation 停了。这是 INV-K1 + INV-K6 合力的结果。

### Q4: 同名 patch 冲突时效果？

factory (base) < central patch < user patch。但因为是 patch 语义（增量叠加），factory 未被 patch 触及的字段永远保留。

### Q5: Agent 自主调用 Gate 3，算"显式"吗？

算。INV-K4 的"显式"定义是 "走独立工具链 + audit log"，不是 "必须人点"。Agent 走独立 MCP tool 并写 `distillations.approved_by = agent:<model>` 满足条件。

---

## 11. 一句话版本

**AIMA 的知识系统是：Edge 只产 Evidence、不写 Prior；Prior = factory（完整）+ `~/.aima/catalog/central/`（Central 下发的 patch）+ `~/.aima/catalog/user/`（用户手写的 patch），按 AIMA Strategic Merge 规则合成 effective asset；`central/` 由 Central Distillation Engine 独占生成（Agent + 人协同审批）；Resolver 按 effective Prior → L2c → UserOverride → L3a fallback 合成 Decision；跨设备共享只能经 Central 中介，Edge 之间永不直连。**

---

## 附录 A — 术语表

| 术语 | 含义 |
|---|---|
| **Prior / Evidence / Decision** | 知识三层；架构讨论用此术语 |
| **L0 / L1 / L2c / L3a** | Resolver 内部合并顺序；仅在讨论 Resolver 时使用 |
| **Effective Asset** | `merge(factory, central patches, user patches)` 的合并结果 |
| **Factory base** | go:embed 的完整 asset YAML |
| **Patch (overlay)** | `kind: *_patch` 的 YAML 文件，只含相对 factory 的增量 |
| **Merge Key** | List 合并时的身份字段（如 variants 按 `name`） |
| **AIMA Strategic Merge** | §6 定义的 AIMA 自己的 patch merge 规则 |
| **Gate 1** / **Gate 2** | Edge 自动门（Evidence 落盘 / Golden promote） |
| **Gate 3** / **Gate 4** | Central production gate（model_asset_patch / deployment_scenario_patch） |
| **Distillation Engine** | Central 的第三能力模块，生成 Prior patch |
| **Overlay manifest** | `~/.aima/catalog/central/manifest.json`，记 distillation_id + hash |

## 附录 B — Invariant 速查卡

```
INV-K1  Evidence-First & Edge Read-Only for Prior
INV-K2  Orthogonal Consumption Paths (L2c vs Synthetic 正交)
INV-K3  Overlay Patch Discipline (kind: *_patch + merge key 白名单)
INV-K4  Explicit Gates with Audit (Gate 3/4 走独立工具链 + audit log)
INV-K5  Decision Traceability
INV-K6  Device Isolation + Central Mediation + Fleet Exception
```

## 附录 C — 参考

- `design/PRD.md`, `design/ARCHITECTURE.md`, `design/MRD.md` — AIMA 整体产品 / 架构 / 市场文档
- `docs/knowledge.md §3.4.1` — Benchmark-Backed Knowledge Contract
- `docs/superpowers/specs/2026-04-07-v0.4-knowledge-automation-design.md` — Explorer / Harvester 具体实现（本文相应部分已更新为"Harvester 只写 Evidence"语义）
- `aima-central-knowledge/api/openapi.yaml` — 跨 repo API 契约
- Kubernetes Strategic Merge Patch — merge key 理念来源
- MLflow Model Registry — staged promotion 理念来源
