# AIMA UAT Workspace

本目录保留两类 UAT 资产：

- `handbook/`：旧版操作手册，适合指令式测试轮次。
- `plan/`：新的场景式 UAT 设计，只写用户目标、预期体验和通过信号，不写操作步骤。
- `sessions/`：每次真实执行后的实机记录，重点写发生了什么、卡在什么地方。
- `issues/`：当前活跃轮次的问题汇总单，以及必要时的分机问题单。Round-4 已进入“边修阻塞点、边做统一回归”的阶段。

## 当前状态

- Round-2 已完成修订验证，上一轮 active bug 已清零并转为历史记录。
- Round-3 按 `CLAUDE.md` 的 `ALL COLLECT, THEN ANALYZE` 逻辑执行：
- 同一 commit 一次性分发到测试机。
- 先收集完整设备矩阵，无法接入的机器明确记为 `UNREACHABLE`。
- 只在全部结果回收后统一归并新的 bug。
- Round-4 是当前 active 轮次，聚焦当前 `develop` 上“已有 knowledge + 真实已有资产”的 `deploy` / `run` / `serve/ui` 主链路：
- 仍按同一 commit 一次性分发到测试机。
- 仍先收完整设备矩阵，再统一归并结果。
- 功能优先于 UI；先看真实部署是否成立，再看 UI 是否忠实表达当前状态。
- Round-4 已完成一次阻塞点修复后的 closeout re-verify：
- 结论基于最新构建的统一重分发、统一 `run/status` 回归，以及本机失败态 + `gb10` ready 态的真实 UI 检查。
- 之后完成了定点修复和本地 final closeout re-retest。
- 但 2026-03-31 对已推送 `origin/develop` `160b8aa` 的真实矩阵重跑后，Round-4 一度重新打开 active 问题。
- 同日追加修复并完成真机复测后，当前已无 active product bug。
- 当前剩余 2 个现场 blocker（`amd395` 端口冲突、`aibook` GPTQ 资产不一致）。

## Round-3 记录原则

- 场景文档只描述真实用户意图，不给测试员操作指令。
- 会话记录保留真实机器、真实构建、真实结果，不把测试脚本噪音写成产品问题。
- Bug 文档只保留当前轮次仍未解决的新问题；已修复问题不继续留在 active 列表里。
- 未完成的链路也要记录到停止点，不为了“跑通”去改代码。

## Round-4 入口

- 场景设计：`plan/ROUND-4-DEPLOY-RUN-UI-SCENARIOS-20260330.md`
- 会话记录：`sessions/2026-03-30-round4-deploy-run-ui.md`
- 阻塞点修复复测：`sessions/2026-03-30-round4-blocker-fix-retest.md`
- 收口回归：`sessions/2026-03-30-round4-closeout-reverify.md`
- 后续定点复测：`sessions/2026-03-30-round4-devmac-detach-reretest.md`
- 最终本地收口：`sessions/2026-03-30-round4-final-closeout-local-reretest.md`
- 已推送构建的真实矩阵重跑：`sessions/2026-03-31-round4-real-matrix-rerun-160b8aa.md`
- 追加代码修复后的真机复测：`sessions/2026-03-31-round4-postfix-real-reretest.md`
- 问题汇总：`issues/BUG-ROUND4-DEPLOY-RUN-UI-20260330.md`
