# AMD395 Windows 默认参数修改、构建与验收手册

本文面向需要维护 AMD395 / Strix Halo Windows 预装包的合作方开发与测试人员。目标是让合作方独立完成默认参数修改、Pull Request、自动构建和真机验收，仓库维护方负责流水线和代码评审。

## 1. 先判断应该修改哪一层

AIMA 的 catalog 有三层，优先级为：

1. EXE 内嵌的 factory catalog
2. `<AIMA_DATA_DIR>/catalog/central/` 集中下发 patch
3. `<AIMA_DATA_DIR>/catalog/user/` 本机用户 patch

后面的层会覆盖前面的层。

### 要修改 EXE 的默认参数

修改仓库中的 factory catalog 源文件：

- 模型：`catalog/models/*.yaml`
- 引擎：`catalog/engines/*.yaml`
- 硬件：`catalog/hardware/*.yaml`
- 场景：`catalog/scenarios/*.yaml`

这些文件由 `catalog/embed.go` 通过 `go:embed` 编译进 EXE。修改后必须重新构建 EXE；已经下载的旧 EXE 不会自动从 GitHub 拉取最新 YAML。

这是合作方希望“用户只更新 EXE 就得到新默认参数”时应采用的方式。

### 要独立下发一批统一参数，但不更新 EXE

把合法的 `*_patch` YAML 下发到：

```text
<AIMA_DATA_DIR>/catalog/central/models/
<AIMA_DATA_DIR>/catalog/central/engines/
<AIMA_DATA_DIR>/catalog/central/scenarios/
```

central patch 适合配置与 EXE 分开升级的场景，但它不会改变 EXE 内嵌的默认值。

### 只做单机调试

`aima catalog override ...` 会写入 `catalog/user`。这属于当前机器的最高优先级覆盖层，不会修改源码或 EXE。不要把这个命令当作默认包制作工具。

> 重要：旧的 `catalog/user` 或 `catalog/central` patch 可能遮住新 EXE 中的 factory 参数。验证 EXE 默认值时必须使用干净的 `AIMA_DATA_DIR`。

## 2. 修改源码并提交 Pull Request

从最新 `amd395-win` 创建变更分支：

```bash
git clone https://github.com/Approaching-AI/AIMA.git
cd AIMA
git fetch origin amd395-win
git switch -c partner/<change-name> origin/amd395-win
```

只修改本次需要调整的 catalog 源文件和相关测试。例如 AMD395 上的 Qwen 参数可能涉及：

```text
catalog/models/qwen3.6-35b-a3b.yaml
catalog/engines/llamacpp-hip-windows.yaml
catalog/hardware/amd-radeon-8060s-x86.yaml
```

提交前执行：

```bash
go test ./...
git diff --check
git diff origin/amd395-win...HEAD -- catalog/
```

推送分支并创建目标为 `amd395-win` 的 Pull Request：

```bash
git push -u origin partner/<change-name>
gh pr create --base amd395-win --fill
```

如果没有 GitHub CLI，也可以在 GitHub 网页上创建 Pull Request。

## 3. 自动构建做什么

`AMD395 Windows Build` 在以下时机运行：

- Pull Request 的目标分支为 `amd395-win`
- 代码 push 到 `amd395-win`

流水线会：

1. 检出 PR head 或分支的准确提交。
2. 执行 `go test ./...`。
3. 验证打包和 workflow 契约。
4. 交叉构建 Windows amd64 EXE。
5. 验证 SHA-256 和构建元数据。
6. 上传保留 30 天的 GitHub Actions artifact。

第一阶段不提供 Actions 页面的人工 `Run workflow`。该入口要求 workflow 同时存在于仓库默认分支 `master`；当前通过 PR 更新或 push 到 `amd395-win` 即可触发构建。

artifact 名称格式：

```text
aima-amd395-windows-<12位提交SHA>
```

内容：

```text
aima-windows-amd64-v0.5-dev-amd-strix-halo-<12位提交SHA>.exe
checksums.txt
build-metadata.json
```

这是开发候选包，不是正式 GitHub Release。

## 4. 下载并核对构建产物

### GitHub 网页

1. 打开 Pull Request。
2. 打开通过的 `AMD395 Windows Build` check。
3. 进入对应 workflow run。
4. 在页面底部 `Artifacts` 下载名称包含 PR head SHA 的包。

### GitHub CLI

```bash
gh run list --workflow "AMD395 Windows Build" --branch partner/<change-name> --limit 5
gh run download <run-id> --name aima-amd395-windows-<12位提交SHA> --dir ./amd395-package
```

不要下载其他 PR、其他提交或已经过期的本地缓存包。

在 AMD395 Windows 测试机上打开 PowerShell：

```powershell
Set-Location D:\path\to\amd395-package

$metadata = Get-Content .\build-metadata.json -Raw | ConvertFrom-Json
$exe = Join-Path $PWD $metadata.filename
$expected = ((Get-Content .\checksums.txt -Raw) -split '\s+')[0].ToLowerInvariant()
$actual = (Get-FileHash $exe -Algorithm SHA256).Hash.ToLowerInvariant()

if ($actual -ne $expected) {
    throw "SHA-256 mismatch: expected=$expected actual=$actual"
}

$metadata | Format-List
Write-Host "SHA-256 verified: $actual"
```

确认 `build-metadata.json` 中的完整 `git_commit` 等于 PR head SHA。校验失败时禁止安装或下发。

## 5. 使用干净目录验证 EXE 默认参数

不要删除或覆盖正在使用的用户目录。为本次验收创建独立数据目录：

```powershell
$env:AIMA_DATA_DIR = Join-Path $env:TEMP "aima-amd395-$([guid]::NewGuid())"
New-Item -ItemType Directory -Force $env:AIMA_DATA_DIR | Out-Null

& $exe version
& $exe hal detect
& $exe catalog validate
& $exe catalog status
```

`version` 输出中的 `commit` 必须与 `build-metadata.json.git_commit` 一致。

查看目标模型最终生效值。将 `<model-name>` 替换为 catalog 的 `metadata.name`：

```powershell
& $exe catalog effective model_asset <model-name>
& $exe catalog diff model_asset <model-name>
```

验收 factory 默认值时：

- `catalog status` 不应显示本次目标模型存在 central/user 覆盖。
- `catalog effective` 应显示源码 PR 中的新参数。
- `catalog diff` 应为空或不包含由旧 overlay 产生的差异。

再用原来的用户数据目录重复执行 `catalog status/effective/diff`，可以单独确认是否有旧 patch 覆盖了新默认值。不要把这个覆盖层现象误判为“新 EXE 没有打进去”。

## 6. AMD395 真机启动和最小推理验收

先预览实际解析出的引擎和参数：

```powershell
& $exe deploy <model-name> --engine <engine-name> --dry-run
```

确认 dry-run 参数符合 PR 要求后启动：

```powershell
& $exe deploy <model-name> --engine <engine-name>
& $exe deploy status <model-name>
```

对于本次 AMD395 Windows llama.cpp 路径，通常使用 `llamacpp-hip-windows`；以目标 catalog 和 dry-run 解析结果为准，不要在手册命令之外额外加入会覆盖默认值的 `--config` 参数。

服务 ready 后，向部署输出所显示的实际端口发送最小请求。如果通过 AIMA 默认代理端口 `6188` 验证：

```powershell
$body = @{
    model = "<model-name>"
    messages = @(@{ role = "user"; content = "hello" })
    max_tokens = 8
} | ConvertTo-Json -Depth 4

Invoke-RestMethod `
    -Uri http://127.0.0.1:6188/v1/chat/completions `
    -Method Post `
    -ContentType application/json `
    -Body $body
```

若部署使用其他端口，请替换 URL。最低通过条件：

- `deploy` 成功返回，服务进入 ready。
- 完成至少一次非空的 chat completion。
- 日志中没有 `LLVM ERROR: Can't get available size`。
- 日志中没有 COMGR cache、HIP device enumeration、显存查询或引擎进程异常退出。

发生失败时保留原始日志，并执行：

```powershell
& $exe diagnostics export
```

不要只发送截图；同时提供可检索的文本日志和诊断包。

## 7. Pull Request 验收记录模板

合作方测试人员把下面内容回填到 PR：

```markdown
## AMD395 Windows 验收

- PR head SHA：
- Artifact 名称：
- EXE 文件名：
- EXE SHA-256：
- 测试机器/AMD395 型号：
- Windows 版本：
- AMD 驱动版本：
- HIP/相关运行库版本：
- AIMA_DATA_DIR：全新临时目录 / 已有目录
- 模型：
- 引擎：

### 结果

- [ ] `aima version` 的 commit 与 artifact 一致
- [ ] `catalog validate` 通过
- [ ] 干净目录中的 `catalog effective` 参数符合 PR
- [ ] `deploy --dry-run` 参数符合 PR
- [ ] llama 服务启动并 ready
- [ ] 最小 chat completion 成功
- [ ] 无 LLVM/COMGR/HIP/显存查询错误

### 证据

- `catalog status/effective/diff`：
- dry-run 摘要：
- 部署与请求结果：
- 日志或 diagnostics artifact：
- 最终结论：通过 / 不通过
```

在上述项目未完成前，不应把 PR 合并到 `amd395-win`，也不应向最终用户下发该 artifact。

## 8. 常见问题归类

| 现象 | 判断 | 处理人 |
| --- | --- | --- |
| `go test` 或交叉构建失败 | 源码或构建问题 | PR 提交者修复 |
| SHA-256 不一致 | 下载损坏或拿错 artifact | 停止使用并重新下载 |
| 干净目录参数正确，旧目录错误 | central/user overlay 覆盖 factory | 合作方清理或升级 overlay |
| 干净目录也还是旧参数 | 修改了错误源文件或 artifact 与提交不对应 | PR 提交者核对 diff、SHA 和元数据 |
| 托管 CI 通过，AMD395 启动失败 | Windows 驱动/HIP/COMGR 或产品运行时兼容问题 | 合作方提供日志，维护方定位代码 |
| 仅用 `catalog override` 后本机生效 | 正常的 user overlay 行为，不代表 EXE 默认值已改变 | 按本手册修改源码并重新构建 |

## 9. 合并后的下发候选包

PR 合并到 `amd395-win` 后，分支 push 会再次运行 `AMD395 Windows Build`。下发时使用这个合并提交对应的 artifact，而不是 PR 合并前的 head artifact，并再次核对完整提交 SHA 和 SHA-256。

正式版本发布仍使用仓库原有的 `v*.*.*` tag release 流程；本流水线不自动创建 Release。
