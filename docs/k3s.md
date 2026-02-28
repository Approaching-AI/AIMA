# K3S Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的 K3S 集成和 Pod 管理。

## 接口定义

### K3S Client (internal/k3s/client.go)

```go
type Client interface {
    Apply(ctx context.Context, yaml []byte) error
    Get(ctx context.Context, namespace, name string) (*Pod, error)
    List(ctx context.Context, namespace string, labels map[string]string) ([]*Pod, error)
    Delete(ctx context.Context, namespace, name string) error
    Logs(ctx context.Context, namespace, name, container string, opts LogOptions) (<-chan LogEntry, error)
}
```

---

## 为什么选择 K3S

K3S 是 Rancher 维护的轻量级 Kubernetes 发行版，单一二进制 (<70MB)。

| 特性 | 实际数据 |
|------|---------|
| 服务端最低要求 | 2 核 CPU, 2 GB RAM |
| Agent 节点最低 | 1 核 CPU, 512 MB RAM |
| 支持架构 | x86_64, arm64/aarch64, armhf |
| 默认数据库 | 嵌入式 SQLite (单节点); 嵌入 etcd (HA 多节点) |
| 容器运行时 | 内置 containerd |
| 单节点实测 | ~6% CPU + ~1.6 GB RAM |

### 可禁用组件

```bash
k3s server \
  --disable=traefik \          # 不需要 Ingress Controller
  --disable=metrics-server \   # Agent 用 nvidia-smi 直接采集
  --disable=coredns \          # 单节点不需要集群 DNS
  --disable=servicelb \        # 不需要 LoadBalancer
  --disable=local-storage      # 用 hostPath 直接挂载模型目录
```

### 与直接 Docker 的对比

| 能力 | Docker 直接管理 | K3S |
|------|----------------|-----|
| 健康检查 | 需自己写轮询代码 | 原生 livenessProbe / readinessProbe |
| 重启策略 | 需自己写退避逻辑 | restartPolicy + 指数退避 |
| 资源限制 | docker --cpus, --memory | Pod resources.limits (声明式) |
| GPU 切分 | docker --gpus (全卡或 N 卡) | HAMi: 显存 MB + 算力 % 细粒度 |
| 多容器编排 | 自己管理依赖和顺序 | Pod / Deployment 声明式 |
| 状态查询 | docker inspect (自定义解析) | kubectl get pods (标准) |
| 扩展到多节点 | 需额外方案 | K3S agent 加入即可 |

---

## Pod 管理

### Pod YAML 生成

Pod YAML 由 knowledge.generate_pod 从知识资产模板生成：

```go
func GeneratePod(engine Asset, model Asset, slot Slot) ([]byte, error) {
    // 渲染模板，注入动态值
    // - GPU 资源名 (从 Hardware Profile 读取)
    // - HAMi annotations (gpumem, gpucores)
    // - 模型路径
    // - 引擎参数
    // ...
}
```

### 健康检查

```yaml
livenessProbe:
  httpGet:
    path: /health
    port: 8000
  initialDelaySeconds: 30
  periodSeconds: 10
  failureThreshold: 3

readinessProbe:
  httpGet:
    path: /health
    port: 8000
  initialDelaySeconds: 10
  periodSeconds: 5
  failureThreshold: 3
```

### 状态查询

```go
func (c *K3SClient) Get(ctx context.Context, namespace, name string) (*Pod, error) {
    out, err := c.exec(ctx, "kubectl", "get", "pod", name,
        "-n", namespace, "-o", "json")
    // 解析 JSON → Pod
}
```

---

## HAMi 集成

### 什么是 HAMi

HAMi (Heterogeneous AI Computing Virtualization Middleware) 是 CNCF Sandbox 项目，
用于在 Kubernetes 中实现异构 AI 加速器的细粒度切分和隔离。

### 核心组件

| 组件 | 职责 |
|------|------|
| **hami-device-plugin** (DaemonSet) | 发现 GPU 设备并注册为 K8s 扩展资源 |
| **hami-scheduler** (Deployment) | Scheduler Extender，让原生调度器理解 vGPU 资源 |
| **MutatingWebhook** | 自动注入 libvgpu.so 到容器 |
| **libvgpu.so** (容器内) | 通过 LD_PRELOAD 拦截 CUDA API |

### GPU 虚拟化原理

libvgpu.so 通过 `LD_PRELOAD` 机制在容器启动时注入。它拦截关键 CUDA API 调用:

- **显存管理**: `cuMemAlloc`, `cudaMalloc` — 每次分配前检查是否超出配额
- **设备查询**: `cuDeviceTotalMem` — 返回配额值而非物理总量
- **内核执行**: `cuLaunchKernel` — 当启用算力限制时修改内核参数约束 SM 使用率

显存隔离是**硬限制**: 超出配额时 `cuMemAlloc` 返回 `cudaErrorMemoryAllocation`。

### 支持的加速器

| 厂商 | 设备 |
|------|------|
| NVIDIA | 全系列 GPU (含 A100/H100 MIG) |
| 华为 | 昇腾 910B, 910B3, 310P NPU |
| 寒武纪 | MLU 370, MLU 590 |
| 海光 | DCU Z100, Z100L |
| 天数智芯 | CoreX GPU |
| 摩尔线程 | MTT S4000 GPU |
| MetaX | MXC500 GPU |

### Pod 中声明 GPU 资源

```yaml
resources:
  limits:
    nvidia.com/gpu: 1
annotations:
  nvidia.com/gpumem: "8192"        # 显存配额 (MB)
  nvidia.com/gpucores: "50"        # 算力配额 (%)
```

### GPU 资源名动态化

GPU 资源名不再硬编码 `nvidia.com/gpu`，而是从 Hardware Profile 的 `gpu.resource_name` 字段读取：
- NVIDIA: `nvidia.com/gpu`
- AMD: `amd.com/gpu`
- Intel: `gpu.intel.com/i915`

HAMi annotation 的 vendor 前缀通过 `GPUVendorDomain()` 从资源名自动推导。
当 `resource_name` 为空时，Pod 不生成 GPU 资源请求（无 `resources.limits` 中的 GPU 项）。

### 厂商无关容器访问

Pod 模板不包含任何厂商特定逻辑。所有容器访问配置从 Hardware Profile YAML 的 `container` 字段读取：

```
Hardware Profile YAML                    Pod YAML 渲染
┌─────────────────────┐                 ┌──────────────────────────┐
│ container:          │                 │ env:                     │
│   devices:          │ ──→ volumes +   │   - name: LD_PRELOAD     │
│     - /dev/kfd      │     volumeMounts│     value: /opt/rocm/... │
│     - /dev/dri      │                 │ volumeMounts:            │
│   env:              │ ──→ env:        │   - name: dev-kfd        │
│     LD_PRELOAD: ... │                 │     mountPath: /dev/kfd  │
│   security:         │ ──→ security    │ securityContext:         │
│     supplemental_   │     Context     │   supplementalGroups:    │
│     groups: [44,110]│                 │     - 44                 │
└─────────────────────┘                 │     - 110                │
                                        └──────────────────────────┘
```

**Env 合并规则**：Hardware Profile 的 `container.env`（基础层）与 Engine YAML 的 `startup.env`（覆盖层）合并。
当同名 key 冲突时，**引擎 env 优先**（引擎比硬件更了解运行时需求）。

**NVIDIA 示例** (`nvidia-rtx4060-x86.yaml`):
```yaml
container:
  env:
    NVIDIA_VISIBLE_DEVICES: "all"
    NVIDIA_DRIVER_CAPABILITIES: "all"
    LD_LIBRARY_PATH: "/lib/x86_64-linux-gnu:/usr/local/nvidia/lib:/usr/local/nvidia/lib64"
```

**AMD 示例** (`amd-radeon-x86.yaml`):
```yaml
container:
  devices: ["/dev/kfd", "/dev/dri"]
  env:
    LD_PRELOAD: "/opt/rocm/lib/librocm_smi64.so"
  security:
    supplemental_groups: [44, 110]
```

### 最小部署

对于单节点场景：
- 只启用 hami-daemon (device-plugin)，禁用 scheduler 和 WebUI
- 资源预算: ~150m CPU + ~228Mi RAM (每 GPU 节点)

---

## 使用示例

### 应用 Pod

```bash
kubectl apply -f <(aima knowledge generate_pod --engine vllm --model glm-4.7-flash)
```

### 查询状态

```bash
kubectl get pods -l aima.dev/model=glm-4.7-flash
kubectl logs -f <pod-name>
```

### 删除 Pod

```bash
kubectl delete pod <pod-name>
```

---

## 相关文件

- `internal/k3s/client.go` - K3S 客户端封装
- `internal/knowledge/podgen.go` - Pod YAML 生成（厂商无关模板）
- `catalog/hardware/*.yaml` - Hardware Profile（含 `container` 厂商特定配置）

---

*最后更新：2026-02-28*
