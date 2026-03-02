# llama.cpp Native / Vulkan 部署经验

> 覆盖 llama.cpp 原生二进制在 AMD RDNA3.5 (amd395) 上的部署调试经验
> 基于：amd395 (Ryzen AI MAX+ 395, Radeon 8060S, RDNA3.5)
> 日期：2026-02-28

---

## 一、硬件特征（Strix Halo / RDNA3.5）

```
GPU：AMD Radeon 8060S (RADV GFX1151, RDNA3.5)
架构特点：
  - UMA（统一内存架构）：CPU 和 GPU 共享物理内存
  - Vulkan 1.3.275 支持（通过 RADV 驱动）
  - /dev/dri/renderD128 渲染节点
  - 设备内存 = 系统 RAM（~97 GB 可用）

性能特点：
  - CPU（Zen5, 16c/32t, AVX-512 BF16 VNNI）与 GPU 内存带宽基本相当
  - 对于小模型（< 1B），CPU 和 GPU 推理速度几乎相同
  - 瓶颈是内存带宽，不是算力
```

---

## 二、llama.cpp Vulkan 二进制部署

### 确认 Vulkan 支持

```bash
# 检查 Vulkan 设备
vulkaninfo --summary 2>/dev/null | head -20
# 或
ls /dev/dri/
# 期望看到 card0, renderD128

# 验证 Vulkan API 版本
vulkaninfo 2>/dev/null | grep "apiVersion"
```

### 通过 AIMA 部署

```bash
# 拉取 Vulkan 版 llama.cpp 二进制
~/aima engine pull llamacpp

# 部署（AIMA 会自动选择 llamacpp-vulkan engine，因为 gpu_arch: RDNA3.5）
~/aima deploy Qwen3-0.6B-Q8_0 --engine llamacpp
```

**注意**：如果模型不在 catalog 中（auto-detected），需要检查 `--n-gpu-layers` 是否被正确传递：

```bash
# 检查 llama-server 进程完整命令行
ps aux | grep llama-server | grep -v grep

# 如果缺少 --n-gpu-layers，手动重启
kill <PID>
~/.aima/dist/linux-amd64/llama-server \
  --model ~/.aima/models/<model-dir>/<model>.gguf \
  --host 0.0.0.0 --port 8080 \
  --n-gpu-layers 999 \
  --ctx-size 4096 \
  > /tmp/llama.log 2>&1 &
```

### 验证 GPU 被使用

启动日志中应看到：
```
ggml_vulkan: Found 1 Vulkan devices:
ggml_vulkan: 0 = Radeon 8060S Graphics (RADV GFX1151) (radv) | uma: 1 | fp16: 1 | bf16: 0
load_backend: loaded Vulkan backend from /path/to/libggml-vulkan.so
...
llama_params_fit_impl: projected to use 1360 MiB of device memory vs. 97335 MiB of free device memory
```

关键字：
- `uma: 1` = 统一内存，GPU 和 CPU 共享同一物理内存池
- `projected to use N MiB of device memory` = 模型实际加载到 GPU

### 检查推理性能

```bash
# 检查运行时状态
curl -s http://127.0.0.1:8080/props

# GPU 内存使用（amd395 需要 sudo 或 root）
~/aima status | python3 -m json.tool | grep -A5 '"gpu"'
```

---

## 三、K3S + 原生二进制双 runtime

amd395 同时运行 K3S（runtime=k3s）和 native runtime。AIMA 根据 engine YAML 中的
`runtime.platform_recommendations.linux/amd64: "native"` 自动选择：

```
aima deploy → DeployApply →
  resolved.RuntimeRecommendation == "native" → 使用 native runtime
  否则 → 使用 K3S runtime
```

K3S 主要用于需要 K8s 特性（HAMi、资源限制等）的场景。
llamacpp 等有 native binary 的引擎默认走 native，避免容器开销。

---

## 四、性能数据（2026-02-28）

| 模型 | 引擎 | 模式 | tok/s | 备注 |
|------|------|------|-------|------|
| Qwen3-0.6B-Q8_0 (640MB) | llamacpp b8157 | CPU only | ~233 | 未传 --n-gpu-layers |
| Qwen3-0.6B-Q8_0 (640MB) | llamacpp b8157 | Vulkan GPU | **~238** | --n-gpu-layers 999 |

**结论**：CPU 和 GPU 速度相近（差 < 2%）。

**原因分析**：
- UMA 架构下两者共享同一内存总线
- Zen5 + AVX-512 BF16 的 CPU 矩阵运算能力极强
- 0.6B 模型过小，不足以发挥 GPU 并行优势
- 预期大模型（7B+）差距会更明显，但 amd395 没有足够的 GGUF 大模型可测试

---

## 五、amd395 已知模型路径

```bash
# GGUF 模型
~/.aima/models/qwen3-0.6b/Qwen3-0.6B-Q8_0.gguf   # 640MB
~/data/models/BF16/Qwen3-30B-A3B-BF16-*.gguf      # 61GB GGUF（两个分片）

# Safetensors 模型
~/data/models/Qwen3-30B-A3B-bf16/                 # 61GB safetensors
```

---

## 六、amd395 llama-server 日志位置

```bash
# AIMA 管理的进程日志
~/.aima/logs/<model-name>.log

# 手动启动时可重定向到
/tmp/llama-gpu.log
```

---

## 七、为什么 vLLM 在 amd395 不可行（2026-03-01）

对 Qwen3-30B-A3B（30B MoE）做了完整 vLLM ROCm 测试，三条路都走不通：

| 方案 | 失败原因 |
|------|---------|
| FP8 (31GB) | ROCm vLLM TheRock 无 FP8 MoE kernels (gfx1151) |
| BF16 (58GB) | 58GiB model + 5GiB PyTorch overhead > 64GiB VRAM → OOM |
| BF16 + CPU offload | 统一内存下无效：PyTorch reserved 吃掉 offload 释放的空间 |

**结论**：amd395 (64GiB unified) 上 30B 级别模型只能走 **llamacpp + GGUF 量化**。
推荐 Q4_K_M (~18.6GB) 或 Q8_0 (~32GB)。

vLLM ROCm 在 amd395 的适用范围：**< 50GiB BF16 权重**（约 25B dense 参数以下）。
考虑到 PyTorch overhead ~5GiB + KV cache 需求，实际上限更低。

---

## 八、amd395 模型路径更新（2026-03-01）

```bash
# Safetensors (BF16, 58GB, 16 shards) — vLLM 无法使用
~/.aima/models/qwen3-30b-a3b/            # 通过 aima model pull 下载

# GGUF 模型
~/.aima/models/qwen3-0.6b/Qwen3-0.6B-Q8_0.gguf   # 640MB
~/data/models/BF16/Qwen3-30B-A3B-BF16-*.gguf      # 61GB GGUF（两个分片）
```
