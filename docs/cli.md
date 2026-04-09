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
aima serve                                # 启动 AIMA 服务器
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
aima engine plan                          # 结合当前硬件给出引擎推荐
aima engine pull [engine]                 # 拉取引擎镜像
aima engine import <path>                 # 从 OCI tar 导入
aima engine remove <engine>               # 删除引擎镜像
```

### 知识、目录与基准

```bash
aima knowledge list                       # 列出知识资产
aima knowledge resolve <model>            # 解析最优配置
aima knowledge export [--output]          # 导出知识
aima knowledge import <path>              # 离线导入知识
aima knowledge sync                       # 与中心服务同步知识
aima knowledge validate                   # 校验预测与实际性能
aima catalog override <kind> <name> <yaml-file>  # 写入 overlay catalog
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
```

`aima askforhelp` 默认连接 `https://aimaserver.com`，运行时自动归一化为 `/api/v1` 支持接口。
如需覆盖，可使用 `--endpoint`，或通过 `aima config set support.endpoint <url>` / `AIMA_SUPPORT_ENDPOINT` 持久化配置。

### 集成与运维

```bash
aima config get <key>                     # 读取配置
aima config set <key> <value>             # 修改配置
aima discover                             # 发现局域网中的 AIMA 服务
aima fleet devices                        # 列出局域网设备
aima fleet info <device-id>               # 查看远端设备详情
aima fleet tools <device-id>              # 列出远端工具
aima fleet exec <device-id> <tool>        # 在远端执行 MCP 工具
aima mcp                                  # 通过 stdio 启动 MCP 服务
aima tui                                  # 打开终端仪表盘
aima version                              # 查看版本信息
```

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

*最后更新：2026-04-01 (对齐当前命令面，移除已不存在的 start/stop/chat 示例)*
