# Switch-Model Redeploy UI UAT

**日期**：2026-03-31  
**目标**：单独验证“已有模型 A 正在运行时，重新 deploy 模型 B，UI 的 Deployments 状态是否精准、可信，且能显示真实进度”。  
**环境**：`dev-mac` / Apple M4 / `native + llamacpp` / 本地 `serve --addr 127.0.0.1:6195`  
**构建**：本地 worktree 构建 `/tmp/aima-uat`（含当前未提交改动）  

## UAT 场景

- 基线已有 deployment：`qwen3-4b`
- 基线 endpoint：`127.0.0.1:8080`
- 切换目标模型：`qwen3-0.6b`
- 关注点：
  - UI 是否在换模 deploy 期间展示真实 `starting / progress / ready / failed`
  - UI 展示的 `ready + endpoint` 是否和真实服务一致

## 基线

- `deploy list` 起始只有 1 个实例：
  - `qwen3-4b`
  - `phase=running`
  - `ready=true`
  - `address=127.0.0.1:8080`
- Web UI 打开后，Deployments 面板只显示 `qwen3-4b` ready。

证据：
- `output/playwright/switch-model-ui-before.yml`
- `output/playwright/switch-model-ui-before.png`

## 操作

执行：

```bash
/tmp/aima-uat deploy qwen3-0.6b
```

CLI 现场：

- 解析到 `Qwen3-0.6B-Q8_0 / llamacpp / native`
- 启动命令使用 `--port 8080`
- 命令约 5 秒返回
- 返回 JSON：

```json
{
  "engine": "llamacpp",
  "model": "Qwen3-0.6B-Q8_0",
  "name": "qwen3-0-6b-q8-0-llamacpp",
  "runtime": "native",
  "slot": "primary",
  "status": "deploying"
}
```

- 同一轮 CLI 日志还出现：
  - `health check passed name=Qwen3-0.6B-Q8_0`
  - `warming up engine name=Qwen3-0.6B-Q8_0 url=http://127.0.0.1:8080/v1/chat/completions`

## 实际观察

### 1. UI 没有展示可信的切换过程

- 没有观察到新模型的 `starting` 或进度条。
- UI 在下一次轮询后直接显示两个 deployment 都是 ready：
  - `Qwen3-0.6B-Q8_0 -> Ready: 127.0.0.1:8080`
  - `qwen3-4b -> Ready: 127.0.0.1:8080`

证据：
- `output/playwright/switch-model-ui-after.yml`
- `output/playwright/switch-model-ui-after.png`

页面文本抓取结果：

```text
Qwen3-0.6B-Q8_0
native 127.0.0.1:8080
Ready: 127.0.0.1:8080

qwen3-4b
native 127.0.0.1:8080
Ready: 127.0.0.1:8080
```

### 2. CLI / 状态源本身已经失真

`deploy list` 和 `deploy status` 同时把两个模型都标为：

- `phase=running`
- `ready=true`
- `address=127.0.0.1:8080`

也就是说，UI 这里不是“单独渲染错了”，而是忠实映射了一个已经不可信的后端状态。

### 3. endpoint 与模型身份不一致

对 UI/CLI 声称的 ready endpoint 连续请求：

```bash
curl http://127.0.0.1:8080/v1/models
```

连续 6 次都返回：

```text
Qwen3-4B-Q4_K_M.gguf
```

这和 UI/CLI 声称 `Qwen3-0.6B-Q8_0` 已经 ready 且可通过同一 endpoint 访问直接矛盾。

### 4. 新进程确实被拉起，但 readiness 语义仍然不可信

现场进程：

- 老进程：`qwen3-4b` 的 `llama-server --port 8080`
- 新进程：`Qwen3-0.6B-Q8_0` 的 `llama-server --port 8080`

新 deployment 日志最终也写到：

- `main: model loaded`
- `main: server is listening on http://0.0.0.0:8080`

这说明问题不是“新进程完全没起来”，而是：

- deploy 切换没有提供可信的单一目标 endpoint
- readiness / warmup 很可能在旧服务仍占用相同 endpoint 时就被提前判定成功
- UI 只会拿到“两个 ready 同地址”的假稳定态

## 结论

**结论：FAIL**

对“换模型重新 deploy 时，UI 需要精准、可信地反映 deploy 实际情况”这个目标，本轮不通过。

失败原因不是单一前端视觉问题，而是整条状态链路在切模场景下失真：

- UI 没有看到可信的 `starting / progress`
- UI 最终展示了两个不同模型同时 `ready` 且共享同一 endpoint
- 真实 endpoint 返回的模型身份与其中一个 ready deployment 不一致

所以从用户视角看，当前 UI 在“切换模型 redeploy”场景下**不可信**。

## 清理

- 本轮额外拉起的 `Qwen3-0.6B-Q8_0` 已通过：

```bash
/tmp/aima-uat undeploy Qwen3-0.6B-Q8_0
```

清理完成后，`deploy list` 恢复为仅 `qwen3-4b` ready。
