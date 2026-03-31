# gb10 Scenario Apply Smoke UAT

**机器**：`gb10`  
**日期**：2026-03-31  
**构建**：`v0.2-dev` / commit `3db1e64`  
**二进制**：`~/aima-scenario-uat-20260331/bin/aima`  
**二进制 SHA256**：`cc70ee7d9fedab14f2587816987ad4bacae2d6f3ac720ef4dbdf8031ed17f3e5`  
**数据目录**：`~/aima-scenario-uat-data-20260331`  
**目标**：验证 `openclaw-multi` 在目标硬件 `nvidia-gb10-arm64` 上是否能稳定解析，并确认现场资产是否足以支撑真实 `scenario.apply`。

## 执行命令

```bash
ssh gb10 'uname -a && whoami && pwd'
scp build/aima-linux-arm64 gb10:~/aima-scenario-uat-20260331/bin/aima

ssh gb10 '
  export AIMA_DATA_DIR=$HOME/aima-scenario-uat-data-20260331
  BIN=$HOME/aima-scenario-uat-20260331/bin/aima
  $BIN version
  $BIN hal detect
  $BIN engine plan
  $BIN deploy list
  $BIN scenario list
  $BIN scenario show openclaw-multi
  $BIN scenario apply openclaw-multi --dry-run
  $BIN model scan | rg "Qwen3.5|qwen3-tts|qwen3-asr|z-image"
'

ssh gb10 '
  ls -1 ~/.aima/models
  docker images --format "{{.Repository}}:{{.Tag}}" |
    rg "vllm-openai:qwen3_5-cu130|qwen3-tts-cuda-arm64:latest|vllm-asr-audio:latest|qujing-z-image:latest"
'

ssh gb10 '
  export AIMA_DATA_DIR=$HOME/aima-scenario-uat-data-20260331
  BIN=$HOME/aima-scenario-uat-20260331/bin/aima
  stdbuf -oL -eL "$BIN" scenario apply openclaw-multi 2>&1 |
    tee ~/aima-scenario-uat-20260331/openclaw-multi-apply-20260331-023521.log
'
```

## 结果

### 1. 目标硬件识别正确

- `hal detect` 正确识别：
- GPU = `NVIDIA GB10`
- GPU arch = `Blackwell`
- VRAM = `122504 MiB`
- runtime = `docker`
- 这意味着 `openclaw-multi` 的 target hardware `nvidia-gb10-arm64` 与现场硬件是一致的。

### 2. `openclaw-multi --dry-run` 在 gb10 上完全可解析

- 4 个 deployment 全部返回 `status=dry_run`，没有解析失败。
- 4 个 deployment 的 `fit_report.fit` 全部为 `true`。
- 解析结果与 scenario YAML 一致：
- `qwen3.5-9b -> vllm-nightly-blackwell -> docker`
- `qwen3-tts-0.6b -> qwen-tts-fastapi-cuda-blackwell -> docker`
- `qwen3-asr-1.7b -> vllm-nightly-audio-blackwell -> docker`
- `z-image -> z-image-diffusers -> docker`
- 本轮唯一统一 warning 是：
- `unified memory system has swap enabled (16383 MiB); high gmu may cause swap thrashing instead of clean OOM-kill`

### 3. 当前现场资产并不完整

- `deploy list` 里当前只有 `qwen3-8b-vllm` 是 `running/ready=true` 的正向样本。
- `~/.aima/models` 里可见目录：
- `qwen3-0.6b`
- `qwen3-4b`
- `qwen3-8b`
- `qwen3.5-35b-a3b`
- `model scan` 过滤后没有发现本 scenario 需要的：
- `qwen3.5-9b`
- `qwen3-tts-0.6b`
- `qwen3-asr-1.7b`
- `z-image`
- relevant images 现场只确认已有：
- `vllm/vllm-openai:qwen3_5-cu130`
- `qwen3-tts-cuda-arm64:latest`
- 当前没看到：
- `vllm-asr-audio:latest`
- `qujing-z-image:latest`

### 4. 真实 `scenario apply openclaw-multi` 已执行，但停止点比 orchestration 更早

- 我实际前台执行了 `scenario apply openclaw-multi`。
- 它没有立刻在 `scenario.apply` 框架层报错，而是进入了第一个 deployment：
- `qwen3.5-9b`
- `vllm-nightly-blackwell`
- 随后 AIMA 发现本地没有该模型，进入自动拉取。
- 首先尝试 `huggingface-cli download`，访问 `https://hf-mirror.com` 时命中了：
- `SSLError: UNEXPECTED_EOF_WHILE_READING`
- 随后自动回退到 HTTP repo 下载。
- 在我停止测试前，它已经完成：
- `config.json`
- `merges.txt`
- `model.safetensors-00001-of-00004.safetensors.partial` 写到约 `409M`
- 停止时 `~/aima-scenario-uat-data-20260331/models/qwen3.5-9b` 目录大小约 `413M`。
- 这次停止是我主动中断，退出码 `130`，不是 AIMA 自己收敛出的失败态。
- 在整个执行窗口里，没有新的 deployment 被创建，`deploy list` 仍只有现场原有的：
- `qwen3-8b-vllm` (`running/ready=true`)
- 一个历史失败的 `qwen3-coder-next-fp8-vllm-nightly`

### 5. 已确认但尚未真正走到的后续阻塞

- 现场 `8000` 已被现有 `qwen3-8b-vllm` 占用：
- `0.0.0.0:8000->8000/tcp`
- 而 `openclaw-multi` 的第一个 deployment 也配置为 `port: 8000`。
- `~/.openclaw/openclaw.json` 当前不存在。
- 因此即使前面 4 个 deployment 都 eventually 成功，最后的 `openclaw_sync` 也大概率会失败，除非现场先准备好 OpenClaw 配置。

## 判断

- 对 `scenario.apply` 来说，`gb10` 上的 dry-run 结果是正向的：
- target hardware 正确命中；
- 4 个 deployment 都能解析；
- 资源估算和 fit 判断没有自相矛盾。
- 真实 `apply` 也已经证明一件事：
- 它不会在进入前几秒就因为 scenario 结构或 target hardware 匹配而崩掉；
- 它真实会进入首个 deployment 的模型准备阶段。
- 但这还不等于今天已经证明了“真实 apply 经常 work”。
- 当前现场缺少至少两个 scenario 专用 engine image，且缺少多项 scenario 模型资产。
- 这次真实执行已经把测试性质变成：
- 下载链路 + 资产准备 + orchestration 的混合验证。
- 从当前现场看，最先暴露的真实停止点不是编排器，而是首次模型准备。
- 即使下载问题解决，后面还至少有两个高概率停止点：
- 端口 `8000` 冲突；
- `openclaw_sync` 缺少 `~/.openclaw/openclaw.json`

## 本轮结论

- 如果问题是“这条 feature 在目标硬件上是不是一上来就解析崩掉”，答案是否定的；`gb10` 上 dry-run 是通的，而且比本机 Apple M4 上可信得多。
- 如果问题是“今天我能不能下结论说它在 gb10 上真实 apply 已经稳定”，答案仍然是否定的。
- 现在已知的真实链路顺序是：
- 先卡 `qwen3.5-9b` 首次下载；
- 之后大概率撞 `8000` 端口冲突；
- 最后还可能撞 `openclaw_sync` 缺配置。
- 下一步若要继续，应该先做环境清理再重跑：
- 处理或避让现有 `8000` 服务；
- 预置 `~/.openclaw/openclaw.json` 或暂时去掉 `post_deploy`;
- 再决定是否让 AIMA 继续完成 `qwen3.5-9b` 的首次大文件下载。

## 追加复测：清掉 `8000` 现有服务后继续跑

### 执行动作

```bash
ssh gb10 '
  export AIMA_DATA_DIR=$HOME/aima-scenario-uat-data-20260331
  BIN=$HOME/aima-scenario-uat-20260331/bin/aima
  $BIN undeploy qwen3-8b-vllm
'

ssh gb10 '
  export AIMA_DATA_DIR=$HOME/aima-scenario-uat-data-20260331
  BIN=$HOME/aima-scenario-uat-20260331/bin/aima
  nohup bash -lc "stdbuf -oL -eL \"$BIN\" scenario apply openclaw-multi >> \
    ~/aima-scenario-uat-20260331/openclaw-multi-apply-resume-20260331-024208.log 2>&1" &
'
```

### 追加结果

- `qwen3-8b-vllm` 已被 AIMA 正常删除，`8000` 端口释放成功。
- 后台重跑后，AIMA **没有继续下载** `qwen3.5-9b`，而是直接把先前只有 `.partial` 文件的目录当成本地模型使用：
- `model path fallback: using alternative location original=/home/qujing/aima-scenario-uat-data-20260331/models/qwen3.5-9b`
- 这意味着中断下载留下的残缺目录会被 `scenario.apply` 误当成可部署资产。
- 之后流程继续往前推进：
- `qwen3.5-9b` 容器已启动，占用 `8000`
- `qwen3-tts-0.6b` 容器已启动并 `ready=true`，占用 `8002`
- `qwen3-asr-1.7b` 遇到 `vllm-asr-audio:latest` 不存在，本地日志明确报：
- `engine image not found locally and no registries configured`
- 即便如此，scenario 仍继续推进到了 `z-image`，并开始下载 `Tongyi-MAI/Z-Image`
- 当前后台 `scenario apply` 进程仍在运行，`z-image` 下载活跃。

### 新暴露的问题

- `qwen3.5-9b` 当前不是 healthy ready，而是 `starting`；其容器日志已经出现新的真实失败信号：
- `ValueError: The checkpoint you are trying to load has model type qwen3_5 but Transformers does not recognize this architecture`
- 这说明当前 `vllm/vllm-openai:qwen3_5-cu130` 这条链路至少在现场镜像/依赖版本上还没稳住。
- 组合起来，`openclaw-multi` 现在在 gb10 上至少有 4 个独立风险点：
- 中断下载残留目录会被误当成本地可用模型；
- `qwen3.5-9b` 当前镜像内 Transformers / checkpoint 兼容性存在问题；
- `qwen3-asr-1.7b` 缺少 `vllm-asr-audio:latest`；
- `openclaw_sync` 仍缺 `~/.openclaw/openclaw.json`

## 修复后回归

**日期**：2026-03-31  
**验证构建**：本地修复后的 `linux/arm64` 二进制（未覆盖旧远端文件，使用 `aima-fixed` 直接执行）

### 已验证修复

- `findModelDir()` 不再把可读但不兼容的 exact 模型目录误判为 fallback。
- 在保留 `qwen3.5-9b` partial 目录的前提下，用修复后二进制重新执行：

```bash
timeout 20s /home/qujing/aima-scenario-uat-20260331/bin/aima-fixed scenario apply openclaw-multi
```

- 新日志行为已经改变为：
- `model not found locally, auto-pulling model=qwen3.5-9b`
- `resuming HuggingFace download via HTTP repo=Qwen/Qwen3.5-9B`
- `skipping already downloaded progress=[1/11] file=config.json`
- `skipping already downloaded progress=[2/11] file=merges.txt`
- 这说明它已经从“误用残缺目录直接部署”修成了“识别本地资产不可用并进入续传”。

### 正式路径追加验证

- 修复后二进制已正式替换到：
- `/home/qujing/aima-scenario-uat-20260331/bin/aima`
- 原旧二进制保留为：
- `/home/qujing/aima-scenario-uat-20260331/bin/aima.pre-fix-20260331`
- 用正式路径执行 `timeout 20s ... aima scenario apply openclaw-multi`，结果与 `aima-fixed` 一致：
- 先尝试 `hf-mirror.com`
- mirror 返回 `EOF` 后自动回退到 `https://huggingface.co`
- 随后继续 HTTP 续传 `model.safetensors-00001-of-00004.safetensors`

- 之后我把正式路径的 `scenario apply openclaw-multi` 后台挂起继续运行：
- PID `688161` / `688562`
- 日志：`/home/qujing/aima-scenario-uat-20260331/openclaw-multi-apply-fixed-20260331-030916.log`
- 30 秒后现场状态：
- 进程仍在运行
- `qwen3.5-9b` 目录从约 `422M` 增长到 `426M`
- `deploy list` 里仍没有新 deployment 被创建
- 这说明新的 `startup_order` + fail-fast 路径已经生效：
- 当前只处理第一个 deployment 的资产准备
- 不会像旧行为那样在首模型未 ready 时继续推 TTS / ASR / Z-Image

### 本次修复未覆盖的现场问题

- `qwen3-asr-1.7b` 仍缺 `vllm-asr-audio:latest` 镜像。
- `openclaw_sync` 仍需要 `~/.openclaw/openclaw.json`。
- `qwen3.5-9b` 的 `trust_remote_code` 与 `startup_order` 已在 catalog 修复，但还需要下一轮真机完整运行来验证是否足以解决 `qwen3_5` 架构识别失败。
