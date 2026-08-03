# AIMA 通用推理恢复与 Engine 生命周期设计

日期：2026-07-30
状态：已完成需求方设计确认，等待书面规格复核

## 1. 背景

合作伙伴提出两类需求：

1. 推理框架进程异常退出后，AIMA 能检测并执行有限次数的恢复。
2. 推理框架由 AIMA 统一发现、下载、安装和升级，调用方不再维护各引擎的生命周期逻辑。

本设计将它们实现为 AIMA 的通用能力，不增加合作伙伴名称、产品名称、引擎类型或模型类型分支。合作伙伴接入只提供 Catalog 数据、预装资产信息和验证证据。

## 2. 现状与缺口

### 2.1 已有能力

- K3S Pod 使用 `restartPolicy: Always`、liveness probe 和 readiness probe。
- Docker 使用 `--restart unless-stopped`。
- Native Runtime 能持久化进程 metadata、检测进程退出、查询 HTTP 健康状态并安全停止进程。
- Patrol 能发现失败部署，Healer 能诊断部分 OOM 和镜像拉取失败。
- Engine 已有 `scan`、`pull`、`import`、`remove`、Native 二进制下载和预装探测。
- Engine Asset 已能通过 YAML 描述平台、来源、版本、摘要、探测方式和启动行为。

### 2.2 已确认缺口

- Native Runtime 检测退出后只记录失败，不会恢复进程。
- 没有跨 Runtime 的期望状态、恢复窗口、退避和隔离状态。
- Patrol 间隔较长且依赖 Agent/自愈配置，不适合作为基础可用性机制。
- Docker 状态未读取容器重启次数。
- Engine inventory 未保存来源、检测版本、Catalog 版本、摘要、活动版本和回滚关系。
- Native 托管二进制使用平台级目录，无法安全地版本并存和原子切换。
- OCI digest 与 Native SHA256 校验不匹配时目前只记录告警，没有阻断使用。
- 预装探测只要解析到版本就标记为 `exact`，没有和 Catalog 期望版本比较。
- `engine.remove --delete-files` 缺少预装资产所有权保护。

## 3. 目标

### 3.1 恢复能力

- 外部结束 Native 推理进程、进程崩溃或健康检查持续失败时，AIMA 按通用策略恢复。
- 用户通过 AIMA 显式删除部署后，后台不得重新拉起。
- AIMA 服务重启后，从持久化期望状态继续恢复。
- AIMA 服务被明确停止时，不承诺 Native 推理进程自动恢复。
- K3S 和 Docker 继续负责实际容器重启；AIMA 只下发声明、观察状态、执行隔离和提供审计。

### 3.2 Engine 生命周期

- 优先发现和复用合作伙伴预装资产。
- 支持在线获取、局域网来源和离线导入。
- 支持托管版本并存、验证、激活和回滚。
- 不覆盖、不移动、不物理删除预装资产。
- 升级不静默重启现有部署。
- 所有操作通过 MCP 工具提供，CLI 只做薄包装。

## 4. 非目标

- 不接管合作伙伴源代码仓库或构建流水线。
- 不新增 Windows Service、systemd 或 launchd 级的推理进程守护。
- 不保证 AIMA 服务停止后的 Native 自动恢复。
- 不实现 AIMA 自有的容器重启循环。
- 不实现自动滚动升级正在运行的推理部署；运行中版本切换仍需显式 `deploy.apply`。
- 不增加通用软件包管理器、依赖解析器或厂商专用安装脚本框架。

## 5. 设计原则

1. 引擎、模型和合作伙伴差异属于 YAML 知识，不属于 Go 分支。
2. AIMA 对 K3S/Docker 只执行 apply、get/status、delete 和 logs。
3. MCP 是唯一操作事实源，人工、Agent、CLI 和后台控制器复用同一业务路径。
4. 核心发现、恢复、离线安装和回滚不依赖公网。
5. Go 代码只实现通用状态机与机制，策略值来自配置或 Catalog。
6. 预装资产默认不可变；只有 AIMA 明确拥有的资产可以物理删除。

## 6. 方案选择

采用“持久化期望状态 + AIMA Reconciler”方案。

未采用以下方案：

- 仅扩展 Patrol/Healer：恢复延迟高、依赖 Agent，并且无法提供稳定的基础重启语义。
- 分别接入三种操作系统 Supervisor：超出本次可靠性边界并引入平台专用实现。

## 7. 部署期望状态

### 7.1 持久化模型

在 SQLite 中新增 `deployment_intents`。它表示期望状态，不替代 Runtime 返回的实际状态，也不替代现有 Native process metadata。

建议字段：

| 字段 | 说明 |
|---|---|
| `name` | 部署唯一名称 |
| `revision` | 并发比较与交换版本 |
| `model` | 模型逻辑名称 |
| `engine_asset` | 固定的 Engine Asset 名称 |
| `engine_version` | 固定版本或内容摘要 |
| `slot` | 资源 Slot |
| `runtime` | 解析后的 Runtime |
| `config_json` | 非敏感用户覆盖参数 |
| `desired_state` | `running` 或 `stopped` |
| `recovery_state` | `healthy`、`waiting`、`recovering` 或 `quarantined` |
| `recovery_policy_json` | 解析并校验后的通用恢复策略 |
| `attempt_count` | 当前窗口已用恢复次数 |
| `consecutive_failure_count` | 当前连续健康失败次数 |
| `observed_restart_count` | 最近一次已处理的底层累计重启数 |
| `window_started_at` | 当前恢复窗口起点 |
| `next_attempt_at` | 下一次允许恢复时间 |
| `healthy_since` | 连续健康起点 |
| `last_exit_code` | 最近退出码 |
| `last_error` | 最近失败摘要，必须脱敏 |
| `created_at` / `updated_at` | 审计时间 |

Intent 只保存能够重新解析部署的声明式输入，不保存 API Key、Token 或展开后的敏感环境变量。恢复时重新走 Catalog 和配置解析，但必须使用已固定的 Engine Asset 与版本，不能因为 Catalog 默认值变化而静默升级。

### 7.2 恢复策略

默认策略：

```yaml
recovery:
  enabled: true
  check_interval_s: 5
  consecutive_failures: 3
  max_attempts: 3
  window_s: 600
  backoff_s: [2, 10, 30]
  stable_reset_s: 600
```

优先级为：部署请求显式覆盖、Engine Asset `startup.recovery`、AIMA 系统默认值。Go 代码只解释字段，不按引擎类型选择策略。

`backoff_s` 长度短于 `max_attempts` 时，后续尝试使用最后一个退避值；空数组使用系统默认值。所有数值必须通过统一范围校验。

### 7.3 状态流转

```text
deploy.apply
  -> 写入 desired=running
  -> Runtime.Deploy
  -> healthy

异常退出或连续健康失败
  -> waiting
  -> 到达 next_attempt_at
  -> recovering
  -> 成功后 healthy
  -> 失败后 waiting
  -> 超过窗口阈值后 quarantined

deploy.delete
  -> 先写入 desired=stopped
  -> Runtime.Delete
  -> 保持 stopped，后台不得恢复
```

显式再次调用 `deploy.apply` 会解除隔离并重置恢复窗口。Reconciler 自己调用恢复路径时不得重置计数。两者通过进程内可信调用上下文区分：`source=reconciler` 只能由 AIMA 内部设置，不作为公共 MCP 入参，外部调用者不能伪造。

连续健康达到 `stable_reset_s` 后清零恢复窗口。单次健康探测失败不会触发恢复，必须达到 `consecutive_failures`。

### 7.4 Runtime 行为

#### Native

- Reconciler 在 AIMA `serve` 生命周期内检测实际进程和 HTTP 健康状态。
- 期望为 `running` 且实际失败时，按退避重新走同一 `deploy.apply` 业务路径。
- AIMA 重启后读取 Intent，若期望仍为 `running` 且实际进程不存在，则继续未完成恢复。

#### Docker

- 保留 `unless-stopped`，不在 AIMA 中编写 Docker 重启循环。
- 状态解析增加 Docker `RestartCount` 和退出信息。
- 达到隔离阈值时，AIMA 将 Intent 置为 `quarantined`，再通过现有 `Runtime.Delete` 停止容器反复拉起。

#### K3S

- 保留 `restartPolicy: Always` 和现有 probes。
- 使用 Pod restart count、container state 和探针结果更新统一恢复状态。
- 达到隔离阈值时，先置为 `quarantined`，再通过现有 `Runtime.Delete` 删除 Pod，避免 CrashLoop 持续消耗资源。

上述 Docker/K3S 隔离仍只使用架构允许的 status/get 与 delete，不接管底层重启实现。

### 7.5 并发控制

- 人工操作与 Reconciler 按部署名称共享同一把锁。
- Intent 更新携带 `revision`，使用比较与交换避免旧状态覆盖新状态。
- `deploy.delete` 必须先提交 `stopped`，再操作实际进程或容器。
- Reconciler 获取锁后重新读取 revision 和 desired state，确认仍为 `running` 才能恢复。
- Reconciler 调用与 MCP handler 相同的部署业务函数，并通过内部 context 标记调用来源；不得复制一套部署实现。
- AIMA 停止时取消未开始的恢复计时器，不主动杀死健康工作负载。

### 7.6 Patrol 与 Healer

- Reconciler 是基础恢复机制。
- Patrol 只生成诊断、告警和 Explorer 事件。
- Healer 可以对 OOM、镜像拉取失败等已识别原因提出配置修复，但不得绕过隔离状态或重置恢复窗口。
- 同一部署进入 `quarantined` 后只生成一个活动告警，避免每轮 Patrol 重复恢复。

## 8. Engine 生命周期

### 8.1 Inventory 扩展

对现有 Engine inventory 做增量迁移，新增以下信息：

| 字段 | 说明 |
|---|---|
| `asset_name` | Catalog Engine Asset 名称 |
| `version` | 检测或安装的实际版本 |
| `catalog_version` | Catalog 期望版本 |
| `origin` | `preinstalled`、`managed`、`imported` 或 `legacy` |
| `content_digest` | SHA256 或 OCI digest |
| `location` | 镜像引用或 Native 路径 |
| `active` | 是否为该 Asset 当前活动版本 |
| `lifecycle_status` | `discovered`、`staged`、`verified`、`active` 或 `failed` |
| `verification_status` | `verified`、`unverified` 或 `mismatch` |
| `previous_engine_id` | 上一活动版本，用于回滚 |

旧记录迁移后标记为 `legacy` 并默认保护。重新扫描获得足够证据后，才能分类为预装或 AIMA 托管资产。

### 8.2 存储布局

Native 托管资产按版本或摘要并存：

```text
<AIMA_DATA_DIR>/dist/<platform>/<asset-name>/<version-or-digest>/
```

路径分量必须经过白名单化处理，禁止绝对路径、`..` 和路径分隔符注入。

预装资产保留在合作伙伴提供的原路径，只记录 inventory，不复制、不移动、不覆盖。

容器资产使用不可变 digest 作为验证依据；不同 tag/digest 可以并存。活动版本是 AIMA inventory 的选择，不通过删除旧镜像实现切换。

### 8.3 `engine.ensure`

`engine.ensure` 是统一的发现、安装和升级入口。

输入至少包含：

- `name`：Engine Asset 名称或通用引擎查询。
- `version`：可选；缺省使用当前 Catalog 固定版本。
- `apply`：缺省 `false`，只返回计划。

计划阶段：

1. 按硬件、平台和 Catalog 解析具体 Engine Asset。
2. 查找完全匹配的活动版本、预装版本、本地托管版本或兼容版本。
3. 计算来源、空间需求、校验要求、活动版本变化和受影响部署。
4. 若缺少安全升级所需摘要，将计划标记为 blocked。

执行阶段仅在 `apply=true` 且通过确认门控时发生：

1. 复用完全匹配的本地资产，或下载/导入到 staging。
2. 校验 SHA256 或 OCI digest。
3. 执行版本探测并与 Catalog 期望版本比较。
4. 执行 Catalog 声明的兼容性探测。
5. 将资产标记为 `verified`。
6. 在数据库事务中原子更新 active 和 previous 关系。
7. 保留旧版本。

`engine.ensure` 激活新版本后，只影响新的显式部署。当前运行中的部署继续固定原版本，直到用户或上层 Agent 明确调用 `deploy.apply`。

### 8.4 `engine.rollback`

- 必须显式确认。
- 目标必须仍然存在、与当前平台兼容且状态为 `verified`。
- 只原子切换 inventory 活动版本，不自动重启当前部署。
- 失败时活动版本保持不变。

### 8.5 删除保护

- `preinstalled` 和 `legacy` 永远不能通过 `delete_files` 物理删除。
- `managed/imported` 只有在规范化路径位于 AIMA 管理目录内时才能删除。
- 物理删除前检查是否被活动版本、回滚关系或现有 Intent 引用。
- 回滚 snapshot 只能恢复数据库状态，不能被描述为能够恢复已经删除的二进制；因此有引用的旧资产不得删除。

### 8.6 校验语义

- Catalog 声明摘要时，不匹配必须返回错误并阻止激活。
- 新的托管升级如果没有摘要，默认 blocked。
- 已存在的预装资产可以登记为 `unverified`，以保持向后兼容，但不能冒充 `verified`。
- `VersionMatch` 必须比较检测版本与 Catalog 版本，结果为 `exact`、`compatible`、`unknown` 或 `mismatch`。
- `compatible` 关系必须来自 Catalog，不由 Go 代码猜测。

## 9. MCP 与 CLI 契约

### 9.1 部署工具

- `deploy.apply` 增加可选 `recovery_policy`。
- 显式 `deploy.apply` 可以解除 `quarantined`；后台恢复调用不得解除或重置。
- `deploy.list` 和 `deploy.status` 增量返回：
  - `desired_state`
  - `recovery_state`
  - `recovery_attempts`
  - `next_recovery_at`
  - `quarantine_reason`
- `deploy.delete` 保持现有名称和主要语义，但先写 `stopped` Intent。

### 9.2 Engine 工具

- 新增 `engine.ensure`，缺省只输出计划，`apply=true` 才修改状态。
- 新增 `engine.rollback`，必须显式确认。
- `engine.info/list` 增量返回来源、检测版本、Catalog 版本、摘要、验证状态、活动状态和可回滚版本。
- 保留 `engine.scan/pull/import/remove`，供底层操作和向后兼容；生命周期编排优先使用 `engine.ensure`。

CLI 只负责参数解析、调用相同 MCP 处理路径和格式化结果。

## 10. 安全与审计

- Intent、错误信息和审计记录不得保存敏感环境变量明文。
- 对 `token`、`api_key`、`secret`、`password`、`credential` 等键统一脱敏。
- `engine.ensure apply=true`、`engine.rollback`、解除隔离和物理删除加入 Agent 确认或阻断规则。
- Agent 不能传入任意下载命令或绕过摘要校验。
- staging 解包继续执行路径穿越和符号链接逃逸检查。
- 每次恢复、隔离、激活、回滚和删除记录操作来源、结果、版本及原因。

## 11. 失败处理

- staging 下载、导入、校验或探测失败：清理 staging，活动版本不变。
- 数据库激活事务失败：旧 active 保持不变，新资产保留为 `verified`，允许重试。
- Runtime 恢复失败：记录失败并按持久化退避执行下一次尝试。
- AIMA 在恢复中退出：重启后根据 `recovery_state`、revision 和实际 Runtime 状态继续，不重复计算已记录尝试。
- `deploy.delete` 的 Runtime 操作失败：Intent 仍保持 `stopped`，返回错误并允许人工重试清理，Reconciler不得重启。
- K3S/Docker 隔离删除失败：保持 `quarantined` 并持续告警，但不发起重新部署。

## 12. 兼容性

- SQLite 使用增量迁移，不删除或重命名现有字段。
- MCP 和 CLI 只增加字段与工具，不移除现有契约。
- 未运行 `aima serve` 时，一次性 CLI 的现有行为保持不变。
- 没有配置 `startup.recovery` 的 Engine Asset 使用系统通用默认值。
- 完全离线时，预装扫描、Intent 恢复、本地导入、激活和回滚可用。
- 新增 Engine 或合作伙伴预装方案仍只需要 YAML 与测试资产。

## 13. 测试策略

### 13.1 单元测试

- 恢复状态机的正常、失败、退避、窗口、稳定清零和隔离路径。
- `deploy.delete` 与 Reconciler 并发时的 revision/CAS 行为。
- 敏感字段脱敏和 Intent 持久化边界。
- Docker restart count 与统一状态映射。
- K3S restart count、CrashLoop 和退出状态映射。
- Native PID退出、HTTP不健康和重新部署。
- 版本比较、兼容关系和摘要严格校验。
- staging 失败不影响活动版本。
- 预装/legacy 删除保护、AIMA管理目录校验和引用检查。
- 活动版本原子切换与回滚。

### 13.2 MCP 与集成测试

- `deploy.apply/list/status/delete` 新字段和状态流转。
- `engine.ensure` dry-run、blocked、apply、复用、安装和升级。
- `engine.rollback` 成功与拒绝路径。
- AIMA 重启后从 SQLite Intent 恢复。
- Patrol 对 quarantined 部署只告警，不重复恢复。

### 13.3 构建与真机验证

- `go test ./...`
- `go vet ./...`
- 交叉编译 `windows/amd64`、`darwin/arm64`、`linux/amd64`、`linux/arm64`
- 真机验证遵循项目的 “ALL COLLECT, THEN ANALYZE” 原则，同一轮先采集全部设备结果，再统一修改。

## 14. 验收标准

1. Native 推理进程异常退出后按 2、10、30 秒退避恢复。
2. 10 分钟窗口内失败 3 次后进入 `quarantined`，不再部署。
3. 连续健康 10 分钟后清零恢复窗口。
4. `deploy.delete` 后不会被后台重新拉起。
5. AIMA 重启后继续未完成恢复，不重复计数。
6. Native 进程存在但 HTTP 健康接口持续失败时能够恢复。
7. Docker/K3S 重启次数映射到统一恢复状态，达到阈值后停止 CrashLoop。
8. 预装资产优先复用，且不能被覆盖、移动或物理删除。
9. 托管版本能够并存、验证、激活和回滚。
10. 摘要或版本不匹配时升级失败，原活动版本继续可用。
11. 离线导入后能够激活和回滚。
12. MCP 与 CLI 使用同一业务路径。
13. 相关测试、vet 和四目标交叉编译通过。
14. 本次变更的 Go diff 不包含合作伙伴或产品专用判断。

## 15. 实施顺序

1. 增加 Intent 与 Engine inventory 的增量数据库迁移。
2. 实现独立可测的恢复状态机和 Intent Store。
3. 将 deploy MCP 路径接入 Intent，并实现 AIMA serve Reconciler。
4. 补齐三种 Runtime 的健康与重启状态映射。
5. 收敛 Patrol/Healer 与 Reconciler 的职责。
6. 实现版本化 Engine 存储、严格校验和 inventory 激活事务。
7. 实现 `engine.ensure`、`engine.rollback` 及 CLI 薄包装。
8. 增加安全门控、兼容测试、交叉编译和真机 UAT。

## 16. 完成定义

只有在恢复与 Engine 生命周期均通过单元测试、MCP集成测试、静态检查和四平台构建，并且没有合作伙伴专用 Go 分支时，本功能才算完成。真机条件不可用时必须明确记录未执行项，不能把本地测试结果描述为完整 UAT。
