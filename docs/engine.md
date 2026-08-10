# Engine Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的统一引擎管理功能（容器镜像 + Native 二进制）。

## 设计理念：异构引擎统一管理

AIMA 支持两种引擎运行时，提供统一的用户界面：

| 运行时 | 适用场景 | 引擎类型 |
|--------|---------|----------|
| **Container Runtime** (K3S/Docker) | Linux 服务器，GPU 集群 | vLLM, SGLang, SGLang-Ascend, Ollama, llama.cpp |
| **Native Runtime** (进程) | Windows/macOS, 边缘设备, 无容器环境 | llama.cpp, 其他 Native 引擎 |

**`aima engine scan` 自动检测可用运行时并扫描对应引擎：**
- 有 K3S/Docker → 扫描容器镜像
- 无容器运行时 → 扫描 Native 二进制 (distDir + PATH)

---

## 接口定义

### CLI 命令

| 命令 | 功能 |
|------|------|
| `aima engine scan` | 扫描本地引擎（容器镜像或 Native 二进制，自动检测） |
| `aima engine info <name>` | 查看引擎详情（目录知识 + 本地可用性） |
| `aima engine list` | 列出所有已注册引擎 |
| `aima engine ensure <name> [--version <version>] [--apply]` | 规划或应用版本复用、安装和激活；默认只输出计划 |
| `aima engine rollback <name> --runtime <container-or-native> --confirm` | 激活指定运行时组中 verified、available 的前一版本 |
| `aima engine pull [name]` | 拉取引擎镜像（容器运行时） |
| `aima engine import <path>` | 从本地 OCI 包、Native 二进制、目录或压缩包离线导入 |
| `aima engine remove <id> [--delete-files]` | 删除无引用库存；物理删除受所有权和路径保护 |

### MCP 工具

| 工具 | JSON-RPC 方法 | 功能 |
|------|---------------|------|
| `engine.scan` | `engine.scan` | 扫描本地引擎（统一） |
| `engine.info` | `engine.info` | 查询引擎详情（目录知识 + 本地状态） |
| `engine.list` | `engine.list` | 列出所有引擎 |
| `engine.ensure` | `engine.ensure` | 默认 plan-only；`apply=true` 才改变库存或激活版本 |
| `engine.rollback` | `engine.rollback` | 指定 `runtime_type` 且 `confirm=true` 后回滚到该运行时组的 verified、available 前一版本 |
| `engine.pull` | `engine.pull` | 拉取引擎镜像 |
| `engine.import` | `engine.import` | 离线导入容器或 Native 引擎 |
| `engine.remove` | `engine.remove` | 删除无引用库存，可选受保护物理删除 |

`aima engine plan` 已并入 `knowledge.resolve` + `deploy.dry_run(output=pod_yaml)`，不再是独立 CLI/MCP 工具。

---

## Engine 生命周期合同

### 库存与来源

SQLite v21 为每个 Engine 版本记录以下生命周期证据：

| 字段 | 含义 |
|------|------|
| `asset_name` | Catalog Engine Asset 的稳定名称 |
| `version` | 扫描、包布局或严格摘要证据得到的实际版本 |
| `catalog_version` | 当前 Catalog 声明版本，不替代实际版本 |
| `origin` | `managed`、`imported`、`preinstalled` 或 `legacy` |
| `content_digest` | Native 文件 SHA256 或可取得的 OCI digest |
| `location` | Native 绝对路径或容器镜像引用 |
| `active` | 是否为该 asset/platform/runtime 当前默认版本 |
| `lifecycle_status` | `discovered`、`staged`、`verified` 或 `active` |
| `verification_status` | `unverified` 或 `verified` |
| `previous_engine_id` | 激活事务记录的上一版本，用于显式回滚 |

来源语义：

- `managed`：AIMA 通过严格 ensure 流程取得并存放的资产。
- `imported`：操作者显式从本地包导入、由 AIMA 暂存和记录摘要的资产。
- `preinstalled`：在 Catalog probe、外部目录、PATH 或既有容器存储中发现的资产；文件保持原位。
- `legacy`：v21 以前缺少所有权证据的库存，按不可物理删除处理。

普通扫描不会把 `managed` 或 `imported` 降级为 `preinstalled`。

### 版本目录与不可变性

受管 Native 版本固定存放在：

```text
<AIMA_DATA_DIR>/dist/<os>-<arch>/<asset-name>/<version-or-digest>/
```

下载和导入先进入同一数据文件系统下的私有 staging 目录。校验通过后使用原子 rename 提升；如果目标已存在，只在目录类型、权限、链接目标和文件内容完全相同时复用，绝不覆盖不同内容。

### Ensure 与严格校验

`engine.ensure` 默认 `apply=false`，相同输入和库存产生稳定计划。计划会给出 `reuse`、`install`、`upgrade` 或 `activate` 动作、网络需求、校验证据、阻断原因和受影响部署名称。

- 精确版本的本地资产优先，离线可复用时不访问网络。
- 网络 Native 安装必须有 Catalog SHA256；容器安装必须有 Catalog OCI digest。
- SHA256/digest 不匹配或无法取得预期证据时直接失败，旧 active 不变。
- 非 active 的 unverified 版本不能被新激活。已经 active 的旧预装版本可以保持原状。
- Catalog 版本标签不能冒充实际检测版本；兼容只接受 Catalog 明确列出的精确值或尾部 `.x` 规则。

### 离线导入

Native 离线包必须能由 Catalog 的 `source.binary` 唯一识别，并携带实际版本目录。支持的核心布局是：

```text
<asset-name>/<version>/<binary-and-support-files>
```

归档工具去掉共同顶层目录后，`<version>/<binary-and-support-files>` 也可接受，但该二进制在当前平台 Catalog 中必须只解析到一个兼容 Asset。裸单文件若没有版本目录会被拒绝，避免把 Catalog 版本误当成检测版本。

本地导入是显式操作者信任边界：AIMA 始终记录导入后二进制 SHA256；Catalog 若为当前平台声明 `source.sha256`，文件包必须先通过该摘要校验。声明了文件 SHA256 时，目录型输入会被拒绝，因为无法证明它等同于声明的归档。导入只登记为 inactive `imported/verified`，后续仍需显式执行 `engine.ensure --apply` 才激活。

OCI 导入后只把本次新发现的容器镜像标记为 `imported`；只有运行时 digest 与 Catalog OCI digest 一致时才标记 `verified` 并采用 Catalog 版本。

### 激活、回滚与删除

- 激活和回滚只修改 SQLite 版本指针，不调用 deploy 或 Runtime。
- 回滚要求 `confirm=true`，且 `previous_engine_id` 必须存在、available、verified，并匹配 asset/platform/runtime。
- 已运行部署继续使用其持久化 intent 中固定的 Engine Asset/版本。激活或回滚不会重启、替换或重新绑定现有部署；新选择只影响后续显式部署。
- 删除前必须确认该版本不是 active、未被其他版本的回滚链引用、也未被部署 intent 引用。
- `preinstalled` 和 `legacy` 永不物理删除。`managed`/`imported` 只有在 Native 绝对路径经 `Abs`、`EvalSymlinks`、`Rel` 证明严格位于 `AIMA_DATA_DIR` 下时，才允许 `delete_files=true`。
- 容器镜像存储不位于 `AIMA_DATA_DIR`，因此生命周期删除接口不会物理删除镜像层；可删除的只是无引用库存记录。

---

## 数据结构

### Engine (internal/sqlite.go)

数据库表定义，存储已注册的引擎（支持容器和 Native）：

```sql
CREATE TABLE engines (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,               -- vllm | llamacpp | ollama | sglang
    image TEXT NOT NULL,              -- 容器镜像名（容器引擎）或空（Native）
    tag TEXT NOT NULL,               -- 容器镜像 tag（容器引擎）或空（Native）
    size_bytes INTEGER,
    platform TEXT,                    -- linux-amd64 | linux-arm64 | darwin-arm64 | windows-amd64
    runtime_type TEXT DEFAULT 'container', -- "container" or "native"
    binary_path TEXT,                 -- Native 二进制路径（Native 引擎）
    available BOOLEAN DEFAULT TRUE,   -- 引擎是否在本地可用
    asset_name TEXT,
    version TEXT,
    catalog_version TEXT,
    origin TEXT DEFAULT 'legacy',
    content_digest TEXT,
    location TEXT,
    active BOOLEAN DEFAULT FALSE,
    lifecycle_status TEXT DEFAULT 'discovered',
    verification_status TEXT DEFAULT 'unverified',
    previous_engine_id TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

### EngineImage (internal/engine/scanner.go)

扫描返回的引擎表示：

```go
type EngineImage struct {
    ID         string // 引擎唯一标识（容器 SHA256 或 Native 二进制 hash）
    Type       string // 引擎类型：vllm, llamacpp, sglang, ollama
    Image      string // 容器镜像名（容器引擎）或空（Native）
    Tag        string // 容器镜像 tag（容器引擎）或空（Native）
    SizeBytes  int64  // 大小（字节）
    Platform   string // 平台标识
    RuntimeType string // "container" or "native"
    BinaryPath string // Native 二进制完整路径
    Available  bool   // 是否可用
    Origin string     // managed | imported | preinstalled
    CatalogVersion string
    DetectedVersion string
    VersionMatch string
    ContentDigest string
    DockerOnly bool   // true = 镜像仅在 Docker 中，不在 K3S containerd 中
}
```

### Engine Asset YAML (catalog/engines/*.yaml)

```yaml
kind: engine_asset
metadata:
  name: vllm-0.8-blackwell
  type: vllm
  version: "0.8"
image:
  name: vllm/vllm-openai
  tag: "latest"
  size_approx_mb: 8500
  platforms: [linux/amd64, linux/arm64]
  registries:                           # 按优先级排列的镜像源
    - docker.io/vllm/vllm-openai
    - registry.cn-hangzhou.aliyuncs.com/aima/vllm-openai
source:                                 # Native 运行时二进制来源（可选）
  binary: "llama-server"
  platforms: [linux/amd64, linux/arm64, darwin/arm64, windows/amd64]
  download:                              # 按平台的下载 URL
    linux/amd64: "https://github.com/.../llama-server-linux-x64"
    darwin/arm64: "https://github.com/.../llama-server-macos-arm64"
  mirror:                                # 国内镜像（可选）
    linux/amd64: "https://mirror.example.com/.../llama-server-linux-x64"
hardware:
  gpu_arch: Blackwell
  vram_min_mib: 4096
startup:
  command: ["vllm", "serve", "--model", "{{.ModelPath}}"]
  default_args:
    port: 8000
    gpu_memory_utilization: 0.75
    max_model_len: 8192
  health_check:
    path: /health
    timeout: 5m
  warmup:                                # 部署后预热配置（可选）
    enabled: true
    prompt: "Hello"
    max_tokens: 1
    timeout_s: 30
api:
  protocol: openai
  base_path: /v1
```

---

## 核心功能

### 1. 统一引擎扫描 (engine.scan)

`aima engine scan` 自动检测运行时并扫描对应引擎：

```
engine.scan (ScanUnified)
  │
  ├── 1. 容器扫描 (crictl + docker 同时扫描)
  │   ├── crictl images → K3S containerd 镜像列表 (source="containerd")
  │   ├── docker images → Docker 镜像列表 (source="docker")
  │   └── 合并去重：containerd 优先，Docker-only 镜像标记 DockerOnly=true
  │
  ├── 2. 模式匹配 (matchImages)
  │   ├── 按 Engine Asset YAML 的 patterns 匹配引擎类型
  │   ├── 同 type 多个 YAML 的 patterns 合并（非覆盖）
  │   └── DockerOnly 标记传递到 EngineImage
  │
  ├── 3. Docker-only 镜像标记
  │   └── 仅标记 DockerOnly=true（不自动导入，除非 AutoImport=true）
  │
  ├── 4. Native 扫描 (并行)
  │   ├── 扫描 distDir: ~/.aima/dist/{os}-{arch}/
  │   └── 扫描 PATH
  │
  └── 5. 扫描结果注册到 SQLite engines 表
```

**扫描行为：**
1. Container：crictl + docker 同时扫描，containerd 优先去重
2. Docker-only 镜像：标记 `docker_only=true`，**不自动导入**（避免每次 scan 都需要 root）
3. 自动导入仅在以下场景触发：
   - `aima init` 安装 K3S 后自动导入（init 以 root 运行）
   - `aima engine scan --import` 显式请求导入
4. Native：始终扫描 distDir + PATH（不依赖容器运行时）
5. Pattern 合并：同 type 多个 Engine YAML 的 patterns 合并匹配，不互相覆盖

**Native 扫描规则：**
- 扫描 `~/.aima/dist/{os}-{arch}/` 目录
- 扫描 PATH 中的可执行文件
- 匹配 Engine Asset YAML 中的 `source.binary` 字段
- 映射和 probe 证据全部来自 Engine Asset YAML，不在 Go 中按引擎或厂商分支

---

### 2. 引擎镜像拉取 (engine.pull)

### 2. 引擎镜像拉取

**获取方式优先级** (本地优先):

| 方式 | 场景 | 网络要求 |
|------|------|---------|
| 本地已存在 | containerd 已有镜像 | 无 |
| 离线导入 OCI tar | `aima engine import /media/usb/vllm.tar` | 无 |
| 局域网 Registry | 企业内部镜像仓库 | 局域网 |
| 国内镜像 | registry.cn-hangzhou.aliyuncs.com | 互联网 (国内) |
| Docker Hub | docker.io | 互联网 (国际) |

**拉取流程**:

```
aima engine pull vllm
  │
  ├── 1. 查找 Engine Asset YAML → 获取 image.registries 列表
  │
  ├── 2. 空间检查: 磁盘剩余 > image.size_approx_mb × 1.5
  │
  ├── 3. 按 registries 优先级 + 网络环境自动选择:
  │      ├── 检测网络可达性 (timeout 3s)
  │      ├── 国内 IP → 优先使用国内镜像源
  │      └── 国际 IP → 使用 Docker Hub
  │
  ├── 4. 通过 containerd (ctr/crictl) 拉取:
  │      └── crictl pull <registry>/<image>:<tag>
  │
  ├── 5. 拉取成功 → 更新 SQLite engines 表
  │
  └── 6. Agent 可通过 deploy.apply 使用此引擎
```

### 3. Docker ↔ K3S Containerd 互通

Docker 和 K3S containerd 使用独立的镜像存储。通过 `docker pull` 拉取的镜像不会自动出现在 K3S containerd 中。

**engine scan 自动检测与处理：**
- 扫描时同时查询 crictl (containerd) 和 docker，按 image:tag 去重
- 仅在 Docker 中的镜像标记 `docker_only=true`
- **默认不自动导入**（避免非 root 用户 scan 时报错）
- 自动导入仅在 `AutoImport=true` 时触发（`aima init` 或 `aima engine scan --import`）
- 导入时如无 containerd 写权限，打印 WARN 和手动修复命令：
  ```
  WARN engine in Docker but not in K3S containerd; import requires root
       engine=vllm image=vllm/vllm-openai:latest
       fix="sudo docker save vllm/vllm-openai:latest | sudo k3s ctr -n k8s.io images import -"
  ```

**Pod 部署保障：**
- Pod 模板设置 `imagePullPolicy: IfNotPresent`，防止 K3S 尝试从 registry 拉取已存在的镜像
- deploy 前置检查：如果检测到镜像在 Docker 中，打印提示信息（非致命）

### 4. Native 二进制管理

除容器镜像外，AIMA 还管理 native 引擎二进制（用于非 K3S 环境）。

**BinaryManager** (`internal/engine/binary.go`) 负责 native 引擎二进制的解析、下载和缓存：

```
BinaryManager.Resolve(ctx, source)
  │
  ├── 1. distDir 查找: ~/.aima/dist/{os}-{arch}/{binary}
  │      → 预装或之前下载的二进制
  │
  ├── 2. PATH 查找: which/where {binary}
  │      → 用户手动安装到 PATH 的二进制
  │
  └── 3. 自动下载:
         ├── 检查 platform 兼容性 (source.platforms)
         ├── 选择 URL: 优先 mirror (国内)，fallback 到 download (国际)
         ├── 下载到 distDir
         ├── chmod +x (非 Windows)
         └── 返回完整路径
```

**binary 缓存目录**:
```
~/.aima/
  dist/
    linux-amd64/
      <asset-name>/
        <version>/
          <binary>
    darwin-arm64/
      <asset-name>/<version>/<binary>
    windows-amd64/
      <asset-name>/<version>/<binary>.exe
```

**导入本地 native bundle**:

```bash
# 二进制与共享库位于同一目录时，会同时导入共享库和常见运行目录
aima engine import /opt/llamacpp/llama-server

# 二进制与运行库分散在 bundle 内时，直接导入完整目录或压缩包
aima engine import /opt/llamacpp
aima engine import /media/usb/llamacpp-linux-amd64.tar.gz
```

Linux 单文件导入会解析软链接的真实目录，并复制同目录的 `*.so*` 及
`lib`、`lib64`、`plugins`、`backends` 目录。启动时 AIMA 从实际二进制目录运行，
并将这些目录合并到 `LD_LIBRARY_PATH`，避免动态 backend 因只复制主程序而缺失。

**与 NativeRuntime 的集成**:
- `BinaryManager` 通过 `BinaryResolveFunc` 函数类型注入到 `NativeRuntime`
- `NativeRuntime.Deploy()` 在 `findInDist` 失败后调用 `resolveBinary` 作为第三级 fallback
- 类型转换在 `main.go` 的 `selectRuntime()` 中完成，避免 runtime ↔ engine 包直接依赖

### 5. 部署后预热 (Warmup)

引擎冷启动后首次推理通常很慢（CUDA kernel JIT 编译、模型权重加载到 GPU 等）。
Engine Asset 可声明 `warmup` 配置，NativeRuntime 在 health check 通过后自动执行预热：

```
Deploy → 启动进程 → health check 轮询
  → health check 通过
  → warmup: POST /v1/chat/completions {"messages":[...], "max_tokens":1}
  → 预热完成 → 标记 ready
```

预热使用 dummy prompt 触发一次完整推理路径，将 CUDA kernel 编译和模型权重加载提前完成。

---

## 使用示例

### 扫描并查看引擎

```bash
# 自动检测运行时并扫描引擎
./aima engine scan

# 输出示例（容器运行时）
[
  {
    "id": "sha256:9fed...",
    "type": "vllm",
    "image": "vllm/vllm-openai",
    "tag": "v0.15.0",
    "size_bytes": 8900000000,
    "platform": "linux/amd64",
    "runtime_type": "container",
    "available": true
  }
]

# 输出示例（Native 运行时）
[
  {
    "id": "a1b2c3d4e5f6...",
    "type": "llamacpp",
    "image": "",
    "tag": "",
    "size_bytes": 52428800,
    "platform": "windows/amd64",
    "runtime_type": "native",
    "binary_path": "C:\\Users\\user\\.aima\\dist\\windows-amd64\\llama-server.exe",
    "available": true
  }
]

# 查看所有已注册引擎
./aima engine list
```

### 拉取引擎镜像

```bash
# 从镜像源拉取
./aima engine pull vllm

# 拉取成功后自动注册到数据库
./aima engine list
```

### 离线导入

```bash
# 在有网环境导出 OCI 镜像
docker save vllm/vllm-openai:latest -o /media/usb/vllm-latest.tar

# 在隔离环境导入
./aima engine import /media/usb/vllm-latest.tar

# Native 包先导入为 inactive imported/verified
./aima engine import /media/usb/engine-a-2.0.0.tar.gz

# 先查看无副作用计划，再显式激活
./aima engine ensure engine-a --version 2.0.0
./aima engine ensure engine-a --version 2.0.0 --apply

# 回滚只切换库存指针，不重启现有部署
./aima engine rollback engine-a --runtime native --confirm
```

---

## 相关文件

- `internal/engine/scanner.go` - 统一引擎扫描（容器 + Native + Docker-only 检测）
- `internal/engine/puller.go` - 镜像拉取 + Docker↔containerd 导入
- `internal/engine/importer.go` - OCI tar 导入
- `internal/engine/binary.go` - Native 二进制管理
- `internal/cli/engine.go` - CLI 命令处理
- `internal/mcp/tools_engine.go` - Engine MCP 工具定义
- `internal/mcp/tools.go` - `RegisterAllTools()` 注册入口
- `internal/sqlite.go` - SQLite v21 Engine 版本库存、激活、回滚和引用检查

---

*最后更新：2026-08-02（增加通用 Engine 版本生命周期、严格校验、离线导入、回滚与不可删除边界）*
