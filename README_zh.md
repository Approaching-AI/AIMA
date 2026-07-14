# AIMA

这是面向摩尔线程 AIBook 的 AIMA 应用版本。

由 AI 管理的 AI 基础设施。目标是 Ollama 级的 TCO 跑出 vLLM 级的性能，做法是把 AI agent 放进推理回路里。

AIMA 是一个 Go 单二进制，在设备上管理 AI 推理：识别硬件，从 YAML 知识库里挑引擎和配置，部署模型，跑 benchmark，把胜出配置写回知识库。整个回路由内置 agent 驱动；AIMA 本身也是 MCP server，可以被外部 agent（比如 OpenClaw）接管。

[English](README.md) · [为什么是 AIMA](#为什么是-aima) · [快速开始](#快速开始) · [真机实测](#真机实测)

---

## 为什么是 AIMA

市面上的"私有化 AI"方案通常落在两个极端。

Ollama 和 LM Studio 走简单路线：一个二进制，一个引擎（llama.cpp / GGUF），一套默认参数。代价是吞吐被引擎天花板锁死。

裸 vLLM、SGLang、TensorRT-LLM 走性能路线：数字好看，但参数调优、量化选型、部署脚本、跨厂商兼容问题都压在你身上。每换一家芯片基本等于重做一遍。

AIMA 的做法是让 agent 来做操作员。

|  | Ollama / LM Studio | 裸 vLLM / SGLang | AIMA |
|---|---|---|---|
| 一行安装 | ✅ | — | ✅ |
| OpenAI 兼容 API | ✅ | ✅ | ✅ |
| 推理后端 | llama.cpp | vLLM / SGLang | vLLM · SGLang · llama.cpp（按硬件自动挑） |
| 独显上的 SOTA 吞吐 | ❌ | ✅（要会调） | ✅（agent 帮你调） |
| NVIDIA / AMD / Apple | ✅ | 部分 | ✅ |
| 华为昇腾 / 海光 DCU / 摩尔线程 / 沐曦 | ❌ | 自己搞 | ✅（真机实测） |
| MCP server 开箱即用 | ❌ | ❌ | ✅ |
| 自调优循环（plan → deploy → benchmark → learn） | ❌ | ❌ | ✅ |
| 局域网 fleet / 多机集群 | ❌ | 自己搞 | ✅（mDNS 自动发现） |
| 离线 / airgap | 部分 | 自己搞 | ✅（镜像离线预装） |

Ollama 和 LM Studio 用放弃性能换了低 TCO，一种引擎一种格式就是全部。vLLM 和 SGLang 用放弃易用换了性能，操作员是你。AIMA 把操作员换成 agent，"这片芯片最快的跑法"由 YAML 知识库积累。

---

## Agent 原生

AIMA 是一个 MCP server。

### 外部 agent 驱动 AIMA

把任何 MCP 兼容 runtime 指向 AIMA 的端口，它就能拿到完整操作面：硬件检测、模型扫描、引擎选择、部署、benchmark、集群发现、知识同步。不需要自己写 REST wrapper，也不依赖官方 SDK。

目前 AIMA 已经作为 OpenClaw 的推理后端在跑（OpenClaw 是一个社区活跃的开源多模态 agent 框架），覆盖 LLM / ASR / TTS / 图像生成 / VLM。其他会说 MCP 的 runtime 接入方式一样。

```jsonc
// MCP client 指向 AIMA 的 HTTP 端点就是全部集成
{
  "mcpServers": {
    "aima": { "type": "http", "url": "http://<aima-host>:6188/mcp" }
  }
}
```

### AIMA 内部也跑 agent

AIMA 自己也消费 MCP。内置的 PDCA agent（代号 Explorer）持续规划 benchmark、部署配置、采样 throughput / TTFT，把胜出配置提升到共享知识库。新芯片到手时，agent 自己跑调优矩阵。

这是 "agent in the loop" 的具体含义，也是单二进制能跑出 vLLM 级吞吐的原因。

---

## 快速开始

### 1. 拿到二进制

一行安装：

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/Approaching-AI/AIMA/master/install.sh | sh

# Windows PowerShell
irm https://raw.githubusercontent.com/Approaching-AI/AIMA/master/install.ps1 | iex
```

也可以从 [Releases](https://github.com/Approaching-AI/AIMA/releases) 下载预编译二进制（macOS arm64、Linux amd64/arm64、Windows amd64）。

或从源码构建：`git clone https://github.com/Approaching-AI/AIMA && cd AIMA && make build`。

### 2. 看看 AIMA 识别到什么硬件

```bash
aima hal detect
```

打印识别到的 GPU / NPU（NVIDIA、AMD、昇腾、DCU、Apple、摩尔线程、沐曦，或者仅 CPU）、驱动版本和 RAM。这一步也是确认二进制能在这台机器上跑起来的快速方法。

### 3. 运行首次使用向导

```bash
aima onboarding
```

向导会依次做状态检查、资源扫描、模型推荐，并打印下一条可以直接执行的命令。默认是只读检查，不会自动安装系统服务，也不会在没有明确确认的情况下部署模型。

### 4. 跑一个安全的入门模型

```bash
aima run qwen3-4b
```

`run` 会解析模型、按当前硬件选择引擎和配置、拉取缺失资源、部署模型并等待就绪。你也可以把 `qwen3-4b` 换成 `aima onboarding recommend` 推荐的模型。

如果需要在当前终端保持 OpenAI 兼容 API 和 Web UI 常驻：

```bash
aima serve
```

### 5. Linux 共享服务端路径

如果这台 Linux 工作站或服务器要给其他机器提供推理服务，先初始化基础设施栈：

```bash
sudo aima init
aima deploy qwen3-4b
aima serve
```

`init` 会安装 Linux 本地服务栈。macOS 和 Windows 的本地 native 使用可以跳过这一步。

### 6. 调 OpenAI 兼容 API

```bash
curl http://127.0.0.1:6188/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen3-4b","messages":[{"role":"user","content":"hello"}]}'
```

任何 OpenAI SDK 客户端都能连 `http://<server-ip>:6188/v1`。

### 其他

- 多机 fleet：运行 `aima fleet devices` 列出 mDNS 自动发现的 AIMA 节点，再用 `aima fleet exec <id> hal.detect` 远程驱动。
- AIMA 是 MCP server，任何 MCP 兼容 agent runtime 都能驱动它。见上方 [Agent 原生](#agent-原生) 一段。
- 启用 API Key 认证：`aima config set api_key <key>`（热更新，参见 [安全](#安全)）。

---

## AIBook 源码部署流程

下面这套流程适合在源码目录里改完代码后，重新生成 AIBook 用的 `.deb` 并部署到目标机器。

### 1. 本地构建与自测

建议在 Linux 或 WSL 环境里执行，原因是 AIBook 打包脚本依赖 `bash` 和 `dpkg-deb`。

```bash
cd AIMA-aibook-source
go test ./internal/onboarding ./internal/cli ./internal/ui ./internal/mcp ./cmd/aima -count=1
make aibook-deb
```

`make aibook-deb` 会用仓库里的 `-ldflags "-s -w ..."` 构建 arm64 二进制，去掉调试信息后再打包。不要直接用普通 `go build` 打 AIBook 包，否则可能把调试信息带进 `.deb`，导致包体积明显变大。

AIBook 登记的产品版本固定为 `v0.4.1`。Debian 包版本会追加打包日期以便区分构建，应用名、包名和进程名分别保持为 `AIMA`、`aima-aibook` 和 `aima`。该版本的 DEB 使用登记的橙色 AIMA 图标。

### 2. 生成 AIBook 的 `.deb`

默认会在 `build/release/` 下生成一个类似下面名字的包：

```text
aima-aibook_0.4.1+aibookYYYYMMDD.1_arm64.deb
```

如果你确实需要手动指定版本号，先用 `make aibook-deb` 生成去掉调试信息的 `build/aima-linux-arm64`，再调用打包脚本：

```bash
bash scripts/package-aibook-deb.sh build/aima-linux-arm64 0.4.1+aibookYYYYMMDD.1
```

### 3. 部署到 AIBook 机器

```bash
AIMA_DEB=$(ls -1t build/release/aima-aibook_*_arm64.deb | head -n 1)
scp "$AIMA_DEB" aibook@192.168.109.142:/tmp/aima-aibook_arm64.deb
ssh aibook@192.168.109.142
sudo dpkg -i /tmp/aima-aibook_arm64.deb
```

### 4. 安装后的校验

```bash
dpkg -s aima-aibook | grep Version
systemctl status aima.service --no-pager
ss -lntp | grep 6188
curl http://127.0.0.1:6188/health
curl -s http://127.0.0.1:6188/ui/api/onboarding-manifest | jq '.default_locale'
```

正常情况下你应该看到：

- 包版本已经切到新版本
- `aima.service` 为 `active (running)`
- 端口监听在 `6188`
- `/health` 返回 `status: "ok"`
- onboarding manifest 的 `default_locale` 为 `zh`

### 5. 当前 AIBook 包的升级覆盖行为

现在 AIBook 的 `.deb` 支持直接覆盖升级，不需要先卸载旧版：

```bash
sudo dpkg -i ./aima-aibook_*_arm64.deb
```

升级时会自动：

- 停掉旧的 `aima.service`
- 删除遗留的 `aima-serve.service`
- 用新包覆盖 `/usr/local/bin/aima`
- 重写 `/etc/aima/aima.env`
- 重写 `/etc/aima/data-dir`
- 清理旧的 UI 缓存目录
- `daemon-reload` 后重新拉起新的 `aima.service`

升级时会保留这些持久数据，不会因为覆盖安装被删掉：

- `/var/lib/aima/aima.db`
- 已扫描/已部署的模型状态
- 设备身份、注册信息和其他持久配置

如果安装完成后浏览器仍显示旧语言或旧页面，优先检查浏览器本地站点数据，例如 `localStorage` 里的 `aima_lang`，必要时刷新或清掉该站点缓存。

---

## AIBook 干净卸载流程

如果只是临时移除程序但保留数据，可以直接：

```bash
sudo dpkg -r aima-aibook
```

这会移除包和服务入口，但会保留 `/var/lib/aima` 里的数据库、模型状态和设备信息。

如果你要做真正的“干净卸载”，也就是连配置、缓存、数据库和日志一起清掉，建议按下面顺序执行：

```bash
sudo systemctl stop aima.service || true
sudo systemctl disable aima.service || true
sudo dpkg -P aima-aibook
sudo rm -rf /var/lib/aima /var/log/aima /etc/aima
sudo rm -f /etc/systemd/system/aima-serve.service
sudo systemctl daemon-reload
sudo systemctl reset-failed
sudo update-desktop-database /usr/share/applications || true
sudo gtk-update-icon-cache -q /usr/share/icons/hicolor || true
```

执行完这套命令后，机器上会一并清掉：

- AIMA 主程序和桌面入口
- `/etc/aima` 下的环境配置
- `/var/lib/aima` 下的数据库、模型状态、设备身份和缓存
- `/var/log/aima` 下的日志
- 旧遗留的 `aima-serve.service`

如果你只想“像全新安装一样重新来”，但又不想丢设备或数据库，先备份 `/var/lib/aima`，再执行干净卸载，最后按需把备份文件恢复回来。

---

## 真机实测

不用 mock，不用模拟器。每个 release tag 切之前都要过一轮 UAT 矩阵，同一个二进制在所有厂商的真机上都要跑通。

### 覆盖的硬件

GPU / NPU 厂商 7 家：NVIDIA、AMD、华为昇腾、海光 DCU、Apple Silicon、摩尔线程、沐曦。另外 Intel CPU-only 也覆盖。

操作系统 5 个：Ubuntu、Windows 11、macOS、EulerOS、Kylin V10。CPU 架构 2 种：x86_64、aarch64。每次构建交叉编译 4 个目标：`windows/amd64`、`darwin/arm64`、`linux/amd64`、`linux/arm64`。

### v0.4.0 发布门槛

| 指标 | 数字 |
|---|---|
| 纳入发布门槛的 UAT 项 | 16 项（P0 5 · P1 7 · P2 4） |
| UAT PASS + 已跟踪已知问题 | 11 PASS · 5 tracked |
| `artifacts/uat/v0.4/` 下的证据目录 | 20 个 |
| 原始证据文件（日志、DB 快照、status dump、JSON） | 1,200+ 个，分布在 86 个子目录 |
| 发布前 Explorer 端到端修复-重跑轮次 | 7 轮（2026-03-XX → 2026-04-17） |
| v0.3.0 → v0.4.0 发版周期 | 18 天，176 commits，每次切 tag 都过一轮真机 smoke |
| 累计上机运行时长 | 约 1,000 小时（artifact 里能查到的真实 wall clock） |

### 测试方法

UAT 按 "ALL COLLECT, THEN ANALYZE"（全量采集后再分析）来做：所有设备跑同一个二进制、同一组命令，结果齐了才允许碰代码。不接受"本机修好了就发"。绿灯 tag 意味着修复在同一轮次里每家硬件上都过了。

证据链：
- [`docs/uat/v0.4-release-uat.md`](docs/uat/v0.4-release-uat.md)
- [`artifacts/uat/v0.4/`](artifacts/uat/v0.4/)
- [`CHANGELOG.md`](CHANGELOG.md)

YAML 优先、引擎无代码分支这套架构，只有在没控制权的芯片上也跑通了才算数。上面列出的每家厂商都是一个干净的 YAML PR 接进来的，Go 源码里没有 `if engine == "vllm-ascend"` 这类分支。集群里有我们还没测过的芯片，加进来的方式是提 YAML PR，不是 fork 代码。

---

## 工作原理

完整架构文档在 [`design/ARCHITECTURE.md`](design/ARCHITECTURE.md)。四个不变量：

1. 引擎和模型类型零代码分支。引擎行为放 YAML，模型元数据放 YAML。加新引擎或新模型只写 YAML，Go 代码不动。
2. 不管容器生命周期。K3S / Docker 负责，AIMA 只下发 `apply / status / delete / logs`。
3. MCP 工具是唯一事实源。CLI、Web UI、内部 agent 都走同一套工具 API。
4. 离线优先。所有核心能力零网络依赖，网络只是增强。

分层智能 L0-L3，上层不可用时逐层降级：

- L0：YAML 知识库默认值，始终可用，离线安全
- L1：人工 CLI 覆盖
- L2：历史 benchmark 里提升上来的 golden config
- L3：Explorer agent（规划、部署、测量、学习）

三种运行时：K3S（Pod）用于服务器和集群，Docker 用于单机，Native（exec）用于裸机边缘设备。

---

## 支持硬件

| 厂商 | SDK | 说明 |
|---|---|---|
| NVIDIA | CUDA | 含 GB10（Grace Blackwell） |
| AMD | ROCm / Vulkan | 含 W7900D（RDNA3 8-GPU 服务器）、Ryzen AI MAX+ 395 APU |
| 华为 | CANN | Ascend 910B1（aarch64 / 鲲鹏） |
| 海光 | DCU | BW150（HBM） |
| Apple | Metal | Apple Silicon（M 系列） |
| 摩尔线程 | MUSA | M1000 独显和 SoC（GPU + NPU） |
| 沐曦 | MACA | N260 |
| Intel | — | 仅 CPU 推理 |

## 支持引擎

| 引擎 | 加速器 | 格式 |
|---|---|---|
| vLLM | NVIDIA CUDA · AMD ROCm · 海光 DCU · 沐曦 MACA · 摩尔线程 MUSA | Safetensors |
| SGLang | NVIDIA CUDA · 华为昇腾（CANN） | Safetensors |
| llama.cpp | NVIDIA CUDA · AMD Vulkan · Apple Metal · CPU | GGUF |

引擎路由默认由 agent 按硬件和模型画像挑选，也可以通过 CLI / MCP 手动指定。

---

## 安全

`aima init` 默认无认证启动，走局域网信任模型。启用 API Key：

```bash
aima config set api_key <your-key>       # 热更新，不用重启
aima fleet devices --api-key <your-key>  # 远程 fleet 调用
# Web UI 和 MCP 此后都要求 Authorization: Bearer <your-key>
```

## 项目结构

```
cmd/aima/          # Edge 二进制入口
internal/
  hal/             # 硬件检测
  knowledge/       # YAML 知识库 + SQLite 解析器
  runtime/         # K3S (Pod) + Docker + Native 运行时
  mcp/             # MCP 服务端 + 工具实现
  agent/           # Explorer PDCA agent (L3) + dispatcher
  cli/             # MCP 工具的薄 CLI 包装
  ui/              # 内嵌 Web UI（Alpine.js SPA）
  proxy/           # OpenAI 兼容 HTTP 代理
  fleet/           # mDNS 集群发现 + 远程执行
catalog/           # YAML 知识资产：hardware / engines / models / partitions / stack / scenarios
```

## 构建

```bash
make build                  # 本地构建
make all                    # 交叉编译 windows / darwin-arm64 / linux-{amd64,arm64}
make release-assets         # 打包 release 资产 + checksums.txt
make publish-release-assets # 通过 gh 上传到对应的 GitHub release
go test ./...               # 运行测试
```

推 SemVer tag（如 `v0.4.0`）会触发 `.github/workflows/release.yml`，自动构建并发布同一套资产。

## 许可证

Apache License 2.0。详见 [LICENSE](LICENSE)。
