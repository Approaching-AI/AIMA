# Runtime Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的 Multi-Runtime 抽象，支持 K3S 和 Native 两种运行时。

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

## 两种 Runtime

| Runtime | 适用场景 | 部署方式 | GPU 切分 | 平台 |
|---------|---------|---------|---------|------|
| **K3S** | Linux + K3S 已安装 | Pod YAML + kubectl apply | HAMi 细粒度切分 | Linux |
| **Native** | 跨平台 fallback | 直接 exec 引擎二进制 | 不支持（单进程独占） | 全平台 |

### 选择逻辑

```go
func selectRuntime() (Runtime, runtimeType) {
    if runtime.GOOS == "linux" && k3sAvailable() {
        return &K3SRuntime{}, "k3s"
    }
    return &NativeRuntime{}, "native"
}
```

- Linux + K3S 可用 → K3S Runtime（完整体验：GPU 切分、多模型并行、声明式健康检查）
- 否则 → Native Runtime（跨平台 fallback：单模型、进程管理）

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

## Native Runtime

### 进程管理

Native Runtime 在非 K3S 环境下提供基础进程管理：

```
Deploy → exec 引擎二进制 → 健康检查轮询 → 预热 → Ready
  │
  ├── start: 启动进程并记录 PID
  ├── stop: 发送 SIGTERM，等待优雅退出，超时则 SIGKILL
  ├── logs: 追踪进程 stdout/stderr
  └── status: 检查进程是否存在 + HTTP 健康检查
```

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
Native      → 单模型 + 直接 exec + 极简进程管理（start/stop/logs）
```

---

## 相关文件

- `internal/runtime/runtime.go` - Runtime 接口定义
- `internal/runtime/k3s.go` - K3S Runtime 实现
- `internal/runtime/native.go` - Native Runtime 实现
- `internal/engine/binary.go` - BinaryManager (Native 二进制管理)

---

*最后更新：2026-02-28*
