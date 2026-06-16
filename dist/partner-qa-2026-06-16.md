# AIMA 合作方诉求 Q&A（2026-06 轮）

交付载体：分支 `amd395-win`，新构建 `v0.5-dev-amd-strix-halo-20260616.exe`（`dist/`，`serve.bat` 已指向）。
全部在 AMD Strix Halo (Ryzen AI Max+ 395 / Radeon 8060S) Win11 + llama.cpp HIP 上实测通过。

---

## 一、四个模型的 context_window / max_tokens 能否正常启动？

**Q**：按以下参数能在机器上正常启动吗？需要的话更新 yaml。

| model | context_window | max_tokens |
|---|---|---|
| GLM-4.7-Flash-Q4_K_M | 202752 | 131072 |
| Qwen3.6-35B-A3B | 262144 | 81920 |
| Qwen3.6-35B-A3B-UD-Q4_K_M | 262144 | 81920 |
| Qwen3-Embedding-4B-Q4_K_M | 40960 | null |

**A**：全部实测通过（iGPU 全量 offload），catalog yaml 已更新为各模型的完整训练上限：

| 模型 | ctx 实测 | 结论 |
|---|---|---|
| GLM-4.7-Flash | n_ctx=202752 加载+服务 | ✅ |
| Qwen3.6-35B-A3B(-UD-Q4_K_M) | n_ctx=262144 加载+服务 | ✅（新增 llamacpp 变体 + 别名 `Qwen3.6-35B-A3B-UD-Q4_K_M`/`qwen3.6-35b-a3b-q4_k_m`） |
| Qwen3-Embedding-4B | `--embedding` 模式，`/v1/embeddings` 返回 2560 维 | ✅（盘上为 Q8_0，同模型；Q4/Q8 别名都已挂） |

**关于 max_tokens**：AIMA **不单独定制** max_tokens，统一按 `context_window / 2` 自动计算（GLM→101376，Qwen3.6-35B→131072）。如需与上面表格完全一致的精确值，需要在 catalog 增加一个 `max_tokens` 字段（本轮按决定**未做**）。

**关于大默认值的安全性**：这些大上下文作为 catalog 默认值是安全的——AIMA 部署时会做**硬件自适应裁剪**：读 GGUF 真实架构算出 KV 占用，按检测到的可用显存/内存把 ctx 自动夹到能装下的最大值，并封顶在模型训练上限。大内存机器拿满、小内存机器自动降，**不会 OOM**。可用 `--config ctx_size=N` 临时覆盖。

---

## 二、embedding 模型是否同步进 OpenClaw？

**Q**：embedding 需不需要 sync 到 OpenClaw？

**A**：**不需要，也不支持**。embedding 不是对话（chat）provider，`aima openclaw sync` 不会把它写进 OpenClaw 配置。它部署后直接用标准 `/v1/embeddings` 端点调用即可（经 AIMA 代理 6188 或后端端口）。

---

## 三、OpenClaw sync 是否把同步模型设为主模型（可控）

**Q**：sync 成功后默认会把同步模型设为主模型。希望可控：既可指定设为主，也可不改用户当前主模型。

**A**：**已支持**，通过环境变量 `AIMA_OPENCLAW_SET_DEFAULT`（CLI sync 和 serve 自动 sync 都生效）：

| 取值 | 行为 |
|---|---|
| 不设 / `true` | 把同步的本地模型设为 OpenClaw 主模型（默认行为） |
| `false` | 只写入 provider 和模型列表，**不改用户当前的主模型** |

用法（Windows）：
```
set AIMA_OPENCLAW_SET_DEFAULT=false
aima openclaw sync
```
（serve 模式下在启动 AIMA 的环境里设同一个变量即可。）

---

## 四、AIMA 退出后模型状态持久化（读取历史服务信息）

**Q**：退出 AIMA 后再次启动时，期望能读取上次使用的模型信息（模型名、engine 路径等），以便百应侧自动拉起对应推理框架。

**A**：AIMA **不主动自动拉起**（按你方决定），但所有历史服务信息都**持久化在本地、可随时读取**，百应侧启动时读取后自行拉起。三种方式任选其一：

**① `aima deploy list`（推荐，JSON 接口）** —— 列出所有历史部署（含已停的），每条含：
`name`、`model`、`engine`、`address`/`port`、`phase`(running/starting/failed/stopped)、`served_model`、`context_window_tokens`、`runtime`、`slot`、`start_time`。

**② 原始记录文件 `~/.aima/deployments/*.json`**（每服务一文件，跨重启保留）字段：
`name, pid, port, engine, config, labels, command, log_path, start_time, health_check_path`
- `command` 是完整启动命令数组，**`command[0]` 即引擎二进制的绝对路径**；
- `labels` 含 `aima.dev/model`、`aima.dev/engine`、`aima.dev/context_window`。

**③ 最后使用的 LLM**：`aima system config get llm.model`（或 SQLite `config["llm.model"]`）。

> 补充：若引擎进程在 AIMA 重启后**仍存活**，`aima serve` 启动时会**自动重新发现并挂回代理**（每 5s 对账），无需任何操作；只有进程已死时才需要百应按上面信息自己拉起。

---

## 五、OpenClaw sync 配置目录可定制

**Q**：当前 sync 默认写入 `.openclaw` 目录。若百应用自定义目录名（如 `.byClaw`），是否支持指定同步目标路径？是否不受 OpenClaw/百应claw 产品更新影响？

**A**：**已支持**，通过环境变量 `AIMA_OPENCLAW_CONFIG`，且**纯 AIMA 侧、完全不受对方产品如何变化影响**（AIMA 只负责"被告知往哪写就往哪写"）：

```
set AIMA_OPENCLAW_CONFIG=C:\Users\<user>\.byClaw\openclaw.json
aima openclaw sync
```

设置后，`openclaw.json` 配置、以及配套的 `skills`、`extensions`、managed-state **整套**都会写到该目录（它们都基于配置文件所在目录推导）。已实测：`.byClaw\openclaw.json` 与 `.byClaw\skills` 均正确生成。

---

## 交付清单

- 分支：`amd395-win`
- 新 exe：`dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260616.exe`（`serve.bat` 已指向）
- 本轮新增能力：4 个模型上下文 catalog 默认值 + 硬件自适应 ctx 裁剪 + `AIMA_OPENCLAW_SET_DEFAULT` + `AIMA_OPENCLAW_CONFIG`
