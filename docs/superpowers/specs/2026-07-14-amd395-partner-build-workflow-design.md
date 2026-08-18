# AMD395 合作方 Windows 构建与验收流水线设计

## 背景

合作方需要修改 AIMA 源码中的模型 catalog YAML 默认参数，并把包含新默认值的 Windows EXE 下发给最终用户。运行时命令生成的用户 YAML 属于本机覆盖层，不会修改 EXE 内嵌的默认 catalog，因此不能代替源码修改和重新构建。

当前仓库已有发布流水线，但只在正式版本标签上运行；`amd395-win` 分支没有面向合作方的持续构建产物。合作方每次调整默认参数后，仍需要项目维护方手工打包，责任边界不清晰，也缺少可追溯的测试证据。

## 目标

第一阶段建立以下闭环：

1. 合作方从 `amd395-win` 创建分支，修改源码中的 catalog YAML 并提交 Pull Request。
2. GitHub Actions 自动执行 Go 测试、构建 Windows amd64 EXE、生成 SHA-256 校验和与构建元数据。
3. 每个 PR 提供与提交 SHA 唯一对应的、可下载的 GitHub Actions artifact。
4. 合作方在自己的 AMD395 Windows 测试机上人工验证，并把结果记录到 PR。
5. PR 合并到 `amd395-win` 后再次生成分支构建产物，作为可下发候选包。

## 非目标

- 第一阶段不接入合作方 AMD395 测试机作为 GitHub self-hosted runner。
- 不自动发布 GitHub Release，也不覆盖正式 `v*.*.*` 发布流程。
- 不把构建后的 EXE 自动提交回 Git 仓库。
- 不允许运行时用户 YAML 覆盖层反向修改 EXE 内嵌默认 catalog。
- 不在 CI 中宣称已完成 AMD395 真机兼容性验证；CI 只负责可重复的测试和交叉构建。

## 责任边界

| 环节 | 负责人 | 产物或证据 |
| --- | --- | --- |
| catalog 默认参数修改 | 合作方开发者 | PR 中的源码 YAML diff |
| 单元测试与 Windows 构建 | GitHub Actions | Check 状态、EXE、校验和、构建元数据 |
| AMD395 真机验证 | 合作方测试人员 | PR 验收记录、日志、模型调用结果 |
| 合并与下发决策 | 仓库维护者 | 已合并提交和对应分支 artifact |

维护方只维护流水线、评审变更和处理产品代码问题，不再为每次参数调整承担手工改 YAML、手工构建或代替合作方做现场验收。

## 触发时机

新增独立工作流 `AMD395 Windows Build`，在以下事件触发：

- Pull Request 的目标分支为 `amd395-win`
- Push 到 `amd395-win`

工作流只需要 `contents: read` 权限，不使用仓库 secret。相同 ref 上的新运行会取消旧运行，避免为已经过时的提交继续消耗构建资源。

第一阶段不配置 `workflow_dispatch`。GitHub 要求可人工触发的 workflow 文件存在于默认分支，而本工作流只交付到 `amd395-win`；如后续也将 dispatcher 合入默认分支，再单独增加人工触发入口。

## CI 流程

工作流在 `ubuntu-latest` 上完成以下步骤：

1. 完整检出源代码，使构建元数据能够记录真实提交。
2. 按 `go.mod` 安装并缓存 Go 工具链。
3. 执行 `go test ./...`。
4. 调用仓库脚本构建 `GOOS=windows GOARCH=amd64` 的 AIMA EXE。
5. 验证 EXE 存在、非空，并校验生成的 SHA-256 文件。
6. 上传 EXE、`checksums.txt` 和 `build-metadata.json`，保留 30 天。

不在 CI 内启动 llama 服务，因为 GitHub 托管 Linux runner 不能代表 AMD395 Windows 驱动、HIP/COMGR 和显存环境。

## 构建脚本契约

新增 `scripts/package-amd395-windows.sh`，让本地和 GitHub Actions 使用同一套构建逻辑。脚本：

- 默认输出到 `dist/amd395-windows/`
- 从 `internal/buildinfo/series.txt` 读取版本系列并追加 `-dev`
- 接受环境变量 `GIT_COMMIT`、`BUILD_TIME` 和 `OUTPUT_DIR`，便于 CI 固定元数据和自动化测试
- 使用 Go linker flags 写入版本、提交和构建时间
- 生成不可混淆的文件名：

  `aima-windows-amd64-v<series>-dev-amd-strix-halo-<12位提交SHA>.exe`

- 生成标准 `checksums.txt`
- 生成包含版本、完整提交 SHA、构建时间、目标 OS/架构和文件名的 `build-metadata.json`

提交 SHA 是产物身份的主键。日期只保留在元数据中，不作为唯一标识，避免同一天多个构建互相覆盖或无法追溯。

## Artifact 规则

GitHub Actions artifact 名称为：

`aima-amd395-windows-<12位提交SHA>`

artifact 内只包含：

- Windows EXE
- `checksums.txt`
- `build-metadata.json`

PR 构建和 `amd395-win` 分支构建使用相同格式。最终下发时必须依据完整提交 SHA 和校验和确认包来源，不能只凭本地文件名判断版本。

## 合作方操作手册

新增 `docs/amd395-partner-build.md`，覆盖以下流程：

1. 区分“源码默认 catalog”和“用户本机 YAML 覆盖层”。
2. 从 `amd395-win` 创建变更分支，只修改需要调整的 catalog 源文件和相关测试。
3. 提交 PR，等待 `AMD395 Windows Build` 通过。
4. 从该 workflow run 下载 artifact，并在 Windows 上执行 SHA-256 校验。
5. 在干净的数据目录或明确移除旧用户覆盖层后验证默认参数，避免旧 YAML 掩盖 EXE 的新默认值。
6. 执行 catalog 校验、dry-run、实际模型启动和最小推理请求。
7. 检查日志中是否存在 `LLVM ERROR`、COMGR、HIP、显存查询和服务拉起失败。
8. 将机器信息、驱动信息、提交 SHA、校验和、执行命令和结果回填到 PR。

手册同时给出失败归类：

- CI 测试或构建失败：由提交者修复源码或构建问题。
- 校验和不一致：禁止安装或下发，重新下载并核对 run。
- 干净目录生效、旧目录不生效：属于用户覆盖层优先级问题。
- AMD395 真机启动失败：附完整日志，由维护方定位产品或驱动兼容问题。

## 验收门槛

PR 可以合并到 `amd395-win` 的最低门槛：

- `go test ./...` 通过
- Windows EXE 构建和 SHA-256 自校验通过
- 构建元数据中的提交 SHA 与 PR head SHA 一致
- 合作方完成至少一台 AMD395 Windows 测试机验证
- 使用干净配置验证新的 catalog 默认参数确实进入 EXE
- llama 服务能启动并完成一次最小模型请求
- PR 中记录验证命令、结果及必要日志

## 后续阶段

当人工验收流程稳定后，再把合作方的 AMD395 Windows 测试机注册为受限的 GitHub self-hosted runner，实现合并前自动真机验证。该阶段需要单独处理 runner 安全隔离、模型缓存、驱动状态清理、并发锁和失败日志脱敏，不与本次第一阶段混合实施。

## 风险与控制

- **PR artifact 被误认为正式发布包**：文件名和文档明确标记 `dev`，且不创建 Release。
- **旧用户 YAML 掩盖新默认参数**：验收必须使用干净数据目录，并单独验证覆盖层行为。
- **无法追踪包对应源码**：文件名、元数据和 linker build info 都记录提交 SHA。
- **托管 runner 测试通过但真机失败**：PR 合并门槛保留 AMD395 人工验收。
- **合作方继续依赖维护方手工打包**：操作手册把源码修改、PR、下载和验收明确归属合作方。
