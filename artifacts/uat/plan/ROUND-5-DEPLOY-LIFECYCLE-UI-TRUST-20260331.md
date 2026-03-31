# Round-5 Deploy Lifecycle UI Trust UAT

**轮次**：Round-5  
**日期**：2026-03-31  
**目标**：单独验证 Web UI 对 deploy 全生命周期的表达是否**精准、可信**。  
**方法**：先定义“可信”的判定边界，再按本机真实场景逐条执行；记录 CLI、`deploy.status/list/logs`、真实 endpoint 响应、UI 快照四类证据，统一交叉对账。  

## 本轮边界

- 本轮优先级是**状态可信度**，不是部署成功率。
- 只接受“能被真实服务身份证明”的 ready，不接受“看起来 ready”。
- 如果 UI 与 CLI 一致，但 CLI 本身与真实 endpoint 不一致，本轮仍判定为 **FAIL**。
- 只要链路中任一层出现“误导性乐观状态”，就记录为有效问题。

## 本轮通过标准

UI 对 deploy 生命周期的表达，必须同时满足：

1. **状态一致**
   - UI 的 deployment 名称、`phase`、`ready`、失败摘要，与 `deploy.status` / `deploy.list` 一致。

2. **进度可信**
   - 当部署真实处于启动阶段，UI 应出现 `starting` 或进度信息。
   - 不要求百分比绝对精确，但不能把“未完成”误报成 ready。

3. **endpoint 可信**
   - UI 标成 ready 的 deployment，其 endpoint 必须可访问，并返回与该 deployment 身份一致的模型/服务。

4. **切换关系可信**
   - 当已有模型 A 在跑，再 deploy 模型 B 时，UI 必须如实表达：
     - 是复用已有实例
     - 还是新实例启动中
     - 还是失败/冲突
   - 不允许把两个不同 deployment 同时显示成共享同一 ready endpoint，除非底层服务身份也能证明它们确实等价。

## 场景矩阵

### UAT-DLU-01 基线 ready 态是否可信

**用户故事**  
用户打开 UI，先确认当前已有 deployment 的真实状态。UI 不需要花哨，但必须让用户一眼看到“这个 ready 到底是不是真的”。

**重点观察**

- UI 与 `deploy.list` 是否一致
- UI 的 endpoint 是否可访问
- endpoint 返回的服务身份是否与该 deployment 模型一致

**通过信号**

- UI ready 不是假阳性

### UAT-DLU-02 新 deployment 的启动过程是否被 UI 捕获

**用户故事**  
用户新部署一个模型时，希望 UI 至少能表达“正在起”而不是长时间空白，或直接跳到一个未经证明的 ready。

**重点观察**

- deploy 发起后，UI 是否出现新 deployment
- 在真实 ready 之前，UI 是否显示 `starting` / progress / startup message
- 最终 ready 后，endpoint 身份是否与 deployment 一致

**通过信号**

- UI 至少不会漏掉真实启动期，也不会提前宣布 ready

### UAT-DLU-03 已有模型 A 运行时，切换 deploy 到模型 B，UI 是否仍可信

**用户故事**  
这是本轮主场景。用户最在意的是：当已有服务在跑，重新换一个模型 deploy 时，UI 还能不能准确表达当前真实部署情况。

**重点观察**

- 新旧 deployment 在 UI 上的关系是否清楚
- 若发生端口冲突、错误复用、错误健康检查、假 ready，UI 是否会暴露而不是掩盖
- UI 的最终 ready endpoint 是否能被真实请求验证

**通过信号**

- UI 不会把“旧服务还在响应”误显示成“新模型已 ready”

## 执行策略

1. 先记录基线 `deploy.list` 和 UI 快照。
2. 选择对现有环境最小侵入的本机真实场景。
3. 每个场景至少保留四类证据：
   - CLI 命令输出
   - `deploy.status/list/logs`
   - endpoint 实测
   - UI snapshot / screenshot
4. 若场景会污染现有 deployment，必须在结尾清理并验证恢复。

## 本轮预期输出

- 一份 session 记录，逐场景给出 `PASS / FAIL / PARTIAL`
- 关键截图和 snapshot
- 若发现 bug，明确指出是：
  - UI 渲染问题
  - 状态源问题
  - deploy 生命周期设计问题
  - 三者叠加
