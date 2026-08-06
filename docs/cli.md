# CLI Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的命令行接口。

## 设计原则

CLI 命令是 MCP 工具的人类友好包装。
以 `aima deploy qwen3-8b` 为例，命令层负责解析参数并调用共享的 ToolDeps/MCP 执行路径；
真正的配置解析、部署、状态查询都在 MCP 工具与其依赖里完成。

CLI 永不实现 MCP 工具之外的逻辑——确保 Agent 和人类走同一条代码路径。

---

## 命令列表

以下分组与 `aima --help` 当前输出一致。

### 服务与部署

```bash
aima init                                 # 安装基础设施栈（默认 Docker 层，--k3s 安装完整栈）
aima serve                                # 启动 AIMA 服务器及部署恢复 Controller
aima run <model>                          # 下载、部署并直接提供服务（类似 ollama run）
aima deploy <model> [--engine] [--slot]   # 部署推理服务
aima undeploy <name>                      # 删除部署
aima status                               # 查看系统状态
```

### 硬件、模型、引擎

```bash
aima hal detect                           # 检测硬件能力
aima hal metrics                          # 查看实时硬件指标
aima model scan                           # 扫描本地模型
aima model list                           # 列出已注册模型
aima model info <name>                    # 获取模型详细信息
aima model pull <model>                   # 下载模型（断点续传）
aima model import <path>                  # 从本地路径/USB 导入
aima model remove <name>                  # 注销模型
aima model remove --delete-files <name>   # 删除模型记录并删除文件
aima engine scan                          # 扫描本地引擎
aima engine info <name>                   # 查看引擎详情
aima engine list                          # 列出可用引擎
aima engine ensure <name>                 # 输出无副作用的版本计划
aima engine ensure <name> --version <v> --apply  # 校验、安装或激活指定版本
aima engine rollback <name> --runtime <container|native> --confirm  # 回滚指定运行时组
aima engine pull [engine]                 # 拉取引擎镜像
aima engine import <path>                 # 离线导入 OCI 或版本化 Native 包
aima engine remove <engine-id>            # 删除无引用库存记录
aima engine remove <engine-id> --delete-files  # 仅删除 AIMA 数据目录内受管 Native 文件
```

### 知识、目录与基准

```bash
aima knowledge list                       # 列出知识资产
aima knowledge resolve <model>            # 解析最优配置
aima knowledge export [--output]          # 导出知识
aima knowledge import <path>              # 离线导入知识
aima knowledge sync                       # 与中心服务同步知识
aima knowledge validate                   # 校验预测与实际性能
aima catalog override <kind> <name> <yaml-file>  # 写入 user-owned catalog patch
aima catalog validate                     # 校验目录资产
aima catalog status                       # 查看 factory/overlay 状态
aima benchmark run --model <name>         # 在线基准测试（TTFT/TPOT/吞吐量）
aima benchmark matrix --model <name>      # 组合矩阵测试
aima benchmark record                     # 手动记录性能数据
aima benchmark list                       # 查询历史测试结果
```

### Agent 与自动化

```bash
aima ask "指令"                           # 让 Agent 执行任务
aima ask --session <id> "指令"            # 继续会话
aima agent status                         # 查看 Agent 状态
aima agent rollback-list                  # 查看可回滚快照
aima agent rollback <snapshot-id>         # 从快照恢复资源
aima explore start                        # 启动持久化探索任务
aima explore status                       # 查看探索任务状态
aima tuning start                         # 启动自动调优
aima tuning results                       # 查看调优结果
aima scenario list                        # 列出部署场景模板
aima scenario apply <scenario-name>       # 批量部署场景中的模型
aima app register                         # 注册应用依赖
aima app provision [name]                 # 自动为应用补齐依赖服务
aima openclaw sync                        # 同步已部署模型到 OpenClaw
aima askforhelp [request]                 # 请求远程协助/支持服务
aima onboarding                           # 首次使用向导：状态、扫描、推荐
aima onboarding start                     # 显式启动首次使用向导
aima onboarding recommend                 # 推荐适合当前硬件的模型/引擎组合
aima onboarding deploy --model <model>    # 通过 onboarding 流程部署模型
aima diagnostics export                   # 导出本地脱敏诊断包，不自动上传
```

`aima askforhelp` 默认连接 `https://aimaserver.com`，运行时自动归一化为 `/api/v1` 支持接口。
如需覆盖，可使用 `--endpoint`，或通过 `aima config set support.endpoint <url>` / `AIMA_SUPPORT_ENDPOINT` 持久化配置。

### 集成与运维

```bash
aima config get <key>                     # 读取配置
aima config set <key> <value>             # 修改配置
aima fleet devices                        # 列出局域网设备
aima fleet info <device-id>               # 查看远端设备详情
aima fleet tools <device-id>              # 列出远端工具
aima fleet exec <device-id> <tool>        # 在远端执行 MCP 工具
aima mcp                                  # 通过 stdio 启动 MCP 服务
aima tui                                  # 打开终端仪表盘
aima version                              # 查看版本信息
```

---

## 部署恢复的 CLI 语义

- 自动恢复只在 `aima serve` 运行期间生效。关闭 serve、只运行 `aima mcp`，或执行一次性 CLI 命令，都不会留下独立恢复守护进程。
- `aima undeploy <name>` 通过共享 `deploy.delete` 路径先把持久化意图置为 `stopped`，再删除 Runtime 对象。该操作表示明确停止，后台 Controller 不会把它重新部署。
- 在 AIMA 之外结束 Native 进程、Docker 容器或 K3S Pod 不等同于 `undeploy`，不会修改 desired state。Native 可由 AIMA 按策略重新部署；Docker/K3S 的普通重启仍由各自平台负责，AIMA 只观察重启计数并执行有界隔离。
- 状态显示 `recovery_state=quarantined` 时，自动尝试预算已耗尽。修复根因后重新执行原始 `aima deploy <model>`（或直接调用 MCP `deploy.apply`）会执行显式 apply、清除隔离计数并重新部署。

默认策略为：启用；每 5 秒检查；Native 连续 3 次失败后恢复；600 秒窗口内最多 3 次恢复；失败退避 2/10/30 秒；持续健康 600 秒后清空计数。Catalog Engine Asset 和 MCP `recovery_policy` 可以在通用边界内覆盖这些值；CLI 当前没有独立的恢复策略 flag。

显式部署、删除和后台恢复只在同一个 AIMA 进程内共享部署操作锁。不要同时启动多个写进程并让它们共享同一 SQLite 数据库；当前锁不提供跨进程 Runtime 副作用协调。

---

## Engine 生命周期的 CLI 语义

- `aima engine ensure` 默认只输出计划；没有 `--apply` 时不下载、不写数据库、不切换 active。
- 网络安装要求 Catalog 中存在严格 SHA256/OCI digest。校验失败保持旧 active 不变。
- `aima engine import` 对 Native 包要求实际版本目录，导入后只登记为 inactive；使用 `ensure --apply` 才激活。
- `aima engine rollback` 必须显式传 `--runtime container|native` 和 `--confirm`，且该运行时组的前一版本必须 verified、available。
- 激活和回滚不会重启当前部署。部署继续使用持久化 intent 中固定的 Engine Asset/版本，直到操作者显式重新部署。
- `engine remove --delete-files` 只允许无引用的 `managed`/`imported` Native 资产，且规范路径必须严格位于 `AIMA_DATA_DIR`。preinstalled、legacy、容器镜像层和外部路径不会被物理删除。

---

## 命令实现模式

### Thin CLI Pattern

每个 CLI 命令是 MCP 工具的薄包装：

```go
func newDeployCmd(app *App) *cobra.Command {
    cmd := &cobra.Command{
        Use:   "deploy <model>",
        Short: "Deploy a model inference service",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            ctx := cmd.Context()
            model := args[0]

            // 直接调用 MCP 工具函数，不包含业务逻辑
            data, err := app.ToolDeps.DeployApply(ctx, engine, model, slot)
            if err != nil {
                return err
            }
            fmt.Fprintln(cmd.OutOrStdout(), string(data))
            return nil
        },
    }
    return cmd
}
```

**CORRECT**: CLI 调用 MCP 工具
**WRONG**: CLI 包含业务逻辑

---

## 使用示例

### 快速部署

```bash
# 一步完成下载、部署和服务暴露
aima run qwen3-8b

# 查看状态
aima status
```

### 扫描并部署本地模型

```bash
# 从本地目录导入模型
aima model import ./models/glm-4-9b-chat

# 部署导入后的模型名
aima deploy glm-4-9b-chat
```

### Agent 查询

```bash
# 让 Agent 回答简单问题
aima ask "我有什么 GPU?"

# 让 Agent 回答复杂问题
aima ask "为什么我的模型推理很慢？"
```

---

## 相关文件

- `internal/cli/` - CLI 命令实现
- `cmd/aima/main.go` - 进程 bootstrap
- `cmd/aima/tooldeps_*.go` - CLI 与 MCP 共享执行路径的依赖装配

---

*最后更新：2026-08-02（增加 Engine ensure、rollback、离线导入和删除保护语义）*
