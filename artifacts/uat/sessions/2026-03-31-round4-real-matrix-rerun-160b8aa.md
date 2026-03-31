# Round-4 Real Matrix Re-run After Push

**日期**：2026-03-31  
**构建**：`v0.2-dev` / commit `160b8aa`  
**分支**：`origin/develop`  
**方法**：从已推送构建重新交叉编译四个平台二进制，统一分发到可达设备，再按 Round-4 主链路执行 `version`、`hal detect`、`engine plan`、`deploy --dry-run`、`deploy`、`deploy status`、`deploy logs`、`run --no-pull`。原始日志保留在本机临时目录 `/tmp/aima-real-matrix-160b8aa-20260331`。

## Reachability Barrier

| 设备 | 结果 | 说明 |
| --- | --- | --- |
| `dev-mac` | REACHABLE | 本机直接执行 |
| `test-win` | REACHABLE | `jguan@100.114.25.35` / Windows 11 / RTX 4060 |
| `gb10` | REACHABLE | `gb10` / Ubuntu / GB10 |
| `linux-1` | REACHABLE | `linux-1` / Ubuntu / 2×4090 |
| `amd395` | REACHABLE | `amd395` / Ubuntu / AMD Strix Halo |
| `m1000` | REACHABLE | `m1000` / Ubuntu / Moore Threads M1000 |
| `aibook` | REACHABLE | `aibook@100.106.164.54` / Ubuntu / M1000 SoC |
| `hygon` | UNREACHABLE | SSH `Permission denied (publickey)` |
| `qjq2` | UNREACHABLE | 当前环境无法解析主机名 `qjq2` |
| `metax-n260` | UNREACHABLE | `100.94.119.128:22` 连接超时 |

## 第一层基线

- `dev-mac`：`deploy --dry-run` 稳定解析到 `qwen3-4b / llamacpp / native`。
- `test-win`：`deploy --dry-run` 稳定解析到 `qwen3-8b / llamacpp / native`。
- `gb10`：`deploy --dry-run` 稳定解析到 `qwen3-8b / vllm / docker`。
- `linux-1`：`deploy --dry-run` 稳定解析到 `qwen3-4b / vllm / docker`，并自动把 `gpu_memory_utilization` 下调到 `0.10`。
- `amd395`：`deploy --dry-run` 稳定解析到 `qwen3-30b-a3b / llamacpp / native`。
- `m1000`：`engine plan` 已稳定暴露 `vllm-musa`；`deploy --dry-run` 解析到 `qwen3-30b-a3b / vllm / native`，且会忽略被污染的 `bf16` 同名目录。
- `aibook`：`deploy --dry-run` 解析到 `qwen3-8b / vllm / native`，并明确忽略 `/opt/mt-ai/llm/models/gptq-Qwen3-8B` 这个 `bf16` 伪装目录。

## 真实矩阵

| 设备 | `deploy` | `deploy status` | `run --no-pull` | 结论 |
| --- | --- | --- | --- | --- |
| `dev-mac` | PASS，重新拉起 `qwen3-4b-llamacpp` | PASS，`running / ready=true` | PASS，直接返回 `http://127.0.0.1:8080` | detached launch 修复在真实本机成立 |
| `test-win` | PASS，直接复用现有 ready 实例并返回 `status=ready` | PASS，`running / ready=true` | PASS，直接返回 `http://127.0.0.1:8080` | `deploy` 幂等复用在 Windows 真实成立 |
| `gb10` | PASS，直接复用现有 `qwen3-8b-vllm` | PASS，`running / ready=true` | PASS，直接返回 `http://127.0.0.1:8000` | Docker ready 复用链路稳定 |
| `linux-1` | PASS，直接复用现有 `qwen3-4b-vllm` | PASS，`running / ready=true` | PASS，直接返回 `http://127.0.0.1:8000` | 主链路稳定；ready 复用成立 |
| `amd395` | PARTIAL，会重新发起 native deploy，但很快因端口冲突失败 | FAIL，`phase=failed`，明确报 `deployment metadata is stale; port is in use by another process` | FAIL，快速返回 `deployment failed: main: exiting due to HTTP server error` | 产品语义已修正为诚实失败，现场仍被 `8080` 冲突阻塞 |
| `m1000` | PARTIAL，alias 发现已生效，真实落到 `Qwen3-30B-A3B-GPTQ-Int4` | FAIL，`phase=failed`，vLLM-MUSA engine init 失败 | FAIL，最终返回 `RuntimeError: Engine core initialization failed` | 旧的“半下载目录/错目录”问题已修复，但当前 tuning 仍不够，真实失败推进到 MUSA OOM |
| `aibook` | PARTIAL，不再误用 `/opt/mt-ai/...`，真实走 `~/.aima/models/qwen3-8b` | FAIL，`phase=failed`，最终暴露 `KeyError: 'layers.18.mlp.down_proj.g_idx'` | FAIL，且失败回传不及时；在 status 已 failed 后 CLI 仍长时间等待 | 旧的坏 symlink / 错目录问题已修复，但现场 GPTQ 资产仍不干净，且 `run` 失败传播仍偏慢 |

## 关键结论

- 已推送 `160b8aa` 的关键修复在真实设备上成立：
- `dev-mac` detached native launch 真正稳住了，`run` 返回后 `deploy status` 仍是 `ready=true`。
- `test-win` 的 `deploy` 已不再被 `already running` 卡死，而是直接复用现有 ready 实例。
- `m1000` 的 alias 兼容目录发现是真实生效的，部署已经不再落到污染的同名 `bf16` 目录。
- `aibook` 也已经不再误吃 `/opt/mt-ai/llm/models/gptq-Qwen3-8B` 这个坏目录。

- 这轮真实矩阵仍保留 3 个剩余现场问题：
- `amd395` 不是产品状态机问题了，而是现场 `8080` 被别的进程占用。
- `m1000` 已推进到新的真实停止点：`Qwen3-30B-A3B-GPTQ-Int4 + vLLM-MUSA` 当前在加载 MoE expert 权重时触发 `MUSA out of memory`。
- `aibook` 当前真正的深层失败是 `KeyError: 'layers.18.mlp.down_proj.g_idx'`，说明本地 GPTQ 资产和 vLLM-MUSA 预期权重结构仍不一致。

- 额外发现 1 个真实 product bug：
- `aibook` 上 `run qwen3-8b --no-pull` 的等待循环仍然过长。即使并行 `deploy status` 已经明确收敛为 `failed`，CLI 也没有及时把失败带回用户，而是继续停在 `Waiting for qwen3-8b-vllm to be ready`。
