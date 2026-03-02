# Engine Scanner 调试经验

> 覆盖 engine scan/list 的 4 个 Bug、pattern matching 机制、DB 生命周期管理
> 基于：gb10 (K3S containerd) 实际调试
> 日期：2026-02-28

---

## 一、核心架构：Engine Scanner 数据流

```
engine YAML (catalog/engines/*.yaml)
  → patterns: ["^vllm$", "vllm/vllm-openai:qwen3_5"]
  → scanner.go: matchImages(images, assetPatterns)
  → sqlite.go: UpsertEngine() → engines 表
  → ListEngines(WHERE available=1) → CLI/MCP 输出
```

### 镜像来源

```
Docker store:  docker images --format json → source="docker"
K3S containerd: crictl images -o json     → source="containerd"
                (fallback: k3s crictl)
合并: containerd 优先，Docker-only 标记 DockerOnly=true
```

---

## 二、4 个 Bug 及修复

### Bug 1：K3S 环境下 `crictl` 命令不存在

**现象**：`aima engine scan` 在 gb10 上扫不到任何 K3S containerd 镜像。

**根因**：代码直接调用 `crictl`，但 K3S 不安装独立 crictl，而是内置为 `k3s crictl` 子命令。

**修复**：`runCrictl()` 先尝试 `crictl`，失败后 fallback 到 `k3s crictl`：

```go
func runCrictl(ctx context.Context, runner CommandRunner, args ...string) ([]byte, error) {
    if out, err := runner.Run(ctx, "crictl", args...); err == nil {
        return out, nil
    }
    k3sArgs := append([]string{"crictl"}, args...)
    return runner.Run(ctx, "k3s", k3sArgs...)
}
```

**影响范围**：`scanner.go` 的 `listCrictlImages()` + `puller.go` 的 `ImageExists()` / `Pull()`。

---

### Bug 2：vllm-nightly 的 pattern 无法匹配 `vllm/vllm-openai` 镜像

**现象**：`vllm/vllm-openai:qwen3_5-cu130` 已在 K3S containerd 中，但 scan 不识别为 `vllm-nightly` 类型。

**根因**：
- 引擎 YAML pattern 是 `"^vllm-nightly$"` 和 `"vllm-nightly"`
- 镜像 repo 是 `vllm/vllm-openai`，tag 是 `qwen3_5-cu130`
- repo 名完全不包含 "vllm-nightly"，所以无法匹配

**修复**：引入 **tag-aware pattern**（含 `:` 的 pattern 匹配 `repo:tag`）：

```yaml
# catalog/engines/vllm-nightly-blackwell.yaml
patterns:
  - "^vllm-nightly$"        # 精确匹配 repo 名
  - "vllm-nightly"          # 包含匹配
  - "vllm/vllm-openai:qwen3_5"  # tag-aware: 匹配 repo:tag
```

**代码变化**（`matchImages()`）：
1. 将 pattern 分为 tag-aware（含 `:`）和 repo-only 两组
2. 对每个镜像：先用 tag-aware pattern 匹配 `repo:tag`，再用 repo-only 匹配 `repo`
3. Tag-aware 优先级高于 repo-only

```go
// 两阶段匹配
searchRef := strings.ToLower(img.repo + ":" + img.tag)   // tag-aware
searchName := strings.ToLower(img.repo)                    // repo-only
engineType := patternMatch(searchRef, tagPatterns)         // 优先
if engineType == "" {
    engineType = patternMatch(searchName, repoPatterns)    // fallback
}
```

---

### Bug 3：`^pattern$` 双 anchor 匹配失败

**现象**：`"^vllm-nightly$"` 应精确匹配 repo 名 `vllm-nightly`，但实际什么都匹配不到。

**根因**：旧代码分别处理 `^` 和 `$`，`^` 走 HasPrefix，`$` 走 HasSuffix，没有同时有两者的 case。

**修复**：`patternMatch()` 提取为独立函数，正确处理 4 种组合：

```go
func patternMatch(search string, patterns map[string]string) string {
    for pattern, engineType := range patterns {
        lower := strings.ToLower(pattern)
        cmp := lower
        hasPrefix := strings.HasPrefix(cmp, "^")
        hasSuffix := strings.HasSuffix(cmp, "$")
        if hasPrefix { cmp = cmp[1:] }
        if hasSuffix { cmp = cmp[:len(cmp)-1] }
        switch {
        case hasPrefix && hasSuffix:  // ^pattern$ → 精确匹配
            if search == cmp { return engineType }
        case hasPrefix:               // ^pattern → 前缀匹配
            if strings.HasPrefix(search, cmp) { return engineType }
        case hasSuffix:               // pattern$ → 后缀匹配
            if strings.HasSuffix(search, cmp) { return engineType }
        default:                      // pattern → 包含匹配
            if search == cmp || strings.Contains(search, cmp) { return engineType }
        }
    }
    return ""
}
```

---

### Bug 4：Scan 只 upsert，不清理已消失的引擎

**现象**：镜像 rename/删除后，`engine list` 仍显示旧条目。

**根因**：`ScanUnified()` 只做 `UpsertEngine()`，不处理"本次扫描未发现的旧引擎"。

**修复**：
1. `MarkEnginesUnavailableExcept(keepIDs)` — 将不在本次扫描结果中的引擎标记 `available=0`
2. `ListEngines()` 增加 `WHERE available = 1` 过滤
3. Scan 完成后收集所有 `scannedIDs`，调用清理

```go
// sqlite.go
func (d *DB) MarkEnginesUnavailableExcept(ctx context.Context, keepIDs []string) error {
    if len(keepIDs) == 0 { return nil }  // 防止误清空
    placeholders := make([]string, len(keepIDs))
    args := make([]any, len(keepIDs))
    for i, id := range keepIDs { placeholders[i] = "?"; args[i] = id }
    query := fmt.Sprintf(`UPDATE engines SET available = 0 WHERE id NOT IN (%s)`,
        strings.Join(placeholders, ","))
    _, err := d.db.ExecContext(ctx, query, args...)
    return err
}

// main.go — scan 后调用
db.MarkEnginesUnavailableExcept(ctx, scannedIDs)
```

**设计决策**：用 `available` 软删除而非硬删除，保留历史记录便于诊断。

---

## 三、Pattern 设计指南

### Pattern 类型

| 语法 | 匹配方式 | 匹配目标 | 示例 |
|------|---------|---------|------|
| `"vllm"` | 包含 | repo | `vllm/vllm-openai` ✅ |
| `"^vllm$"` | 精确 | repo | 仅 `vllm` ✅ |
| `"^vllm"` | 前缀 | repo | `vllm-openai` ✅ |
| `"openai$"` | 后缀 | repo | `vllm/vllm-openai` ✅ |
| `"vllm/vllm-openai:qwen3_5"` | 包含（tag-aware） | repo:tag | `vllm/vllm-openai:qwen3_5-cu130` ✅ |

### 最佳实践

1. **同一镜像 repo 多 tag 对应不同引擎类型** → 用 tag-aware pattern
   ```yaml
   # vllm-ada (stable)
   patterns: ["^vllm/vllm-openai:v$"]      # v0.8.5 等
   # vllm-nightly
   patterns: ["vllm/vllm-openai:qwen3_5"]  # qwen3_5-cu130 等
   ```

2. **独占 repo 名的引擎** → 用 `^name$` 精确匹配 + 包含匹配
   ```yaml
   patterns: ["^llamacpp$", "llama-server", "ggml-org/llama.cpp"]
   ```

3. **避免过于宽泛的 pattern**：`"vllm"` 会匹配所有含 vllm 的镜像。如果多个引擎共享 repo，必须用 tag-aware 区分。

---

## 四、Deploy 前数据流水线

### 完整数据流：scan → resolve → deploy

```
[1] aima model scan
    → 扫描 /mnt/data/models/* → DB: models 表 (name, path, params)
    → 必须先于 deploy 执行，否则 resolveWithFallback 拿不到实际 model path

[2] aima engine scan  (需要 sudo / root)
    → Docker images + K3S containerd → DB: engines 表 (type, image, tag)
    → Docker-only 镜像自动导入 K3S containerd

[3] aima deploy <model> --engine <engine>
    → resolveWithFallback():
        Catalog YAML (engine config, model variant)
        + DB (model path, engine image)
        → ResolvedConfig
    → DeployApply():
        ResolvedConfig → DeployRequest (+ hwInfo.CPUArch)
        → toResolvedConfig() (k3s.go)
        → GeneratePod() (podgen.go)
        → k3s.Apply()
```

### CPUArch 传递链（Bug 修复重点）

```
hal.Detect() → hw.CPU.Arch
    → buildHardwareInfo() → hwInfo.CPUArch (e.g. "arm64")
        → DeployApply() → DeployRequest.CPUArch
            → toResolvedConfig() → ResolvedConfig.CPUArch
                → GeneratePod() → libDirForArch("arm64")
                    → LD_LIBRARY_PATH: "/lib/aarch64-linux-gnu:..."
```

**教训**：新增字段必须在整条链上都传递。`DeployRequest` 加了 `CPUArch` 字段但不赋值 → `libDirForArch("")` 默认 x86_64 → arm64 Pod 环境变量错误。

---

## 五、测试方法

### 单元测试（已实现）

```go
// TestScanK3sCrictlFallback: standalone crictl 失败 → k3s crictl 成功
// TestScanTagAwarePatternPriority: 同 repo 不同 tag → 正确分类
// TestPatternMatchExactAnchors: ^pattern$ 精确匹配
```

### 实机验证

```bash
# 构建 + 上传
GOOS=linux GOARCH=arm64 go build -o build/aima-linux-arm64 ./cmd/aima
scp build/aima-linux-arm64 qujing@100.105.58.16:~/aima

# 测试 scan
ssh qujing@100.105.58.16 'echo <base64pwd> | base64 -d | sudo -S ~/aima engine scan 2>&1'

# 验证 list
ssh qujing@100.105.58.16 '~/aima engine list'

# 检查 Pod YAML 中的关键字段
ssh qujing@100.105.58.16 'sudo kubectl get pod <name> -o yaml | grep -A2 "LD_LIBRARY_PATH\|hostPath\|image:"'
```

---

## 六、常见问题

| 问题 | 原因 | 解法 |
|------|------|------|
| scan 扫不到 K3S 镜像 | `crictl` 未安装，K3S 用 `k3s crictl` | `runCrictl()` fallback 已修复 |
| 同 repo 不同 tag 分类错误 | repo-only pattern 无法区分 tag | 用 tag-aware pattern（含 `:`）|
| `^pattern$` 不匹配 | 旧代码无双 anchor 处理 | `patternMatch()` 4-way switch |
| 删除镜像后 list 仍显示 | scan 只 upsert 不清理 | `MarkEnginesUnavailableExcept` |
| deploy 模型路径空 | 未执行 `model scan` | 先 `aima model scan` 注册路径 |
| arm64 LD_LIBRARY_PATH 错误 | CPUArch 未传递到 Pod spec | 确保整条链传递 CPUArch |
| engine scan 需要 sudo | containerd socket root-owned | `sudo aima engine scan` |

---

更新：2026-02-28（engine scanner 4 Bug 修复 + tag-aware pattern + CPUArch 传递链）
