# AIMA ↔ OpenClaw 撤回同步(exclude / include)集成说明 — 2026-06-29

## 背景

AIMA 的 `openclaw sync` 是**权威式全量对账**:它把 OpenClaw 配置对齐到"当前所有已部署(ready)的模型",
并且 serve 里有个**自动 sync 循环**(每 ~5s)。所以用户在 OpenClaw 里手动删掉某个模型后,
下一轮 sync 会**又把它加回来**——这正是要解决的"脏数据/删不掉"问题。

本版新增**撤回同步标识**能力:把某个模型标记为"已撤回",sync(含自动循环)**跳过**它;
**只有显式恢复后才会重新同步**。

## 语义(重点)

- **持久 + 可逆**:撤回是持久标识(重启、reconcile 都保持),但随时可一条命令恢复。**不是**一次性删除(一次性会被 5s 自动 sync 打回),**也不是**永久锁死。
- **撤回 ≠ 卸载**:exclude 只摘掉"在 OpenClaw 里的曝光",**模型在 AIMA 里继续部署、继续 serving**(:8080 / 代理照常),别的消费方不受影响。要停模型用 `undeploy`(两件独立的事)。
- 标识存放:AIMA 的 `aima-openclaw-managed.json`(与 openclaw.json 同目录)里的 `excluded_models`。

## 一、AIMA 侧用法(CLI / 给人用)

```bash
aima openclaw exclude <modelID>   # 撤回:从 OpenClaw 删掉 + 打标识,自动 sync 不再加回
aima openclaw include <modelID>   # 恢复:清标识,下一次 sync 自动把它加回 OpenClaw
aima openclaw status              # 查看,JSON 里 excluded_models 列出当前被撤回的模型
```

`exclude`/`include` 执行后会立刻跑一次 reconcile sync 并回显最新 `status`(含 `excluded_models`)。

## 二、OpenClaw 侧集成(核心 —— 给合作方做产品集成)

**不需要新配 MCP server。** AIMA 在 sync 时已经把自己注册进 OpenClaw:

```jsonc
// openclaw.json 里(AIMA 自动写入/维护)
"mcp": { "servers": { "aima": { "command": "aima", "args": ["mcp", "--profile", "operator"] } } }
```

这个 `operator` profile **已经包含 `openclaw` 工具**。所以 OpenClaw 侧只要在
"用户删除/恢复模型"的动作上,**调用这个已存在的 `aima` MCP server 的 `openclaw` 工具**即可。

### 工具调用约定

- 工具名:`openclaw`
- 撤回:`{ "action": "exclude", "model": "<modelID>" }`
- 恢复:`{ "action": "include", "model": "<modelID>" }`
- 返回:撤回/恢复后的 `status` JSON(含更新后的 `excluded_models`)

### JSON-RPC(MCP `tools/call`)示例

```jsonc
// 用户在 OpenClaw 里"删除"某模型 → OpenClaw 调:
{ "jsonrpc": "2.0", "id": 1, "method": "tools/call",
  "params": { "name": "openclaw", "arguments": { "action": "exclude", "model": "Qwen3.6-35B-A3B-UD-Q4_K_M" } } }

// 用户想"重新接上" → OpenClaw 调:
{ "jsonrpc": "2.0", "id": 2, "method": "tools/call",
  "params": { "name": "openclaw", "arguments": { "action": "include", "model": "Qwen3.6-35B-A3B-UD-Q4_K_M" } } }
```

> `model` 用 AIMA 部署/同步出来的模型 ID(= OpenClaw provider 里那条 model 的 id,
> 也就是 `aima openclaw status` / `/v1/models` 里看到的那个名字)。

### 合作方需要做的产品层工作

1. 在 OpenClaw 的"删除模型"按钮/动作上,调用 `openclaw` 工具 `action=exclude`(而不是只改本地配置——否则 5s 后被 AIMA 重新同步覆盖)。
2. 在"重新添加/恢复"入口上,调用 `action=include`。
3. (可选)在 UI 上展示哪些模型当前被撤回:读 `openclaw status` 的 `excluded_models`,或本地标记。

> 为什么必须由 OpenClaw 回调 AIMA:AIMA 无法可靠区分"用户故意在 Claw UI 里删了"和"配置被重置/首次同步",所以撤回意图必须显式传给 AIMA(经此工具),不能靠 AIMA 猜。

## 三、行为说明(默认值,可按需调整)

| 场景 | 行为 | 说明 |
|---|---|---|
| 自动 sync 循环(~5s) | **跳过**被撤回的模型 | 这是核心:撤回后不会被重新加回 |
| `deploy` 一个被撤回的模型 | **不自动清标识** | 部署是"serving",撤回是"对 Claw 曝光",两回事;如需在 Claw 里恢复请显式 `include` |
| `undeploy` 一个被撤回的模型 | **保留标识** | 显式撤回一直有效,直到显式 `include` |
| `exclude` 已撤回的模型 | 幂等,不重复 | |

被撤回 = AIMA 不再管这个模型在 OpenClaw 里的状态(交还用户/Claw 侧),`status.excluded_models` 可见。

## 四、最小验证

```bash
aima openclaw exclude <modelID> && aima openclaw status | grep excluded_models   # 应列出该模型，且 Claw 里已删
aima openclaw include <modelID> && aima openclaw status | grep excluded_models   # 应不再列出，下一次 sync 自动加回
```
