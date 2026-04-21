# AIMA v0.4.0 Knowledge YAML 生成与消费设计分析

日期: 2026-04-21

## 1. 目标与结论

本文分析 AIMA 仓库 **`develop` 分支** 下，`exploration` 产生的知识与 `central` 产生的知识，应该如何转成新的 YAML 文件供 AIMA 消费。

先给结论：

1. **不应该**把 `exploration + central` 的所有知识揉成一个统一的“知识 YAML 包”。
2. AIMA v0.4 的知识体系本身就是 **双层架构**：
   - 动态知识: SQLite / JSON，同步、查询、分析、推理都走这层
   - 静态知识资产: catalog YAML / overlay YAML，只承接“已经足够稳定、值得被 resolver 或 scenario 系统直接消费”的知识
3. 因此，合理的 YAML 生成策略不是“一种文件”，而是 **按消费面拆成两种资产**：
   - `model_asset`:
     承接 **经过蒸馏、值得固化/分发** 的单模型知识，而不是所有本地运行时事实
   - `deployment_scenario`:
     承接 **可显式应用** 的多模型编排知识
4. `central` 的原始 `advisory`、跨设备 `benchmark/configuration`、`knowledge_note` **默认不应直接自动物化为 YAML**。
   它们应该先停留在动态知识层，只有满足明确晋升条件时才转为 YAML。

换句话说，v0.4 下更合理的边界是：

- `exploration` 的结果先落事实库，默认通过 `L2c golden config + observation` 被消费，只有在需要固化/分发时才选择性晋升到 `model_asset`
- `central` 的结果先作为动态候选输入，再通过本地验证或显式导入，晋升到 `model_asset` 或 `deployment_scenario`

## 2. 分析范围与依据

本分析基于当前仓库的 `develop` 分支阅读，主要参考以下实现与设计：

- `docs/knowledge.md`
- `docs/superpowers/specs/2026-04-07-v0.4-knowledge-automation-design.md`
- `docs/superpowers/specs/2026-04-09-explorer-agent-planner-design.md`
- `docs/superpowers/specs/2026-04-13-multimodal-benchmark-design.md`
- `internal/agent/exploration.go`
- `internal/agent/explorer.go`
- `internal/knowledge/loader.go`
- `cmd/aima/tooldeps_knowledge.go`
- `cmd/aima/tooldeps_integration.go`
- `internal/central/advisor.go`
- `cmd/aima/scenario.go`

需要额外强调一条 `CLAUDE.md` 边界：

- **Central 已拆分到独立 repo**
- edge repo 中关于 central 的工作只应包括：
  - HTTP payload / normalization
  - 本地消费与落盘策略
- central 端 schema、`scenario_yaml` 结构、advisory/scenario 生命周期的服务端实现，归 `aima-central-knowledge` repo 负责

因此，本文里凡是涉及 central payload 结构的改动，都应理解为 **跨 repo 契约变更**，不是只改 edge repo 就能落地。

## 3. 当前设计里的知识分层

### 3.1 双层知识架构是显式设计，不是实现细节

`docs/knowledge.md` 已经明确把知识拆成两层：

- YAML:
  人可读、可 diff、可版本化、可 go:embed、可 overlay 热更新
- SQLite:
  Agent 可查询、可 JOIN、可聚合、可承接动态探索结果

当前表意非常清楚:

- YAML 是“编写/分发格式”
- SQLite 是“查询/推理格式”

因此，**任何需要频繁同步、频繁变更、存在证据链和状态流转的知识，都应该优先留在动态层**。

### 3.2 动态知识目前承接什么

当前 v0.4 的动态知识核心是：

- `configurations`
- `benchmark_results`
- `knowledge_notes`
- 以及 central 侧的 `advisories` / `scenarios`

本地 `knowledge export/import/sync` 走的是 JSON 包，不是 YAML。`cmd/aima/tooldeps_knowledge.go` 与 `cmd/aima/tooldeps_integration.go` 已经把这条链路打通：

- `ExportKnowledge`: 导出 `configurations/benchmark_results/knowledge_notes`
- `ImportKnowledge`: 导入同一结构
- `SyncPush`: 把导出的 JSON 包发往 central
- `SyncPull`: 从 central 拉 JSON，再导入本地 SQLite

这说明当前体系里：

- “知识交换格式”是 JSON
- “知识运行态”是 SQLite
- “YAML”不是同步协议

这也与 `CLAUDE.md` 的几条原则一致：

- `INV-5`: MCP tools 是 single source of truth
- `INV-8`: offline-first，网络只是 enhancement
- Prime Directive: 少写 Go，多复用已有存储与现有边界

因此，对“新增 YAML 文件供 AIMA 消费”的判断必须很克制：

- 不是任何可同步的知识都该进 YAML
- 不是任何本地验证结果都该自动写 catalog

### 3.3 静态 YAML 目前承接什么

`internal/knowledge/loader.go` 当前支持的 overlay 资产种类是：

- `hardware_profile`
- `engine_asset`
- `model_asset`
- `partition_strategy`
- `stack_component`
- `deployment_scenario`

其中和本问题直接相关的只有两种：

1. `model_asset`
2. `deployment_scenario`

这已经天然限定了“供 AIMA 消费的新 YAML”应该落到哪个 kind，不需要再发明第三种“综合知识 YAML”。

## 4. 当前实现到底做到了哪里

### 4.1 Exploration 已经会自动写 `model_asset` overlay

`internal/agent/exploration.go` 的 `maybeCreateKnowledge()` 会在成功 benchmark 后：

1. 解析 benchmark 结果
2. 取真实 deploy config
3. 构造 `model_asset`
4. 调用 `catalog.override`
5. 把 YAML 写到 overlay catalog

这说明 v0.4 当前已经存在一条明确链路：

`exploration -> benchmark evidence -> model_asset overlay`

这条链路的方向本身是对的。

### 4.2 Central 的动态知识拉取也已经存在

`cmd/aima/tooldeps_integration.go` 当前已经支持：

- `sync_pull` 拉回动态知识 JSON 并导入 SQLite
- 额外拉 `advisories`
- 额外拉 `scenarios`

也就是说，central 的知识已经能到达 edge，但到达后的默认落点仍然是：

- SQLite
- EventBus
- knowledge note

而不是 catalog YAML。

### 4.3 `knowledge.resolve` 已经优先消费 L2c，本地知识不必先转 YAML

`knowledge.resolve` 当前显式会合并：

- YAML defaults
- golden configs
- user overrides

其中 golden config 来自 SQLite 的 L2c 路径，而不是来自自动生成的 `model_asset` YAML。

这意味着从设计哲学上看：

- **本地探索知识的默认消费路径已经存在**
- 它就是 `configurations/benchmark_results` -> `golden` -> `knowledge.resolve`
- 不需要为了让本机继续工作，先把本地知识都物化成 YAML

这点和 `CLAUDE.md` 的以下原则一致：

- less code
- offline-first
- MCP / resolve chain 是 single source of truth

因此，后文所有“生成 `model_asset` YAML”的建议都应该理解为：

- 用于固化、分发、或给 catalog 提供稳定 prior
- **不是本地运行时知识的默认主路径**

### 4.4 Central scenario 目前还没有闭环成 catalog scenario

当前 `Explorer.handleScenario()` 的行为是：

1. 解析收到的 central scenario 事件
2. 检查本机硬件是否大致匹配
3. 记录一条 knowledge note

它不会：

- 生成 `deployment_scenario` YAML
- 写入 `~/.aima/catalog/scenarios/*.yaml`
- 让 `scenario.list/show/apply` 自动消费这份 central scenario

这意味着 `central scenario -> AIMA 本地可消费 scenario asset` 这段在 `develop` 上还没有真正落地。

### 4.5 Central 生成的 `scenario_yaml` 不是 edge catalog 直接消费的格式

`internal/central/advisor.go` 的 `GenerateScenario()` 当前持久化的 `scenario_yaml` 结构更像一个轻量记录：

- `name`
- `description`
- `goal`
- `hardware`
- `deployments`
- `total_vram`
- `reasoning`
- `confidence`

但 edge 侧 `deployment_scenario` 的 schema 在 `internal/knowledge/loader.go` 里要求的是：

- `kind: deployment_scenario`
- `metadata`
- `target.hardware_profile`
- `deployments`
- 可选的 `post_deploy`
- 可选的 `startup_order`
- 可选的 `memory_budget`
- 可选的 `verified`

所以 central 现有的 `scenario_yaml` **不能原样当 overlay catalog 资产使用**，中间必须有一次显式转换。

### 4.6 `develop` 里已经存在“观测写到 catalog 之外”的实现信号

`cmd/aima/benchmark.go` 里已有 `updatePerfOverlay()`，并且注释写得很直接：

- benchmark observations 写在 catalog merge path 之外
- runtime overlays 不应伪装成 `model_asset`
- 原因是同名 asset 会在重启后替换 embedded catalog

这说明 `develop` 当前其实已经出现一个重要设计信号：

- **稳定 catalog 资产** 与 **运行时性能观测** 应该分开

这点会直接影响本文对 `expected_performance` 的建议。按 `CLAUDE.md` 的 less-code 与边界原则，不能要求一个运行时生成器为了解决“观测持久化”，去承担完整 catalog 影子复制的复杂性。

## 5. 为什么“合成一个总 YAML 文件”不合理

如果把 `exploration` 和 `central` 产出的所有知识合成一个新 YAML，会同时踩到以下问题。

### 5.1 消费面不同

单模型经验知识与多模型编排知识的消费路径完全不同：

- `model_asset` 被 `knowledge.resolve`、resolver、部署推荐消费
- `deployment_scenario` 被 `scenario.list/show/apply` 消费

把两者塞进一个总文件，既不符合 loader schema，也会让后续消费方继续分拆。

### 5.2 可信度不同

`exploration` 本地成功 benchmark 的知识是“已验证事实”。

`central` 提供的很多内容则只是：

- 跨设备经验
- 候选建议
- 尚未在本机验证的 scenario

这些东西的可信级别不同，不应该在 YAML 里伪装成同一层“静态真相”。

### 5.3 schema 能力不同

`model_asset` 天然适合表达：

- 某模型在某类硬件上的变体
- 默认配置
- 预期性能

它不擅长表达：

- central advisory 的状态
- 反馈闭环
- “这是跨设备建议但本机未验证”

而 `deployment_scenario` 天然适合表达全设备级方案，但不适合承接单 benchmark 的所有分析痕迹。

### 5.4 生命周期不同

动态知识更新频率高，YAML 应该更稳定：

- benchmark / config / note 可以频繁变化
- catalog YAML 应该只在“值得晋升”为稳定资产时变化

如果每次 central 或 exploration 一有新观察就重写 YAML，catalog 会失去“稳定 prior”的意义。

## 6. 当前实现里的一个关键风险

### 6.1 Overlay 是整 asset 替换，不是 patch

`internal/knowledge/loader.go` 的 `MergeCatalog()` 语义是：

- 同名 overlay asset 直接替换 base asset
- 不是深度 merge
- 更不是 variant 级 patch

这是本文最重要的设计约束之一。

### 6.2 这意味着薄 YAML 会把原始 model asset 覆盖掉

当前 `maybeCreateKnowledge()` 生成的是一个偏薄的 `model_asset`：

- 写 `metadata`
- 写 `storage.formats`
- 写一个 `variants` 项

但如果 factory catalog 里原本已经有同名 model asset，那么 overlay 替换后，原始资产里诸如以下内容都可能丢失：

- `storage.default_path_pattern`
- `storage.sources`
- `metadata.aliases`
- 其它已有 variants
- `openclaw` hints

这会导致两个后果：

1. overlay 语义上不再是“增量补充”，而是“重写整个模型资产”
2. 只用 exploration 单次结果生成的薄 YAML，可能反而削弱 catalog 原有能力

因此，**任何正式的 YAML 生成方案都必须先做“基于现有 asset 的完整 merge”，再输出完整 YAML**。

## 7. 合理的设计边界

推荐把知识晋升拆成三条线，而不是一锅端。

### 7.1 线 A: 本地运行时知识默认走 L2c / observation，不直接晋升 YAML

适合来源：

- 本地 exploration 成功验证
- tuning / benchmark 产出的 golden config
- benchmark 观测与 knowledge note

默认落点：

- `configurations`
- `benchmark_results`
- `knowledge_notes`
- `golden` status
- 运行时 observation sidecar

理由：

- 本地 runtime 已能通过 L2c 消费这些知识
- 这条路径不依赖网络
- 这条路径不需要重写 catalog 资产
- 更符合 `CLAUDE.md` 的 less-code / offline-first

### 7.2 线 B: 蒸馏后的单模型知识晋升为 `model_asset`

适合来源：

- 本地 exploration 成功验证后，经过显式蒸馏/导出
- central advisory 在本地验证通过后，被吸收到稳定 prior

不适合直接来源：

- central 未验证 advisory
- central 跨设备 benchmark 事实

原因是 `model_asset` 会直接影响 resolver 的 L0 prior，而不是只影响 L2c 运行时覆盖。

### 7.3 线 C: 多模型方案晋升为 `deployment_scenario`

适合来源：

- central 生成或下发的 scenario
- 人工编辑的设备级编排方案
- 本地把多个已验证 deployment 汇总为可复用场景

它天然是“显式 apply”的资产，风险面比 `model_asset` 小，允许保留“未验证但可尝试”的状态。

### 7.4 不应自动物化为 YAML 的内容

以下内容默认应留在动态层：

- `configurations`
- `benchmark_results`
- `knowledge_notes`
- central `advisories`
- central 跨设备 benchmark/configuration 原始记录

这些内容应该继续走：

- JSON export/import/sync
- SQLite query/search/analytics

而不是直接写 catalog asset。

## 8. 推荐设计: 四段式晋升模型

### 8.1 Stage 0: 动态事实层

所有新知识首先进入动态层：

- `configurations`
- `benchmark_results`
- `knowledge_notes`
- `advisories`
- `scenarios`

这是唯一完整保存证据链的地方。

### 8.2 Stage 1: 本地运行时消费层

本地 exploration 的默认消费方式是：

- benchmark / tune 结果进入 SQLite
- 最优配置提升为 `golden`
- `knowledge.resolve` 通过 L2c 注入 golden config
- 详细观测进入 sidecar observation，而不是 catalog

这是符合 `CLAUDE.md` 的首选路径，因为：

- 核心功能在零网络下可用
- 不新增额外静态资产复杂度
- CLI / MCP / resolver 继续共用同一条代码路径

### 8.3 Stage 2: 蒸馏后的 `model_asset` 晋升

当满足以下条件时，允许把知识晋升为 `model_asset` overlay：

1. 至少有一组成功 benchmark
2. 对应真实 deploy config 已落 `configurations`
3. benchmark 元数据完整，满足 benchmark-backed knowledge contract
4. 若知识最初来自 central advisory，则必须已经过本地验证

核心原则：

- **本地验证是 `model_asset` 的硬门槛**
- central 只能提供候选，不直接生成 resolver 会吃的 model variant

### 8.4 Stage 3: 显式导入的 `deployment_scenario` 晋升

当满足以下条件时，允许把 central scenario 物化为 `deployment_scenario` overlay：

1. scenario 明确绑定 `hardware_profile`
2. `deployments` 结构能映射到 edge schema
3. 用户显式导入，或已有本地缓存后再做离线消费

按 `INV-8`，这里不应把“联网拉 central + 自动导入 scenario”设计成核心默认路径。
如果未来提供 auto-import，也应是 enhancement，且默认关闭。

这里不要求“先本地全量验证再落 YAML”，因为 scenario 本来就是显式应用资产。
但必须保留“未验证”状态，而不能伪装成已验证真相。

## 9. 推荐的 YAML 生成规则

## 9.1 `model_asset` 生成规则

### 输入来源

- base:
  当前 merged catalog 里的同名 `model_asset`
- local evidence:
  本地 `configurations + benchmark_results + knowledge_notes`
- optional prior:
  central advisory 的内容，但只能作为候选输入，不能直接成为已验证输出

### 生成方式

1. 读取 base `model_asset`
2. 深拷贝为 working copy
3. 选择或创建一个稳定命名的 variant
4. 只更新该 variant 的：
   - `hardware`
   - `engine`
   - `format`
   - `default_config`
   - `expected_performance`
5. 保留 base asset 的其它字段：
   - `metadata.aliases`
   - `storage.default_path_pattern`
   - `storage.sources`
   - `openclaw`
   - 其它 variants
6. 输出完整 `model_asset` YAML

但这里需要按 `CLAUDE.md` 再加一条边界：

- 这个生成器应被理解为 **蒸馏/导出器**
- 不是每次 runtime benchmark 后都自动触发的默认持久化路径
- Go 侧只负责结构安全与一致性，不负责“这次要不要晋升”为 catalog prior 的策略判断

### 稳定的 variant key

推荐不要用 `benchmark_id` 做 variant 名字，否则每次验证都会新增一个 variant。

更合理的 key 应该稳定地反映“可执行边界”：

- `model`
- `engine`
- `gpu_arch`
- `gpu_count_min`
- `modality` if needed

推荐命名：

- 单模态:
  `<model>-<gpu_arch>-<engine>-validated`
- 多模态:
  `<model>-<gpu_arch>-<engine>-<modality>-validated`

### hardware 维度怎么定

`model_asset` schema 没有 `hardware_profile` 字段，因此它不适合表达“完全设备特定”的知识。

推荐从本地事实里抽取相对稳定的硬件边界：

- 必填:
  - `gpu_arch`
- 视情况补充:
  - `gpu_count_min`
  - `ram_min_mib`
  - `unified_memory`
  - `gpu_model`

使用规则：

1. 默认优先 `gpu_arch` 级泛化
2. 如果配置显著依赖多卡，补 `gpu_count_min`
3. 如果配置显著依赖 CPU/RAM offload，补 `ram_min_mib`
4. 只有在经验明显只适用于某型号 GPU 时才写 `gpu_model`

这比直接把 variant 绑定死到某个 `hardware_profile` 更符合当前 schema。

### `default_config` 规则

必须来自真实 deploy config，而不是 benchmark profile。

应保留：

- `tensor_parallel_size` / `tp_size`
- `gpu_memory_utilization` / `mem_fraction_static`
- `max_model_len`
- `max_running_requests`
- 所有 offload 参数

必须剔除：

- 内部临时参数
- 只用于执行层的隐藏键
- `nil` 值

### `expected_performance` 规则

按理想知识契约，`expected_performance` 希望能同时保留两类字段：

1. resolver 友好的平铺字段
2. 分析友好的结构化证据块

推荐至少包含：

- 平铺:
  - `throughput_tps`
  - `qps`
  - `ttft_p50_ms`
  - `ttft_p95_ms`
  - `ttft_p99_ms`
  - `tpot_p50_ms`
  - `tpot_p95_ms`
  - `error_rate`
  - `tokens_per_second`
  - `latency_first_token_ms`
- 结构化:
  - `benchmark_profile`
  - `resource_usage`
  - `heterogeneous_observation`
  - `benchmark_id`
  - `config_id`
  - `engine_version`
  - `engine_image`

但结合 `CLAUDE.md` 的 less-code 原则，以及 `develop` 中 `updatePerfOverlay()` 已经把详细观测放到 catalog merge path 之外，这里需要收紧：

1. `model_asset.expected_performance` 默认只放 **resolver 真正会消费、且相对稳定的摘要字段**
2. 完整 benchmark profile / resource_usage / heterogeneous observation 默认放在：
   - SQLite
   - perf observation sidecar
3. 只有当未来 loader/merge 语义支持安全承载这些细粒度观测时，才考虑把完整结构重新并入 catalog asset

也就是说，当前更符合项目哲学的做法是：

- `model_asset` 保持精简稳定
- 运行时观测放到动态层或 sidecar

### central advisory 的合并规则

central advisory 如果还没在本地验证：

- 不进入 `default_config`
- 不进入 `expected_performance` 的已验证字段
- 只允许写入 `notes` 或暂存到动态层

central advisory 如果已经在本地验证通过：

- 它就不再是“central 原始知识”
- 而是“本地已验证知识”
- 可以正常写入 variant

## 9.2 `deployment_scenario` 生成规则

### 输入来源

- central `scenario_yaml`
- central `scenario` 元数据
- 可选的本地已验证 deployment 事实

### 转换规则

central 当前的轻量 `scenario_yaml` 需要被映射为 edge catalog schema：

- `name` -> `metadata.name`
- `description` -> `metadata.description`
- `hardware` -> `target.hardware_profile`
- `deployments` -> `deployments`
- `goal` / `reasoning` / `confidence`:
  不建议新造顶层字段，优先放入：
  - `metadata.description`
  - 各 deployment 的 `notes`
  - 或 `open_questions`

### 推荐输出 shape

```yaml
kind: deployment_scenario
metadata:
  name: central-qwen3-8b-glm47-balanced
  description: "Imported from central on 2026-04-21; source=generated; confidence=medium"
target:
  hardware_profile: nvidia-gb10-arm64
deployments:
  - model: qwen3-8b
    engine: vllm-spark
    role: llm
    config:
      gpu_memory_utilization: 0.82
      max_model_len: 32768
    notes: "Imported from central scenario; not yet locally verified"
  - model: glm-4.7-flash
    engine: vllm-spark
    role: vlm
    config:
      gpu_memory_utilization: 0.65
    notes: "Imported from central scenario; not yet locally verified"
```

### 验证状态表达

由于 `deployment_scenario` 已有 `verified` 可选字段，推荐：

- central 新导入但未本地验证:
  不写 `verified`
- 本地跑通过后:
  写 `verified.date`
  写 `verified.hardware`
  写 `verified.results`

这样可以自然表达“候选场景”与“已验证场景”的差异。

### naming 策略

scenario 是全设备编排，建议以 exact `hardware_profile` 维度落盘，不做 `gpu_arch` 泛化。

推荐文件名 / metadata.name：

- `central-<hardware_profile>-<normalized-name>`

这样可以避免与 factory scenario 同名冲突，也能一眼识别来源。

## 10. central 知识哪些应该进 YAML，哪些不应该

### 10.1 应该进 YAML 的

1. **central scenario**
   - 因为它天然对应 `deployment_scenario` 消费面
   - 且 `scenario.apply` 是显式操作，允许保留“未验证”状态

2. **central advisory 经本地验证后沉淀出的单模型最佳配置**
   - 此时它已经转化为本地证据
   - 可以进入 `model_asset`

### 10.2 不应该直接进 YAML 的

1. central 原始 advisory
2. central 跨设备 benchmark/configuration 原始记录
3. central knowledge note

原因：

- 它们要么缺少本地验证
- 要么是状态流转中的过程对象
- 要么 schema 上不适合直接被 catalog 消费

## 11. 推荐的最终设计

综合当前 `develop` 设计，我建议把“生成新的 YAML 文件供 AIMA 消费”设计成下面这套规则。

### 11.1 总原则

**只把“稳定、可直接被 catalog 消费的知识”晋升为 YAML。**

其它所有知识继续留在 SQLite/JSON。

补一条 `CLAUDE.md` 风格约束：

- **机制写在 Go / MCP tool**
- **策略尽量不写死在 Go**

也就是说：

- Go 负责：
  - schema 转换
  - safety gate
  - merge 语义
  - 写 overlay
- Agent / tool input / 配置负责：
  - 何时晋升
  - 选哪个 scenario 导入
  - 哪个 validated variant 值得固化为 catalog prior

### 11.2 推荐的晋升矩阵

| 知识来源 | 原始落点 | 是否自动生成 YAML | 目标 YAML kind | 条件 |
|----------|----------|------------------|----------------|------|
| 本地 exploration 成功 benchmark | SQLite | 否，默认只进 L2c / observation | 无 | 先服务本地运行时消费 |
| 本地 validated knowledge 经显式蒸馏 | SQLite | 是 | `model_asset` | 满足 benchmark-backed contract 且确认值得固化 |
| central advisory | SQLite/EventBus | 否 | 无 | 先验证 |
| central advisory + 本地验证成功 | SQLite | 是 | `model_asset` | 以本地验证结果为准 |
| central sync 回来的 benchmark/config/note | SQLite | 否 | 无 | 保持动态事实 |
| central scenario | SQLite/EventBus | 建议显式导入，默认非自动 | `deployment_scenario` | 完成 schema 转换 |
| 本地验证后的 scenario | overlay YAML | 更新 | `deployment_scenario` | 回写 `verified` |

### 11.3 实现建议

建议新增两个清晰的生成器，而不是一个总生成器：

1. `GenerateModelAssetOverlayFromEvidence(...)`
   - 输入: base asset + local evidence + optional central prior
   - 输出: 完整 `model_asset` YAML

2. `GenerateScenarioOverlayFromCentral(...)`
   - 输入: central scenario payload
   - 输出: 完整 `deployment_scenario` YAML

但落地方式需要符合 `INV-5`：

- 若新增对外入口，先做成 MCP tool
- CLI 只做 thin wrapper
- 不要把 import / promote 逻辑直接塞进 CLI

两者都应优先复用现有：

- `catalog.override`
- `knowledge.resolve`
- `knowledge.promote`
- `central.scenario` / `central.sync`

而不是新造一层旁路。

## 12. 对 develop 当前实现的改进建议

如果后续要补齐这条链路，优先级建议如下。

### P0: 重新界定本地知识默认路径

当前文档原先把本地 validated knowledge 默认推向 `model_asset`，这一点按 `CLAUDE.md` 需要修正为：

- 本地默认走 L2c + observation
- `model_asset` 仅用于显式蒸馏/固化

这能减少 Go 逻辑复杂度，也更符合 offline-first。

### P1: 修正 `model_asset` overlay 生成方式

若仍需要生成 `model_asset` overlay，则必须：

- 先读 base asset
- 做 variant 级 merge
- 输出完整 asset
- 避免把 runtime observation 直接塞成 catalog shadow

### P2: 新增 central scenario 到 deployment_scenario 的转换器

当前 central scenario 已经能到 edge，但还不能成为本地 scenario catalog 资产。

建议新增：

- 显式 import MCP tool
- 或基于本地缓存的离线导入

完成：

`central scenario -> deployment_scenario overlay`

### P3: 统一 promotion gate

把“动态知识什么时候晋升为 YAML”收敛成显式规则，而不是散在不同模块里。

最少应统一这四条：

1. 本地 validated knowledge 默认先走 L2c，而不是先写 YAML
2. `model_asset` 必须本地验证且显式蒸馏
3. `deployment_scenario` 可未验证导入，但必须显式标识
4. `advisory` / `benchmark` / `note` 默认不落 YAML

## 13. 最终建议

如果你的目标是“让 AIMA 当前 v0.4 合理消费 exploration + central 产生的新知识”，最合理的设计不是生成一个总 YAML，而是：

1. **继续把动态事实留在 SQLite/JSON**
2. **把本地探索结果的默认消费路径定义为 L2c + observation，而不是自动写 catalog**
3. **只把显式蒸馏后的单模型稳定知识生成 `model_asset`**
4. **只把多模型编排知识生成 `deployment_scenario`**
5. **central 原始知识默认不直接物化为 YAML**
6. **生成 overlay 时必须输出完整 asset，不做薄覆盖**
7. **所有新增入口先走 MCP tool，CLI 不持有业务逻辑**

这套设计和当前 v0.4 的体系边界是一致的：

- 不违背双层知识架构
- 不破坏 sync/import/export 现有 JSON 协议
- 更符合 `CLAUDE.md` 的 less-code / offline-first / MCP single-source 原则
- 不把未验证的 central 建议伪装成 resolver 真相
- 能自然补齐 central scenario 目前“能到 edge、不能被 catalog 消费”的缺口

## 14. 一句话版本

`exploration` 的知识默认应先停留在 SQLite，并通过 L2c + observation 被消费；
只有经过显式蒸馏、确认值得固化时，才晋升成 `model_asset`；
`central` 的知识大多仍应停留在动态层，只有 `scenario` 这类天然面向显式 apply 的对象，才适合转换成 `deployment_scenario`；
无论哪种资产，生成时都必须基于现有 asset 做完整 merge，不能输出会覆盖 factory 信息的薄 YAML。
