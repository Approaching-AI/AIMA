# Engine Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的统一引擎管理功能（容器镜像 + Native 二进制）。

## 设计理念：异构引擎统一管理

AIMA 支持两种引擎运行时，提供统一的用户界面：

| 运行时 | 适用场景 | 引擎类型 |
|--------|---------|----------|
| **Container Runtime** (K3S/Docker) | Linux 服务器，GPU 集群 | vLLM, SGLang, Ollama, llama.cpp |
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
| `aima engine list` | 列出所有已注册引擎 |
| `aima engine pull <name>` | 拉取引擎镜像（容器运行时） |
| `aima engine import <path>` | 从 OCI tar 文件导入镜像（容器运行时） |
| `aima engine remove <name>` | 删除引擎 |

### MCP 工具

| 工具 | JSON-RPC 方法 | 功能 |
|------|---------------|------|
| `engine.scan` | `engine.scan` | 扫描本地引擎（统一） |
| `engine.list` | `engine.list` | 列出所有引擎 |
| `engine.pull` | `engine.pull` | 拉取引擎镜像 |
| `engine.import` | `engine.import` | 导入引擎镜像 |
| `engine.remove` | `engine.remove` | 删除引擎 |

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
    platform TEXT,                    -- linux/amd64 | linux/arm64 | darwin/arm64 | windows/amd64
    runtime_type TEXT DEFAULT 'container', -- "container" or "native"
    binary_path TEXT,                 -- Native 二进制路径（Native 引擎）
    available BOOLEAN DEFAULT TRUE,   -- 引擎是否在本地可用
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
engine.scan
  │
  ├── 尝试容器扫描 (crictl / docker)
  │   ├── 成功 → 返回容器镜像列表
  │   │   └── vllm/vllm-openai:latest       → vllm 引擎 (runtime_type=container)
  │   │   └── ghcr.io/ggerganov/llama.cpp:server → llamacpp 引擎 (runtime_type=container)
  │   │
  │   └── 失败 → 尝试 Native 扫描
  │       ├── 扫描 distDir: ~/.aima/dist/{os}-{arch}/
  │       │   └── llama-server                   → llamacpp 引擎 (runtime_type=native)
  │       ├── 扫描 PATH
  │       │   └── /usr/local/bin/llama-server   → llamacpp 引擎 (runtime_type=native)
  │       │
  │       └── 扫描结果注册到 SQLite engines 表
```

**扫描优先级：**
1. Container Runtime 优先：有 K3S/Docker 则扫描容器镜像
2. Native Fallback：无容器运行时时扫描 Native 二进制
3. 两者都不可用：返回错误

**Native 扫描规则：**
- 扫描 `~/.aima/dist/{os}-{arch}/` 目录
- 扫描 PATH 中的可执行文件
- 匹配 Engine Asset YAML 中的 `source.binary` 字段
- 已知映射：`ghcr.io/ggerganov/llama.cpp` → `llama-server`

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

### 3. Native 二进制管理

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
      llama-server           # llamacpp binary
    darwin-arm64/
      llama-server
    windows-amd64/
      llama-server.exe
```

**与 NativeRuntime 的集成**:
- `BinaryManager` 通过 `BinaryResolveFunc` 函数类型注入到 `NativeRuntime`
- `NativeRuntime.Deploy()` 在 `findInDist` 失败后调用 `resolveBinary` 作为第三级 fallback
- 类型转换在 `main.go` 的 `selectRuntime()` 中完成，避免 runtime ↔ engine 包直接依赖

### 4. 部署后预热 (Warmup)

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
```

---

## 相关文件

- `internal/engine/scanner.go` - 统一引擎扫描（容器 + Native）
- `internal/engine/puller.go` - 镜像拉取
- `internal/engine/importer.go` - OCI tar 导入
- `internal/engine/binary.go` - Native 二进制管理
- `internal/cli/engine.go` - CLI 命令处理
- `internal/mcp/tools.go` - MCP 工具定义
- `internal/sqlite.go` - 数据库 schema (migrateV4 添加 runtime_type/binary_path)

---

*最后更新：2026-02-27 (v4: 统一引擎扫描)*
