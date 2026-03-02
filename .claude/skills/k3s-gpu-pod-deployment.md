# K3S GPU Pod 部署经验

> 覆盖 vLLM / 通用 GPU 推理容器在 K3S 上的部署调试经验
> 基于：gb10 (GB10 Grace-Blackwell arm64) + linux-1 (2× RTX 4090 x86_64) + amd395 (AMD Ryzen AI MAX+ 395)
> 日期：2026-02-28（更新：2026-03-01 linux-1 TP=2 双卡部署 + CLI bugs）

---

## 一、4 个 Pod 部署 Bug 及修复

### Bug 1：NGC 镜像 ENTRYPOINT 包装问题（vllm-blackwell exit 137）

**现象**：Pod 启动后立即以 exit 137 (SIGKILL) 终止，日志显示 shard 进程异常。

**根因**：NGC 镜像（`nvcr.io/nvidia/vllm:26.01-py3`）将 ENTRYPOINT 设为
`/opt/nvidia/nvidia_entrypoint.sh`，K8s 的 `args:` 字段将命令传给这个 shell 包装脚本，
而不是直接传给 vllm。包装脚本对参数解析不兼容，导致实际进程启动失败后被 SIGKILL。

**修复**：Pod spec 从 `args:` 改为 `command:`，强制覆盖 ENTRYPOINT：
```yaml
# 错误：command 传给了 shell 包装脚本
args:
  - "vllm"
  - "serve"
  - "/models"

# 正确：完全覆盖 ENTRYPOINT
command:
  - "vllm"
  - "serve"
  - "/models"
```

**AIMA 代码位置**：`internal/knowledge/podgen.go` — pod 模板中 `args:` 改为 `command:`。

**推广**：所有 NGC 镜像（`nvcr.io/nvidia/*`）都有此问题，必须用 `command:` 而不是 `args:`。

---

### Bug 2：Config 值未转换为 CLI flags（引擎以默认值运行）

**现象**：vllm 以 `max_model_len=40960`（内置默认值）运行，忽略了 YAML 中配置的 `max_model_len: 8192`。

**根因**：`ResolvedConfig.Config` 中的 `default_args`（来自 engine YAML）从未被转换成
CLI flags 传给引擎进程。

**修复**：在 `podgen.go` 和 `native.go` 中，将 `Config` map 按 key 排序后追加为 CLI flags：
```go
keys := sortedKeys(resolved.Config)
for _, k := range keys {
    if k != "port" {  // port 单独处理
        args = append(args, "--"+strings.ReplaceAll(k, "_", "-"),
                      fmt.Sprintf("%v", resolved.Config[k]))
    }
}
```

**推广**：K3S 和 native runtime 两条路径都需要这个转换，必须保持对称。

---

### Bug 3：Liveness Probe 在模型加载中途杀死 Pod

**现象**：Pod 在 80% shard 加载时（约 80 秒）被杀死，K8s 事件显示 liveness probe failed。

**根因**：`initialDelaySeconds: 30` + `failureThreshold: 3 × periodSeconds: 10` = 60 秒总宽限期。
Qwen3-8B 在 GB10 需要 ~85 秒加载，容器在加载完成前就被判定为不健康并杀死。

**修复**：用 engine YAML 的 `health_check.timeout_s` 作为 `initialDelaySeconds`：
```go
// podgen.go
data.HealthCheckInitDelaySec = resolved.HealthCheck.TimeoutS
// vllm-ada.yaml 中设置 timeout_s: 300 → initialDelaySeconds: 300
```

**推广**：**永远不要硬编码 `initialDelaySeconds: 30`**。不同模型加载时间差异极大（7B=85s，70B可达10min+）。

---

### Bug 4：/dev/shm 默认 64MB 不足（vLLM V1 多进程 IPC）

**现象**：vLLM V1 引擎（APIServer + EngineCore_DP0 子进程）启动时被 SIGKILL，无错误日志。

**根因**：vLLM V1 使用 POSIX 共享内存（`/dev/shm`）在主进程和推理子进程间传递 weights。
K8s 默认 `/dev/shm` 为 64MB，远小于模型 weight 传输需求，导致写 shm 时内存不足触发 SIGKILL。

**修复**：在 pod spec 中添加 Memory-backed emptyDir 覆盖 `/dev/shm`：
```yaml
volumeMounts:
  - name: dshm
    mountPath: /dev/shm
volumes:
  - name: dshm
    emptyDir:
      medium: Memory
```

**NVIDIA 也有提示**：`nvidia-smi` 输出 "SHMEM allocation limit is set to default of 64MB, may be insufficient for vLLM"。

**推广**：任何使用多进程 IPC 的推理引擎都需要此配置（vLLM V1、某些 TensorRT-LLM 配置等）。

---

### Bug 5：Catalog 模型路径解析空值（模型挂载到错误目录）

**现象**：Pod 启动报 `Invalid repository ID or local directory specified: '/models'`。
hostPath 挂载了 `~/.aima/models/qwen3.5-35b-a3b`（K8s DirectoryOrCreate 自动创建的空目录），
但实际模型文件在 `/mnt/data/models/Qwen3.5-35B-A3B`。

**根因**：`resolveWithFallback()` 命中 catalog 后，`resolved.ModelPath` 为空（因为 catalog YAML
不记录文件系统路径，路径来自 `model scan/import` 写入 DB 的记录）。后续 podgen 用空路径拼出了错误的
hostPath，K8s 的 `DirectoryOrCreate` 创建了一个空目录。

**修复**：catalog 命中后，额外查询 DB 获取实际注册路径：
```go
resolved, err := cat.Resolve(hw, modelName, engineType, overrides)
if err == nil {
    if resolved.ModelPath == "" {
        if dbModel, dbErr := db.FindModelByName(ctx, modelName); dbErr == nil && dbModel.Path != "" {
            resolved.ModelPath = dbModel.Path
        }
    }
    return resolved, modelName, nil
}
```

**AIMA 代码位置**：`cmd/aima/main.go` — `resolveWithFallback()` 函数。

**教训**：Catalog YAML 和 SQLite DB 各自持有模型的不同维度信息。Catalog 提供
engine/model/hardware 匹配和配置参数，DB 提供本地文件系统的实际路径。两者必须联合查询。

---

### Bug 6：LD_LIBRARY_PATH 硬编码 x86_64 架构路径 → **已通过厂商无关重构彻底解决**

**现象**：GB10 (arm64) 上 Pod 内 CUDA 初始化失败，`LD_LIBRARY_PATH` 指向
`/lib/x86_64-linux-gnu`（不存在于 arm64 系统）。

**初始修复**（已废弃）：`libDirForArch()` 辅助函数根据 CPUArch 返回正确路径。

**最终修复**（vendor-agnostic 重构，2026-02-28）：
- 删除 `libDirForArch()` 和 `LibDir` 字段
- 所有环境变量（含 `LD_LIBRARY_PATH`）移到 Hardware Profile YAML 的 `container.env` 字段
- 每个硬件 Profile 各自定义正确的架构路径：
  - arm64: `LD_LIBRARY_PATH: "/lib/aarch64-linux-gnu:..."`（`nvidia-gb10-arm64.yaml`）
  - x86_64: `LD_LIBRARY_PATH: "/lib/x86_64-linux-gnu:..."`（`nvidia-rtx4060-x86.yaml`）
- Pod 模板通用渲染 `container.env`，不含架构分支代码

**设计改进**：这个 Bug 的根因是 Go 代码中存在厂商特定逻辑。vendor-agnostic 重构将
所有厂商/架构特定配置移到 YAML，彻底消除了此类 Bug 的可能性。

**教训**：当在 Go 代码中写出 `if cpuArch == "arm64"` 这类分支时，说明配置应该在 YAML 中，
而非代码中。符合 AIMA 架构不变量 INV-1/INV-2。

---

### Bug 7：Docker.io 镜像拉取单点故障（大镜像拉取中断）

**现象**：`vllm/vllm-openai:qwen3_5-cu130`（~8.8GB）通过 daocloud 镜像拉取 1 小时后
`unexpected EOF`，整个拉取失败需重来。

**根因**：GB10 的 K3S `registries.yaml` 只配了一个 docker.io 镜像源（daocloud）。
大 layer（~3.4GB）拉取耗时长，单一镜像源在传输中途断开连接。

**修复**：扩展为 4 个镜像源轮询：
```yaml
mirrors:
  docker.io:
    endpoint:
      - "https://docker.1ms.run"
      - "https://docker.m.daocloud.io"
      - "https://docker.nju.edu.cn"
      - "https://docker.rainbond.cc"
```

**注意**：
- `k3s ctr images pull` 绕过 registries.yaml，直连 docker.io（国内被墙）
- 只有 kubelet 拉取（Pod scheduled 触发）才走 registries.yaml 配置
- 修改 registries.yaml 后需 `sudo systemctl restart k3s` 生效
- 每次添加新的大镜像（>5GB）都建议提前确认镜像源可用

---

## 二、CUDA Error 803（cudaErrorSystemDriverMismatch）完整诊断

### 症状

K3S 容器（使用 `runtimeClassName: nvidia`）中 vLLM 启动时报：
```
torch.cuda.CudaError: initialization error (or: CUDA driver version is insufficient)
# 底层错误码: cuInit(0) → 803
```

### 根因链

```
K3S CDI 注入路径：
  /lib/x86_64-linux-gnu/libcuda.so.550.135  ← 真实驱动（Host Driver 550.135）

ldcache 中 stub 优先于真实驱动：
  /usr/local/cuda-12.4/compat/libcuda.so.550.54.15  ← CUDA forward-compat stub
  （为不同的 550.x 点版本构建，与 Host Driver 550.135 不兼容）

结果：dynamic linker 加载了 stub → cuInit 返回 803
```

**关键诊断命令**：
```bash
# 在容器内运行（临时 debug pod）
ldconfig -p | grep libcuda        # 查看 ldcache 顺序
ls -la /lib/x86_64-linux-gnu/libcuda*  # 查看 CDI 注入的真实驱动
python3 -c "
import ctypes
lib = ctypes.cdll.LoadLibrary('libcuda.so.1')
ret = lib.cuInit(0)
print('cuInit:', ret)  # 0=成功, 803=失败
"
```

### 修复

**当前实现**（vendor-agnostic 重构后）：这些 env vars 定义在 Hardware Profile YAML 的 `container.env` 中，
由 Pod 模板通用渲染，不再硬编码在 Go 代码中。

NVIDIA x86_64 Profile (`nvidia-rtx4060-x86.yaml`):
```yaml
container:
  env:
    NVIDIA_VISIBLE_DEVICES: "all"
    NVIDIA_DRIVER_CAPABILITIES: "all"
    LD_LIBRARY_PATH: "/lib/x86_64-linux-gnu:/usr/local/nvidia/lib:/usr/local/nvidia/lib64"
```

NVIDIA arm64 Profile (`nvidia-gb10-arm64.yaml`):
```yaml
container:
  env:
    NVIDIA_VISIBLE_DEVICES: "all"
    NVIDIA_DRIVER_CAPABILITIES: "all"
    LD_LIBRARY_PATH: "/lib/aarch64-linux-gnu:/usr/local/nvidia/lib:/usr/local/nvidia/lib64"
```

每个架构的正确路径在各自的 Hardware Profile YAML 中定义，消除了 Go 代码中的架构分支。

---

## 三、linux-1 多 GPU 管理（GPU 被其他进程占用）

### 场景

linux-1 有 2× RTX 4090：
- GPU 0：被 `ftransformers::scheduler`（DeepSeek-V3）长期占用 26GB，仅剩 ~23GB
- GPU 1（UUID: `GPU-dbf28a52-9613-ba5b-f7f3-a344693ad3cd`）：49GB 空闲

### 诊断

```bash
# 查看所有 GPU 上的进程
nvidia-smi --query-compute-apps=pid,used_memory,name --format=csv,noheader

# 查看每个 GPU 的内存状态
nvidia-smi -q -d MEMORY | grep -A5 "GPU 0\|GPU 1\|Free\|Total"
```

### 绕过方案（手动固定 GPU）

修改 Pod YAML，通过 UUID 固定到 GPU 1：
```yaml
env:
  - name: NVIDIA_VISIBLE_DEVICES
    value: "GPU-dbf28a52-9613-ba5b-f7f3-a344693ad3cd"
```

### 长期方案

安装 NVIDIA device plugin，让 K8s 自动管理 GPU 分配：
```bash
kubectl apply -f https://raw.githubusercontent.com/NVIDIA/k8s-device-plugin/main/deployments/static/nvidia-device-plugin.yml
```

---

## 四、K3S 用户级访问配置

K3S kubeconfig 默认仅 root 可读 (`/etc/rancher/k3s/k3s.yaml`)。

```bash
# 为普通用户配置 kubectl 访问
mkdir -p ~/.kube
sudo cp /etc/rancher/k3s/k3s.yaml ~/.kube/config
sudo chown $USER ~/.kube/config
# 如果是通过 Tailscale 访问，替换 server IP
sed -i 's/127.0.0.1/<ACTUAL_IP>/g' ~/.kube/config
```

**重要**：`sudo ~/aima deploy` 会使用 root 的 DB（`/root/.aima/`），模型路径可能为空。
必须以注册模型的普通用户运行 `~/aima deploy`。

---

## 五、镜像 CUDA 版本查询

```bash
# 查看 K3S containerd 中镜像的 CUDA 版本
sudo ctr -n k8s.io images ls | grep vllm   # 列出已拉取的镜像
sudo ctr -n k8s.io run --rm --net-host \
  docker.io/vllm/vllm-openai:v0.8.5 check \
  env | grep -E 'CUDA|NVIDIA'
```

---

## 六、性能基线（2026-02-28）

### 测试方法

```bash
PODIP=<pod_ip>; PORT=8000
MODEL=$(curl -s http://$PODIP:$PORT/v1/models | python3 -c "import sys,json; print(json.load(sys.stdin)['data'][0]['id'])")
for i in 1 2 3; do
  START=$(date +%s%N)
  RESP=$(curl -s -X POST http://$PODIP:$PORT/v1/completions \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"$MODEL\",\"prompt\":\"Explain the history of artificial intelligence.\",\"max_tokens\":200,\"temperature\":0.1}")
  END=$(date +%s%N)
  MS=$(( (END - START) / 1000000 ))
  TOK=$(echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['usage']['completion_tokens'])" 2>/dev/null)
  echo "Round $i: ${MS}ms, ${TOK} tokens, $(python3 -c "print(round(${TOK:-0}*1000.0/${MS},1))") tok/s"
done
```

**注意**：K3S pod 没有 hostPort，必须用 **pod IP** 访问，不是 localhost。

### 结果

| 设备 | 引擎 | 模型 | tok/s | TTFT | TPOT | 备注 |
|------|------|------|-------|------|------|------|
| gb10 (GB10 arm64) | vllm v0.8.5 | qwen3-8b bf16 | **13.8** | — | — | K3S pod IP |
| gb10 (GB10 arm64) | vllm-nightly qwen3_5-cu130 | qwen3.5-35b-a3b bf16 (MoE 35B/3B) | **29.6** | 96-133ms | 31.5-33.6ms | K3S pod IP, VRAM 65.53GiB |
| linux-1 (RTX 4090) | vllm v0.8.5 | qwen3-8b bf16 | **53.2** | — | — | K3S pod IP，GPU 1 only |
| linux-1 (2×RTX 4090) | vllm-nightly qwen3_5-cu130 | qwen3.5-35b-a3b bf16 TP=2 (MoE 35B/3B) | **174** | 41-83ms | 5.7-5.8ms | TP=2 双卡, VRAM 44GiB×2, no NVLink |

**MoE 大模型注意事项**：
- MoE 架构（如 Qwen3.5-35B-A3B）总参数 35B 但激活仅 3B，TTFT/TPOT 远优于同规模 Dense 模型
- 冷启动首次请求极慢（~3 tok/s），因 torch.compile 缓存未预热，后续稳定在 ~30 tok/s
- `enable_chunked_prefill: false` 对 MoE 有显著性能影响，需关闭
- GB10 128GB 统一内存可容纳 bf16 全精度（~72GB），无需量化

---

## 七、sudo 密码传递技巧（特殊字符密码）

密码含 `$#` 等 shell 特殊字符时，使用 base64 避免转义问题：

```bash
# 本地编码
echo -n 'your_password' | base64

# 远程使用（SSH 命令内）
ssh user@host 'echo <base64> | base64 -d | sudo -S <command> 2>&1'
```

---

## 八、常见问题速查

| 问题 | 诊断命令 | 解决方案 |
|------|---------|---------|
| Pod 一直 ContainerCreating | `kubectl describe pod <name>` | 查看 Events，通常是镜像拉取失败 |
| Pod CrashLoopBackOff | `kubectl logs <pod> --previous` | 看上一次的日志 |
| cuInit 返回 803 | `ldconfig -p \| grep libcuda` | 检查 compat stub 是否覆盖了真实驱动 |
| "No available memory for cache blocks" | `nvidia-smi` | 其他进程占用 GPU，切换到其他 GPU |
| Liveness probe killed | `kubectl describe pod` 看 Events | 增大 `initialDelaySeconds` |
| vLLM 静默 SIGKILL | 看 dmesg 或 k8s events | 检查 /dev/shm 大小（vLLM V1 需要大 shm）|
| Pod 挂载空目录 | `ls <hostPath>` 检查是否空 | 检查 resolveWithFallback 是否查询了 DB 路径 |
| 镜像拉取中途 EOF | `kubectl describe pod` 看 Events | 添加多个 docker.io 镜像源到 registries.yaml |
| arm64 CUDA 初始化失败 | 检查 LD_LIBRARY_PATH 内路径 | 确认 libDirForArch 返回正确的 aarch64 路径 |
| aima status 显示 running 但实际崩溃 | `kubectl describe pod` 查 State.Waiting | CrashLoopBackOff 的 Pod Phase 是 Running，不是 Failed（见 Bug 10）|
| aima status 显示 starting 不变 | `kubectl get pod -o json` 查 containerStatuses | ImagePullBackOff 的 Pod Phase 是 Pending，不变为 Failed（见 Bug 10）|

---

## 九、HAMi 兼容性与 skip_profiles 机制

### GB10 不兼容 HAMi

HAMi v2.4.1 device plugin 在 GB10 上 CrashLoopBackOff：
```
NVML_ERROR_NOT_SUPPORTED: unified memory 设备不支持标准 NVML 调用
```

### 解决方案：conditions.skip_profiles

在 `catalog/stack/hami.yaml` 中配置跳过条件：
```yaml
conditions:
  skip_profiles:
    - Blackwell-arm64    # GB10: unified memory 不兼容 NVML
```

Go 代码中 `shouldSkip()` 函数读取此字段，匹配当前设备 profile 后跳过安装。
**注意**：不要用 `exclude_gpu_arch`（Go struct 未解析此字段）。

### 删除已安装的 HAMi

```bash
kubectl delete namespace hami-system --wait=true
# 验证
kubectl get pods -A | grep hami
```

---

## 十、TTFT/TPOT 精确测量方法

### Python Streaming 测量脚本

```python
import requests, time, json

url = "http://<pod_ip>:8000/v1/completions"
payload = {
    "model": "<model_name>",
    "prompt": "Explain quantum computing in simple terms.",
    "max_tokens": 200,
    "temperature": 0.1,
    "stream": True
}

for run in range(3):
    t0 = time.perf_counter()
    first_token_time = None
    tokens = 0
    resp = requests.post(url, json=payload, stream=True)
    for line in resp.iter_lines():
        if line and line.startswith(b"data: "):
            chunk = line[6:]
            if chunk == b"[DONE]":
                break
            data = json.loads(chunk)
            if data.get("choices", [{}])[0].get("text"):
                if first_token_time is None:
                    first_token_time = time.perf_counter()
                tokens += 1
    total = time.perf_counter() - t0
    ttft = (first_token_time - t0) * 1000 if first_token_time else 0
    tpot = ((total - (first_token_time - t0)) / max(tokens - 1, 1)) * 1000
    print(f"Run {run+1}: TTFT={ttft:.0f}ms, TPOT={tpot:.1f}ms, "
          f"{tokens} tokens in {total:.1f}s = {tokens/total:.1f} tok/s")
```

### 指标定义

| 指标 | 定义 | 意义 |
|------|------|------|
| TTFT | 从请求发出到首个 token 返回的时间 | 用户感知的响应延迟 |
| TPOT | 首个 token 之后，每个后续 token 的平均间隔 | 生成速度的稳定性 |
| Throughput | 总 tokens / 总时间（含 TTFT） | 整体吞吐量 |

---

## 十一、核心经验：系统性修复 vs 逐台修复

> **"不能只是简单的改一台机器，有啥用呢"**

### 原则

遇到部署问题时，**绝不**做单机 workaround（如 symlink、手动环境变量）。
必须追溯到 AIMA 代码中的根因，做系统性修复。

### 检查链

发现 Bug 后的完整检查流程：
1. **定位根因**：不要只看表面现象，追踪完整调用链（CLI → MCP tool → resolver → podgen）
2. **评估影响面**：这个 Bug 是否在其他设备/架构/模型上也会触发？
3. **修复代码**：在 Go 代码或 YAML 中修复，而不是在单台机器上做 workaround
4. **更新 Catalog**：如果涉及硬件差异，更新 YAML knowledge base
5. **全量验证**：按 CLAUDE.md 中的 "ALL COLLECT, THEN ANALYZE" 流程，所有设备重新测试
6. **记录 Skills**：将 Bug 模式和修复方案记录到 skills，防止复发

### 本次修复的三个系统性 Bug 示例

| Bug | 单机 Workaround（❌） | 系统性修复（✅） |
|-----|----------------------|----------------|
| 模型路径空值 | 在 GB10 上创建 symlink | resolveWithFallback 联合查询 Catalog + DB |
| LD_LIBRARY_PATH | 在 GB10 pod 里手动设环境变量 | libDirForArch() 根据 CPUArch 自动适配 |
| 镜像源单点故障 | 手动重试拉取 | registries.yaml 配置 4 个镜像源轮询 |

---

## 十二、256K 最大上下文部署经验（GB10 Qwen3.5-35B-A3B）

### 背景

Qwen3.5-35B-A3B 的 `max_position_embeddings: 262144`（256K tokens），但初始部署仅用了 128K。
目标：推到模型官方最大 262144 tokens，验证稳定性和性能衰减曲线。

### Bug 8：Multimodal Encoder Profiling OOM（262K + chunked_prefill=false）

**现象**：Pod 启动时 GB10 整机卡死（SSH 超时），K3S node 不响应，需等待 crash loop 间隙才能操作。

**根因**：vLLM 在启动时会 profiling multimodal encoder cache。当 `max_model_len=262144` 且
`enable_chunked_prefill=false` 时，encoder 尝试分配 262,144 tokens × 16 images 的缓存，
远超 GB10 的 128GB 统一显存。

**日志关键行**：
```
encoder cache budget of 262144 tokens, profiled with 16 image items
```

**修复**：
- `enable_chunked_prefill: true` — 将 encoder profiling budget 从 262,144 降至 16,384 tokens
- `gpu_memory_utilization: 0.8 → 0.9` — 释放更多 KV cache 空间（37.42 GiB，支持 490K tokens）
- 结果：KV cache 可支持 7.37 个 262K 并发请求

**教训**：multimodal 模型开大上下文时，`enable_chunked_prefill: true` 是**必须**的。否则 encoder
profiling 会按 max_model_len 全量分配，几乎确定 OOM。

### Bug 9：Pod 模板无法传递 JSON 格式 CLI 参数

**现象**：`--limit-mm-per-prompt image=1` 报 `ValueError: cannot be converted to json.loads`。
vLLM 实际期望 `--limit-mm-per-prompt '{"image": 1}'`（JSON 格式）。

**根因**：AIMA pod 模板用 `- "{{ . }}"` 包裹 CLI args。JSON 值 `{"image": 1}` 含双引号，
嵌套在 YAML 字符串中导致解析错误。

**当前状态**：**未修复（tech debt）**。绕过方案：不使用 `limit_mm_per_prompt`，依赖 chunked_prefill
自动限制 encoder 内存。

**长期修复建议**：pod 模板对 JSON 类型的 config value 做特殊处理（单引号包裹或 YAML flow notation）。

### chunked_prefill 性能影响

| 场景 | chunked_prefill=false | chunked_prefill=true | 影响 |
|------|---------------------|---------------------|------|
| 短 context (1K) TTFT | 0.498s | 1.085s | **2.2x 慢** |
| 中 context (16K) TTFT | 2.254s | 3.727s | **1.7x 慢** |
| 长 context (128K) TTFT | 34.015s | 37.829s | **1.1x 几乎无差** |
| TPOT (各长度) | ~33-41ms | ~34-42ms | 基本一致 |

**结论**：chunked_prefill 对短 context TTFT 有固定开销（~0.5s），但对长 context 几乎无影响。
在需要支持 256K 的场景下，开启 chunked_prefill 的 TTFT 开销完全可接受。

### 256K 上下文实测性能

```
260K tokens: TTFT=131.85s, TPOT=55.9ms, decode=17.9 tok/s
261K tokens: TTFT=132.86s, TPOT=55.9ms, decode=17.9 tok/s (接近理论上限)
```

**模型在 261K 下稳定运行，无崩溃、无 OOM。**

### 极限上下文测试技巧：Token 校准

直接用大量重复文本填充 prompt 时，难以精确控制 token 数。方法：

```python
# 1. 先校准：用较少重复数发一次请求（max_tokens=1），从 usage.prompt_tokens 获取实际 token 数
# 2. 计算每 repeat 的 token 数
# 3. 根据 max_model_len - max_tokens 反算目标 repeat 数

# 校准示例
base = "The quick brown fox jumps over the lazy dog. " * 100
prompt = base * 200  # 校准用
# 结果: 200 repeats = 200,015 tokens → 每 repeat ~1000 tokens
# 目标 261K → 261 repeats
```

### SSH 端口转发绕过 Proxy

当 `aima serve` 代理的模型名路由有问题时，可直接 SSH 端口转发到 pod IP：

```bash
# 建立隧道（本地 18080 → GB10 pod 10.42.0.77:8000）
ssh -f -N -L 18080:10.42.0.77:8000 qujing@100.105.58.16

# 直接访问 vLLM，绕过 proxy
curl http://localhost:18080/v1/chat/completions \
  -d '{"model":"/models","messages":[...]}'
```

---

## 十三、VLM 视觉能力测试经验（Qwen3.5-35B-A3B, GB10）

### 视觉 Token 化规律

vLLM 对输入图片做内部 resize 后切成固定 patch，token 数与原始分辨率的关系**不是线性的**：

| 原始分辨率 | Prompt Tokens | 说明 |
|-----------|-------------|------|
| 64~256px | ~85 | 最小 patch 下限，低于此分辨率的图都一样 |
| 512px | ~350 | 第一个有意义的分辨率档 |
| 1024px | ~4,117 | **主力档位**，每张 ≈ 1,025 tokens |
| 2048px | ~4,117 | 与 1024 相同！内部 resize 到同一 patch grid |
| 3072px | ~9,237 | 跳到更高分辨率 tier，token 翻倍 |
| 3424px | ~11,467 | 接近单图上限 |

**关键发现**：
- 1024 和 2048 产生**几乎一样**的 token 数 (~4117)，说明 vLLM 内部对 ≤2048 的图做同样的 resize
- 3072+ 跳到新的分辨率 tier，token 数陡增
- 送大于 1024 的图除非需要 3072+ tier 的精度，否则是浪费 TTFT

### 单图分辨率上限

**3424×3424 (11.7M px) OK，3456×3456 OOM 崩溃。**

崩溃机制：vLLM encoder 在处理超大图时需要的中间张量超过了可用显存（即使 chunked_prefill=true 也无法拆分 encoder 阶段的计算）。chunked_prefill 只保护 startup profiling，不保护 inference-time 的 encoder。

**二分查找方法**（用于找任意模型的图片上限）：
```python
# 从 2048 开始，每次 +128，直到崩溃
# 崩溃后 pod 会 restart（~6min），用新 pod IP 继续
# 注意：重启后 pod IP 可能变化，必须重新查询
kubectl get pod <name> -o jsonpath="{.status.podIP}"
```

### 多图上限

瓶颈是 **context window**，不是视觉子系统：
- 255 × 1024×1024 = 261,906 tokens（99.9% of 262,144）→ 全部成功
- 时间随 prompt tokens 线性增长：32 张 16s → 128 张 60s → 255 张 157s

### TPOT 不受图片影响

所有视觉测试中 TPOT 稳定在 **33~34ms**，与纯文本完全一致。图片只影响 prefill (TTFT)，不影响 decode。这是因为图片在 prefill 阶段被编码为 token embeddings 后，decode 阶段就只是普通的 KV cache attention。

### 视觉性能测试方法

```python
from PIL import Image, ImageDraw
import io, base64, requests, json, time

def make_test_image(sz, bg=(240,240,240), fg=(255,0,0), shape="circle"):
    img = Image.new("RGB", (sz, sz), color=bg)
    draw = ImageDraw.Draw(img)
    r = sz // 4
    draw.ellipse([sz//2-r, sz//2-r, sz//2+r, sz//2+r], fill=fg)
    buf = io.BytesIO(); img.save(buf, format="PNG")
    return base64.b64encode(buf.getvalue()).decode()

def test_vision(pod_url, model, images_b64, prompt="Describe.", max_tokens=64):
    content = [{"type":"image_url","image_url":{"url":f"data:image/png;base64,{i}"}} for i in images_b64]
    content.append({"type":"text","text":prompt})
    payload = {"model":model,"messages":[{"role":"user","content":content}],
               "max_tokens":max_tokens,"temperature":0.3,"stream":False}
    start = time.time()
    resp = requests.post(f"{pod_url}/v1/chat/completions", json=payload, timeout=300)
    return resp.json(), time.time()-start
```

### Pod 崩溃后的恢复流程

视觉测试可能导致 OOM → pod crash → restart。注意事项：
1. Pod restart 后 **IP 可能变化**，必须重新查询
2. 模型名可能从旧的 `/models` 变成 `served_model_name` 配的名字（重建 pod spec 时生效）
3. 冷启动 warmup 约 60s（首请求 TTFT ~60s），之后恢复正常
4. K3S 自动处理 restart，无需手动干预，但 `restartPolicy: Always` 下的 crash loop backoff 可能延迟恢复

### 视觉测试数据记录位置

```yaml
# catalog/models/qwen3.5-35b-a3b.yaml → expected_performance.vision
vision:
  tokens_per_image_1024: 1025      # 每张 1024x1024 ≈ 1025 tokens
  max_single_resolution: 3424      # 3424x3424 OK, 3456x3456 OOM
  max_images_1024: 255             # 255 × 1024x1024 = 261906 tokens
  single_image_scaling:            # resolution: [ttft_s, prompt_tokens]
    64:    [0.21,  85]
    1024:  [0.66,  4117]
    2048:  [9.78,  4117]
    3072:  [9.30,  9237]
  multi_image_1024:                # N × 1024x1024: [time_s, prompt_tokens]
    1:   [0.40,  350]
    128: [59.60, 131477]
    255: [157.20, 261906]
```

---

## 十四、跨机推理测试（LAN Proxy + Performance Testing Bot）

### aima serve 跨机访问流程

K3S pod 没有 hostPort，外部无法直接访问。`aima serve` 解决了这个问题：

```bash
# 在 GB10 上启动代理（自动发现 K3S pod 并路由）
ssh gb10 'nohup ~/aima serve --addr :8080 --mdns > ~/aima-serve.log 2>&1 &'

# 从任意 LAN 机器调用
curl http://192.168.110.70:8080/status          # 查看后端列表
curl http://192.168.110.70:8080/v1/models       # OpenAI models 接口
curl http://192.168.110.70:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"/models","messages":[...]}'     # 推理调用
```

**关键注意**：`model` 字段必须用 vLLM 后端注册的模型名（如 `/models`），不是 AIMA 的模型名（如 `qwen3.5-35b-a3b`）。代理路由到正确后端，但请求体透传给 vLLM。

### 使用 Performance Testing Bot 做阶梯测试

工具位置：`C:\Users\jguan\Desktop\Performance_Testing_Bot`

```yaml
# config.gb10-context-test.yaml 关键配置
api:
  url: "http://192.168.110.70:8080/v1/chat/completions"
  key: ""
  model: "/models"       # 必须用 vLLM 的模型名

test_matrix:
  rounds_per_combination: 1
  requests_per_round: 3
  combinations:
    - input_length: 1024
      output_length: 128
      concurrency_levels: [1]
    # ... 2K, 8K, 16K, 32K, 64K, 128K

request:
  stream: true           # TTFT 测量必须开启 streaming
  timeout: 600           # 128K 上下文需要较长超时

advanced:
  token_counter: "local_only"  # 避免 API token 计数请求
```

运行：`python main.py config.gb10-context-test.yaml`

**已知问题**：
- TPOT/TPS 为 0：模型名 `/models` 不在 tiktoken 编码器注册表中，导致 output_tokens=0
- 解法：从 `total_latency - ttft` 推算 decode time，再除以 max_tokens 估算 decode speed
- 中间数据在 `.test_cache/<timestamp>/requests.jsonl` 中，可用 Python 解析每请求详情

### 后端 vLLM Prometheus 指标采集

vLLM 暴露 `/metrics` 端点，提供精确的后端指标：

```bash
# 从 K3S node 上采集（pod IP）
curl -s http://10.42.0.73:8000/metrics | grep -E "^vllm:(time_to_first_token|time_per_output|e2e_request|prompt_tokens_total|generation_tokens_total|request_success|kv_cache)"
```

**关键指标**：
| Prometheus 指标 | 含义 |
|----------------|------|
| `vllm:time_to_first_token_seconds_sum/count` | 后端 TTFT 总和/计数 |
| `vllm:request_time_per_output_token_seconds_sum/count` | 后端 TPOT 总和/计数 |
| `vllm:e2e_request_latency_seconds_sum/count` | 端到端延迟 |
| `vllm:prompt_tokens_total` | 累计输入 tokens |
| `vllm:generation_tokens_total` | 累计输出 tokens |
| `vllm:kv_cache_usage_perc` | KV cache 使用率 |
| `vllm:num_preemptions_total` | KV cache preemption 次数 |
| `vllm:request_success_total{finished_reason}` | 请求终止原因分布 |

### benchmark record CLI 数据录入

测试完成后，使用 `aima benchmark record` 将数据写入 SQLite：

```bash
aima benchmark record \
  --hardware nvidia-gb10-arm64 \
  --engine vllm-nightly \
  --model qwen3.5-35b-a3b \
  --device gb10 \
  --input-bucket 1K \
  --output-bucket 128 \
  --throughput 30.0 \
  --ttft-p50 498 \
  --tpot-p50 33.3 \
  --vram 67100 \
  --concurrency 1 \
  --samples 3 \
  --stability stable \
  --notes "context scaling test, vLLM v0.16.0rc2"
```

验证数据：
```bash
sqlite3 -header -column ~/.aima/aima.db \
  "SELECT input_len_bucket, throughput_tps, ttft_ms_p50, tpot_ms_p50 FROM benchmark_results ORDER BY rowid;"
```

### 完整测试流程（端到端）

```
1. aima serve 启动代理     → LAN 可达
2. Performance Testing Bot → 前端测量 (TTFT, E2E)
3. vLLM /metrics           → 后端精确指标
4. 前后端对比分析           → 验证数据一致性
5. aima benchmark record   → 结构化存入 SQLite
6. 更新 model YAML         → context_scaling 写入 catalog
```

### 性能数据的两层存储

| 层 | 存储位置 | 写入方式 | 用途 |
|----|---------|---------|------|
| L0 静态 | `catalog/models/*.yaml` → go:embed | 手动更新 expected_performance | variant 选择参考、Agent 预估 |
| L2 动态 | `~/.aima/aima.db` → benchmark_results | `aima benchmark record` CLI | knowledge.search_configs/compare/gaps 查询 |

---

## 十五、Bug 10：aima status 将失败部署显示为 running/starting

### 现象

amd395 上 `aima status` 显示 pod phase 为 `"running"`，但实际容器在 CrashLoopBackOff 不断重启。
用户看到的效果是 "一直在 deploy"，无法判断部署已失败。

### 根因：K8s Phase 与容器状态的不一致

K8s Pod Phase 不直接反映容器的 waiting reason：

| 容器状态 | Pod Phase | AIMA 修复前 | AIMA 修复后 |
|----------|-----------|------------|------------|
| CrashLoopBackOff | **Running** (Pod 已调度，容器在重启循环中) | "running" | "failed" |
| ImagePullBackOff | **Pending** (镜像拉取失败) | "starting" | "failed" |
| ErrImagePull | **Pending** | "starting" | "failed" |
| CreateContainerConfigError | **Pending** | "starting" | "failed" |

K8s 的设计逻辑：Pod Phase 表示 Pod 的生命周期阶段（Pending→Running→Succeeded/Failed），
**不表示容器健康状态**。CrashLoopBackOff 的容器仍处于 "Running" 阶段（因为它在不断重启，
而非终止），只有所有容器正常退出才进入 Succeeded/Failed。

### 修复（`internal/runtime/k3s.go`）

```go
// podToStatus 在 Phase switch 之后增加：
if pod.Message != "" && (phase == "starting" || (phase == "running" && !pod.Ready)) {
    reason := pod.Message
    if i := strings.Index(reason, ":"); i > 0 {
        reason = reason[:i]
    }
    switch reason {
    case "ImagePullBackOff", "ErrImagePull", "CrashLoopBackOff",
        "CreateContainerConfigError", "InvalidImageName":
        phase = "failed"
    }
}
```

**关键设计**：
1. 只在 `pod.Message != ""` 时检查（Message 由 `parsePodJSON` 从 `containerStatuses[].state.waiting.reason` 提取）
2. `phase == "starting"` 覆盖 Pending 阶段（ImagePullBackOff）
3. `phase == "running" && !pod.Ready` 覆盖 Running 阶段（CrashLoopBackOff），`!pod.Ready` 避免误判正常运行的 Pod
4. 提取 `:` 前的 reason 部分做精确匹配（Message 格式为 `"CrashLoopBackOff: back-off 5m0s ..."`)

### 附带修复：Native runtime `metaToStatus`

原代码在 port 不可达 + 超过 health check timeout 时返回 `"stopped"`。
改为 `"failed"`，因为正常卸载走 `Delete()` 路径会移除 metadata 文件，
metadata 残留 + port 死掉 = 进程崩溃。

### 验证结果

| 设备 | Pod 状态 | 修复前 | 修复后 |
|------|---------|--------|--------|
| amd395 | CrashLoopBackOff | `"phase": "running"` | `"phase": "failed"` |
| gb10 | 正常加载中 | `"phase": "running"` | `"phase": "running"`（无误报） |

---

## 十六、Qwen3-Coder-Next-FP8 部署经验（GB10, 80B MoE）

### 部署概要

| 项目 | 值 |
|------|-----|
| 模型 | Qwen3-Coder-Next-FP8 (80B total / 3.2B active, 512 experts × 10 active) |
| 架构 | `Qwen3NextForCausalLM` (model_type: `qwen3_next`) |
| 引擎 | aima-vllm-qwen3-omni:latest (vLLM v0.1.dev1+g6a3c7ede8) |
| 硬件 | GB10 Grace-Blackwell, 122 GiB unified memory |
| VRAM 占用 | 110.8 GiB (model) + 22.86 GiB (KV cache) |
| 配置 | gmu=0.85, max_model_len=128000, chunked_prefill=true, dtype=auto |

### Bug 11：kv_cache_dtype=fp8_e4m3 与 FLASH_ATTN 不兼容

**现象**：vLLM 启动失败，报错 `ValueError: Selected backend AttentionBackendEnum.FLASH_ATTN is not valid for this configuration. Reason: ['kv_cache_dtype not supported']`

**根因**：Qwen3-Coder-Next 使用 Mamba-2 混合架构（attention + SSM），其 attention backend 选择逻辑不支持 FP8 KV cache。FP8 KV cache 需要特定 attention kernel 支持（如 FlashInfer），而 `qwen3_next` 架构强制走 FLASH_ATTN，两者不兼容。

**修复**：从模型 YAML `default_config` 中移除 `kv_cache_dtype: fp8_e4m3`，让 vLLM 自动选择（`kv_cache_dtype: auto`）。

**教训**：MoE + Mamba 混合架构的 attention backend 限制比纯 Transformer 更多。不要假设对一个模型有效的 KV cache 优化适用于所有模型。

### Bug 12：NGC 26.01 (vllm stable) 不支持 qwen3_next 架构

**现象**：模型 YAML 原配 `engine: vllm` 映射到 NGC 26.01-py3 (vLLM ~v0.13)，该版本没有 `Qwen3NextForCausalLM` 支持。

**修复**：
1. 模型 YAML `engine` 从 `vllm` 改为 `vllm-nightly`
2. 引擎 YAML `vllm-nightly-blackwell.yaml` 更新镜像为 `aima-vllm-qwen3-omni:latest`（实际为 vLLM nightly build）

**规律**：新架构模型发布后通常 2-4 周才进入 vLLM stable release。部署新模型时优先用 nightly build。

### Bug 13：disk-pressure taint 阻止 pod 调度

**现象**：Pod 一直 Pending，`kubectl describe` 显示 `0/1 nodes are available: 1 node(s) had untolerated taint {node.kubernetes.io/disk-pressure: }`

**根因**：磁盘使用率 91%（916GB 磁盘仅 83GB 空闲），K3S 默认 `imagefs.available < 15%` 触发 disk-pressure eviction。disk-pressure 还导致 K3S containerd 主动 GC 之前导入的镜像。

**修复**：
1. 清理不用的 Docker 镜像（`docker rmi`）释放 ~35GB
2. 删除不再需要的模型（`rm -rf /mnt/data/models/GLM-*`）释放 ~141GB
3. 等待 kubelet 的 `eviction-pressure-transition-period`（默认 5 分钟）自动移除 taint

**教训**：
- disk-pressure 有 **滞后效应**：触发后需等 5 分钟才解除，即使空间已充足
- K3S containerd GC 在 disk-pressure 下会删除已导入的镜像
- 大模型 (80GB) + 大引擎镜像 (25GB) 对磁盘空间要求高，部署前应检查

### 关键发现：Docker↔K3S 镜像同步

通过 `sudo aima engine scan` 可以自动将 Docker 中的引擎镜像导入 K3S containerd（之前每次手动 `docker save | k3s ctr import` 已不需要）。但该命令需要 sudo 权限。

### 性能数据 — 引擎对比（2026-02-28）

#### 引擎 A: aima-vllm-qwen3-omni (自建, prefill 最优)

vLLM v0.1.dev1+g6a3c7ede8, FLASH_ATTN, max_model_len=128000

| Input | TTFT(s) | TPOT(ms) | tok/s | Prefill(tok/s) |
|-------|---------|----------|-------|----------------|
| 1K | 0.239 | 22.4 | 42.5 | 4,280 |
| 2K | 0.301 | 23.1 | 38.4 | 6,801 |
| 4K | 0.466 | 22.6 | 40.3 | 8,795 |
| 8K | 0.853 | 22.8 | 37.0 | 9,602 |
| 16K | 1.649 | 23.2 | 31.9 | **9,937 (peak)** |
| 32K | 3.347 | 24.2 | 24.5 | 9,791 |
| 64K | 7.067 | 25.6 | 16.5 | 9,274 |
| 128K | 15.890 | 28.7 | 6.8 | 8,055 |

#### 引擎 B: vllm/vllm-openai:qwen3_5-cu130 (官方 nightly, 262K 已验证)

vLLM v0.16.0rc2, TRITON_ATTN, max_model_len=262144

| Input | TTFT(s) | TPOT(ms) | tok/s | Prefill(tok/s) |
|-------|---------|----------|-------|----------------|
| 1K | 0.280 | 22.3 | 41.1 | 3,570 |
| 2K | 0.451 | 22.5 | 38.7 | 4,433 |
| 4K | 0.859 | 22.6 | 34.3 | 4,658 |
| 8K | 1.686 | 22.9 | 27.8 | **4,745 (peak)** |
| 16K | 3.514 | 24.0 | 19.5 | 4,610 |
| 32K | 7.624 | 25.5 | 11.8 | 4,276 |
| 64K | 18.079 | 28.9 | 5.9 | 3,617 |
| 128K | 47.635 | 35.4 | 2.5 | 2,750 |
| 192K | 85.6 | 39.6 | 0.4 | 2,244 |
| 220K | 106.4 | 42.0 | 0.3 | 2,068 |
| 240K | 122.7 | 44.2 | 0.2 | 1,956 |
| 256K | 135.6 | 46.2 | 0.2 | 1,888 |
| **262K** | **141.2** | **46.6** | **0.2** | **1,855** |

#### 引擎对比总结

| 维度 | A: aima-vllm-qwen3-omni | B: qwen3_5-cu130 |
|------|-------------------------|-------------------|
| **vLLM 版本** | **v0.14.0** (scitrera.ai 社区构建) | **v0.16.0rc2** (官方 nightly) |
| CUDA | 13.1.0 | 13.0 |
| PyTorch | 2.10.0 | — |
| FlashInfer | 0.6.1 | — |
| Attention | FLASH_ATTN | TRITON_ATTN |
| Prefill peak | **9,937 tok/s** (@16K) | 4,745 tok/s (@8K) |
| Prefill 倍率 | **2.1x 更快** | 基准 |
| TPOT @1K | 22.4ms | 22.3ms (持平) |
| TPOT @128K | **28.7ms** | 35.4ms (+23%) |
| 并发 8×1K | 126.5 tok/s | **145.6 tok/s (+15%)** |
| Max context | 128K | **262K** |
| 推荐场景 | **长 context 代码生成** | 短请求高并发 / 超长 context |

**架构兼容性**：
| 模型架构 | aima-vllm-qwen3-omni (vLLM v0.14.0, scitrera.ai) | qwen3_5-cu130 (vLLM v0.16.0rc2, official) |
|----------|---|---|
| `Qwen3NextForCausalLM` (Coder-Next 80B) | ✓ | ✓ |
| `Qwen3_5MoeForConditionalGeneration` (Qwen3.5 35B) | **✗** (v0.14 无此架构) | ✓ |
| `Qwen3ForCausalLM` (Qwen3 base) | ✓ | ✓ |

> Qwen3.5-35B-A3B 只能用 qwen3_5-cu130，无替代引擎。aima-vllm-qwen3-omni 基于 vLLM v0.14.0，缺少 v0.15+ 才加入的 Qwen3_5Moe 架构支持。

**结论**：自建引擎 prefill 2-3x 快，decode 持平，是代码生成首选。官方 nightly 支持 262K 全量上下文、并发略优、且是唯一支持 Qwen3.5 架构的引擎。

#### 并发缩放对比（1K context, 128 output）

| 并发 | A tok/s | B tok/s | A TPOT | B TPOT |
|------|---------|---------|--------|--------|
| 1 | 41.4 | 42.6 | 22.4 | 22.3 |
| 2 | 65.3 | 68.5 | 28.6 | 27.7 |
| 4 | 96.3 | 104.1 | 37.5 | 35.8 |
| 8 | 126.5 | **145.6** | 55.6 | 51.1 |

**与 Qwen3.5-35B-A3B 对比**：

| 维度 | Qwen3-Coder-Next (80B/3.2B) | Qwen3.5-35B-A3B (35B/3B) |
|------|-------------------------------|---------------------------|
| VRAM | 110.8 GiB | 65.5 GiB |
| TPOT @1K | 22.4ms | 33.5ms |
| tok/s @1K | 42.5 | 29.6 |
| Prefill peak | 9,937 tok/s (引擎 A) | 7,269 tok/s |
| Max context | 262K (引擎 B) | 256K |

### 部署检查清单（大 MoE 模型）

1. **磁盘空间**: 确保 > 15% 可用（`df -h /`），否则 K3S disk-pressure taint 阻止调度
2. **镜像同步**: `sudo aima engine scan` 自动导入 Docker 镜像到 K3S containerd
3. **引擎兼容性**: 新架构优先用 vllm-nightly，不要假设 stable 支持
4. **KV cache 参数**: 不要盲目设 `kv_cache_dtype: fp8_e4m3`，混合架构可能不兼容
5. **冷启动**: 首次请求 ~55s（torch.compile），后续正常
6. **VRAM 预算**: 80B FP8 模型占 ~111 GiB，128GB unified memory 下 KV cache 仅 23 GiB
7. **Env 传递**: engine YAML `startup.env` 通过 `resolved.Env` → `DeployRequest.Env` → `toResolvedConfig` → Pod spec 传递。旧版本可能丢失此字段
8. **Transformers 版本**: 新模型架构（如 `glm4_moe_lite`）需要 Transformers 5.0+，而 vLLM 官方 pin `<5`。解决：用 spark-vllm-docker `--tf5` 构建

---

## 十七、GLM-4.7-Flash 部署经验（GB10, 30B MoE）

### 核心问题：Transformers 版本依赖

GLM-4.7-Flash 使用 `Glm4MoeLiteForCausalLM` 架构（`model_type: glm4_moe_lite`），需要 **HuggingFace Transformers 5.0+** 才能识别。

**所有现有引擎均不支持**：

| 引擎 | vLLM 版本 | Transformers | 结果 |
|------|-----------|-------------|------|
| aima-vllm-qwen3-omni | v0.14.0 (scitrera.ai) | 5.0.0 (但 vLLM 太旧无模型代码) | ✗ `Model architectures not supported` |
| qwen3_5-cu130 | v0.16.0rc2 (官方 nightly) | <5.0 (pin >=4.56, <5) | ✗ `Transformers does not recognize glm4_moe_lite` |
| NGC 26.01-py3 | ~v0.7 | <5.0 | ✗ 同上 |

**已知 Bug**: [vLLM Issue #34098](https://github.com/vllm-project/vllm/issues/34098)

### 解决方案：spark-vllm-docker

[github.com/eugr/spark-vllm-docker](https://github.com/eugr/spark-vllm-docker) — 社区维护的 DGX Spark/GB10 专用 vLLM 构建。

**构建命令**：
```bash
git clone https://github.com/eugr/spark-vllm-docker.git
cd spark-vllm-docker

# 手动下载 FlashInfer wheels（GitHub 镜像加速，国内直连 <1MB/s）
cd wheels
MIRROR="https://ghfast.top/https://github.com"
curl -L -o flashinfer_cubin-0.6.4-py3-none-any.whl \
  "$MIRROR/eugr/spark-vllm-docker/releases/download/prebuilt-flashinfer-current/flashinfer_cubin-0.6.4-py3-none-any.whl"
curl -L -o flashinfer_jit_cache-0.6.4-cp39-abi3-manylinux_2_28_aarch64.whl \
  "$MIRROR/eugr/spark-vllm-docker/releases/download/prebuilt-flashinfer-current/flashinfer_jit_cache-0.6.4-cp39-abi3-manylinux_2_28_aarch64.whl"
curl -L -o flashinfer_python-0.6.4-py3-none-any.whl \
  "$MIRROR/eugr/spark-vllm-docker/releases/download/prebuilt-flashinfer-current/flashinfer_python-0.6.4-py3-none-any.whl"
cd ..

# 构建（--tf5 = Transformers 5 支持，GLM-4.7-Flash 必需）
./build-and-copy.sh -t vllm-spark-tf5 --tf5 -j 8
```

**构建产物**：
- 镜像：`vllm-spark-tf5:latest` (24.9GB)
- vLLM 版本：v0.16.1rc1 (from main branch)
- 包含：FlashInfer + Transformers 5 + SM121 优化
- 构建时间：~4h10m（NGC base 拉取 + vLLM 编译 1h48m + Runner 构建 2h22m）

**关键经验**：
1. **FlashInfer wheels 预下载**：GitHub releases 在国内极慢（~0.7MB/s），必须用镜像加速预下载到 `wheels/` 目录，共 471MB（3 个文件）
2. **NGC base image 很大**：`nvcr.io/nvidia/pytorch:26.01-py3` 含 ~15GB 层，首次构建拉取慢
3. **`--tf5` 不能省**：不加此 flag 构建的镜像仍然无法加载 `glm4_moe_lite`
4. **`VLLM_ATTENTION_BACKEND` 变量**：v0.16.1rc1 不再识别此环境变量（WARNING），可忽略

### AIMA 引擎集成

新增 `catalog/engines/vllm-spark.yaml`：
- `type: vllm-spark`，`image: vllm-spark-tf5:latest`
- patterns: `^vllm-spark`, `vllm-spark`, `spark-vllm`
- 仅 `linux/arm64` (Blackwell)

**部署流程**（全程 AIMA）：
```bash
sudo aima engine scan       # 扫描注册引擎
sudo aima deploy glm-4.7-flash --engine vllm-spark
```

### 性能数据

**Context Scaling**（1-concurrency, 128 output）：

| Input | TTFT | TPOT | tok/s |
|-------|------|------|-------|
| 1K | 0.135s | 39.0ms | 25.8 |
| 4K | 0.179s | 40.4ms | 24.9 |
| 8K | 0.229s | 42.4ms | 23.8 |
| 16K | 1.157s | 46.2ms | 21.8 |
| 32K | 3.170s | 53.9ms | 18.7 |
| 64K | 10.27s | 69.3ms | 14.5 |
| **128K** | **OOM 崩溃** | — | — |

> 128K OOM 原因: 59GB 模型 + KV cache 超出 128GB unified memory。`max_model_len` 应设 65536。

**Concurrency**（1K context, 128 output）：

| 并发 | Total tok/s | TPOT |
|------|------------|------|
| 1 | 25.9 | 38.7ms |
| 2 | 43.1 | 46.4ms |
| 4 | 63.1 | 63.2ms |
| 8 | 87.4 | 91.5ms |

### 三模型对比（GB10 Blackwell 128GB unified）

| 维度 | GLM-4.7-Flash (30B/3.6B) | Qwen3.5-35B-A3B (35B/3B) | Qwen3-Coder-Next (80B/3.2B) |
|------|--------------------------|---------------------------|------------------------------|
| VRAM | ~59 GiB | 65.5 GiB | 110.8 GiB |
| tok/s @1K | 25.8 | 29.6 | 42.5 |
| TPOT @1K | 39.0ms | 33.5ms | 22.4ms |
| Conc 8×1K | 87.4 tok/s | — | 145.6 tok/s |
| Max context | **64K** (128K OOM) | 256K | **262K** |
| 引擎 | vllm-spark v0.16.1rc1 | qwen3_5-cu130 v0.16.0rc2 | aima-vllm-qwen3-omni v0.14.0 |

> GLM-4.7-Flash 59GB 模型太大，KV cache 空间不足 → context 能力受限。换 FP8/Q4 量化可扩展。

### 引擎版本全景（GB10 可用）

| 引擎镜像 | vLLM | 构建方 | Transformers | 支持模型 |
|----------|------|--------|-------------|---------|
| aima-vllm-qwen3-omni | v0.14.0 | scitrera.ai | 5.0.0 | Qwen3-Coder-Next（prefill 最快） |
| qwen3_5-cu130 | v0.16.0rc2 | vLLM 官方 | <5.0 | Qwen3.5-35B-A3B（唯一选择） |
| vllm-spark-tf5 | v0.16.1rc1 | spark-vllm-docker | 5.0+ | GLM-4.7-Flash（唯一选择）+ 全部 |
| NGC 26.01-py3 | ~v0.7 | NVIDIA | <5.0 | 不推荐（太旧） |

## 十八、linux-1 TP=2 双 RTX 4090 部署经验（2026-03-01）

### 部署概要

| 项目 | 值 |
|------|-----|
| 模型 | Qwen3.5-35B-A3B (bf16, 35B total / 3B active, MoE) |
| 引擎 | vllm-nightly (qwen3_5-cu130, v0.16.0rc2) |
| 硬件 | 2× RTX 4090 (49140 MiB each), Xeon 8488C 48c, 503GB RAM |
| TP | 2 (tensor_parallel_size=2) |
| VRAM | 44030 + 44068 = 88098 MiB (89% per GPU) |
| KV cache | 5.9 GiB per GPU, 154,176 tokens total |
| 配置 | gmu=0.85, max_model_len=4096, bf16, chunked_prefill=true |

### 关键性能

| 指标 | 值 | vs gb10 |
|------|-----|---------|
| Decode speed | **172-174 tok/s** | 5.8x faster |
| TPOT | 5.7-5.8ms | 5.7x faster (vs 33ms) |
| TTFT (short) | 41-83ms | ~1.5x faster |
| TTFT (2K) | 481ms | comparable |
| Model load | 32.86 GiB in 9.3s | 7.7x faster (vs 72s) |
| torch.compile | 22.9s | — |
| CUDA graph | 13s (86 graphs) | — |
| Cold start (first req) | ~47s | — |

**关键发现**：
1. **TP=2 无 NVLink 也很快**：RTX 4090 无 NVLink/P2P，custom allreduce 被禁用，但 174 tok/s 仍远超 gb10 的 30 tok/s（NVMe SSD 高带宽 + Ada SM 更多 + PCIe 4.0 TP 通信足够）
2. **No MoE config warning**：`Using default MoE config. Performance might be sub-optimal! Config file not found at .../E=256,N=256,device_name=NVIDIA_GeForce_RTX_4090.json` — vLLM 缺少 RTX 4090 的 MoE kernel 配置，有优化空间
3. **TPOT 极其稳定**：5.7-5.8ms 不随 context 长度变化（在 4K max_model_len 内）
4. **模型加载极快**：14 shards × NVMe SSD = 9.3s（gb10 的 71.97s 受 eMMC 带宽限制）

### 部署前置条件 Checklist

```bash
# 1. K3S kubeconfig 给普通用户
sudo cp /etc/rancher/k3s/k3s.yaml ~/.kube/config
sudo chown $USER ~/.kube/config

# 2. AIMA_DATA_DIR 目录权限（/mnt/data 是 root-owned）
sudo mkdir -p /mnt/data/aima
sudo chown $USER /mnt/data/aima

# 3. Engine image 必须在 K3S containerd（不是 Docker）
docker save vllm/vllm-openai:qwen3_5-cu130 | sudo k3s ctr -n k8s.io images import -
# 验证:
sudo k3s ctr -n k8s.io images list -q | grep vllm

# 4. 模型文件 (72GB BF16, 用 hf-mirror.com)
HF_ENDPOINT=https://hf-mirror.com huggingface-cli download Qwen/Qwen3.5-35B-A3B --local-dir /mnt/data/aima/models/Qwen3.5-35B-A3B

# 5. Import model to AIMA DB
AIMA_DATA_DIR=/mnt/data/aima ~/aima model import /mnt/data/aima/models/Qwen3.5-35B-A3B
```

### 新增 Catalog 文件

- `catalog/engines/vllm-nightly-ada.yaml` — Ada GPU 专用 vLLM nightly 引擎
  - 与 Blackwell 版共用 `qwen3_5-cu130` 镜像，但 `gpu_arch: Ada`
  - 必要原因：`vllm-ada.yaml` (v0.8.5) 太旧不支持 Qwen3_5MoeForConditionalGeneration
- `catalog/models/qwen3.5-35b-a3b.yaml` Ada variant 更新：
  - `engine: vllm` → `engine: vllm-nightly`
  - 添加 `source.repo: Qwen/Qwen3.5-35B-A3B`
  - 添加实测 `expected_performance`

---

## 十九、linux-1 测试发现的 CLI Bugs（2026-03-01）

### Bug 14：`aima deploy list/logs` 解析为 model name

**现象**：
```
$ aima deploy list
aima: deploy list: resolve config: model "list" not found in catalog
$ aima deploy logs
aima: deploy logs: resolve config: model "logs" not found in catalog
```

**根因**：`deploy` 是 `cobra.Command` 而非 parent command，`Args: cobra.ExactArgs(1)` 将 `list`/`logs` 当作 model 参数。

**修复建议**：将 `deploy` 改为 parent command，添加 `list`/`logs`/`status` 子命令。`aima deploy <model>` 变为 `aima deploy apply <model>` 或保持为默认子命令。

### Bug 15：`model pull` 在 runtime=native 时推断错误引擎

**现象**：未配置 kubectl 时，runtime=native → 引擎推断 fallback 到 wildcard llamacpp → 下载 GGUF 而非 safetensors。

**根因**：`InferEngineType()` 在 native runtime 下优先匹配 `gpu_arch: "*"` 的 llamacpp variant，而非特定 gpu_arch 的 vllm-nightly variant。

**应该的行为**：引擎推断不应依赖 runtime 类型，而应基于 GPU 架构 + VRAM 匹配最优 variant。

### Bug 16：Engine scan 报 "Docker not in containerd" 对已导入的镜像

**根因**：containerd 使用 `docker.io/vllm/vllm-openai:qwen3_5-cu130` 完整引用，Docker 使用 `vllm/vllm-openai:qwen3_5-cu130` 短引用。scanner.go 的 `listImages()` 按短名去重时未对 containerd 的 `docker.io/` 前缀做 normalize。

**修复建议**：`listImages()` 合并时 strip `docker.io/` 前缀再做去重匹配。

### Bug 17：Engine pull 非 root 失败无清晰指引

**现象**：`aima engine pull vllm-nightly` 报 "all registries failed"。实际是 `crictl pull` 需要 root 权限访问 containerd socket。

**修复建议**：检测 permission denied 时给出明确提示："engine pull requires root (containerd socket). Use: sudo aima engine pull ..."

---

## 二十、跨设备性能对比总览（2026-03-01）

### Qwen3.5-35B-A3B 全设备测试

| 设备 | GPU | TP | tok/s | TPOT | TTFT @1K | VRAM | 引擎 |
|------|-----|----|-------|------|----------|------|------|
| **linux-1** | 2× RTX 4090 | 2 | **174** | **5.8ms** | 83ms | 88 GiB | vllm-nightly |
| **gb10** | GB10 Blackwell | 1 | 30 | 33.5ms | 498ms | 65.5 GiB | vllm-nightly |
| **gb10** | GB10 Blackwell | 1 | 29.3 | — | — | — | vllm-spark (multi-model) |

**为什么 linux-1 比 gb10 快 5.8x？**
1. **Ada SM 数量**：2× RTX 4090 = 2×128 SM = 256 SM (@ 2235 MHz)；GB10 = 1 GPU × 84 SM (@ 2000 MHz)
2. **内存带宽**：RTX 4090 = 1 TB/s ×2 GPUs；GB10 = ~256 GB/s (LPDDR5X shared)
3. **NVMe SSD vs eMMC**：模型加载 9.3s vs 72s
4. **MoE 3B active param**：TP=2 每卡仅 ~1.5B active weights，PCIe 4.0 TP 通信开销可忽略

---

## 二十一、AMD395 ROCm vLLM 部署实战（2026-03-01）

### 硬件

| 项目 | 值 |
|------|-----|
| CPU | AMD Ryzen AI MAX+ 395 (Zen5, 16c/32t) |
| GPU | Radeon 8060S (RDNA3.5, gfx1151, 2048 CU) |
| 内存 | 62GB 统一内存 (ROCm 报告 64GiB VRAM) |
| 引擎 | kyuz0/vllm-therock-gfx1151:latest (vLLM v0.16.1rc1, TheRock ROCm build) |

### 三个死胡同

**1. FP8 — ROCm 无 FP8 MoE kernels**

```
NotImplementedError: No FP8 MoE backend supports the deployment configuration.
```

ROCm vLLM TheRock 对 gfx1151 没有 FP8 MoE compute kernels。FP8 量化目前仅支持 NVIDIA H100/B200/GB10 和 AMD MI 系列。

**2. BF16 — OOM（58GiB model + 5GiB overhead > 64GiB）**

```
CUDA out of memory. Tried to allocate 816.00 MiB.
GPU 0 has a total capacity of 64.00 GiB of which 880.00 MiB is free.
Of the allocated memory 58.42 GiB is allocated by PyTorch.
```

**3. CPU offload — 统一内存下无效**

| Offload | PyTorch Alloc | PyTorch Reserved | Total |
|---------|---------------|-----------------|-------|
| 0 GB | 58.42 GiB | 4.39 GiB | 62.81 GiB |
| 4 GB | 55.31 GiB | 7.51 GiB | 62.82 GiB |
| 10 GB | 51.04 GiB | 11.89 GiB | 62.93 GiB |

> **根因**：UMA 下 CPU 和 GPU 共享同一物理内存池。`--cpu-offload-gb` 只是在 PyTorch 内部搬数据，释放的 GPU allocated 被 reserved 吃掉，总量不降。

### Bug #9：ROCm 容器 GPU 访问权限（P0）

**现象**：容器启动后 `Failed to get device count: no ROCm-capable device is detected`

**根因**：Hardware YAML `supplemental_groups: [44, 110]`，其中 GID 110 = `lxd`，不是 `render`（在此 Ubuntu 上 render = GID 109）。

**修复**：
1. `catalog/hardware/amd-radeon-x86.yaml` 添加 `privileged: true`（ROCm 容器标准实践）
2. 修正 GID 110 → 109

```yaml
container:
  devices:
    - /dev/kfd
    - /dev/dri
  security:
    privileged: true
    supplemental_groups: [44, 109]  # 44=video, 109=render (Ubuntu)
```

> **教训**：GID 是 per-installation 的，不同发行版不同。ROCm 容器用 `privileged: true` 最稳妥。

### 结论

Qwen3-30B-A3B (30B MoE) 在 AMD395 64GiB 统一内存上无法通过 vLLM 运行。
唯一可行路径：**llamacpp + GGUF Q4_K_M**（~18.6GB）。

### VLLM_USE_TRITON_FLASH_ATTN 环境变量过时

```
WARNING: Unknown vLLM environment variable detected: VLLM_USE_TRITON_FLASH_ATTN
```

TheRock v0.16.1rc1 不再识别此变量。ROCm 自动选择 Triton Attention backend，无需显式指定。
Engine YAML 中的此 env 可移除（无害但产生警告）。

更新：2026-03-01（linux-1 TP=2 双卡部署 + CLI bugs + AMD395 ROCm vLLM 死胡同 + 跨设备性能对比）
