# Round-5 Deploy Lifecycle UI Trust Session

**日期**：2026-03-31  
**计划**：`artifacts/uat/plan/ROUND-5-DEPLOY-LIFECYCLE-UI-TRUST-20260331.md`  
**环境**：`dev-mac` / Apple M4 / `native + llamacpp` / `serve --addr 127.0.0.1:6196`  
**构建**：本地 worktree 构建 `/tmp/aima-uat`（含当前未提交改动）  
**目标**：验证 UI 对 deploy 全生命周期的追踪是否足够精准、可信。  

## 场景结果

| 场景 | 结果 | 结论 |
| --- | --- | --- |
| `UAT-DLU-01` 基线 ready 态是否可信 | PASS | UI 的 `qwen3-4b` ready 与 CLI 一致，`127.0.0.1:8080/v1/models` 返回 `Qwen3-4B-Q4_K_M.gguf`，endpoint 身份可信 |
| `UAT-DLU-02` 新 deployment 的启动过程是否被 UI 捕获 | PARTIAL | 新 deployment 最终 ready 且 endpoint 身份正确，但 UI 没有捕获到 `starting/progress`，直接从“无实例”跳到 ready |
| `UAT-DLU-03` 已有模型 A 运行时切换 deploy 到模型 B，UI 是否仍可信 | FAIL | UI 前 12 秒完全没显示新 deployment；晚些轮询后又把新旧两个 deployment 同时显示成 `Ready: 127.0.0.1:8080`，但真实 endpoint 始终返回旧模型 |

## UAT-DLU-01 基线 ready 态是否可信

起始现场：

- `deploy list` 只有 `qwen3-4b`
- `phase=running`
- `ready=true`
- `address=127.0.0.1:8080`

UI 现场：

- Deployments 面板只显示 `qwen3-4b`
- 显示 `Ready: 127.0.0.1:8080`

endpoint 实测：

```bash
curl http://127.0.0.1:8080/v1/models
```

返回：

```text
Qwen3-4B-Q4_K_M.gguf
```

判定：

- 这一条 ready 是可信的，UI 与 CLI、endpoint 三者一致。

证据：

- `output/playwright/round5-dlu-01-baseline.yml`
- `output/playwright/round5-dlu-01-baseline.png`

## UAT-DLU-02 新 deployment 的启动过程是否被 UI 捕获

操作：

```bash
/tmp/aima-uat deploy qwen3-0.6b --config port=8091
```

CLI 现场：

- 解析到 `Qwen3-0.6B-Q8_0 / llamacpp / native`
- 命令使用 `--port 8091`
- 返回 `status=deploying`

UI 时间点：

- `t+2s`：UI 仍只显示旧的 `qwen3-4b`
- `t+6s`：UI 已直接显示 `Qwen3-0.6B-Q8_0 -> Ready: 127.0.0.1:8091`
- `t+12s`：仍为 ready

关键观察：

- 这条 deployment 最终是可信 ready：
  - `deploy.status Qwen3-0.6B-Q8_0` 为 `running / ready=true`
  - `curl http://127.0.0.1:8091/v1/models` 返回 `Qwen3-0.6B-Q8_0.gguf`
- 但 UI 生命周期追踪不完整：
  - 没看到 `starting`
  - 没看到进度条
  - 直接从“没有新实例”跳到 ready

判定：

- 结果 `PARTIAL`
- 状态最终可信，但过程追踪不充分。

证据：

- `output/playwright/round5-dlu-02-tplus2.yml`
- `output/playwright/round5-dlu-02-tplus6.yml`
- `output/playwright/round5-dlu-02-tplus12.yml`
- `output/playwright/round5-dlu-02-final.png`

## UAT-DLU-03 已有模型 A 运行时切换 deploy 到模型 B，UI 是否仍可信

起始现场：

- `qwen3-4b` 仍在 `127.0.0.1:8080`
- endpoint 身份为 `Qwen3-4B-Q4_K_M.gguf`

操作：

```bash
/tmp/aima-uat deploy qwen3-0.6b
```

CLI 现场：

- 新 deployment 使用默认端口 `8080`
- deploy 刚返回时就打印：
  - `health check passed name=Qwen3-0.6B-Q8_0`
  - `warming up engine name=Qwen3-0.6B-Q8_0 url=http://127.0.0.1:8080/v1/chat/completions`

UI 时间点：

- `t+2s`：只显示旧的 `qwen3-4b`
- `t+6s`：仍只显示旧的 `qwen3-4b`
- `t+12s`：仍只显示旧的 `qwen3-4b`
- 更晚轮询后：UI 同时显示
  - `Qwen3-0.6B-Q8_0 -> Ready: 127.0.0.1:8080`
  - `qwen3-4b -> Ready: 127.0.0.1:8080`

CLI / 状态源：

- `deploy.list` 也同时把两个 deployment 都标为
  - `phase=running`
  - `ready=true`
  - `address=127.0.0.1:8080`

endpoint 实测：

```bash
for i in 1 2 3; do
  curl http://127.0.0.1:8080/v1/models
done
```

连续返回：

```text
Qwen3-4B-Q4_K_M.gguf
Qwen3-4B-Q4_K_M.gguf
Qwen3-4B-Q4_K_M.gguf
```

关键观察：

- UI 在 deploy 早期完全漏掉了新 deployment
- UI 后续又把两个不同 deployment 同时显示成共享同一 ready endpoint
- 真实 endpoint 身份只证明旧模型仍在服务
- 所以新模型的 ready 是假阳性

判定：

- 结果 `FAIL`
- 这里不是单一前端渲染问题，而是“状态源已失真 + UI 忠实映射失真状态”的组合问题。

证据：

- `output/playwright/round5-dlu-03-tplus2.yml`
- `output/playwright/round5-dlu-03-tplus6.yml`
- `output/playwright/round5-dlu-03-tplus12.yml`
- `output/playwright/round5-dlu-03-late.yml`
- `output/playwright/round5-dlu-03-late.png`

## 总结

Round-5 证明了两件事：

1. UI 在“稳定 ready 且 endpoint 独立”的场景下可以可信。
2. UI 在“启动过程追踪”和“切换模型 redeploy”这两个更关键的生命周期场景下还不够可信。

具体来说：

- `UAT-DLU-02` 暴露出：**UI 会漏掉短启动期**
- `UAT-DLU-03` 暴露出：**UI 会在状态源失真后，把假 ready 如实展示给用户**

因此，如果验收标准是“UI 要对 deploy 全生命周期做到精准、可信追踪”，当前状态仍**不通过**。

## 清理

本轮额外拉起的 `Qwen3-0.6B-Q8_0` 已在每个场景结束后清理，结束时现场恢复为：

- 仅 `qwen3-4b` 保持 ready
- `127.0.0.1:8080/v1/models` 返回 `Qwen3-4B-Q4_K_M.gguf`
