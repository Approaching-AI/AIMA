# Round-5B Deploy Startup Window Trust UAT

**轮次**：Round-5B  
**日期**：2026-03-31  
**目标**：补充验证 UI 在 deploy 启动窗口内的可见性和可信度，尤其是短启动与失败启动。  
**定位**：这是 Round-5 的补充，不再重复验证“假 ready / 假 endpoint”主 bug，而是继续压实生命周期过程追踪。  

## 本轮通过标准

1. **快速启动可见**
   - 即使 deployment 最终很快 ready，UI 也应尽量在 ready 前暴露 `starting`、进度条或至少新实例占位。

2. **中等启动可见**
   - 对持续数秒的启动，UI 应稳定显示新 deployment，不应长时间只剩旧状态。

3. **失败启动可见**
   - 当 deployment 在 ready 前失败，UI 不应保持空白或乐观态。
   - 至少应出现新实例并最终收敛为 `failed`，或通过明确错误阻止 deploy 创建假状态。

4. **endpoint 交叉验证**
   - 所有 UI 标成 ready 的 deployment，都要用真实 endpoint 身份验证。

## 场景

### UAT-DLU-04 快速启动样本

- 候选：`qwen3-0.6b`
- 端口：独立端口
- 观察点：`t+1s / t+2s / t+3s / t+5s`

### UAT-DLU-05 中等启动样本

- 候选：`Qwen3-4B-Q5_K_M` 或等价本机可启动 4B 变体
- 端口：独立端口
- 观察点：`t+1s / t+3s / t+5s / t+8s / t+12s`

### UAT-DLU-06 失败启动样本

- 候选：本机已注册但预期无法完整启动的模型资产
- 端口：独立端口
- 观察点：
  - deploy 返回是否立即失败
  - 若已创建 deployment，UI 是否最终进入 `failed`
  - 失败细节是否与日志和 endpoint 现实一致

## 执行策略

1. 所有样本均使用独立端口，避免再次把“端口冲突修复”与“启动窗口可见性”混在一起。
2. 每个样本都保留：
   - CLI 输出
   - `deploy.status/list/logs`
   - endpoint 响应
   - UI 多时间点 snapshot / screenshot
3. 每个样本结束后立即清理，恢复本机原始状态。
