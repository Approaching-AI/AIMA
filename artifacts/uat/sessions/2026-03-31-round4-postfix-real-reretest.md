# Round-4 Postfix Real Re-retest

**日期**：2026-03-31  
**构建**：`v0.2-dev` / `develop` 本地修复构建（`160b8aa` 之后，含未提交修复）  
**范围**：只复测 2026-03-31 真实矩阵重跑后仍未收口的 `aibook` / `m1000`。

## 代码修复点

- `cmd/aima/main.go`
  - `run --no-pull` 在 failed 分支会二次刷新 `deploy status` / `deploy logs`，并按根因优先级提取失败摘要。
  - `OOM` / `KeyError` / `AssertionError` 会压过外层 `RuntimeError: Engine core initialization failed`。
- `internal/runtime/native.go`
  - 持久化 metadata 与当前进程状态合并时，继续保留更强失败信号。
  - `processMatchesMeta()` 现在允许安全的 interpreter prefix，例如 `/usr/bin/python3 /usr/local/bin/vllm ...` 与 metadata 中的 `/usr/local/bin/vllm ...` 视为同一 deployment。

## 实机复测

| 设备 | 命令 | 结果 | 结论 |
| --- | --- | --- | --- |
| `aibook` | `run qwen3-8b --no-pull` | FAIL，但最终直接返回 `KeyError: 'layers.18.mlp.down_proj.g_idx'` | `BUG-R4-DRU-011` 已修复；`run` 不再在模型仍在加载时误判失败，也不再只回 generic message |
| `aibook` | 并行 `deploy status qwen3-8b-vllm` | PASS，加载阶段显示 `phase=starting`；失败后再收敛到 `phase=failed` | shebang 包装的 `vllm` 进程不再被误判为 PID 不匹配 |
| `m1000` | `run qwen3-30b-a3b --no-pull` | PASS，返回 `http://127.0.0.1:8000` | 最新实机状态下真实 ready，`/health` 可达 |
| `m1000` | `deploy status qwen3-30b-a3b-vllm` + `/health` | PASS，`running / ready=true` | 当前现场已不再被代码 blocker 卡住 |

## 关键观察

- `aibook` 之前的“失败回传不及时”不是单一日志时序问题，根因是 `vllm` 作为 shebang Python 脚本启动时，`/proc/<pid>/cmdline` 首参数变成了 `/usr/bin/python3`，导致 AIMA 跨进程状态检查把仍在加载中的进程误判成“已退出”。
- 修复后，`aibook` 的 `run` 会一直等到真实加载失败，再把现场真实根因 `KeyError: 'layers.18.mlp.down_proj.g_idx'` 直接返回给用户。
- `m1000` 这轮在最新现场资源状态下完成了真实初始化，说明剩余不稳定性更像环境/资源竞争，而不是当前代码路径仍然错误。

## 当前剩余 blocker

- `amd395`：现场 `8080` 端口冲突。
- `aibook`：本地 GPTQ 资产仍不干净或不完整，真实失败为 `KeyError: 'layers.18.mlp.down_proj.g_idx'`。
