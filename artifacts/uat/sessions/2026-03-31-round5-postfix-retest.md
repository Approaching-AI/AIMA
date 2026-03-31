# Round-5 Postfix Retest

**日期**：2026-03-31  
**目标**：对 Round-5 里最关键的两个问题做修复后定点回归。  
**构建**：本地修复后重新构建 `/tmp/aima-uat`  
**环境**：`dev-mac` / Apple M4 / `native + llamacpp` / `serve --addr 127.0.0.1:6197`  

## 修复点

- `internal/runtime/native.go`
  - native deploy 新增端口冲突前置检查
  - 端口已被活跃 deployment 或其他进程占用时，直接失败返回，不再继续启动并进入假 ready
- `internal/ui/static/index.html`
  - Deployments 轮询从 `10s` 下调到 `2s`
  - 启动中轮询从 `3s` 调整到 `1s`

## 定点回归

### 1. 同端口换模型 redeploy

起始现场：

- `qwen3-4b` 在 `127.0.0.1:8080` 处于 `ready=true`
- endpoint 返回 `Qwen3-4B-Q4_K_M.gguf`

执行：

```bash
/tmp/aima-uat deploy qwen3-0.6b
```

结果：

- 命令快速失败
- 返回错误：

```text
deploy qwen3-0.6b: deploy: deploy Qwen3-0.6B-Q8_0: port 8080 already in use by deployment "qwen3-4b"
```

- `deploy list` 仍只有 `qwen3-4b`
- UI 也仍只显示 `qwen3-4b -> Ready: 127.0.0.1:8080`

结论：

- 之前的“新模型被误判成 ready，并和旧模型共享同一 endpoint”问题已收口。
- `UAT-DLU-03` 的核心 blocker 已修复。

### 2. 独立端口的新 deployment

执行：

```bash
/tmp/aima-uat deploy qwen3-0.6b --config port=8091
```

结果：

- `deploy status Qwen3-0.6B-Q8_0` 返回 `running / ready=true`
- `curl http://127.0.0.1:8091/v1/models` 返回 `Qwen3-0.6B-Q8_0.gguf`
- UI 最终正确显示：
  - `Qwen3-0.6B-Q8_0 -> Ready: 127.0.0.1:8091`
  - `qwen3-4b -> Ready: 127.0.0.1:8080`

结论：

- 修复后至少没有再出现 endpoint 身份错乱。
- 这条回归证明“真实 ready 显示”为正常。
- 但本轮没有重新完整证明 UI 一定能稳定捕获到 `starting/progress` 的每个短启动窗口；这一点仍需要后续更细的生命周期观测来继续压实。

证据：

- `output/playwright/round5-postfix-ui-ready.png`

## 最终结论

- **已修复**：切换模型 redeploy 时的假 ready / 假 endpoint 问题
- **已改善**：UI deploy 轮询频率
- **仍待继续观察**：极短启动期是否总能被 UI 捕获为 `starting/progress`
