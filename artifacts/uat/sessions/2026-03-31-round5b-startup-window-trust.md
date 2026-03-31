# Round-5B Startup Window Trust

**日期**：2026-03-31  
**目标**：补充验证 deploy 启动窗口里的 UI 可见性与可信度，重点看 `starting / progress / failed-before-ready`。  
**计划**：[ROUND-5B-DEPLOY-STARTUP-WINDOW-TRUST-20260331.md](../plan/ROUND-5B-DEPLOY-STARTUP-WINDOW-TRUST-20260331.md)  
**环境**：`dev-mac` / Apple M4 / `native + llamacpp` / `serve --addr 127.0.0.1:6198`  
**基线**：原有 `qwen3-4b -> Ready: 127.0.0.1:8080`

## 结果总览

- `UAT-DLU-04` 快速启动样本：`FAIL`
- `UAT-DLU-05` 中等启动样本：`PARTIAL`
- `UAT-DLU-06` 失败启动样本：`PARTIAL`

本轮结论不是“UI 完全不可信”，而是更具体：

- 最终 `ready` 和最终 `failed` 在本轮都能对上真实状态源。
- 但 deploy 启动前半段仍然明显缺失，UI 没有稳定展示 `starting`、进度条、ETA 或明确的启动消息。
- 三个样本里都没有观察到真正可用的 progress 追踪；最多只看到一个晚出现的 `native running` 占位，甚至失败样本在失败前连这个占位都没有。

## UAT-DLU-04 快速启动样本

执行：

```bash
/tmp/aima-uat deploy qwen3-0.6b --config port=8091
```

观察：

- `t+1s / t+2s / t+3s`
  - UI 仍只显示旧 deployment `qwen3-4b`
  - 新 deployment 完全不可见
- `t+5s`
  - UI 首次出现 `Qwen3-0.6B-Q8_0`
  - 但只有 `native running`
  - 没有 `starting`
  - 没有进度条
  - 没有 ETA
  - 没有 endpoint
- 最终
  - UI 显示 `Ready: 127.0.0.1:8091`
  - `deploy status Qwen3-0.6B-Q8_0` 返回 `running / ready=true`
  - `curl http://127.0.0.1:8091/v1/models` 返回 `Qwen3-0.6B-Q8_0.gguf`

判定：

- 最终 ready 是真的。
- 但快速启动阶段并没有被 UI 精准追踪。
- 按本轮标准，`快速启动可见` 不通过。

证据：

- `output/playwright/round5b-dlu-04-before.png`
- `output/playwright/round5b-dlu-04-t1.yml`
- `output/playwright/round5b-dlu-04-t2.yml`
- `output/playwright/round5b-dlu-04-t3.yml`
- `output/playwright/round5b-dlu-04-t5.yml`
- `output/playwright/round5b-dlu-04-final.yml`
- `output/playwright/round5b-dlu-04-final.png`

## UAT-DLU-05 中等启动样本

执行：

```bash
/tmp/aima-uat deploy Qwen3-4B-Q5_K_M --config port=8092
```

观察：

- `t+1s / t+3s`
  - UI 仍只显示旧 deployment `qwen3-4b`
- `t+5s / t+8s`
  - UI 出现 `Qwen3-4B-Q5_K_M`
  - 状态只有 `native running`
  - 依旧没有 `starting`
  - 没有进度条
  - 没有 ETA
- `t+12s`
  - UI 收敛为 `Ready: 127.0.0.1:8092`
  - `deploy status Qwen3-4B-Q5_K_M` 返回 `running / ready=true`
  - `curl http://127.0.0.1:8092/v1/models` 返回 `Qwen3-4B-Q5_K_M.gguf`

判定：

- UI 在中等时长启动里总算能在 ready 前看到新 deployment。
- 但它出现得偏晚，而且仍然没有提供进度信息。
- 因此这条只能算 `PARTIAL`，不能叫“精准/可信的全生命周期追踪”。

证据：

- `output/playwright/round5b-dlu-05-t1.yml`
- `output/playwright/round5b-dlu-05-t3.yml`
- `output/playwright/round5b-dlu-05-t5.yml`
- `output/playwright/round5b-dlu-05-t8.yml`
- `output/playwright/round5b-dlu-05-t12.yml`

## UAT-DLU-06 失败启动样本

执行：

```bash
/tmp/aima-uat deploy Qwen2-7B-Q8_0 --config port=8093
```

观察：

- deploy 命令没有立即报错，而是返回 `status=deploying`
- `t+0s / t+2s`
  - UI 仍只显示旧 deployment `qwen3-4b`
  - 新 deployment 不可见
- `t+5s / t+8s`
  - UI 出现 `Qwen2-7B-Q8_0`
  - 已直接是 `native failed`
  - 并显示模型加载失败摘要：

```text
llama_model_load_from_file_impl: failed to load model
```

- `deploy status Qwen2-7B-Q8_0` 返回：
  - `phase=failed`
  - `ready=false`
  - `message=process exited before readiness`
  - `error_lines` 与 UI 摘要一致
- 不存在可用 ready endpoint

判定：

- 失败结果本身是可信的，UI 和状态源一致。
- 但失败前没有任何可见的启动过程，也没有过渡态。
- 因此这条不是 `FAIL`，但也只能算 `PARTIAL`。

证据：

- `output/playwright/round5b-dlu-06-t0.yml`
- `output/playwright/round5b-dlu-06-t2.yml`
- `output/playwright/round5b-dlu-06-t5-failed.yml`
- `output/playwright/round5b-dlu-06-t8-failed.yml`

## 本轮结论

- Round-5 修掉了最危险的“假 ready / 假 endpoint”之后，UI 的最终态可信度明显提升。
- 但如果标准是“deploy 全生命周期需要非常精准、可信地追踪过程”，这一轮还不能通过。
- 当前最大缺口不是最终态，而是启动窗口：
  - 新 deployment 经常晚几秒才出现
  - `starting` 基本看不到
  - progress / ETA 在本轮三个样本里都没有出现
  - 失败前的中间过程也几乎不可见

## 建议后续修复方向

1. 把 deployment 创建后的早期阶段立即暴露到状态源，不要等到更晚的 runtime 轮询结果才出现在 UI。
2. 给 native deploy 提供可持续刷新的启动阶段字段，而不是只有最终 `running/failed`。
3. 让 UI 把“实例已创建但尚未 ready”的阶段稳定渲染成明确状态，不要仅靠一个含糊的 `native running`。

