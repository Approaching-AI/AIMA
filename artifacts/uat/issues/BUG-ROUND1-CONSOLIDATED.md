# Round-1 全量 Bug 汇总 — 待修复

**生成时间**：2026-02-27（等待 gb10 确认后执行修复）

---

## BUG-001：`aima version` 命令不存在 [低优先级]

- **影响**：amd395、linux-1（均已报告）
- **定位**：`internal/cli/` 目录无 `version.go`，`root.go` 未注册
- **修复**：新增 `internal/cli/version.go` + `root.go` 注册

---

## BUG-002：K3S Pod Pending（HAMi webhook 注入 schedulerName）[严重，阻塞所有部署]

- **影响**：linux-1（已确认 Pending），gb10（预计相同）
- **根本原因链**（完整分析）：
  1. `catalog/partitions/single-model-default.yaml` 通配符策略 primary slot 设 `gpu.cores_percent: 90`
  2. resolver `pickSlot()` → `PartitionSlot.GPUCoresPercent = 90`
  3. `podgen.HasAnnotations()` = true → pod 生成 `nvidia.com/gpucores: 90` annotation
  4. HAMi MutatingWebhook 见到此 annotation → 注入 `schedulerName: hami-scheduler`
  5. hami-scheduler 不存在（HAMi 以 extender 模式安装，`kubeScheduler.enabled: false`）
  6. Pod 永久 Pending，0 Events

- **修复方案（两步）**：
  - **Step 1 (root cause)**：`catalog/partitions/single-model-default.yaml` 将 `gpu.cores_percent: 90 → 0`
    - 移除 HAMi gpucores annotation → HAMi webhook 不再触发 → Pod 使用 default-scheduler
    - GPU 访问改由 `runtimeClassName: nvidia`（hardware profile 已设置）处理
    - VLLM 内存控制改由 engine args 的 `gpu_memory_utilization` 参数处理（engine_params 策略）
  - **Step 2 (belt-suspenders)**：`internal/knowledge/podgen.go` 模板 spec 部分首行加 `schedulerName: default-scheduler`
    - 显式设置防止将来其他触发器

- **影响评估**：
  - 修复后无 HAMi gpucores annotation → 无 nvidia.com/gpu resource request → Pod 仅通过 runtimeClassName 获 GPU
  - 单节点 K3S 上可行（不需要 GPU 资源约束来定向调度）
  - VLLM 通过 CUDA 自动检测 GPU，不依赖 K8s 资源注入

---

## BUG-003：`aima model pull` 完成后未自动注册数据库 [中优先级]

- **影响**：linux-1（已确认）
- **定位**：`cmd/aima/main.go` `PullModel` 闭包（~441 行）只调 `model.DownloadFromSource`，缺少 upsert
- **参考**：`ImportModel` 闭包（~477 行）下载后正确调用 `db.UpsertScannedModel`
- **修复**：`PullModel` 下载成功后调用 `model.Import(ctx, destPath, modelsDir)`，再 `db.UpsertScannedModel`
  - `model.Import` 检测到 srcPath 已在 destDir 下时不会复制，只扫描（已验证代码逻辑）

---

## NOTICE-001：本地 VLLM 镜像与 catalog 镜像不一致（待观察）

- **影响**：linux-1（zhiwen-vllm:v3.3.1 本地可用，catalog 需要 vllm/vllm-openai:v0.8.5）
- **当前状态**：Round-1 因 BUG-002 未到达镜像拉取阶段
- **Round-2 验证点**：修复 BUG-002 后，观察 K3S 是否尝试拉取 vllm/vllm-openai:v0.8.5 或使用本地镜像
- **如阻塞**：vllm-ada.yaml 已有 `registry.cn-hangzhou.aliyuncs.com/aima/vllm-openai` 备用镜像

---

## NOTICE-002：GB10 nvcr.io/nvidia/vllm 镜像可用性（待 Round-2 验证）

- **影响**：gb10（arm64）
- **关注点**：`nvcr.io/nvidia/vllm:26.01-py3`（12GB）无 China 镜像，如 gb10 未预拉取可能超时
- **Round-2 验证点**：检查 gb10 是否已有此镜像（engine scan 结果）

---

## 执行计划（等 gb10 报告确认后）

```
修复 → 构建 → 分发 → Round-2 测试
```

修复文件列表：
1. catalog/partitions/single-model-default.yaml  → gpu.cores_percent: 0
2. internal/knowledge/podgen.go                  → schedulerName: default-scheduler
3. cmd/aima/main.go                              → PullModel auto-register
4. internal/cli/version.go (新建)               → aima version 命令
5. internal/cli/root.go                          → 注册 version 命令
