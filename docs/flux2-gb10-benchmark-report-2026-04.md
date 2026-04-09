# FLUX.2-dev GB10 推理优化测试报告

> 测试日期：2026-04-09
> 测试平台：NVIDIA GB10 (DGX Spark), SM121, CC 12.1
> 主机配置：gb10-4T (`qujing@100.91.39.109`), 120 GB 统一内存 (LPDDR5X), 273 GB/s 带宽
> 驱动/SDK：Driver 580, CUDA 13.0
> 容器：`flux-test` (image: `lmsysorg/sglang:dev-arm64-cu13`)
> 推理框架：diffusers 0.37.1, PyTorch (CUDA 13.0, CC 12.1 兼容 SM120)

---

## 1. 模型概要

| 项目 | 值 |
|------|-----|
| 模型 | black-forest-labs/FLUX.2-dev |
| 类型 | 文生图 (T2I), Diffusion Transformer (DiT) |
| Pipeline | `Flux2Pipeline` (不兼容 FLUX.1 的 `FluxPipeline`) |
| Transformer | `Flux2Transformer2DModel` — 19 双流 blocks + 48 单流 blocks |
| Text Encoder | `Mistral3ForConditionalGeneration` (~45GB BF16, 10 shards) |
| VAE | `AutoencoderKLFlux2` (321 MB) |
| Tokenizer | `PixtralProcessor` (非标准 CLIP) |
| 总模型大小 | ~106 GB BF16 (transformer 46GB + text_encoder 45GB + VAE 0.3GB + 其他) |
| GPU 显存占用 | 112.6 GB (BF16 全精度加载) |
| 模型路径 | `/mnt/data/models/FLUX.2-dev/` (gb10-4T) |

### 1.1 FLUX.2 vs FLUX.1 关键差异

| 差异点 | FLUX.1 | FLUX.2 |
|--------|--------|--------|
| Pipeline 类 | `FluxPipeline` | `Flux2Pipeline` |
| Transformer 类 | `FluxTransformer2DModel` | `Flux2Transformer2DModel` |
| Text Encoder | T5 + CLIP | **Mistral-3** (45GB) + PixtralProcessor |
| Modulation | AdaGroupNorm (norm1 内置 temb) | **Flux2Modulation** (预计算 shift/scale/gate 传入 block) |
| 单流 blocks | 38 | **48** |
| VAE | AutoencoderKL | AutoencoderKLFlux2 |
| 向后兼容 | — | 不兼容 FLUX.1 pipeline |

---

## 2. 基线性能

| 参数 | 值 |
|------|-----|
| 分辨率 | 512×512 |
| 推理步数 | 20 |
| Guidance Scale | 3.5 |
| 精度 | BF16 |
| 生成耗时 | **43.5s** (3 次平均) |

---

## 3. 优化方案测试

### 3.1 channels_last 内存格式

将 tensor 内存布局从默认的 NCHW 改为 NHWC (channels_last)，对 CNN 操作友好。

| 指标 | 值 |
|------|-----|
| 耗时 | 45.10s |
| 加速比 | **0.97x (无效，反而略慢)** |

**结论**：FLUX.2 以 Transformer (attention + linear) 为主，不像 CNN 那样受益于内存布局优化。**不推荐**。

### 3.2 torch.compile (max-autotune)

对 transformer 进行 JIT 编译，自动搜索最优 kernel。

```python
torch._inductor.config.conv_1x1_as_mm = True
torch._inductor.config.coordinate_descent_tuning = True
torch._inductor.config.epilogue_fusion = False
torch._inductor.config.coordinate_descent_check_all_directions = True
pipe.transformer = torch.compile(pipe.transformer, mode="max-autotune", fullgraph=True)
```

| 指标 | 值 |
|------|-----|
| 编译耗时 | ~234s (一次性) |
| 推理耗时 | 37.34s |
| 加速比 | **1.17x** |

**注意**：GB10 SM121 触发警告 "Not enough SMs to use max_autotune_gemm mode"，限制了 GEMM 自动调优效果。在 SM 数量更多的 GPU 上加速可能更显著。

### 3.3 TeaCache (手动实现)

TeaCache (Timestep Embedding Aware Cache, CVPR 2025 Highlight, arXiv:2411.19108) — 训练无关的 DiT 缓存方法。

#### 3.3.1 原理

1. **观察**：相邻去噪步骤的 transformer 输出高度相似，但变化量非均匀
2. **代理信号**：测量 timestep embedding 调制后的输入差异（仅需过第一个 block 的 norm1），成本极低
3. **多项式校正**：原始 L1 距离 → 4 阶多项式 → 校正后的输出差异估计
4. **缓存决策**：累积差异 < 阈值 → 跳过全部 67 个 transformer blocks (19 双流 + 48 单流)，复用上一步残差

#### 3.3.2 FLUX.2 适配

FLUX.2 的 modulation 结构与 FLUX.1 不同，需要手动计算等价的 modulated input：

```python
# FLUX.1: self.transformer_blocks[0].norm1(inp, emb=temb)  # AdaGroupNorm 内置
# FLUX.2: 需要手动组合
(shift_msa, scale_msa, _), _ = Flux2Modulation.split(double_stream_mod_img, 2)
norm_hs = self.transformer_blocks[0].norm1(hidden_states)   # 普通 LayerNorm
modulated_input = (1 + scale_msa) * norm_hs + shift_msa     # 手动应用 modulation
```

使用 FLUX.1 的多项式系数 `[498.65, -283.78, 55.86, -3.82, 0.264]` 作为近似值（经验证在 FLUX.2 上同样有效）。

#### 3.3.3 测试结果

| 阈值 (rel_l1_thresh) | 耗时 | 加速比 | 缓存命中 | 质量评估 |
|---|---|---|---|---|
| 0.25 | 24.37s | **1.79x** | 9/20 (45%) | 近无损，肉眼难以区分 |
| 0.4 | 22.27s | **1.95x** | 10/20 (50%) | 极轻微细节简化 |
| 0.6 | 15.83s | **2.75x** | 13/20 (65%) | 细节略有简化，整体观感良好 |
| 0.8 | 13.73s | **3.17x** | 14/20 (70%) | 可见细节丢失，构图和氛围保持 |

#### 3.3.4 质量 vs 速度分析

- **thresh=0.25**: 保守策略，适合高质量生产。1.79x 加速已非常显著
- **thresh=0.4**: **推荐默认值**。接近 2x 加速，质量损失几乎不可察觉
- **thresh=0.6**: 适合预览/草稿。16s 内出图，2.75x 加速
- **thresh=0.8**: 激进策略，仅适合快速迭代。可见退化

---

## 4. 优化方案对比总结

| 方案 | 加速比 | 额外开销 | 质量影响 | 推荐度 |
|------|--------|---------|---------|--------|
| channels_last | 0.97x | 无 | 无 | 不推荐 |
| torch.compile | 1.17x | 编译 234s | 无 | 可选（适合长时间运行） |
| **TeaCache 0.25** | **1.79x** | 无 | 近无损 | 推荐（高质量） |
| **TeaCache 0.4** | **1.95x** | 无 | 极轻微 | **推荐（默认）** |
| **TeaCache 0.6** | **2.75x** | 无 | 轻微 | 推荐（预览） |
| TeaCache 0.8 | 3.17x | 无 | 可见 | 仅草稿 |

### 4.1 分辨率缩放测试 (TeaCache thresh=0.4)

| 分辨率 | 像素量 | Baseline | TeaCache 0.4 | 加速比 | 每步耗时 |
|--------|--------|----------|-------------|--------|---------|
| 512×512 | 0.26M | 44.2s | 22.6s | 1.96x | 2.2s / 1.1s |
| 768×768 | 0.59M | 83.6s | 42.4s | 1.97x | 4.2s / 2.1s |
| 1024×1024 | 1.05M | 131.4s | 68.2s | 1.93x | 6.6s / 3.4s |
| 1280×720 | 0.92M | 131.2s | 66.2s | 1.98x | 6.6s / 3.3s |
| 1280×1280 | 1.64M | 232.0s | 117.5s | 1.97x | 11.6s / 5.9s |

**观察**：
- TeaCache 加速比在所有分辨率下稳定 ~1.95-1.98x，与分辨率无关
- 耗时与像素量近线性：~140s/Mpx (baseline), ~72s/Mpx (TeaCache 0.4)
- 1280×720 实用分辨率下 TeaCache 约 66s 出图

### 4.2 理论叠加效果

TeaCache + torch.compile 可以叠加使用：
- TeaCache 0.4 (1.95x) + torch.compile (1.17x) ≈ **~2.3x** 预期
- TeaCache 0.6 (2.75x) + torch.compile (1.17x) ≈ **~3.2x** 预期

---

## 5. 不可行/待测方案

| 方案 | 状态 | 原因 |
|------|------|------|
| fuse_qkv_projections | 不可用 | `Flux2Pipeline` 无此方法 (FLUX.1-only) |
| TeaCache (diffusers 内置) | 不可用 | diffusers 0.37.1 未实现 FLUX.2 的 TeaCache |
| flux-fast (HuggingFace) | 不可用 | 不支持 FLUX.2；依赖 FA3 (SM121 无) 和 x86 PyTorch nightly |
| 量化 (NF4/INT8) | 未测 | GB10 统一内存充足 (112.6GB)，量化价值有限 |
| TeaCache + torch.compile | 待测 | 理论可叠加，未验证 |

---

## 6. 环境与脚本

### 6.1 容器配置

```bash
docker run -d --name flux-test --gpus all --network host --shm-size 64g \
  -v /mnt/data/models:/mnt/data/models \
  lmsysorg/sglang:dev-arm64-cu13 bash -c "sleep infinity"

# 容器内 diffusers 升级
pip install diffusers==0.37.1 accelerate
```

### 6.2 脚本位置 (gb10-4T 容器内)

| 脚本 | 说明 |
|------|------|
| `/tmp/flux_teacache.py` | TeaCache 多阈值基准测试 + baseline |
| `/tmp/flux_bench2.py` | channels_last + torch.compile 基准测试 |
| `/tmp/flux_resolution_bench.py` | 多分辨率缩放基准测试 (baseline vs TeaCache 0.4) |

### 6.3 生成图片

| 文件 | 说明 |
|------|------|
| `/mnt/data/models/teacache_baseline.png` | Baseline 生成 (43.5s) |
| `/mnt/data/models/teacache_0.25.png` | TeaCache 0.25 (24.4s) |
| `/mnt/data/models/teacache_0.4.png` | TeaCache 0.4 (22.3s) |
| `/mnt/data/models/teacache_0.6.png` | TeaCache 0.6 (15.8s) |
| `/mnt/data/models/teacache_0.8.png` | TeaCache 0.8 (13.7s) |
| `/mnt/data/models/flux_test_output.png` | 早期测试生成图 |

---

## 7. 关键发现

1. **TeaCache 是 GB10 上 FLUX.2 最有效的优化手段**，远超 torch.compile (2.75x vs 1.17x at comparable quality)
2. **FLUX.1 的多项式系数在 FLUX.2 上直接可用**，说明两代模型的去噪动力学高度相似
3. **GB10 SM121 对 torch.compile 支持有限**：autotune_gemm 因 SM 数量不足被禁用
4. **channels_last 对 DiT 架构无效**：Transformer 不像 CNN 受益于 NHWC 内存格式
5. **FLUX.2 统一内存占 112.6GB**：GB10 的 120GB 统一内存刚好够 BF16 全精度，无需量化
6. **TeaCache 零额外开销**：无需训练、无需编译、无需额外内存，即插即用
7. **TeaCache 加速比与分辨率无关**：512×512 到 1280×1280 稳定在 ~1.95-1.98x，因为跳过比例由阈值决定
8. **耗时与像素量近线性**：~140s/Mpx (baseline)，GB10 上 attention 为 O(n²) 主导
