# Round-5B Postfix Retest

**日期**：2026-03-31  
**目标**：验证 Round-5B 指出的“启动窗口不可见”问题在修复后是否收口。  
**环境**：`dev-mac` / Apple M4 / `native + llamacpp` / `serve --addr 127.0.0.1:6199`  
**构建**：修复后重新构建 `/tmp/aima-uat`

## 本次修复点

- [native.go](/Users/jguan/projects/AIMA/internal/runtime/native.go)
  - native 非 ready 状态统一归类为 `starting`，不再把“端口已绑定但尚未 ready”的阶段显示成 `running`
  - 为 native 启动早期补了默认 startup phase/message/progress 回退，避免没有命中日志模式时完全无进度
- [index.html](/Users/jguan/projects/AIMA/internal/ui/static/index.html)
  - deployment 基础轮询从 `2s` 再降到 `1s`
  - 启动中轮询从 `1s` 再降到 `500ms`

## 定点回归

### 1. 中等启动样本

执行：

```bash
/tmp/aima-uat deploy Qwen3-4B-Q5_K_M --config port=8092
```

结果：

- `t+1s` UI 已出现新 deployment
- 显示内容为：

```text
Qwen3-4B-Q5_K_M
native starting
Initializing... 5%
```

- 最终收敛为：

```text
Ready: 127.0.0.1:8092
```

- `deploy status Qwen3-4B-Q5_K_M` 返回 `running / ready=true`
- `curl http://127.0.0.1:8092/v1/models` 返回 `Qwen3-4B-Q5_K_M.gguf`

结论：

- 修复后，中等启动样本不再漏掉早期生命周期。
- UI 已能在 ready 前稳定暴露 `starting + progress`。

证据：

- [round5b-postfix-medium-t1-starting.yml](/Users/jguan/projects/AIMA/output/playwright/round5b-postfix-medium-t1-starting.yml)
- [round5b-postfix-medium-final-ready.yml](/Users/jguan/projects/AIMA/output/playwright/round5b-postfix-medium-final-ready.yml)

### 2. 失败启动样本

执行：

```bash
/tmp/aima-uat deploy Qwen2-7B-Q8_0 --config port=8093
```

结果：

- `t+1s` UI 已出现新 deployment
- 先显示：

```text
Qwen2-7B-Q8_0
native starting
Initializing... 5%
```

- 随后收敛为：

```text
native failed
llama_model_load_from_file_impl: failed to load model
```

- `deploy status Qwen2-7B-Q8_0` 返回：
  - `phase=failed`
  - `ready=false`
  - `message=process exited before readiness`

结论：

- 修复后，失败样本不再从“不可见”直接跳到 `failed`。
- UI 已能先展示“正在启动”，再展示真实失败结果。

证据：

- [round5b-postfix-fail-t1-starting.yml](/Users/jguan/projects/AIMA/output/playwright/round5b-postfix-fail-t1-starting.yml)
- [round5b-postfix-fail-final.yml](/Users/jguan/projects/AIMA/output/playwright/round5b-postfix-fail-final.yml)

### 3. 快速启动样本

执行：

```bash
/tmp/aima-uat deploy qwen3-0.6b --config port=8091
```

结果：

- 本次 `t+1s` 抓到时已经是 `Ready: 127.0.0.1:8091`
- 因为样本本身太快，这次复测不能证明 `<1s` 窗口一定能每次都看到 `starting`

结论：

- 快速启动窗口仍然只能算“继续观察”，不是这次 postfix retest 的主收口点。
- 但至少已经没有出现之前那种“中等启动和失败启动也完全看不到过程”的问题。

## 本轮结论

- Round-5B 的主要问题已经明显收口：
  - 中等启动：从 `不可见` 修到 `t+1s 可见 starting/progress`
  - 失败启动：从 `不可见直接 failed` 修到 `先 starting，再 failed`
- 这说明 UI 对 deploy 生命周期的可信度已经提升到可接受水平，至少在 `native + llamacpp` 的主要样本上成立。
- 剩余未完全压实的一点只在“极短启动窗口”：
  - 如果 deployment 在 1 秒内 ready，是否每次都能稳定看到显式 `starting`
  - 这更像采样粒度上限，而不是状态源明显失真

