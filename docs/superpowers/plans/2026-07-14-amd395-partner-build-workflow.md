# AMD395 Partner Windows Build Workflow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 `amd395-win` 分支建立自动测试、Windows EXE 打包、可追溯 artifact 上传和合作方 AMD395 人工验收手册。

**Architecture:** 使用一个仓库内 shell 脚本统一本地与 CI 的 Windows 交叉构建、校验和及元数据生成；用 shell 契约测试先验证脚本和 workflow 的外部行为；GitHub Actions 只负责托管 runner 上的测试与构建，AMD395 运行时验收继续由合作方真机执行。

**Tech Stack:** Bash、Go 1.25、GitHub Actions、PowerShell（合作方验收命令）、SHA-256。

---

## 文件结构

- Create: `scripts/package-amd395-windows.sh` — Windows amd64 构建、命名、SHA-256 和 JSON 元数据的唯一实现。
- Create: `scripts/test-package-amd395-windows.sh` — 使用假的 `go` 命令隔离测试打包脚本契约。
- Create: `scripts/test-amd395-windows-workflow.sh` — 静态验证 workflow 的触发器、安全权限、构建和上传契约。
- Create: `.github/workflows/amd395-windows-build.yml` — PR、分支 push 和人工触发的自动构建。
- Modify: `Makefile` — 增加统一的本地/CI 契约测试入口。
- Create: `docs/amd395-partner-build.md` — 合作方源码修改、artifact 下载、干净配置和真机验收操作手册。

### Task 1: 打包脚本契约测试与实现

**Files:**
- Create: `scripts/test-package-amd395-windows.sh`
- Create: `scripts/package-amd395-windows.sh`

- [ ] **Step 1: 写打包脚本失败测试**

测试创建临时 `go` 替身，记录 `GOOS`、`GOARCH`、`CGO_ENABLED` 和 linker flags，再断言固定提交与时间会生成预期 EXE、有效校验和及 JSON：

```bash
#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

mkdir -p "$TMP_DIR/bin" "$TMP_DIR/out"
cat >"$TMP_DIR/bin/go" <<'FAKE_GO'
#!/usr/bin/env bash
set -euo pipefail
printf 'GOOS=%s\nGOARCH=%s\nCGO_ENABLED=%s\nARGS=%s\n' \
  "${GOOS:-}" "${GOARCH:-}" "${CGO_ENABLED:-}" "$*" >"$FAKE_GO_RECORD"
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    printf 'fake windows executable\n' >"$1"
    exit 0
  fi
  shift
done
exit 1
FAKE_GO
chmod +x "$TMP_DIR/bin/go"

commit="0123456789abcdef0123456789abcdef01234567"
PATH="$TMP_DIR/bin:$PATH" \
FAKE_GO_RECORD="$TMP_DIR/go-record.txt" \
GIT_COMMIT="$commit" \
BUILD_TIME="2026-07-14T08:00:00Z" \
OUTPUT_DIR="$TMP_DIR/out" \
bash "$ROOT_DIR/scripts/package-amd395-windows.sh"

expected="aima-windows-amd64-v0.5-dev-amd-strix-halo-0123456789ab.exe"
test -s "$TMP_DIR/out/$expected"
(cd "$TMP_DIR/out" && shasum -a 256 -c checksums.txt)
python3 - "$TMP_DIR/out/build-metadata.json" "$commit" "$expected" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as fh:
    data = json.load(fh)
assert data == {
    "version": "v0.5-dev",
    "git_commit": sys.argv[2],
    "build_time": "2026-07-14T08:00:00Z",
    "target_os": "windows",
    "target_arch": "amd64",
    "filename": sys.argv[3],
}
PY
grep -F 'GOOS=windows' "$TMP_DIR/go-record.txt"
grep -F 'GOARCH=amd64' "$TMP_DIR/go-record.txt"
grep -F 'CGO_ENABLED=0' "$TMP_DIR/go-record.txt"
grep -F "GitCommit=$commit" "$TMP_DIR/go-record.txt"
```

- [ ] **Step 2: 运行测试并确认因实现缺失而失败**

Run: `bash scripts/test-package-amd395-windows.sh`

Expected: FAIL，错误包含 `scripts/package-amd395-windows.sh: No such file or directory`。

- [ ] **Step 3: 实现最小打包脚本**

实现必须：读取 `series.txt`、解析/校验提交和时间、跨 macOS/Linux 计算 SHA-256、用 linker flags 注入 buildinfo，并输出固定字段 JSON。核心构建调用为：

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
  -ldflags "-s -w -X '$module.Version=$version' -X '$module.BuildTime=$build_time' -X '$module.GitCommit=$git_commit'" \
  -o "$output_dir/$filename" ./cmd/aima
```

- [ ] **Step 4: 运行测试并确认通过**

Run: `bash scripts/test-package-amd395-windows.sh`

Expected: PASS，校验和输出以 `<filename>: OK` 结束。

- [ ] **Step 5: 提交打包脚本与测试**

```bash
git add scripts/package-amd395-windows.sh scripts/test-package-amd395-windows.sh
git commit -m "build: add traceable AMD395 Windows package"
```

### Task 2: Workflow 契约测试与实现

**Files:**
- Create: `scripts/test-amd395-windows-workflow.sh`
- Create: `.github/workflows/amd395-windows-build.yml`
- Modify: `Makefile`

- [ ] **Step 1: 写 workflow 失败测试**

测试用固定字符串断言以下不可省略的契约：PR/push 只面向 `amd395-win`、支持人工触发、只读权限、PR 构建检出 head SHA、运行全量 Go 测试和打包契约测试、调用统一打包脚本、验证校验和、上传 artifact 30 天且缺文件时报错。

```bash
#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKFLOW="$ROOT_DIR/.github/workflows/amd395-windows-build.yml"
test -f "$WORKFLOW"
for expected in \
  'name: AMD395 Windows Build' \
  'pull_request:' \
  'push:' \
  'workflow_dispatch:' \
  '- amd395-win' \
  'contents: read' \
  'github.event.pull_request.head.sha || github.sha' \
  'go test ./...' \
  'make amd395-build-test' \
  'bash ./scripts/package-amd395-windows.sh' \
  'sha256sum -c checksums.txt' \
  'uses: actions/upload-artifact@v4' \
  'retention-days: 30' \
  'if-no-files-found: error'
do
  grep -F -- "$expected" "$WORKFLOW" >/dev/null
done
```

- [ ] **Step 2: 运行测试并确认因 workflow 缺失而失败**

Run: `bash scripts/test-amd395-windows-workflow.sh`

Expected: FAIL at `test -f`。

- [ ] **Step 3: 新增 Makefile 测试入口**

```make
.PHONY: amd395-build-test

## amd395-build-test: Verify AMD395 package and workflow contracts
amd395-build-test:
	bash ./scripts/test-package-amd395-windows.sh
	bash ./scripts/test-amd395-windows-workflow.sh
```

- [ ] **Step 4: 实现 GitHub Actions workflow**

工作流使用 `actions/checkout@v4`、`actions/setup-go@v5` 和 `actions/upload-artifact@v4`。checkout 的 `ref` 必须是 `${{ github.event.pull_request.head.sha || github.sha }}`，确保 artifact 内元数据和实际构建源码一致。打包步骤把同一个 SHA 传入 `GIT_COMMIT`，输出目录固定为 `dist/amd395-windows`。验证步骤运行：

```bash
cd dist/amd395-windows
sha256sum -c checksums.txt
python3 - "$GIT_COMMIT" <<'PY'
import json, sys
with open("build-metadata.json", encoding="utf-8") as fh:
    metadata = json.load(fh)
assert metadata["git_commit"] == sys.argv[1]
assert metadata["target_os"] == "windows"
assert metadata["target_arch"] == "amd64"
PY
```

上传 action 的 artifact 名称为 `aima-amd395-windows-${{ steps.package.outputs.short_sha }}`，path 为 `dist/amd395-windows/*`，保留 30 天。

- [ ] **Step 5: 运行契约测试并确认通过**

Run: `make amd395-build-test`

Expected: 两个 shell 测试全部 exit 0。

- [ ] **Step 6: 提交 workflow 和测试入口**

```bash
git add .github/workflows/amd395-windows-build.yml Makefile scripts/test-amd395-windows-workflow.sh
git commit -m "ci: build AMD395 Windows artifacts"
```

### Task 3: 合作方操作手册

**Files:**
- Create: `docs/amd395-partner-build.md`

- [ ] **Step 1: 编写源码与覆盖层说明**

明确 factory catalog 通过 `go:embed` 进入 EXE，必须改源码并重新构建；`catalog/user` 是单机覆盖，`catalog/central` 是可独立下发的集中覆盖，不会修改 EXE 默认值。

- [ ] **Step 2: 编写 PR 与 artifact 下载流程**

给出从 `amd395-win` 拉分支、提交 YAML 与测试、等待 workflow、确认提交 SHA、下载 artifact、运行 `Get-FileHash -Algorithm SHA256` 的逐步命令。

- [ ] **Step 3: 编写 AMD395 干净环境验收流程**

使用 PowerShell 设置独立目录：

```powershell
$env:AIMA_DATA_DIR = Join-Path $env:TEMP "aima-amd395-$([guid]::NewGuid())"
New-Item -ItemType Directory -Force $env:AIMA_DATA_DIR | Out-Null
.\aima.exe version
.\aima.exe hal detect
.\aima.exe catalog validate
.\aima.exe catalog status
.\aima.exe catalog effective model_asset <model-name>
.\aima.exe catalog diff model_asset <model-name>
.\aima.exe deploy <model-name> --dry-run
.\aima.exe deploy <model-name>
```

再按实际端口请求 `/v1/chat/completions`，并检查日志中没有 `LLVM ERROR`、COMGR/HIP 设备和显存查询失败。

- [ ] **Step 4: 加入 PR 验收回填模板与故障归类**

模板字段包含：PR/head SHA、artifact 名称、EXE SHA-256、机器、Windows、AMD 驱动/HIP、数据目录、模型/引擎、catalog 结果、dry-run、启动、推理、日志和最终结论。

- [ ] **Step 5: 提交文档**

```bash
git add docs/amd395-partner-build.md
git commit -m "docs: add AMD395 partner build runbook"
```

### Task 4: 完整验证、审查和推送

**Files:**
- Verify: all files above

- [ ] **Step 1: 运行仓库测试**

Run: `go test ./...`

Expected: exit 0，所有 Go package 通过。

- [ ] **Step 2: 运行流水线契约测试**

Run: `make amd395-build-test`

Expected: exit 0，打包及 workflow 契约通过。

- [ ] **Step 3: 运行真实 Windows 交叉构建**

```bash
rm -rf /tmp/aima-amd395-package-verify
OUTPUT_DIR=/tmp/aima-amd395-package-verify bash scripts/package-amd395-windows.sh
(cd /tmp/aima-amd395-package-verify && shasum -a 256 -c checksums.txt)
go version -m /tmp/aima-amd395-package-verify/*.exe
```

Expected: 构建 exit 0、校验和 OK、`go version -m` 显示 `GOOS=windows` 和 `GOARCH=amd64`。

- [ ] **Step 4: 检查 diff 和文档/配置语法**

```bash
git diff --check origin/amd395-win...HEAD
bash -n scripts/package-amd395-windows.sh scripts/test-package-amd395-windows.sh scripts/test-amd395-windows-workflow.sh
ruby -e 'require "yaml"; YAML.parse_file(".github/workflows/amd395-windows-build.yml")'
```

Expected: 全部 exit 0。

- [ ] **Step 5: 请求代码审查并修复 Critical/Important 问题**

审查范围为 `origin/amd395-win...HEAD`，要求对照设计文档、实现计划、安全权限、PR SHA 可追溯性、脚本可移植性和操作手册准确性。

- [ ] **Step 6: 重跑 Step 1–4 的全部验证**

Expected: 所有命令在修复后再次 exit 0。

- [ ] **Step 7: 推送目标分支**

```bash
git push origin amd395-win
```

- [ ] **Step 8: 检查 GitHub Actions 运行结果**

```bash
gh run list --branch amd395-win --workflow "AMD395 Windows Build" --limit 1
gh run watch <run-id> --exit-status
```

Expected: workflow conclusion 为 `success`，artifact 名称包含推送提交的 12 位 SHA。
