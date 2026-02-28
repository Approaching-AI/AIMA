# Stack Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的基础设施栈管理（K3S、HAMi、ZeroClaw 安装）。

## 接口定义

### CLI 命令

| 命令 | 功能 |
|------|------|
| `aima init` | 安装+配置基础设施栈 |

---

## Stack Component

Stack Component 是第 6 种知识资产，描述基础设施依赖：

```yaml
kind: stack_component
metadata:
  name: k3s
  version: "1.31.4+k3s1"
  description: "K3S with AIMA-optimized defaults for edge AI inference"

compatibility:
  aima_min: "0.1.0"

source:
  binary: "k3s"
  airgap: "k3s-airgap-images.tar.zst"  # 离线镜像包文件名
  platforms: [linux/amd64, linux/arm64]
  download:                            # 主制品下载 URL (platform → URL)
    linux/amd64: "https://github.com/k3s-io/k3s/releases/download/v1.31.4%2Bk3s1/k3s"
    linux/arm64: "https://github.com/k3s-io/k3s/releases/download/v1.31.4%2Bk3s1/k3s-arm64"
  airgap_download:                      # 离线镜像包下载 URL (Optional)
    linux/amd64: "https://github.com/k3s-io/k3s/releases/download/v1.31.4%2Bk3s1/k3s-airgap-images-amd64.tar.zst"
    linux/arm64: "https://github.com/k3s-io/k3s/releases/download/v1.31.4%2Bk3s1/k3s-airgap-images-arm64.tar.zst"
  airgap_mirror:                       # GFW 备用 URL
    linux/amd64: "https://ghfast.top/https://github.com/k3s-io/..."
    linux/arm64: "https://ghfast.top/https://github.com/k3s-io/..."

install:
  method: binary
  # AIMA 特有的低需求配置
  args:
    - flag: "--disable=traefik"
      rationale: "边缘设备不需要 Ingress Controller"
      source: "k3s-docs"
      verified: true
    - flag: "--disable=servicelb"
      rationale: "不需要 LoadBalancer"
      source: "k3s-docs"
      verified: true
    - flag: "--disable=metrics-server"
      rationale: "AIMA 通过 nvidia-smi 直接采集 GPU 指标"
      source: "hypothesis"
      verified: false
    - flag: "--kubelet-arg=max-pods=20"
      rationale: "边缘单节点场景不需要默认 110 pods 上限"
      source: "community"
      verified: false
  env:
    INSTALL_K3S_SKIP_DOWNLOAD: "true"   # 使用本地预置包

verify:
  command: "k3s kubectl get nodes"
  ready_condition: "Ready"
  timeout_s: 60

# 不同硬件画像的配置变体
profiles:
  nvidia-gb10-arm64:
    extra_args:
      - flag: "--kubelet-arg=kube-reserved=cpu=500m,memory=512Mi"
        rationale: "为系统保留资源，避免挤占推理 VRAM"
        verified: false
  nvidia-rtx4090-x86:
    extra_args:
      - flag: "--kubelet-arg=kube-reserved=cpu=1000m,memory=1Gi"
        rationale: "x86 服务器有更多 CPU/RAM 余量"
        verified: false

# 冷启动阶段待实验的问题
open_questions:
  - question: "GB10 unified memory 下 kubelet reserved 设多少合适？"
    hypothesis: "cpu=500m,memory=512Mi"
    test_method: "部署后观察 kubectl top node 和推理 VRAM 余量"
  - question: "关闭 metrics-server 是否影响 HAMi device plugin 上报？"
    hypothesis: "不影响，HAMi 用独立的 gRPC 注册"
    test_method: "关闭后部署多模型分区，观察 GPU 分配"
```

### 配置来源和验证状态

每个配置值都有来源 (`source`) 和验证状态 (`verified`)：
- `source: "k3s-docs"` - 官方文档推荐
- `source: "community"` - 社区实践
- `source: "hypothesis"` - 假设待验证
- `verified: true` - 已验证
- `verified: false` - 待验证

Agent 可以自动处理 `open_questions`，在真机上实验并将结果写回 Knowledge Note。

---

## aima init 工作流

```
aima init
  │
  ├── 1. 读 catalog/stack/*.yaml (知道要装什么、什么版本、什么配置)
  ├── 2. hardware.detect (检测当前硬件，选择对应 profile)
  ├── 3. PreCheck: 快速失败检查
  │      └── Linux 上 daemon 组件 (K3S) 需 root 权限 → 提前报错
  ├── 4. Preflight: 计算缺失文件列表
  │      ├── 主制品: binary / chart → 必须下载
  │      └── Airgap 镜像包: .tar / .tar.zst → Optional (失败不中断)
  ├── 5. DownloadItems: 并行下载所有缺失文件
  │      ├── 主 URL 失败 → 自动切换 mirror URL
  │      └── Optional 项失败 → 仅 WARN，不 abort
  ├── 6. 按 priority 排序后逐项安装:
  │      ├── writeRegistries → 写容器镜像 mirror 配置
  │      ├── prepareAirgapImages → 导入离线镜像包
  │      ├── checkComponent → 已就绪则跳过
  │      ├── installBinary / installHelm → 安装
  │      └── verify → 验证就绪条件
  └── 7. 输出就绪状态
```

---

## 离线安装包

### 目录结构

```
dist/                          # ~/.aima/dist/{os}-{arch}/
  linux-amd64/
    k3s                        # K3S 二进制 (~70MB)
    k3s-airgap-images.tar.zst  # K3S 系统镜像 (~134MB)
    hami-2.4.1.tgz             # HAMi Helm chart
    hami-airgap-images.tar      # HAMi 容器镜像 (~398MB)
    zeroclaw                   # ZeroClaw 二进制 (~8.8MB)
  linux-arm64/
    ...                        # arm64 版本 (HAMi ~237MB)
```

### 制品来源

- K3S 二进制 + airgap tar: K3S 官方 GitHub release
- HAMi chart: HAMi 官方 GitHub release
- HAMi airgap tar: AIMA GitHub release (v0.1.0-images)

---

## 冷启动知识获取策略

Stack Component 中的先验知识经历三个阶段：

```
阶段 1: 人工研究 (不可跳过)
  读文档 + 社区最佳实践 → 写初始 YAML → 标记 verified: false
  列出 open_questions → 等待真机验证

阶段 2: 真机验证 (Agent 辅助)
  aima init 在真机上运行 → 解决 open_questions
  Agent 记录结果为 Knowledge Note → 更新 verified: true

阶段 3: 社区飞轮
  不同硬件的用户贡献验证结果 → profiles 越来越丰富
  同型硬件自动复用已验证配置
```

---

## 导出 API

### WriteRegistries

`stack.WriteRegistries(registries map[string]any) error` — 将容器镜像 mirror 配置写入
`/etc/rancher/k3s/registries.yaml`。K3S containerd 自动 hot-reload，无需重启服务。

需要 root 权限（`/etc/rancher/k3s/` 目录属 root 所有）。`aima init` 以 root 运行时调用。

---

## 相关文件

- `internal/stack/installer.go` - 通用 stack installer + `WriteRegistries` 导出函数
- `catalog/stack/` - Stack Component YAML

---

*最后更新：2026-02-28*
