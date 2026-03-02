# Skills 目录索引

## GPU 推理部署

| 文件 | 描述 |
|------|------|
| `k3s-gpu-pod-deployment.md` | **K3S GPU Pod 部署调试** - 13 个部署 Bug、CUDA 803、256K 上下文、VLM 视觉测试、**Qwen3-Coder-Next 80B 双引擎对比**、**GLM-4.7-Flash 部署 (spark-vllm-docker 构建)**、三模型性能对比、**引擎版本全景（4 引擎×Transformers 兼容性）**、benchmark record CLI |
| `llamacpp-native-vulkan.md` | **llama.cpp Native/Vulkan 部署** - amd395 RDNA3.5 Vulkan、UMA 架构性能特征 |
| `docker-k3s-image-interop.md` | **Docker↔K3S 镜像互通** - 双存储问题诊断、Docker-only 自动检测（engine scan）、containerd 预检查 + 自动导入、imagePullPolicy:IfNotPresent、deploy 只读检查、pattern 合并修复 |
| `engine-scanner-debugging.md` | **Engine Scanner 调试** - 4 个 Bug（K3S crictl fallback、tag-aware pattern 匹配、双 anchor `^$` 修复、stale DB 清理）、pattern 设计指南、deploy 前数据流水线（scan→resolve→deploy）、CPUArch 传递链 |

## 模型检测

| 文件 | 描述 |
|------|------|
| `model-parameter-detection.md` | **模型参数检测** - GGUF 二进制格式解析（header/KV/ARRAY）、MoE 参数公式（expert+shared+router+GQA）、Dense 精算、VLM text_config 嵌套、compressed-tensors 量化、jsonInt 类型扩展、attnParamsPerLayer 去重 |

## Agent (L3a) 验证

| 文件 | 描述 |
|------|------|
| `agent-l3a-validation.md` | **Go Agent 全量验证** - 40 测试点 (8 phases)、Config 持久化 + Hot-Swap、单轮/多轮工具调用、安全 Guardrails (destructive block + param-level block)、LAN Proxy 通道、审计日志 |

## Fleet 与服务发现

| 文件 | 描述 |
|------|------|
| `fleet-llm-discovery.md` | **Fleet LLM 端点自动发现** - DiscoverFunc 注入模式、降级链设计（本地→Fleet mDNS→错误）、hot-swap baseURL、mDNS 默认开启 |

## AIMA 开发原则

| 文件 | 描述 |
|------|------|
| `aima-design-principles-review.md` | **设计原则实战案例** - INV-1 典型违反/修复、YAML-driven 模式对照、INV-3 边界 |

## NPU 相关

| 文件 | 描述 |
|------|------|
| `npu-experience-summary.md` | **NPU 探索经验总结** - 核心经验、工具链安装、常见问题 |
| `amd395-npu-status.md` | AMD NPU 硬件探索记录 - 固件对比、性能预期 |
| `npu-phase-plan.md` | NPU Phase 计划 - 3a 已完成，3b 待进行 |
| `npu-phase-3a-validation.md` | Phase 3a 详细验证记录 |
| `npu-kernel-driver-ops.md` | NPU 内核驱动操作记录 |

---

## 快速参考

### K3S GPU Pod 常见问题

| 症状 | 根因 | 解法 |
|------|------|------|
| vllm-blackwell exit 137 | NGC 镜像 ENTRYPOINT 包装 | pod spec 用 `command:` 不用 `args:` |
| 引擎忽略 max_model_len 等参数 | Config 值未转为 CLI flags | 检查 podgen.go 的 flag 追加逻辑 |
| Liveness probe 在加载中途杀死 pod | `initialDelaySeconds` 太小 | 用 YAML `health_check.timeout_s` |
| vLLM 静默 SIGKILL | `/dev/shm` 64MB 不足 | pod spec 加 Memory emptyDir |
| `cuInit` 返回 803 | CUDA compat stub 覆盖真实驱动 | env: `LD_LIBRARY_PATH=/lib/x86_64-linux-gnu:...` |
| Pod 挂载空模型目录 | Catalog 命中但缺 DB 路径 | resolveWithFallback 联合查询 Catalog + DB |
| arm64 路径错误 | LD_LIBRARY_PATH 硬编码 x86 | `libDirForArch()` 根据 CPUArch 适配 |
| 大镜像拉取中途 EOF | docker.io 镜像源单点故障 | registries.yaml 配 4 个镜像源轮询 |
| 大 max_model_len 启动 OOM | encoder profiling 262K+16 images | `enable_chunked_prefill: true`（降至 16K profiling）|
| vLLM CLI 参数 JSON 解析失败 | pod 模板不支持嵌套 JSON | 绕过：不用该参数，用 chunked_prefill 替代 |
| 超大图片 OOM 崩溃 | 单图 >3424px encoder 内存溢出 | 限制输入 ≤3072px 或 resize |
| Pod restart 后请求失败 | Pod IP 或模型名变化 | 重新查询 `kubectl get pod -o jsonpath` |
| ImagePullBackOff (本地有镜像) | Docker store ≠ K3S containerd | `sudo aima engine scan`（自动导入）或手动 `docker save \| sudo k3s ctr import` |
| imagePullPolicy Always 拉取失败 | K8s :latest 默认 Always | podgen 已设 `imagePullPolicy: IfNotPresent` |
| engine scan 漏扫 NGC 镜像 | 同 type 多 YAML patterns 覆盖 | 已修复：patterns append 合并（不覆盖）|
| model 参数 0B / class unknown | GGUF 未解析 / dense 粗算偏差 | parseGGUFMeta + calculateDenseParamsFromConfig |
| kv_cache_dtype not supported | FLASH_ATTN 不支持 FP8 KV cache (混合架构) | 移除 `kv_cache_dtype`，让 vLLM auto |
| 新模型架构 not found | NGC stable 版本太旧 | 改用 `vllm-nightly` 引擎 |
| `Transformers does not recognize` model_type | vLLM pin transformers <5 | 用 spark-vllm-docker `--tf5` 构建 |
| 128K OOM 崩溃 (59GB 模型) | model + KV cache > 128GB unified | 降 `max_model_len` 或用量化版本 |
| disk-pressure taint 阻止调度 | 磁盘 >85% 使用 | 清理镜像/模型，等 5min taint 解除 |
| aima status 显示 running 但实际崩溃 | CrashLoopBackOff Pod Phase 仍是 Running | `podToStatus` 检查 Message 中的 waiting reason |
| aima status 显示 starting 不变 | ImagePullBackOff Pod Phase 是 Pending | 同上：Message 含 failure reason 时 phase→failed |
| engine scan 扫不到 K3S 镜像 | `crictl` 未安装，K3S 用 `k3s crictl` | `runCrictl()` fallback 已修复 |
| 同 repo 不同 tag 引擎分类错误 | repo-only pattern 无法区分 tag | 用 tag-aware pattern（含 `:`）|
| `^pattern$` 不匹配任何镜像 | 旧代码无双 anchor 处理 | `patternMatch()` 4-way switch 已修复 |
| 删除/rename 镜像后 list 仍显示 | scan 只 upsert 不清理 | `MarkEnginesUnavailableExcept` 软删除 |
| deploy 模型路径空 → 挂载空目录 | 未执行 `model scan` 注册路径 | 先 `aima model scan`，再 deploy |
| arm64 Pod LD_LIBRARY_PATH 错误 | CPUArch 未从 hwInfo 传到 Pod spec | 检查 DeployRequest.CPUArch 赋值 |

### sudo 密码含特殊字符时

```bash
# 本地编码
echo -n 'your$password#' | base64

# SSH 远程使用
ssh user@host 'echo <base64> | base64 -d | sudo -S <command> 2>&1'
```

### K3S Pod IP 访问

K3S pod 没有 hostPort，用 pod IP：
```bash
PODIP=$(kubectl get pod <name> -o jsonpath='{.status.podIP}')
curl http://$PODIP:8000/health
```

### llama.cpp Vulkan 确认 GPU 已启用

```bash
# 启动日志应包含
ggml_vulkan: Found 1 Vulkan devices:
ggml_vulkan: 0 = Radeon 8060S Graphics (RADV GFX1151)

# 进程命令行必须包含
--n-gpu-layers 999
```

### NPU5 工具链（amd395）

```bash
source ~/mlir-aie/ironenv/bin/activate
export PEANO_INSTALL_DIR=~/mlir-aie/ironenv/lib/python3.12/site-packages/llvm-aie
export PATH=$PEANO_INSTALL_DIR/bin:$PATH

# 编译 GEMM (NPU5/AIE2P)
cd ~/mlir-aie/programming_examples/basic/matrix_multiplication/single_core
make devicename=npu2 use_iron=1 M=64 K=64 N=64 m=32 k=32 n=32
```

### 设备类型对照

| 设备 | Device ID | MLIR 目标 |
|------|-----------|-----------|
| NPU5 (Strix Halo) | 17f0_11 | npu2 |
| NPU4 (Phoenix) | 1502 | npu |

---

### 跨机推理（aima serve LAN Proxy）

```bash
# 在推理节点启动 proxy（自动发现 K3S pods）
ssh qujing@192.168.110.70 'nohup ~/aima serve &'
# LAN 访问（注意 model 用 vLLM 注册名 /models，不是 AIMA 名）
curl http://192.168.110.70:8080/v1/chat/completions \
  -d '{"model":"/models","messages":[{"role":"user","content":"hello"}]}'
```

### 性能数据录入（benchmark record CLI）

```bash
aima benchmark record \
  --hardware nvidia-gb10-arm64 --engine vllm-nightly --model qwen3.5-35b-a3b \
  --throughput 29.6 --ttft-p50 498 --tpot-p50 33.5 --vram 67100 \
  --input-bucket 1K --concurrency 1 --samples 3 \
  --notes "128K context test, vLLM v0.16.0rc2"
```

### 性能测量（TTFT/TPOT）

```bash
# Python streaming 精确测量（在 K3S node 上执行）
python3 -c "
import requests, time, json
url='http://<pod_ip>:8000/v1/completions'
payload={'model':'<name>','prompt':'Explain AI.','max_tokens':200,'temperature':0.1,'stream':True}
for r in range(3):
    t0=time.perf_counter(); ft=None; n=0
    for l in requests.post(url,json=payload,stream=True).iter_lines():
        if l and l.startswith(b'data: '):
            c=l[6:]
            if c==b'[DONE]': break
            if json.loads(c).get('choices',[{}])[0].get('text'):
                if not ft: ft=time.perf_counter()
                n+=1
    t1=time.perf_counter()
    print(f'Run {r+1}: TTFT={(ft-t0)*1e3:.0f}ms TPOT={((t1-ft)/(n-1))*1e3:.1f}ms {n/(t1-t0):.1f}tok/s')
"
```

### 系统性修复原则

遇到部署 Bug → 追溯 Go 代码根因 → 修改代码/YAML（非单机 workaround）→ 全量验证

---

更新：2026-02-28（Qwen3-Coder-Next 80B 部署+性能测试 + kv_cache/disk-pressure/架构兼容修复）
