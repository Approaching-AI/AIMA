# Round-4 Deploy-Run-UI Active Bugs

**日期**：2026-03-30  
**构建**：`v0.2-dev` / 基于 `develop` `9a527bf` 的本地 UAT 构建（含未提交修复）  
**范围**：已有 knowledge + 真实已有资产 + `deploy` / `run` / `serve/ui`。  
**状态**：本地 closeout 曾清零；2026-03-31 对已推送 `origin/develop` `160b8aa` 的真实矩阵重跑后一度重新打开 1 个 active product bug 和 3 个现场 blocker。随后在同日追加修复并完成真机复测，当前已无 active product bug，剩余 2 个现场 blocker。

## 当前 active bug

- 无。

## 当前现场 blocker

- `amd395`：`qwen3-30b-a3b/llamacpp/native` 仍被现场 `8080` 端口占用阻塞，但现在会快速、诚实地收敛为 `failed`，并明确报 `main: exiting due to HTTP server error`。
- `aibook`：不再误用 `/opt/mt-ai/llm/models/gptq-Qwen3-8B` 这个不兼容目录，但当前 `~/.aima/models/qwen3-8b` 仍在权重加载阶段报 `KeyError: 'layers.18.mlp.down_proj.g_idx'`，说明现场 GPTQ 资产本身仍不干净或不完整。

## 本轮已关闭

- `BUG-R4-DRU-005`：`deploy` 对已有 ready / starting 实例已改为幂等复用入口
- `BUG-R4-DRU-006`：移动端 Dashboard 首屏已把 `Deployments` 提升为第一优先级
- `BUG-R4-DRU-007`：`dev-mac` native Unix launch 已改为 detached session / process group
- `BUG-R4-DRU-008`：半下载 safetensors 目录不再被当成可部署模型；兼容 alias 本地资产可被发现
- `BUG-R4-DRU-009`：量化不明确或污染的 safetensors 目录不再被当成 GPTQ 可部署模型
- `BUG-R4-DRU-010`：`run --no-pull` 对无效本地模型目录会更早失败返回，不再拖到深层 engine init
- `BUG-R4-DRU-011`：`aibook` 上 shebang 包装的 `vllm` 进程不再因 `/proc/<pid>/cmdline` 与 metadata 首参数不一致而被误判为“process exited before readiness”；`run qwen3-8b --no-pull` 现已等待到真实加载失败，并直接返回 `KeyError: 'layers.18.mlp.down_proj.g_idx'`

## 本轮清掉的 blocker

- `dev-mac`：`llama.cpp` 启动命令不再错误携带 `--quantization`
- `amd395`：native 失败实例不再长期卡在 `running, ready=false`
- `run --no-pull`：失败摘要不再只剩空的 `deployment failed:`
- `aibook`：不再误用 `/opt/mt-ai/llm/models/gptq-Qwen3-8B` 这个量化不匹配的预装目录
- `m1000`：native warmup 不再因为别的 8000 端口服务而给出假 ready
- `m1000`：最新同日真机复测已真实 `ready=true`，`/health` 可用；之前的 `MUSA out of memory` / memory profiling 失败未再复现为当前代码 blocker
- `dev-mac`：Unix native launch 已改为 detached session / process group，`run qwen3-4b --no-pull` 返回后服务不会再随原 shell/session 一起退出。复测见 `sessions/2026-03-30-round4-devmac-detach-reretest.md`

## 关闭依据

- 阻塞点修复复测：`sessions/2026-03-30-round4-blocker-fix-retest.md`
- `dev-mac` detached launch 定点复测：`sessions/2026-03-30-round4-devmac-detach-reretest.md`
- 本地最终 closeout 回归：`sessions/2026-03-30-round4-final-closeout-local-reretest.md`
- 已推送构建的真实矩阵重跑：`sessions/2026-03-31-round4-real-matrix-rerun-160b8aa.md`
- 追加代码修复后的真机复测：`sessions/2026-03-31-round4-postfix-real-reretest.md`
