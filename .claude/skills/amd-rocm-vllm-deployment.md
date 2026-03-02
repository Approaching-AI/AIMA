# AMD ROCm vLLM 部署经验

> 覆盖 vLLM TheRock 在 AMD RDNA3.5 (gfx1151) + K3S 上的部署调试经验
> 基于：amd395 (Ryzen AI MAX+ 395, Radeon 8060S, 64GiB 统一内存)
> 日期：2026-03-01

---

## 一、ROCm vLLM 引擎选择

AMD GPU 上 vLLM 没有官方发布镜像。需使用社区构建的 TheRock 版本：

| 引擎镜像 | vLLM 版本 | 目标 GPU | 大小 |
|----------|-----------|----------|------|
| kyuz0/vllm-therock-gfx1151:latest | v0.16.1rc1 | RDNA3.5 (gfx1151) | 45.9GB |

> **注意**：TheRock 是 ROCm 在 AMD Consumer GPU 上的社区移植。官方 vLLM ROCm 仅支持 MI 系列。

### Engine YAML 关键配置

```yaml
kind: engine_asset
metadata:
  name: vllm-rocm
  type: vllm
hardware:
  gpu_arch: RDNA3.5
image:
  name: kyuz0/vllm-therock-gfx1151
  tag: latest
env:
  HSA_OVERRIDE_GFX_VERSION: "11.5.1"   # 必须：告诉 HIP 目标架构
  LD_PRELOAD: "/opt/rocm/lib/librocm_smi64.so"  # 必须：ROCm SMI 库
  VLLM_USE_TRITON_FLASH_ATTN: "1"      # 可移除：v0.16.1+ 自动选择
health_check:
  timeout_seconds: 600  # ROCm 冷启动比 CUDA 慢
```

---

## 二、容器 GPU 访问配置

### 必要的 Pod 配置

ROCm 容器需要挂载 `/dev/kfd` 和 `/dev/dri`，并配置正确的权限：

```yaml
spec:
  securityContext:
    supplementalGroups: [44, 109]  # video + render (Ubuntu GID)
  containers:
    - securityContext:
        privileged: true  # ROCm 标准实践，避免 GID 兼容性问题
      volumeMounts:
        - mountPath: /dev/kfd
          name: dev-kfd
        - mountPath: /dev/dri
          name: dev-dri
        - mountPath: /dev/shm
          name: dshm
  volumes:
    - name: dev-kfd
      hostPath:
        path: /dev/kfd
    - name: dev-dri
      hostPath:
        path: /dev/dri
    - name: dshm
      emptyDir:
        medium: Memory
```

### GID 陷阱

**问题**：`render` group 的 GID 因发行版/安装而异。Ubuntu 常见值：
- `video` = 44（稳定）
- `render` = 109（Ubuntu 24.04 默认）
- `lxd` = 110（易与 render 混淆！）

**解决**：用 `privileged: true` 绕过 GID 问题。这是 ROCm 容器的标准做法。

### 诊断 GPU 访问

```bash
# 宿主机检查
ls -la /dev/kfd /dev/dri/
getent group video render

# 容器内检查（如果能 exec 进去）
rocm-smi --showmeminfo vram

# 常见错误
# "no ROCm-capable device is detected" → 权限问题（缺 privileged 或 GID 错误）
# "0 active drivers" → Triton 找不到 GPU（同上）
```

---

## 三、量化支持矩阵

| 量化 | NVIDIA (CUDA) | AMD MI (ROCm) | AMD RDNA3.5 (TheRock) |
|------|--------------|---------------|----------------------|
| BF16 | ✅ | ✅ | ✅ (如果 VRAM 够) |
| FP8 Dense | ✅ (H100+) | ✅ (MI300+) | ❌ |
| FP8 MoE | ✅ (H100+) | ❌ | ❌ |
| AWQ | ✅ | ✅ | 未测试 |
| GPTQ | ✅ | ✅ | 未测试 |

> **关键发现**：FP8 MoE 在任何 AMD GPU 上的 TheRock vLLM 都不支持。
> 错误信息：`NotImplementedError: No FP8 MoE backend supports the deployment configuration.`

---

## 四、统一内存 OOM 分析

AMD APU (Strix Halo) 的 CPU 和 GPU 共享物理内存，ROCm 将全部系统 RAM 报告为 GPU VRAM。

### VRAM 预算计算

```
Total VRAM = 64 GiB (reported by ROCm)
Model weights (BF16 30B) = ~58 GiB
PyTorch overhead = ~5 GiB (reservations, buffers, compilation)
KV cache + misc = 需额外 ~1 GiB
总计 = ~64 GiB → 恰好不够
```

### CPU Offload 在 UMA 上不工作

| 参数 | PyTorch Alloc | PyTorch Reserved | 总计 |
|------|---------------|-----------------|------|
| 0 GB | 58.42 | 4.39 | 62.81 |
| 4 GB | 55.31 | 7.51 | 62.82 |
| 10 GB | 51.04 | 11.89 | 62.93 |

**原理**：
- `--cpu-offload-gb` 使用 UVA (Unified Virtual Addressing) 将部分参数标记为 "CPU"
- 但 UMA 下 CPU 和 GPU 共享同一 DRAM，HIP 分配器仍在同一物理内存池中分配
- UVA Offloader 的元数据开销（reserved memory）吃掉了节省的 allocated memory

### 模型大小上限估算（amd395 64GiB）

```
可用 VRAM (gmu=0.95) = 60.8 GiB
PyTorch overhead ≈ 5 GiB
模型权重上限 ≈ 55 GiB

BF16: 55 GiB / 2 bytes = ~27.5B dense params
FP8:  不支持 MoE
结论: amd395 vLLM 最多跑 ~25B dense 模型 (BF16)
      MoE 模型需要 GGUF 量化走 llamacpp
```

---

## 五、部署 Checklist（AMD ROCm）

```bash
# 1. 确认 ROCm 设备可访问
rocm-smi --showmeminfo vram
# 期望: VRAM Total Memory 64+ GiB

# 2. Docker 镜像可用
docker images | grep vllm-therock
# 如不存在: docker pull kyuz0/vllm-therock-gfx1151:latest

# 3. K3S containerd 导入
sudo aima engine scan
# 或手动: docker save kyuz0/vllm-therock-gfx1151:latest | sudo k3s ctr -n k8s.io images import -

# 4. 模型下载（注意格式！）
aima model pull <model>
# 自动选择正确 variant（需 catalog YAML 配置 gpu_arch: RDNA3.5）

# 5. Dry-run 验证 Pod YAML
aima deploy <model> --engine vllm --dry-run
# 检查: privileged: true, /dev/kfd + /dev/dri mounts, HSA_OVERRIDE_GFX_VERSION

# 6. 部署
aima deploy <model> --engine vllm

# 7. 监控启动
aima deploy logs <name> --lines 50
# ROCm 冷启动较慢（1-3 分钟），注意 liveness probe initialDelaySeconds >= 600
```

---

## 六、常见错误速查

| 错误 | 原因 | 修复 |
|------|------|------|
| `no ROCm-capable device is detected` | 容器无 GPU 访问权限 | 添加 `privileged: true` + 挂载 /dev/kfd, /dev/dri |
| `0 active drivers` | 同上 | 同上 |
| `No FP8 MoE backend` | ROCm 不支持 FP8 MoE | 用 BF16 或 GGUF 量化 |
| `quantization mismatch (fp8 vs none)` | FP8 模型文件 + --quantization none | 不能混用，须用对应量化的模型文件 |
| `CUDA out of memory` | 模型超出 VRAM | 降低模型大小或用 GGUF 量化 |
| `Unknown vLLM env: VLLM_USE_TRITON_FLASH_ATTN` | v0.16.1+ 已废弃此变量 | 可从 engine YAML env 中移除 |
| `HSA_OVERRIDE_GFX_VERSION` 未设 | HIP 无法识别 gfx1151 | Engine YAML env 中必须设置 |

---

## 七、与 NVIDIA 部署的关键差异

| 维度 | NVIDIA (CUDA) | AMD (ROCm/TheRock) |
|------|--------------|-------------------|
| 设备挂载 | runtimeClassName: nvidia | /dev/kfd + /dev/dri hostPath |
| 安全上下文 | 不需要 privileged | 需要 privileged: true |
| 设备发现 | NVIDIA device plugin | 无自动发现，手动挂载 |
| FP8 支持 | H100+ 完整 | 不支持 MoE |
| 引擎来源 | vLLM 官方镜像 | 社区 TheRock 构建 |
| 冷启动 | ~1-3 min | ~1-5 min (torch.compile 更慢) |
| CPU offload | 有效（独立 VRAM） | 无效（UMA 共享内存池） |
| 环境变量 | NVIDIA_VISIBLE_DEVICES 等 | HSA_OVERRIDE_GFX_VERSION + LD_PRELOAD |

---

## 八、AIMA 代码修复记录

### 本次测试修复的 6 个 bugs

| Bug | 严重度 | 描述 | 修改文件 |
|-----|--------|------|---------|
| #3 | P1 | engine info 在 AMD 上匹配到 NVIDIA engine | cmd/aima/main.go |
| #5 | P0 | model pull 不检查 gpu_arch，下载错误 variant | cmd/aima/main.go |
| #6 | P1 | knowledge list JSON 序列化 crash (map[interface{}]) | internal/knowledge/loader.go |
| #7 | P1 | deploy list/status/logs 被解析为 model name | internal/cli/deploy.go |
| #8 | P1 | Download HTTP 超时 30min，大文件失败 | internal/model/downloader.go |
| #9 | P0 | ROCm 容器 GPU 权限（GID 错误 + 缺 privileged） | catalog/hardware/amd-radeon-x86.yaml |

### 核心修复模式：三遍匹配（Hardware-Aware Resolution）

Engine info 和 model pull 都从"找到第一个就返回"改为三遍匹配：

```
Pass 1: exact name / exact gpu_arch match
Pass 2: type match with hardware preference / wildcard variant
Pass 3: image substring / engine-only fallback
```

这解决了多平台 catalog 中同 type 不同 gpu_arch 的 engine/variant 混淆问题。
