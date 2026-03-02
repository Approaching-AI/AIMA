# Docker ↔ K3S Containerd 镜像互通经验

> 覆盖 Docker / K3S containerd 双存储问题、镜像导入、registries.yaml 同步
> 基于：amd395 (Docker + K3S 共存) + gb10 (Docker + K3S 共存)
> 日期：2026-02-28

---

## 一、核心问题：两套独立的镜像存储

### 存储路径

```
Docker 存储:       /var/lib/docker/
K3S containerd:    /var/lib/rancher/k3s/agent/containerd/

两者完全独立，镜像不共享。
```

### 典型故障场景（已修复）

```
用户通过 docker pull 拉取镜像 → 存入 Docker store
↓
aima engine scan → 扫描 Docker + K3S containerd → 标记 docker_only=true
                → 有 root 权限 → 自动 docker save | k3s ctr import
                → 无 root 权限 → WARN 提示手动导入命令
↓
aima deploy → 生成 K3S Pod YAML (imagePullPolicy: IfNotPresent)
           → 镜像已在 containerd → 直接使用
           → 镜像不在 containerd → K3S 通过 registries.yaml 拉取
```

**amd395 实际案例**：`kyuz0/vllm-therock-gfx1151:latest` 通过 `docker pull` 拉到 Docker store，
`aima deploy` 生成 K3S pod 后，K3S 找不到镜像，Pod 一直 ImagePullBackOff。

---

## 二、诊断方法

```bash
# 查看 Docker 中的镜像
docker images | grep vllm

# 查看 K3S containerd 中的镜像
sudo k3s ctr images ls | grep vllm

# 对比两边是否一致
docker images --format "{{.Repository}}:{{.Tag}}" | sort > /tmp/docker-images.txt
sudo k3s ctr images ls -q | sort > /tmp/k3s-images.txt
diff /tmp/docker-images.txt /tmp/k3s-images.txt
```

### K3S Pod 部署失败诊断

```bash
# 查看 Pod 状态
kubectl get pods -A
kubectl describe pod <pod-name>
# Events 里会有 ImagePullBackOff 或 ErrImagePull

# 如果是镜像问题，检查两边存储
docker images -q <image>          # Docker 有？
sudo k3s ctr images ls | grep <image>  # K3S 有？
```

---

## 三、解决方案：Docker → K3S Containerd 导入

### 手动操作

```bash
# 从 Docker 导出 → 导入 K3S containerd
docker save <image:tag> | sudo k3s ctr -n k8s.io images import -

# 验证
sudo k3s ctr -n k8s.io images ls | grep <image>
```

**注意事项**：
- namespace 必须是 `k8s.io`（K3S 的 containerd namespace）
- `k3s ctr` 是 K3S 自带的 containerd CLI，不需要单独安装 ctr
- 大镜像（>5GB）导入需要时间，pipe 方式不需要额外磁盘空间

### 代码实现（puller.go）

```go
// ImageExistsInDocker checks whether image exists in Docker store.
func ImageExistsInDocker(ctx context.Context, image string, runner CommandRunner) bool {
    out, err := runner.Run(ctx, "docker", "images", "-q", image)
    return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// ImportDockerToContainerd pipes `docker save | k3s ctr -n k8s.io images import -`
// Requires root privileges (containerd socket is root-owned).
func ImportDockerToContainerd(ctx context.Context, image string, runner CommandRunner) error {
    _, err := runner.Run(ctx, "sh", "-c",
        fmt.Sprintf("docker save %q | k3s ctr -n k8s.io images import -", image))
    return err
}
```

**重要：containerd 操作需要 root。** AIMA 在非 root 运行时（deploy 路径）只做只读检查，
不尝试 import。import 在 engine scan 中尝试（预检查 `k3s ctr version` 成功才执行）。

---

## 四、K3S Registries.yaml 管理

### 文件位置与格式

```
/etc/rancher/k3s/registries.yaml
```

```yaml
mirrors:
  docker.io:
    endpoint:
      - "https://docker.m.daocloud.io"
      - "https://docker.1ms.run"
      - "https://docker.nju.edu.cn"
      - "https://docker.rainbond.cc"
  ghcr.io:
    endpoint:
      - "https://ghcr.nju.edu.cn"
  nvcr.io:
    endpoint:
      - "https://nvcr.io"
```

### 关键行为

1. **Hot-reload**：K3S containerd 监控 registries.yaml 变更，**无需重启 K3S**（实测 systemctl restart k3s 可以但非必须）
2. **轮询机制**：K3S 按 endpoint 列表顺序尝试，第一个失败后自动 fallback 到下一个
3. **仅对 kubelet 拉取生效**：`k3s ctr images pull` 绕过 registries.yaml，直连源 registry
4. **`k3s ctr images import`**：从本地文件/pipe 导入，不经过任何 registry

### AIMA 管理 registries.yaml

```
catalog/stack/k3s.yaml → registries: section → map[string]any 透传
↓
aima init → stack.Installer.writeRegistries() → 写入 /etc/rancher/k3s/registries.yaml
```

**更新时机**：`aima init` 写入。`stack.WriteRegistries()` 已导出为包级函数，
需要 root 权限。registries.yaml 同步属于 init 职责，不在 deploy 路径执行。

---

## 五、engine scan Docker-only 检测

### 当前行为（scanner.go）

```go
// ScanUnified 同时扫描 Docker + K3S containerd
// K3S:    crictl images -o json  → source="containerd"
// Docker: docker images --format json → source="docker"
// 合并: containerd 优先，Docker-only 标记 DockerOnly=true
```

**关键设计**：
1. `listImages` 先扫 crictl，再扫 docker，按 image:tag 去重（containerd 优先）
2. `matchImages` 将 `source=="docker"` 传递为 `DockerOnly=true`
3. `ScanUnified` 对 DockerOnly 镜像：
   - 预检查 `k3s ctr version` 判断是否有 containerd 写权限
   - 有权限 → `docker save | k3s ctr import`（自动导入）
   - 无权限 → WARN 打印手动修复命令（**不尝试 docker save，避免大镜像阻塞**）

### deploy 前置检查（只读）

```
deploy 时判断 activeRt.Name() == "k3s"
→ ImageExistsInDocker(image)? → Docker 有 → INFO 提示
→ 不做任何导入操作（deploy 非 root，没有 containerd 写权限）
→ Pod 使用 imagePullPolicy: IfNotPresent，kubelet（root）直接检查 containerd
```

**设计原则**：deploy 路径不使用 sudo，不做 containerd 写操作。镜像导入属于 init 或 engine scan（有 root 时）的职责。

---

## 六、常见问题

| 问题 | 原因 | 解法 |
|------|------|------|
| ImagePullBackOff (本地有镜像) | 镜像在 Docker，不在 K3S containerd | `sudo docker save \| sudo k3s ctr import` 或 `sudo aima engine scan` |
| imagePullPolicy Always | K8s 默认对 :latest tag Always 拉取 | podgen 已设 `imagePullPolicy: IfNotPresent` |
| registries.yaml 过时 | 手动维护，未与 catalog 同步 | 重新运行 `sudo aima init` |
| engine scan 扫不到 NGC 镜像 | 同 type 多 YAML patterns 覆盖 | 已修复：patterns append 合并 |
| engine scan 大镜像阻塞 | docker save 45GB 无 root 权限 | 已修复：预检查 containerd 权限再尝试 |
| `k3s ctr images pull` 超时 | 绕过 registries.yaml，直连被墙的 docker.io | 用 `docker pull` + import，或 kubelet 自动拉取 |
| 导入后 Pod 仍 ImagePull | image ref 不匹配（缺 docker.io 前缀） | 确认 `docker save` 输出的 ref 与 Pod spec 一致 |
| registries.yaml 写入权限不足 | 非 root 用户 | warn 不阻塞，提示用户手动执行 |

---

## 七、amd395 特殊情况

### vLLM TheRock 镜像

```bash
# 镜像：~8GB，通过 docker pull 从 Docker Hub 拉取
docker pull kyuz0/vllm-therock-gfx1151:latest

# 导入到 K3S（如果安装了 K3S）
docker save kyuz0/vllm-therock-gfx1151:latest | sudo k3s ctr -n k8s.io images import -
```

### amd395 deploy 路径选择

amd395 上 vLLM ROCm 引擎的 `runtime.platform_recommendations` 未设置 `native` 推荐，
deploy 默认走 K3S runtime → 需要镜像在 K3S containerd 中。

如果 K3S 不可用或不需要，可以通过 `--runtime native` 强制走 Docker/native 路径。

---

## 八、stack/installer.go WriteRegistries 导出

### 修改前

```go
// 私有方法，仅 aima init 调用
func (inst *Installer) writeRegistries(comp knowledge.StackComponent) error {
    dir := "/etc/rancher/k3s"
    os.MkdirAll(dir, 0o755)
    data, _ := yaml.Marshal(comp.Registries)
    os.WriteFile(filepath.Join(dir, "registries.yaml"), data, 0o644)
}
```

### 修改后

```go
// 导出为包级函数，deploy 前置检查也可调用
func WriteRegistries(registries map[string]any) error {
    dir := "/etc/rancher/k3s"
    if err := os.MkdirAll(dir, 0o755); err != nil {
        return err
    }
    data, err := yaml.Marshal(registries)
    if err != nil {
        return err
    }
    return os.WriteFile(filepath.Join(dir, "registries.yaml"), data, 0o644)
}

// 原方法改为调用导出函数
func (inst *Installer) writeRegistries(comp knowledge.StackComponent) error {
    return WriteRegistries(comp.Registries)
}
```

---

---

## 九、linux-1 大镜像导入经验（19.6GB, 2026-03-01）

### 导入时间和资源消耗

`vllm/vllm-openai:qwen3_5-cu130` (19.6GB Docker / 18.3 GiB containerd)：

```
docker save: ~3 min (streaming 19.6GB through pipe)
containerd unpack: ~2 min (layer extraction)
总计: ~5-6 min
磁盘峰值: 原 67GB → 30GB free (37GB consumed: Docker layer + containerd unpacked)
```

**磁盘空间要求**：导入 N GB 镜像需要约 **2×N GB** 临时空间（Docker tar + containerd 解压）。

### Image Reference Format 不匹配 Bug

**问题**：containerd 存储引用为 `docker.io/vllm/vllm-openai:qwen3_5-cu130`（带 docker.io 前缀），
Docker 显示为 `vllm/vllm-openai:qwen3_5-cu130`（不带前缀）。

AIMA engine scanner 用短引用比较 → 误报 "Docker not in containerd"。

**K3S kubelet 不受影响**：kubelet 的 image resolver 能自动 normalize 两种格式，Pod 能正常启动。
AIMA 的 scanner.go 需要同样的 normalize 逻辑。

### sudo 密码传递（nohup 后台）

`nohup` 环境无 tty，`sudo` 需要特殊处理：

```bash
# 方案 1：pipe password to first sudo -S, second sudo uses timestamp cache
printf "cjwx\n" | sudo -S docker save <image> | sudo k3s ctr -n k8s.io images import -

# 方案 2：先 sudo -v 刷新 timestamp cache，再执行
echo "cjwx" | sudo -S -v  # 刷新 cache
sudo docker save <image> | sudo k3s ctr -n k8s.io images import -
```

**注意**：sudo timestamp cache 默认 5-15 分钟（取决于配置）。大镜像导入如果超时，第二个 sudo 会失败。

### 监控导入进度

```bash
# 看 docker save 进程是否还在运行
ps aux | grep "docker save" | grep -v grep

# 看 containerd import 输出（会在最后打印 "unpacking...done"）
cat /tmp/import-vllm.log

# 看磁盘变化估算进度
df -h / | tail -1
```

更新：2026-03-01（linux-1 19.6GB 镜像导入 + reference format 不匹配 + nohup sudo 经验）
