# NPU 探索经验总结

> 记录 AMD Ryzen AI NPU (Strix Halo) 硬件探索的经验教训和可复用流程
> 日期：2026-02-27

---

## 一、环境补齐经验

### 1. MLIR-AIE 工具链安装

#### 安装流程

```bash
# 1. 准备基础环境
sudo apt install ninja-build python3-venv python3-pip

# 2. CMake 升级（系统默认 3.28.3，需要 3.30+）
wget https://github.com/Kitware/CMake/releases/download/v3.30.5/cmake-3.30.5-linux-x86_64.sh
sudo bash cmake-3.30.5-linux-x86_64.sh --skip-license --prefix=/usr/local

# 3. 克隆仓库（国内网络：Windows proxy 下载后 scp 同步）
git clone https://github.com/Xilinx/mlir-aie.git
cd mlir-aie

# 4. 创建 Python venv
python3 -m venv ironenv
source ironenv/bin/activate

# 5. 安装 mlir_aie（有预编译 wheel，不需要源码编译）
pip install mlir_aie -f https://github.com/Xilinx/mlir-aie/releases/expanded_assets/latest-wheels-2

# 6. 安装 Peano (llvm-aie) 编译器
pip install llvm-aie -f https://github.com/Xilinx/llvm-aie/releases/expanded_assets/nightly

# 7. 安装 Python 依赖
pip install -r python/requirements.txt
```

#### 关键路径

| 组件 | 路径 |
|------|------|
| Venv | `~/mlir-aie/ironenv/` |
| Peano (llvm-aie) | `ironenv/lib/python3.12/site-packages/llvm-aie/` |
| aiecc 编译器 | `llvm-aie/bin/aiecc.py` |
| clang++ (Peano) | `llvm-aie/bin/clang++` |

---

## 二、NPU 优化经验

### 1. 设备类型对照

| 设备名称 | Device ID | AIE 版本 | MLIR-AIE 目标 | 架构 |
|----------|-----------|----------|----------------|------|
| Phoenix/Hawk Point | 1502 | AIE2 | `npu` | NPU4 |
| Strix Point B0 | 17f0_10 | AIE2 | `npu` | NPU4 |
| **Strix Halo** | **17f0_11** | **AIE2P** | **npu2** | **NPU5** |

**关键**：NPU5 必须使用 `--dev npu2` 目标，不是 `npu`。

### 2. NPU5/AIE2P 编译

```bash
cd ~/mlir-aie/programming_examples/basic/matrix_multiplication/single_core

# 设置环境
source ~/mlir-aie/ironenv/bin/activate
export PEANO_INSTALL_DIR=~/mlir-aie/ironenv/lib/python3.12/site-packages/llvm-aie
export PATH=$PEANO_INSTALL_DIR/bin:$PATH

# 编译 GEMM (npu2 = AIE2P = NPU5)
make devicename=npu2 use_iron=1 M=64 K=64 N=64 m=32 k=32 n=32

# 输出文件
# - build/final_64x64x64_32x32x32.xclbin  (NPU 可执行文件)
# - build/insts_64x64x64_32x32x32.txt  (用户态指令)
# - single_core.exe  (测试程序)
```

### 3. NPU 测试

```bash
# 运行 GEMM 测试
export XRT_HACK_UNSECURE_LOADING_XCLBIN=1  # 允许未签名 xclbin
./single_core.exe -x build/final_64x64x64_32x32x32.xclbin \
    -k MLIR_AIE -M 64 -K 64 -N 64 \
    -i build/insts_64x64x64_32x32x32.txt -v 2 --iters 1
```

### 4. IRON API 使用

```python
# 导入 IRON
from aie.iron import Kernel, ObjectFifo, Program, Runtime, Worker, str_to_dtype
from aie.iron.placers import SequentialPlacer
from aie.iron.device import NPU1, NPU2

# 设置设备为 NPU2 (AIE2P)
dev = NPU2()
```

---

## 三、关键发现

### 1. xrt-smi validate 失败原因

**现象**：GEMM 测试失败，cycle count = 0

**原因**：使用的是 AIE2PS (NPU3) 架构的测试文件 (`/tmp/gemm.xclbin`)，与 NPU5/AIE2P 不兼容。

**解决**：使用 MLIR-AIE 编译 NPU5 兼容的 xclbin 文件。

### 2. Development Firmware 改善效果

| 测试项 | Production (1.0.0.166) | Development (255.0.11.71) |
|------|-------------------------------|---------------------------|
| Latency | ❌ FAIL | ✅ PASS (62 μs) |
| Throughput | ❌ FAIL | ✅ PASS (65,961 op/s) |
| GEMM | ❌ FAIL | ✅ PASS (1,414 μs, 0.37 GFLOPS) |

### 3. MLIR-AIE 工具链可用性

**之前认知**：PyPI 无 `mlir_aie` 包，需要从源码编译。

**实际**：GitHub releases 有预编译 wheel，可直接 `pip install` 安装。

---

## 四、常见问题解决

| 问题 | 原因 | 解决方案 |
|------|------|----------|
| xrt-smi validate GEMM 失败 | 测试文件架构不匹配 | 用 MLIR-AIE 编译 NPU5 兼容 xclbin |
| "No module named mlir_aie" | PYTHONPATH 未设置 | `source ironenv/bin/activate` |
| CMake 版本太低 | 系统 3.28.3，需要 3.30+ | 下载 3.30.5 安装到 `/usr/local` |
| Segmentation fault 运行测试 | 未设置 `XRT_HACK_UNSECURE_LOADING_XCLBIN=1` | 设置环境变量 |
| Failed to import PyXRT | 正常警告 | 可忽略，不影响编译运行 |
| GitHub 访问超时 | 国内网络环境 | Windows proxy 下载后 scp 同步 |

---

## 五、性能数据

### NPU5 GEMM 性能 (实测)

| 矩阵规模 | 时间 | GFLOPS | 数据类型 |
|----------|------|---------|---------|
| 64×64×64 | 1,414 μs | 0.37 | int16×int16→int32 |

### 与 iGPU 对比

| 设备 | PP512 | TG128 | 说明 |
|------|-------|-------|------|
| iGPU (Vulkan) | 1,032 tok/s | 91.6 tok/s | 已验证可用 |
| NPU | TBD | TBD | 需要推理测试 |

---

## 六、Claude 能力总结

### 我能做什么

| 能力 | 说明 |
|------|------|
| **远程 SSH 操作** | 在 amd395 上执行命令、编译、测试 |
| **文件同步** | Windows 本地下载后通过 scp 同步到远程 |
| **问题诊断** | 通过日志、错误信息分析问题根因 |
| **文档更新** | 将经验记录到 skill 文件中供复用 |
| **信息检索** | 使用 WebSearch、GitHub/GitLab 仓库查询 |
| **代码分析** | 读取 Makefile、README 等理解工具链 |
| **Firmware 获取** | 从 GitLab staging 分支获取 Development Firmware |

### 我不能做什么

| 限制 | 说明 |
|------|------|
| **直接访问 GitHub** | 国内网络环境需要 proxy 或镜像 |
| **实时查看屏幕** | 只能通过 SSH 命令行获取输出 |
| **执行需要 sudo 的操作** | 无法自动执行需要提权的命令 |

---

## 七、Firmware 获取

| 设备类型 | Device ID | Firmware 版本 | 来源 |
|----------|-----------|-------------|------|
| Phoenix/Hawk Point | 1502 | 1.5.2.380 | linux-firmware 官方 |
| Strix Point B0 | 17f0_10 | 1.0.0.63 | linux-firmware 官方 |
| **Strix Halo (NPU5)** | **17f0_11** | **1.0.0.166 / 255.0.11.71** | 官方 / GitLab staging |

Development Firmware 获取：
```bash
git clone --depth 1 --branch amd-ipu-staging https://gitlab.com/kernel-firmware/drm-firmware.git
cp amdnpu/17f0_11/npu.sbin.255.0.11.71 /lib/firmware/amdnpu/17f0_11/npu.dev.sbin
update-initramfs -u
modprobe -r amdxdna; modprobe amdxdna dyndbg==pflm
```

---

## 更新日志

| 日期 | 更新 |
|------|------|
| 2026-02-27 | 文档创建，记录 MLIR-AIE 安装、GEMM 测试经验 |
