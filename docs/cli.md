# CLI Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的命令行接口。

## 设计原则

CLI 命令是 MCP 工具的人类友好包装。
`aima deploy qwen3-8b` 内部调用 `model.scan` → `engine.scan` → `knowledge.resolve`
→ `knowledge.generate_pod` → `deploy.apply`。

CLI 永不实现 MCP 工具之外的逻辑——确保 Agent 和人类走同一条代码路径。

---

## 命令列表

### 初始化与生命周期

```bash
aima init                                 # 安装+配置基础设施栈 (K3S/HAMi)
aima start                                # 启动 AIMA (检查 K3S, 启动 MCP+HTTP)
aima stop                                 # 停止所有服务
```

### 部署 (最常用)

```bash
aima deploy <model> [--engine] [--slot]   # 部署模型 (知识自动匹配)
aima undeploy <name>                      # 停止部署
aima status                               # 系统状态 (硬件+部署+资源+功耗)
```

### 推理快捷方式

```bash
aima chat <model> "message"               # 快速对话
```

### 模型管理

```bash
aima model scan                           # 扫描本地模型
aima model list                           # 列出已注册模型
aima model info <name>                    # 获取模型详细信息
aima model pull <model>                   # 下载模型 (断点续传)
aima model import <path>                  # 从本地路径/USB 导入
aima model remove <name>                   # 注销模型
aima model remove --delete-files <name>     # 删除模型记录并删除文件
```

### 引擎管理

```bash
aima engine scan                          # 扫描本地引擎镜像
aima engine list                          # 列出可用引擎
aima engine pull <engine>                 # 拉取引擎镜像
aima engine import <path>                 # 从 OCI tar 导入
aima engine remove <engine>               # 删除引擎镜像
```

### 知识管理

```bash
aima knowledge list                       # 列出知识资产
aima knowledge resolve <model>            # 解析最优配置
aima knowledge sync [--push|--pull]       # 同步社区知识 (需联网)
aima knowledge import <path>              # 离线导入知识包
aima knowledge export [--output]          # 导出知识 (供离线传递)
```

### Agent

```bash
aima ask "指令"                           # 让 Agent 执行任务 (自动路由 L3a/L3b)
aima ask --local "指令"                   # 强制 Go Agent (L3a)
aima ask --deep "指令"                    # 强制 ZeroClaw (L3b)
aima ask --session <id> "指令"            # 继续 ZeroClaw 会话
aima agent install                        # 安装 ZeroClaw
aima agent status                         # 查看 Agent 状态
```

### 基准测试

```bash
aima benchmark run --model <name>         # 在线基准测试 (TTFT/TPOT/吞吐量)
  --endpoint <url>                        # 指定推理 endpoint (默认自动检测)
  --concurrency <n>                       # 并发数 (默认 1)
  --requests <n>                          # 请求数 (默认 10)
  --max-tokens <n>                        # 最大输出 token 数 (默认 256)
  --input-tokens <n>                      # 输入长度 (默认 128)
  --rounds <n>                            # 测量轮数 (默认 1, 多轮提高统计显著性)
  --min-output-ratio <0-1>                # 最小输出比例 (低于阈值自动重试)
  --max-retries <n>                       # 每请求重试次数 (默认 0)
  --warmup <n>                            # 预热请求数 (默认 2)
  --no-save                               # 不保存到知识库
aima benchmark matrix --model <name>      # 矩阵测试 (多组参数组合)
  --concurrency 1,4,8                     # 逗号分隔的并发级别
  --input-tokens 128,1024                 # 逗号分隔的输入长度
  --max-tokens 128,512                    # 逗号分隔的输出长度
  --endpoint <url>                        # 指定推理 endpoint
  --rounds <n>                            # 每组合测量轮数
aima benchmark record                     # 手动记录性能数据
aima benchmark list                       # 查询历史测试结果
```

### 配置

```bash
aima config [key] [value]                 # 读写配置
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
# 知识自动匹配引擎和配置
aima deploy qwen3-8b

# 查看状态
aima status

# 快速对话
aima chat qwen3-8b "你好"
```

### 扫描并部署本地模型

```bash
# 扫描本地模型
aima model scan

# 部署扫描到的模型
aima deploy ./models/glm-4-9b-chat
```

### Agent 查询

```bash
# 让 Agent 回答简单问题 (Go Agent)
aima ask --local "我有什么 GPU?"

# 让 Agent 进行复杂推理 (ZeroClaw)
aima ask --deep "为什么我的模型推理很慢？"
```

---

## 相关文件

- `internal/cli/` - CLI 命令实现
- `cmd/aima/main.go` - 入口点

---

*最后更新：2026-03-05*
