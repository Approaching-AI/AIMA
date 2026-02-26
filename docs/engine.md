# Engine Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的引擎镜像和二进制管理功能。

## 接口定义

### CLI 命令

| 命令 | 功能 |
|------|------|
| `aima engine scan` | 扫描 containerd 镜像，匹配 Engine Asset |
| `aima engine list` | 列出所有已注册引擎 |
| `aima engine pull <name>` | 拉取引擎镜像 |
| `aima engine import <path>` | 从 OCI tar 文件导入镜像 |
| `aima engine remove <name>` | 删除引擎镜像 |

### MCP 工具

| 工具 | JSON-RPC 方法 | 功能 |
|------|---------------|------|
| `engine.scan` | `engine.scan` | 扫描本地引擎 |
| `engine.list` | `engine.list` | 列出所有引擎 |
| `engine.pull` | `engine.pull` | 拉取引擎镜像 |
| `engine.import` | `engine.import` | 导入引擎镜像 |
| `engine.remove` | `engine.remove` | 删除引擎 |

---

## 数据结构

### Engine (internal/sqlite.go)

数据库表定义，存储已注册的引擎镜像：

```sql
CREATE TABLE engines (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,               -- vllm | llamacpp | ollama | sglang
    image TEXT NOT NULL,              -- 完整镜像名 (含 registry)
    tag TEXT NOT NULL,
    size_bytes INTEGER,
    platform TEXT,                    -- linux/amd64 | linux/arm64
    available BOOLEAN DEFAULT TRUE,   -- 镜像是否在本地
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
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

### 1. 引擎镜像扫描

扫描 containerd 已有镜像，匹配 Engine Asset YAML：

```
containerd 镜像列表 (ctr images ls / crictl images)
  │
  ├── 匹配 image.name:tag
  │   └── vllm/vllm-openai:latest       → 标记为 "vllm" 引擎可用
  │   └── ghcr.io/ggerganov/llama.cpp:server → 标记为 "llamacpp" 引擎可用
  │
  └── 扫描结果注册到 SQLite engines 表
```

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
# 扫描 containerd 已有镜像
./aima engine scan

# 输出示例
[
  {
    "id": "vllm-latest",
    "type": "vllm",
    "image": "vllm/vllm-openai",
    "tag": "latest",
    "available": true,
    "platform": "linux/amd64"
  }
]
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

- `internal/engine/scanner.go` - 容器镜像扫描
- `internal/engine/puller.go` - 镜像拉取
- `internal/engine/importer.go` - OCI tar 导入
- `internal/engine/binary.go` - Native 二进制管理
- `internal/cli/engine.go` - CLI 命令处理
- `internal/mcp/tools.go` - MCP 工具定义

---

*最后更新：2026-02-27*
