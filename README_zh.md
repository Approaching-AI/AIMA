# AIMA

[English](README.md)

**AI Inference Managed by AI** — 一个 Go 单二进制，自动检测硬件、从 YAML 知识库解析最优配置、通过 K3S 部署推理引擎，并暴露 56 个 MCP 工具供 AI Agent 操控一切。

## 特性

- **零配置硬件检测** — 自动发现 GPU（NVIDIA、AMD、Apple Silicon）、CPU 和内存
- **知识驱动部署** — YAML 目录包含硬件画像、引擎、模型和分区策略；无引擎特定代码分支
- **多运行时** — K3S（Pod）容器化负载 + Native（exec）裸机推理
- **56 个 MCP 工具** — AI Agent 可通过程序化接口完整控制硬件、模型、引擎、部署、集群等
- **集群管理** — 基于 mDNS 的局域网自动发现；跨异构设备远程工具执行
- **离线优先** — 所有核心功能零网络依赖；网络仅作增强
- **单二进制，零 CGO** — 可交叉编译到 Windows、macOS、Linux（amd64/arm64），无 C 依赖

## 快速开始

### 下载

从 [Releases](https://github.com/jguan/aima/releases) 页面下载预编译二进制，或从源码构建：

```bash
git clone https://github.com/jguan/aima.git
cd aima
make build
```

### 服务器部署（Linux）

```bash
# 1. 检测硬件
aima hal detect

# 2. 初始化基础设施（安装 K3S + HAMi + aima-serve 守护进程）
#    自动下载 airgap 离线镜像包，容器启动无需联网。
#    需要 root 权限安装 systemd 服务。
sudo aima init

# 3. 部署模型（自动匹配硬件和引擎）
aima deploy apply --model qwen3.5-35b-a3b
```

`aima init` 完成后，三个组件以 systemd 服务运行：

| 组件 | 作用 |
|------|------|
| K3S | 容器编排（containerd 就绪，airgap 镜像已预加载） |
| HAMi | GPU 虚拟化，支持多模型共享显存（不兼容的硬件自动跳过） |
| aima-serve | API 服务监听 `0.0.0.0:6188`，mDNS 自动广播 |

服务器现在可以被局域网内的设备自动发现，随时接受推理请求。

### 客户端使用（任意平台）

在另一台设备上只需要 AIMA 二进制，不需要 `init` 或 `serve`：

```bash
# 通过 mDNS 自动发现局域网中的服务器（无需知道 IP）
aima discover

# 列出所有已发现的 AIMA 设备
aima fleet devices

# 远程查询和操控
aima fleet exec <device-id> hardware.detect
aima fleet exec <device-id> deploy.list

# 直接调用 OpenAI 兼容 API
curl http://<服务器IP>:6188/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen3.5-35b-a3b","messages":[{"role":"user","content":"hello"}]}'
```

### Web UI

每个 AIMA 服务器内置 Web UI，访问 `http://<服务器IP>:6188/ui/`。

如何获取服务器 IP：运行 `aima discover`。

如需 Fleet 全局仪表盘（自动发现局域网内所有节点），在自己的设备上运行 `aima serve --discover`，然后打开 `http://localhost:6188/ui/`。

### 安全

`aima init` 默认 **无认证启动**（局域网信任模型）。启用 API Key 认证：

```bash
# 设置 API Key（热更新，无需重启）
aima config set api_key <your-key>

# 之后所有 API/MCP/Fleet 请求都需要: Authorization: Bearer <your-key>
# Web UI 会自动弹出 Key 输入框。

# 远程 Fleet 命令带认证
aima fleet devices --api-key <your-key>
```

## 支持硬件

| 厂商 | 已测试设备 | SDK |
|------|-----------|-----|
| NVIDIA | RTX 4060、RTX 4090、GB10（Grace Blackwell） | CUDA |
| AMD | Radeon 8060S（RDNA 3.5）、Ryzen AI MAX+ 395 | ROCm / Vulkan |
| Apple | M4 | Metal |
| Intel | 仅 CPU | — |

## 支持引擎

| 引擎 | GPU 支持 | 格式 |
|------|---------|------|
| vLLM | NVIDIA CUDA、AMD ROCm | Safetensors |
| llama.cpp | NVIDIA CUDA、AMD Vulkan、Apple Metal、CPU | GGUF |
| SGLang | NVIDIA CUDA | Safetensors |
| Ollama | 全部（通过 llama.cpp） | GGUF |

## 架构

AIMA 采用分层智能架构（L0-L3）：

- **L0** — YAML 知识库默认值
- **L1** — 人工 CLI 覆盖
- **L2** — 基准测试历史中的黄金配置
- **L3a** — Go Agent 循环（工具调用 LLM）
- **L3b** — ZeroClaw 边车（可选）

系统围绕四个不变量构建：引擎/模型类型无代码分支（YAML 驱动）、不管理容器生命周期（K3S 负责）、MCP 工具作为唯一真相源、离线优先。

完整架构文档见 [design/ARCHITECTURE.md](design/ARCHITECTURE.md)。

## 项目结构

```
cmd/aima/          入口
internal/
  hal/             硬件检测
  knowledge/       YAML 知识库 + SQLite 解析器
  runtime/         K3S（Pod）+ Native（exec）运行时
  mcp/             56 个 MCP 工具实现
  agent/           Go Agent 循环（L3a）
  cli/             Cobra CLI（MCP 工具的薄包装）
  ui/              内嵌 Web UI（Alpine.js SPA）
  proxy/           OpenAI 兼容 HTTP 代理
  fleet/           mDNS 集群发现 + 远程执行
  state/           SQLite 状态存储（modernc.org/sqlite，零 CGO）
  model/           模型扫描/下载/导入
  engine/          引擎镜像管理
  stack/           K3S + HAMi 基础设施安装器
catalog/
  hardware/        硬件画像 YAML
  engines/         引擎资产 YAML
  models/          模型资产 YAML
  partitions/      分区策略 YAML
  stack/           栈组件 YAML
```

## 构建

### 本机构建

```bash
make build
# 输出: build/aima（Windows 上为 build/aima.exe）
```

### 交叉编译所有平台

```bash
make all
# 输出:
#   build/aima.exe          (windows/amd64)
#   build/aima-darwin-arm64 (macOS/arm64)
#   build/aima-linux-arm64  (linux/arm64)
#   build/aima-linux-amd64  (linux/amd64)
```

### 运行测试

```bash
go test ./...
```

## 许可证

Apache License 2.0。详见 [LICENSE](LICENSE)。
