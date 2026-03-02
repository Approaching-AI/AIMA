# Qwen3.5-27B SGLang 部署经验

> 覆盖 Qwen3.5-27B dense 模型在 SGLang v0.5.9 + linux-1 (2× RTX 4090 48GB) 上的完整部署流程
> 11 个 Bug 发现，8 个部署期修复，3 个架构级修复（Go 代码）
> 日期：2026-03-02

---

## 一、硬件 × 模型 × 引擎配置

| 项目 | 值 |
|------|-----|
| 设备 | linux-1: 2× NVIDIA RTX 4090 48GB (Ada, CUDA 13.0, Driver 580) |
| 模型 | Qwen3.5-27B (dense, Gated-DeltaNet hybrid arch, BF16 ~54GB) |
| 引擎 | SGLang v0.5.9 (`lmsysorg/sglang:v0.5.9`, ~38.7GB Docker image) |
| 运行时 | K3S Pod, TP=2 |
| VRAM | GPU0: 42.7/49.1 GiB (87%), GPU1: 42.3/49.1 GiB (86%) |
| 权重 | 25.64 GB/GPU |
| 性能 | ~32.4 tok/s (低于 vLLM 预期, Gated-DeltaNet SSM 开销) |

### 关键配置参数

```yaml
tp_size: 2
dtype: bfloat16
mem_fraction_static: 0.85
context_length: 65536
reasoning_parser: qwen3
tool_call_parser: qwen3_coder
port: 30000
```

### 为什么选 SGLang

Qwen3.5-27B 使用 Gated-DeltaNet 混合架构（非纯 Transformer），需要 SGLang >= v0.5.9 支持。
vLLM stable/nightly 在 2026-03-02 时尚未支持此架构。

---

## 二、11 个 Bug 及修复状态

### 部署期修复（8 个，YAML/配置层）

| # | Bug | 根因 | 修复 |
|---|-----|------|------|
| 1 | SGLang v0.5.7 不识别 Qwen3.5 | 架构太新 | 升级 engine YAML: v0.5.7 → v0.5.9 |
| 2 | CuDNN 版本检查失败 | v0.5.9 要求 CuDNN ≥ 9.15，linux-1 仅 9.10 | engine YAML: `SGLANG_DISABLE_CUDNN_CHECK=1` |
| 5 | variant.engine 匹配失败 | model variant 写了 engine name 而非 type | model YAML: `engine: sglang` (type, 非 name) |
| 6 | K3S 拉取 SGLang 镜像超时 | 38.7GB 直连 docker.io 被墙 | registries.yaml 配多镜像源 |
| 8 | Tool calling parser 错误 | 默认 parser 不支持 Qwen3.5 | `--reasoning-parser qwen3 --tool-call-parser qwen3_coder` |
| 9 | SGLang 重复循环 | v0.5.9 已知 bug (#19393) | 简单 prompt 正常；复杂推理需等 v0.5.10 |
| 10 | Tool calling 参数为空 | v0.5.9 截断 bug | 结构正确但参数常为 {}，等 v0.5.10 |
| 11 | HuggingFace 模型下载被墙 | 直连 huggingface.co 不可达 | 手动设 `HF_ENDPOINT=https://hf-mirror.com` |

### 架构级修复（3 个，Go 代码）

| # | Bug | 根因 | 修复 | Commit |
|---|-----|------|------|--------|
| 3 | K3S registries.yaml 镜像源失效 | nju (403) + rainbond (000) | 替换为 5 个已验证镜像源 (k3s.yaml) | `a02617c` |
| 4 | Docker 有镜像但 K3S ImagePullBackOff | Docker ≠ containerd 存储 | deploy 自动 Docker→containerd import + 预拉取 (main.go) | `a02617c` |
| 7 | huggingface-cli 不走 hf-mirror | CLI 子进程未注入 HF_ENDPOINT | `cmd.Env = append(os.Environ(), "HF_ENDPOINT="+endpoints[0])` | `a02617c` |

---

## 三、Deploy 自动导入机制（Bug #4 修复详解）

### 问题

```
resolver.go:120 丢弃 engine.Image.Registries
→ deploy 时 image 在 Docker 但不在 containerd
→ Pod ImagePullBackOff
→ 旧代码只 log.Info 警告
```

### 修复：三层防御

```
deploy 前检查:
  1. ImageExistsInContainerd(image)? → 已存在 → 跳过
  2. ImageExistsInDocker(image)? → Docker 有 → 自动 import
  3. len(EngineRegistries) > 0? → 都没有 → 预拉取
  4. 以上全失败 → slog.Warn (非致命) → K3S 靠 registries.yaml 兜底
```

### 代码变更

| 文件 | 变更 |
|------|------|
| `resolver.go` | +`EngineRegistries []string` 字段，从 `engine.Image.Registries` 填充 |
| `puller.go` | +`ImageExistsInContainerd()` — 只查 crictl，不查 Docker |
| `main.go` | DeployApply: 被动警告 → 主动三层导入/拉取 |

### 设计原则

- **非致命**：所有 import/pull 错误只 Warn，不阻塞 deploy
- **兜底**：K3S registries.yaml 仍是最后一道防线
- **引擎无关**：不依赖引擎类型，所有容器引擎统一走此路径
- **INV-1 合规**：无 engine-specific 分支

---

## 四、HF Mirror 注入机制（Bug #7 修复详解）

### 问题

```go
// 旧代码：huggingface-cli 子进程不感知 hf-mirror.com
cmd := exec.CommandContext(ctx, hfCLI, "download", repo, "--local-dir", destPath)
// 用户必须手动 export HF_ENDPOINT=https://hf-mirror.com
```

### 修复

```go
endpoints := hfEndpoints() // 返回 [hf-mirror.com, huggingface.co] 或用户自定义
cmd.Env = append(os.Environ(), "HF_ENDPOINT="+endpoints[0])
```

**hfEndpoints() 优先级**：`$HF_ENDPOINT` (用户显式设置) > `hf-mirror.com` > `huggingface.co`

---

## 五、SGLang v0.5.9 已知问题

### 重复循环 Bug (#19393)

**现象**：复杂推理 prompt 导致模型无限重复输出相同 token 序列。
简单 prompt（"hello", "explain AI"）正常。

**影响**：Agent tool calling 在多轮对话中可能触发。

**缓解**：
- 设置 `max_tokens` 上限截断输出
- 简单推理任务不受影响
- 等 v0.5.10+ 修复

### Tool Calling 参数截断

**现象**：`tool_call_parser=qwen3_coder` 生成的 JSON 结构正确，但函数参数常为 `{}`。

**根因**：v0.5.9 的 incremental parser 在流式输出中截断 arguments 部分。

**缓解**：暂不依赖 SGLang 的 tool calling 做关键操作。

### CuDNN 版本检查

SGLang v0.5.9 启动时检查 `CuDNN >= 9.15`，linux-1 的 CuDNN 9.10 会失败。

**修复**：engine YAML 已添加 `SGLANG_DISABLE_CUDNN_CHECK=1`。实测不影响推理正确性。

---

## 六、K3S 镜像源更新

### 旧配置（2 个已失效）

```yaml
- "https://docker.nju.edu.cn"    # 403 Forbidden
- "https://docker.rainbond.cc"   # 000 Connection reset
```

### 新配置（5 个已验证）

```yaml
- "https://docker.m.daocloud.io"  # DaoCloud — 稳定
- "https://docker.1ms.run"        # 1ms.run — 快速
- "https://hub.rat.dev"           # rat.dev — 稳定
- "https://proxy.vvvv.ee"         # vvvv.ee — 稳定
- "https://dockerproxy.net"       # dockerproxy — 稳定
```

**生效方式**：`sudo aima init` 重新写入 `/etc/rancher/k3s/registries.yaml`

---

## 七、升级路径 (v0.5.9 → v0.5.10+)

当 SGLang v0.5.10 发布后：

1. 更新 `catalog/engines/sglang-universal.yaml`: tag `v0.5.9` → `v0.5.10`
2. 测试移除 `SGLANG_DISABLE_CUDNN_CHECK=1`（如果 CuDNN 检查已修复）
3. 验证 tool calling 参数不再截断
4. 验证复杂推理 prompt 不再循环
5. 如果 v0.5.10 要求 CuDNN 升级，更新 Hardware YAML container.env

**注意**：升级只需改 YAML，零 Go 代码变更（INV-1 合规）。

---

更新：2026-03-02（初始版本，11 bugs + 3 Go fixes + SGLang v0.5.9 已知问题）
