# Round-1 linux-1 测试发现汇总

**机器**：linux-1 (cjwx@100.121.255.97, 2×RTX 4090, Ubuntu 22.04 x86_64)
**测试时间**：2026-02-27
**总体结论**：场景A PASS，场景B BLOCKED（schedulerName 问题）

## BUG-002：Pod 生成包含 `schedulerName: hami-scheduler`，导致 Pod 永久 Pending
- **现象**：`aima deploy qwen3-8b --engine vllm` 生成的 Pod YAML 含 `schedulerName: hami-scheduler`
- **根本原因**：HAMi 以 `kubeScheduler.enabled: false`（Extender 模式）安装，不存在独立的 `hami-scheduler` Pod；Pod 使用该 schedulerName 导致永久 Pending
- **影响**：严重，阻塞所有 K3S + VLLM 部署路径
- **修复方向**：
  - 方案A（推荐）：Pod YAML 不设 schedulerName（使用 K3S 默认 default-scheduler）。HAMi extender 模式自动通过 kube-scheduler 参与调度，无需显式指定
  - 方案B：`aima init` 改为强制使用 `kubeScheduler.enabled: true`（独立 scheduler Pod），但与 K3S GB10 已知兼容问题冲突
  - 方案C：运行时检测 HAMi 模式，动态决定 schedulerName（过复杂）
  - **结论**：采用方案A，删除/省略 schedulerName，让 K3S 默认调度器 + HAMi Extender 处理

## BUG-003：`aima model pull` 下载完成后未自动注册数据库
- **现象**：`aima model pull qwen3-8b` 成功下载 15.6GB 到本地，但 `aima model list` 不显示该模型，需额外执行 `aima model import <路径>` 才能注册
- **影响**：中。流程断层，用户体验差；手册描述与实际不符
- **修复方向**：`PullModel` 函数下载完成后调用 `model.Scan` 并 upsert 到 DB（与 `ImportModel` 复用逻辑）

## NOTICE-001：本地 VLLM 镜像与 catalog 镜像不匹配
- **现象**：`engine scan` 发现本地 `zhiwen-vllm:v3.3.1`，catalog 指定 `vllm/vllm-openai:v0.8.5`
- **影响**：低（目前阻塞在 schedulerName，未验证镜像匹配逻辑）
- **观察**：`aima deploy` 直接使用 catalog 镜像，不优先使用已扫描的本地镜像
- **后续**：当 BUG-002 修复后，验证此行为是否影响实际推理

## 正向发现
1. K3S + HAMi 在 linux-1 (RTX 4090) 安装全部成功，约 31 秒
2. 2×RTX 4090 硬件识别正确（CUDA 12.4，各 24GB VRAM）
3. `nvidia-rtx4090-x86` hardware profile 匹配正确
4. K3S runtime 切换正确（native → k3s）
5. `aima model pull` 下载稳定（24 MB/s，11 分钟，15.6 GB）

## 状态
- linux-1 Round-1：完成
- 等待 gb10 结果后统一修复 BUG-002 + BUG-003
