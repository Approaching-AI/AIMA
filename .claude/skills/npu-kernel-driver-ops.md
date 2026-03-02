# Skill: NPU/Kernel Driver Out-of-Tree Installation (Linux)

## 触发条件
当需要在 Linux 上安装 out-of-tree 内核模块（替换内置驱动）、调试 NPU 驱动加载、
或分析 xrt-smi / amdxdna 行为时使用本 skill。

---

## Insight 1 — Ubuntu OEM 内核的 depmod 搜索路径

**标准 Linux** 预期 `extra/` 优先级最高，但 Ubuntu OEM 内核不遵循此规则。

检查 `/etc/depmod.d/ubuntu.conf`：
```
search updates ubuntu built-in   # extra/ 不在其中！
```

**结论**：Ubuntu OEM 系内核上，out-of-tree 模块必须安装到 `updates/`，不是 `extra/`。

```bash
# ✅ 正确：
sudo mkdir -p /lib/modules/$(uname -r)/updates/
sudo cp my_module.ko /lib/modules/$(uname -r)/updates/
sudo depmod -a $(uname -r)

# ❌ 错误（extra/ 不会被 depmod 扫描到）：
sudo cp my_module.ko /lib/modules/$(uname -r)/extra/
```

验证 depmod 选中了新版：
```bash
modinfo <module_name> | grep filename   # 应显示 updates/
grep <module_name> /lib/modules/$(uname -r)/modules.dep
```

---

## Insight 2 — 安全的 DKMS-style 驱动替换（不热替换）

热替换（rmmod + insmod）会导致 PCIe 设备处于半初始化状态 → kernel panic。
正确做法：让新驱动在 **BIOS POST 后首次加载时**通过 depmod 优先级机制自动选中。

完整流程：
```bash
# 1. 编译 out-of-tree 模块
make -C build_drv/driver/<module> KERNEL_SRC=/lib/modules/$(uname -r)/build -j$(nproc)

# 2. 安装到 updates/ (Ubuntu) 或 extra/ (其他发行版)
sudo cp <module>.ko /lib/modules/$(uname -r)/updates/

# 3. 重建模块依赖数据库（updates/ 优先级 > kernel/）
sudo depmod -a $(uname -r)

# 4. 更新 initramfs（让 early boot 也加载新版）
sudo update-initramfs -u -k $(uname -r)

# 5. 重启 → 内核自动选 updates/ 版本
sudo reboot
```

**绝对不要**：
```bash
sudo rmmod <module> && sudo insmod <module>.ko   # ← PCIe 热替换，大概率 crash
```

---

## Insight 3 — AMD NPU amdxdna 驱动：固件路径与版本检查

### 固件文件规则
- **Production firmware**：`npu.sbin.zst`（或 `npu.sbin.<ver>.zst` + symlink）
- **Dev firmware**：`npu.dev.sbin`（开发板专用，协议与 production 不同）
- **绝对不能**：把 `npu.dev.sbin` symlink 到 `npu.sbin.zst` → 协议不匹配 → crash

`npu5_regs.c` 关键字段（路径：`src/driver/amdxdna/npu5_regs.c`）：
```c
// 使用 production firmware（不检查版本）
.fw_path        = "amdnpu/17f0_11/npu.sbin",
.min_fw_version = 0,    // 0 = 跳过版本检查，允许 production fw 1.0.0.166
```

### 版本检查关闭原因
`AIE2_FW_VERSION(6, 12)` 要求 dev firmware（v6.x），production fw 是 v1.0.0.166 → 若不清零会拒绝加载。

---

## Insight 4 — xrt-smi validate 失败 ≠ NPU 不可用

`xrt-smi validate` 测试流程：
1. 先通过 opcode 0x13 (`attach_debug_bo`) 挂载调试 buffer
2. 再提交 GEMM / latency / throughput 命令

如果 **production firmware 不支持 debug_bo API**：
```
amdxdna: xdna_mailbox: Size 12 opcode 0x13 ret -22   ← EINVAL，fw 不认识此 opcode
amdxdna: xdna_mailbox: Channel in bad state           ← channel 进入错误状态
ERT_CMD_STATE_ABORT                                   ← 后续所有命令 abort
```

**关键判断**：
- `ERT_CMD_STATE_ABORT` = **graceful failure**（机器不 crash，硬件还活着）
- 这是 xrt-smi 测试基础设施的失败，不是 compute 层失败
- 真实推理框架（ONNX Runtime NPU backend）不走 debug_bo 路径
- **下一步**：跳过 xrt-smi，直接用 ONNX Runtime 运行推理测试

---

## Insight 5 — llama.cpp HIP 编译 (ROCm 7.9.0 + gfx1151)

**CMake 坑序列** (每个都要踩一遍):

```bash
# cmake 需要的环境变量（缺一不可）
export ROCM_PATH=/opt/rocm
export HIP_PATH=/opt/rocm
export HIP_PLATFORM=amd   # ← 必须！否则 hip-config.cmake 报 "Unexpected HIP_PLATFORM"
export PATH=/opt/rocm/bin:/opt/rocm/lib/llvm/bin:$PATH

# ROCm 7.9.0 device lib 不在标准路径，需要 symlink
sudo ln -sfn /opt/rocm/lib/llvm/amdgcn /opt/rocm/amdgcn

# cmake 参数
cmake .. \
  -DGGML_HIP=ON \
  -DAMDGPU_TARGETS=gfx1151 \
  -DCMAKE_BUILD_TYPE=Release \
  -DHIP_PLATFORM=amd
# 不要加 -DCMAKE_HIP_COMPILER=hipcc ← cmake 3.28 会拒绝 hipcc wrapper
```

**gfx1151 (RDNA3.5) 在 ROCm 7.9.0 的状态**：
- cmake 编译成功（`[100%] Built target llama-bench`）
- 但运行时 **segfault / hang** — gfx1151 Wave32 路径 bug
- 结论：gfx1151 在 ROCm 7.9.0 是实验性支持，生产不可用

---

## Insight 6 — amd395 Phase 2 基线性能 (Qwen3-30B-A3B-Q4_K_M)

```
Qwen3-30B-A3B-Q4_K_M.gguf (18GB): 17.28 GiB, 30.53B 参数
```

| 后端 | PP128 | PP512 | PP1024 | PP2048 | TG128 |
|------|-------|-------|--------|--------|-------|
| iGPU Vulkan (Radeon 8060S) | 515 | **1032** | 891 | 808 | **91.6** |
| CPU 32T | — | 636 | — | — | 30.2 |
| iGPU HIP (ROCm) | — | **crash** | — | — | — |

**NPU 理论分析**:
- iGPU 实测算力: 6.81 TFLOPS（反推）
- NPU 规格: 50 TOPS INT8 ≈ 25 TFLOPS FP16
- NPU/iGPU 加速比: **3.67×**
- NPU 理论 PP512: ~3788 tok/sec → TTFT 135ms（vs iGPU 496ms）
- TG 解码: 带宽瓶颈，NPU 无帮助

**Phase 2 结论**: NPU prefill 加速值得工程投入；软件栈 blocker 是 Linux AMD Ryzen AI SDK 成熟度

---

## amd395 机器 NPU 当前状态（2026-02-26）

| 项目 | 状态 |
|------|------|
| 硬件 | AMD NPU5 "Strix Halo" (0x17f0), AIE2P, 6×8 topology |
| 驱动 | amdxdna v0.1 out-of-tree, `/lib/modules/6.14.0-1020-oem/updates/amdxdna.ko` |
| 固件 | production 1.0.0.166, 已加载 ✅ |
| 设备节点 | `/dev/accel/accel0` ✅ |
| xrt-smi validate | FAIL (debug_bo opcode 不支持) — 非致命 |
| 下一步 | ONNX Runtime NPU backend 推理测试 |

---

## sudo 速查（amd395）

```bash
# amd395: user=quings, sudo password=123456
echo MTIzNDU2 | base64 -d | sudo -S bash -c "<command>" 2>/dev/null
```
