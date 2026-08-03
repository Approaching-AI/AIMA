# Runtime Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的 Multi-Runtime 抽象，支持 K3S、Docker 和 Native 三种运行时。

## 接口定义

### Runtime 抽象 (internal/runtime/runtime.go)

```go
type Runtime interface {
    // Deploy 启动推理服务
    Deploy(ctx context.Context, req DeployRequest) (DeploymentStatus, error)

    // Status 查询部署状态
    Status(ctx context.Context, id string) (DeploymentStatus, error)

    // Delete 删除部署
    Delete(ctx context.Context, id string) error

    // Logs 获取容器/进程日志
    Logs(ctx context.Context, id string, opts LogOptions) (<-chan LogEntry, error)
}

type DeployRequest struct {
    ID              string
    ModelPath       string
    EngineImage     string
    Command         []string
    Args            map[string]string
    GPUMemoryMB     int
    GPUCoresPercent int
    CPUCores        int
    MemoryMB        int
    Port            int
    HealthCheck     HealthCheckConfig
    Warmup          *WarmupConfig
    Env             map[string]string          // 引擎+硬件合并后的环境变量
    Container       *knowledge.ContainerAccess // 厂商特定容器访问（设备、卷、安全上下文）
    GPUResourceName string                     // K8s GPU 资源名（如 "nvidia.com/gpu", "amd.com/gpu"）
    CPUArch         string                     // CPU 架构（如 "x86_64", "arm64"）
}
```

---

## 三种 Runtime

| Runtime | 适用场景 | 部署方式 | GPU 切分 | 平台 |
|---------|---------|---------|---------|------|
| **K3S** | Linux + K3S 集群 | Pod YAML + kubectl apply | HAMi 细粒度切分 | Linux |
| **Docker** | Linux + Docker（无 K3S） | docker run -d | 不支持（`--gpus all` 全量） | Linux |
| **Native** | 跨平台 fallback | 直接 exec 引擎二进制 | 不支持（单进程独占） | 全平台 |

### 选择逻辑

AIMA 在启动时构建所有可用 runtime，然后按两个层次选择：

**默认 Runtime**（全局 fallback）:

```
selectDefaultRuntime: K3S (if available) → Docker (if available) → Native
```

**Per-Deployment Runtime**（基于 Engine YAML 的 `RuntimeRecommendation`）:

```go
pickRuntimeForDeployment(recommendation, k3sRt, dockerRt, nativeRt, defaultRt, hasPartition):
    "native"    → nativeRt
    "docker"    → dockerRt > nativeRt
    "k3s"       → k3sRt (required, error if unavailable)
    "container" → k3sRt > dockerRt (partition needs k3s)
    "auto" / "" → defaultRt
```

| Engine says | Partition | Selected |
|-------------|-----------|----------|
| native | — | Native |
| container | no | Docker (lighter) or K3S |
| container | yes (non-zero GPU memory/cores) | K3S (HAMi required) |
| auto | — | Best available |

**注意**: "有分区" 判定为 `GPUMemoryMiB > 0 || GPUCoresPercent > 0`。全零分区（使用全量设备）不要求 K3S，可由 Docker Runtime 处理。

如果 GPU 分区需要 K3S 但不可用 → 报错: "Run `aima init --k3s` to install"

### Docker CDI GPU 支持

Docker Runtime 部署 NVIDIA GPU 容器时优先使用 CDI (Container Device Interface)：

- 若 `/etc/cdi/nvidia.yaml` 存在 → `--device nvidia.com/gpu=all` (CDI, Docker 25+ 原生支持)
- 否则 → `--gpus all` (传统 nvidia-container-toolkit 方式)

---

## K3S Runtime

### Pod YAML 生成

AIMA 不编写容器生命周期管理代码。引擎部署 = 知识库生成 Pod YAML + kubectl apply。

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: vllm-glm4-flash
  labels:
    aima.dev/engine: vllm
    aima.dev/model: glm-4.7-flash
    aima.dev/slot: primary
  annotations:
    nvidia.com/gpumem: "8192"          # HAMi: 显存配额 (MB)
    nvidia.com/gpucores: "50"          # HAMi: 算力配额 (%)
spec:
  containers:
  - name: inference
    image: vllm/vllm-openai:latest
    command: ["vllm", "serve", "--model", "/models"]
    ports:
    - containerPort: 8000
    resources:
      limits:
        nvidia.com/gpu: "1"            # GPU 资源名从 Hardware Profile 读取
        cpu: "4"
        memory: "16384Mi"
    livenessProbe:
      httpGet:
        path: /health
        port: 8000
      initialDelaySeconds: 30
      periodSeconds: 10
    readinessProbe:
      httpGet:
        path: /health
        port: 8000
      initialDelaySeconds: 10
      periodSeconds: 5
    volumeMounts:
    - name: model-data
      mountPath: /models
      readOnly: true
  volumes:
  - name: model-data
    hostPath:
      path: /mnt/data/models
      type: DirectoryOrCreate
```

**厂商无关 Pod 生成**: Pod 模板中的 GPU 资源声明、环境变量、设备挂载、安全上下文
全部从 Hardware Profile YAML 的 `container` 和 `gpu.resource_name` 字段读取。
Go 代码不包含任何厂商特定逻辑（无 NVIDIA/AMD/Intel 分支）。

### 健康检查、重启、资源限制

| 能力 | 实现方式 |
|------|---------|
| 健康检查 | 原生 livenessProbe / readinessProbe |
| 重启策略 | restartPolicy + 指数退避 (原生) |
| 资源限制 | Pod resources.limits (声明式) |
| GPU 切分 | HAMi: 显存 MB + 算力 % 细粒度 |
| 多容器编排 | Pod / Deployment 声明式 |
| 状态查询 | kubectl get pods (标准 K8s API) |

---

## Docker Runtime

### 容器管理

Docker Runtime 在有 Docker 但无 K3S 的 Linux 机器上提供容器化部署：

```
Deploy → docker run -d → 端口存活检查 → Ready
  │
  ├── deploy: docker run -d --name <name> --restart unless-stopped
  ├── delete: docker rm -f <name>
  ├── logs:   docker logs --tail N <name>
  └── status: docker inspect <name> → phase/ready/address
```

### 与 K3S Runtime 的区别

| 能力 | K3S | Docker |
|------|-----|--------|
| GPU 切分 | HAMi 细粒度（显存 MB + 算力 %） | 不支持（`--gpus all` 全量） |
| 健康检查 | K8s 原生 liveness/readinessProbe | 端口存活检查 (TCP dial) |
| 重启策略 | K8s restartPolicy | `--restart unless-stopped` |
| 多模型并行 | 支持（资源隔离） | 支持（端口隔离，无显存切分） |
| 部署方式 | Pod YAML + kubectl apply | docker run CLI |
| 状态查询 | kubectl get pod | docker inspect / docker ps |
| 启动进度 | Pod conditions + 日志模式匹配 | 日志模式匹配 |

### YAML 驱动

Docker Runtime 与 K3S 共享 `DeployRequest` 中的所有 YAML 驱动字段：

- **Container.Env**: 环境变量（NVIDIA_VISIBLE_DEVICES → `--gpus all`; HSA_OVERRIDE_GFX_VERSION → `--env`）
- **Container.Devices**: 设备挂载（`/dev/kfd` → `--device`; `/dev/davinci*` → `--device`）
- **Container.Volumes**: 卷挂载（→ `--volume`）
- **Container.Security**: 安全上下文（privileged → `--privileged`; groups → `--group-add`）
- **Container.DockerRuntime**: 自定义 Docker Runtime（→ `--runtime`，如 Ascend 的 `--runtime ascend`）
- **Container.NetworkMode**: 网络模式（`"host"` → `--network host`，跳过 `--publish`）
- **Container.ShmSize**: 共享内存（→ `--shm-size`，如 `"500g"`）
- **Container.Init**: Init 进程（→ `--init`）
- **Command**: 启动命令（`{{.ModelPath}}` → `/models`）
- **Config**: 引擎配置参数（自动转为 CLI flags，如 `tp: 8` → `--tp 8`; `disable_radix_cache: true` → `--disable-radix-cache`）
- **InitCommands**: 初始化命令链（→ `bash -c "init && exec main"`，使用 bash 以支持 bash 语法）

Go 代码无厂商分支：GPU 访问方式完全由 Hardware Profile YAML 决定。

---

## Native Runtime

### 进程管理

Native Runtime 在非 K3S 环境下提供基础进程管理：

```
Deploy → exec 引擎二进制 → 健康检查轮询 → 预热 → Ready
  │
  ├── start: 启动进程并记录 PID（持锁预留名称，防 TOCTOU 竞争）
  ├── stop: PID 身份验证 → SIGTERM，等待优雅退出，超时则 SIGKILL
  ├── logs: 追踪进程 stdout/stderr（大文件从尾部 seek 读取，避免 OOM）
  └── status: 检查进程是否存在 + HTTP 健康检查
```

**PID 安全**: Delete 操作通过 `processMatchesMeta()` 验证 PID 身份，防止 OS PID 复用导致误杀无关进程。
Linux 上读 `/proc/PID/cmdline` 比对二进制名；非 Linux 降级为端口存活检查；无法验证时跳过 kill。

**Deploy 并发安全**: Deploy 在持锁状态下完成存在性检查 + 名称预留（`processes[name] = nil`），
释放锁后才执行耗时的进程启动。启动失败时清理预留。

### 预热 (Warmup)

NativeRuntime 在 health check 通过后自动执行预热：

```go
func (r *NativeRuntime) warmup(ctx context.Context, req DeployRequest) error {
    if req.Warmup == nil || !req.Warmup.Enabled {
        return nil
    }

    client := http.Client{Timeout: req.Warmup.TimeoutS * time.Second}
    body := map[string]any{
        "messages": []map[string]string{
            {"role": "user", "content": req.Warmup.Prompt},
        },
        "max_tokens": req.Warmup.MaxTokens,
    }
    // ... POST /v1/chat/completions
    return nil
}
```

预热使用 dummy prompt 触发一次完整推理路径，将 CUDA kernel 编译和模型权重加载提前完成。

---

## 渐进降级

```
K3S + HAMi  → 多模型并行 + GPU 细粒度切分 + 声明式生命周期
     ↓ K3S 不可用
Docker      → 容器隔离 + 自动重启 + 日志模式匹配（无 GPU 切分）
     ↓ Docker 不可用
Native      → 单模型 + 直接 exec + 极简进程管理（start/stop/logs）
```

### 跨 Runtime Fallback

Deploy 操作（delete/status/list/logs）在主 Runtime 找不到部署时，自动搜索其他可用 Runtime：

```
deploy.delete / deploy.status / deploy.logs:
  主 Runtime (按名查找) → Native → Docker → 按 model label 跨所有 Runtime 搜索

deploy.list:
  主 Runtime（失败不中断）+ Native + Docker → 合并所有部署列表
```

这确保了引擎在不同 Runtime 上部署时，所有 MCP 工具都能正确操作。

### 输出契约

- `deploy.list` 返回轻量 overview，用于 UI、Agent、proxy 同步和人工巡检。
  顶层字段以 `name` / `model` / `engine` / `slot` / `phase` / `status` / `ready` / `address` / `runtime` 为主，
  以及 `startup_phase` / `startup_progress` / `startup_message` / `message` / `error_lines` 等摘要字段。
  还包含 proxy/模型路由需要的 `served_model` / `parameter_count` / `context_window_tokens`。
  `deploy.list` 不承诺返回 `config` 或原始 `labels`。
- `deploy.status` 返回单个部署的完整详细状态。
  除 overview 字段外，还保留 `config`、`labels`、重启/退出信息等 detail 字段，供诊断和自动化恢复使用。

常用字段可以理解为：

- `deploy.list`
  `name`, `model`, `engine`, `slot`, `phase`, `status`, `ready`, `address`, `runtime`
  `startup_phase`, `startup_progress`, `startup_message`, `message`, `error_lines`
  `served_model`, `parameter_count`, `context_window_tokens`
- `deploy.status`
  上述 overview 字段全部可见
  以及 `config`, `labels`, `restarts`, `exit_code`, `estimated_total_s`, `start_time`, `started_at_unix`

---

## 部署自动恢复

部署自动恢复是 `aima serve` 的后台能力。只有 `aima serve` 进程存活时，恢复 Controller 才会检查持久化部署意图；单独运行 `aima mcp` 或一次性 CLI 命令不会启动该 Controller。

### 默认策略

未被 Catalog Engine Asset 或 `deploy.apply.recovery_policy` 覆盖时，每个部署使用以下完整默认值：

| 字段 | 默认值 | 含义 |
|------|--------|------|
| `enabled` | `true` | 启用恢复观察 |
| `check_interval_s` | `5` | 每个部署的独立检查周期 |
| `consecutive_failures` | `3` | Native 连续失败达到该值后尝试恢复 |
| `max_attempts` | `3` | 600 秒窗口内最多提交的恢复尝试数 |
| `window_s` | `600` | 尝试计数窗口 |
| `backoff_s` | `[2, 10, 30]` | 各次恢复失败后的退避序列，超出长度后重复最后一项 |
| `stable_reset_s` | `600` | 持续健康达到该时间后清空恢复计数 |

策略优先级为：内置默认值 → 已选 Engine Asset 的 `startup.recovery` → 本次 `deploy.apply` 的字段级覆盖。策略仍然是通用 Catalog 能力，不绑定合作伙伴、设备型号、模型、Engine 类型或 GPU 厂商。

### 操作语义

- 外部结束 Native 推理进程不改变 AIMA 中的 `desired_state=running`。`aima serve` 观察到连续失败后，会按上述预算执行 Native `Delete -> Deploy` 恢复。
- 显式执行 `deploy.delete` 或 `aima undeploy` 会先持久化 `desired_state=stopped`，再删除 Runtime 对象。Controller 不会重新创建显式删除的部署；即使 Runtime 对象原本已不存在，停止意图也会生效。
- `recovery_state=quarantined` 表示恢复预算已经耗尽，AIMA 已停止该部署的自动恢复。隔离状态仍保留 `desired_state=running` 及最后的脱敏原因，但不会继续循环重试。
- 解除隔离必须由操作员重新执行显式 `deploy.apply`（CLI 对应重新执行 `aima deploy <model>`）。显式 apply 会重新解析配置并重置恢复计数；不要通过直接启动外部进程绕过意图状态。

### Runtime 责任边界

| Runtime | AIMA 恢复行为 |
|---------|---------------|
| Native | AIMA 观察进程与健康探针；正常 `starting` 且未 stalled 时保持不动。失败并达到阈值后，在提交恢复状态且锁内复查后执行删除和重新部署。 |
| Docker | 容器自身 restart policy 和健康探针负责重启。AIMA 只观察 restart count；达到预算后先持久化隔离，再删除容器，不另建第二套重启循环。 |
| K3S | Kubernetes 控制器和探针负责 Pod 重启。AIMA 只观察 restart count；达到预算后先持久化隔离，再删除工作负载，不替代 Kubernetes reconciliation。 |

Runtime 观察固定使用部署意图中记录的 Runtime。状态查询基础设施失败不会被当作“部署缺失”，也不会静默切换到另一个 Runtime 触发恢复。

### 并发边界

显式 apply/delete 与后台恢复共享进程内的部署操作锁，并在 Runtime 副作用前校验持久化 revision，因此同一 `aima serve` 进程内的显式操作优先于旧恢复任务。该锁不是跨进程分布式锁：多个独立 AIMA 进程共享并写入同一 SQLite 数据库时，Runtime 副作用不受这一保证保护。部署场景应保持单写进程，跨进程协调不在当前支持范围内。

### 恢复审计

Recovery Controller 将恢复生命周期事件写入现有 SQLite `audit_log`，不新增合作伙伴或 Runtime 专用表。事件使用 `agent_type=reconciler`，工具名形如 `deployment.recovery.recovery_attempt`，记录部署名、Runtime、固定 Engine Asset/版本、revision、尝试次数、结果、下一次退避时间和脱敏原因。当前事件包括恢复尝试、恢复已提交、恢复失败、退避重试、平台重启观察、恢复健康、隔离和隔离执行结果。

审计载荷不包含展开后的部署配置、Token、API Key 或其他凭据。审计写入失败只产生告警，不阻断已经提交的恢复状态或 Runtime 副作用；因此 `deployment_intents` 仍是恢复控制的权威状态，`audit_log` 用于追溯操作来源和结果。

---

## 相关文件

- `internal/runtime/runtime.go` - Runtime 接口定义
- `internal/runtime/k3s.go` - K3S Runtime 实现
- `internal/runtime/docker.go` - Docker Runtime 实现
- `internal/runtime/native.go` - Native Runtime 实现
- `internal/runtime/progress.go` - 启动进度检测（K3S/Docker/Native 共用）
- `internal/engine/binary.go` - BinaryManager (Native 二进制管理)

---

*最后更新：2026-08-02（部署自动恢复策略、Runtime 责任和进程内并发边界）*
