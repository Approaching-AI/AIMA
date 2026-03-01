# Round-1 amd395 测试发现汇总

**机器**：amd395 (quings@100.71.145.56, AMD RDNA3.5, Ubuntu 24.04)
**测试时间**：2026-02-27
**总体结论**：K3S init PASS，VLLM blocked by catalog gap（符合预期）

## BUG-001：`aima version` 命令不存在
- **现象**：手册中引用了 `aima version`，实际运行报 "unknown command"
- **影响**：低。功能不阻塞，但手册误导测试员
- **修复方向**：在 CLI 中添加 `version` 子命令（输出版本号 + 构建时间 + git commit），这是任何 CLI 工具的标准命令

## GAP-001：缺少 AMD VLLM (ROCm) 引擎资产
- **现象**：`aima knowledge resolve qwen3-8b --engine vllm` 在 AMD GPU 上报 `no engine asset for type "vllm" gpu_arch "RDNA3.5"`
- **影响**：高。用户要求在 amd395 上用 VLLM，当前完全不支持
- **修复方向**：
  1. 添加 vllm-rocm 引擎资产 YAML（AMD ROCm 容器，docker pull rocm/vllm）
  2. 或添加 sglang-rocm（SGLang ROCm 支持更成熟）
- **暂不修复原因**：本轮优先保证 NVIDIA 机器 VLLM 通路，AMD ROCm 留下一轮

## 正向发现（良好行为记录）

1. **K3S + HAMi 在 amd395 均安装成功**（耗时 ~15秒，airgap 离线，比 gb10 顺利）
2. **AMD GPU 识别正确**：RDNA3.5，65536 MiB VRAM，unified memory 检测正确
3. **VLLM 拒绝正确**：AIMA 给出明确错误而非尝试用 CUDA 镜像在 AMD 上运行
4. **无引擎约束时推荐 llamacpp-vulkan**：对 AMD GPU 的推荐路径正确
5. **sudo 提示友好**：aima init 以普通用户运行时给出清晰错误提示

## 状态
- amd395 Round-1：完成
- 等待 gb10、linux-1 结果后统一修复
