# AMD395 Linux 验证记录（2026-07-15）

## 基线与目标机

- 源码基线：远端 `amd395-win` 提交 `04dfd941d1e0ab8266bd00aefb5b635b8d0102da`
- Linux 分支：`amd395-linux`
- 机型：AMD Ryzen AI Max+ 395 with Radeon 8060S
- 系统：Ubuntu 24.04.4 LTS，Linux 6.17.0-35-generic，x86_64
- GPU：Radeon 8060S，`gfx1151`，RDNA3.5，64 GiB unified memory
- ROCm：7.2.3
- Linux 引擎：`llamacpp-hip-linux`，官方 llama.cpp b9330 ROCm 7.2 x64 包

## 机器占用检查

验证开始前：

- GPU utilization：0%
- GPU VRAM utilization：0%
- 没有运行中的 Docker 容器或 Kubernetes Pod
- 没有发现推理引擎或性能测试进程

因此可以进行不会启动 GPU 引擎的上传、静态和 dry-run 验证。

## 已完成

- 从今天 Windows UAT 对应的远端 `amd395-win` 最新提交创建独立分支。
- 增加 Linux x86_64 可追溯打包脚本、SHA-256 和构建元数据。
- 增加 `AMD395 Linux Build` GitHub Actions 工作流和契约测试。
- 增加 `llamacpp-hip-linux` catalog，引擎固定为 b9330，自动下载官方
  `ubuntu-rocm-7.2-x64` 包。
- 单元测试覆盖 Linux native 平台解析，确保 AMD395 选择 Linux HIP 引擎。

## 候选包与自动化结果

- 源码提交：`545f6cf04d6f38730064235d6023cea1b21050f4`
- 候选包：`aima-linux-amd64-v0.5-dev-amd-strix-halo-545f6cf04d6f`
- 构建时间：`2026-07-15T13:48:52Z`
- SHA-256：`da72fa5c85f47c09a1713f6077d71d3bf161e9b1905b368f9628605a7a531b3a`
- 格式：静态链接 Linux x86-64 ELF

以下自动化命令通过：

```text
go test ./...
go test -race ./internal/runtime ./cmd/aima
go vet ./...
make amd395-linux-build-test
git diff --check
```

## AMD395 Linux 真机静态验证

候选包上传到独立目录 `~/aima-uat/20260715-linux-545f6cf/`，并使用独立的
`data-clean` 作为 `AIMA_DATA_DIR`，没有读取或覆盖用户现有 AIMA 数据。

- SHA-256 真机复核通过。
- `aima version` 显示 `v0.5-dev`，完整提交与构建元数据一致。
- `hal detect` 正确识别 `RDNA3.5`、`gfx1151`、64 GiB unified memory 和 ROCm 7.2.3。
- `catalog status` 显示 94 个 factory assets、0 个 overlay assets。
- `catalog effective` 显示 Qwen3.6 的 `ctx_size=262144`、`parallel=1`、
  `cache_ram=0`、`n_gpu_layers=999`。
- `deploy --dry-run` 成功，fit=true、runtime=native，配置与上述默认值一致。
- `engine info llamacpp-hip-linux` 显示 b9330、linux/amd64 和官方
  `ubuntu-rocm-7.2-x64.tar.gz` 下载地址。
- `engine pull llamacpp-hip-linux` 完成 124 MiB 下载、解包和 native engine 注册。
- 解包后的 `llama-server --version` 返回 `version: 9330 (328874d05)`。
- 验证结束时 GPU/VRAM utilization 均为 0，未遗留 `llama-server` 进程。

`catalog validate` 命令正常完成；它同时报告仓库基线中 12 项 catalog 问题，包含
10 项 warning 和 2 项与本次 AMD395 Linux 变更无关的 Blackwell container registry
错误。本次新增 Linux HIP engine 没有新增校验问题。

## 真机 GPU 验收阻塞

当前登录账号 `baiying_algorithm_public` 只有自身用户组和 `users` 组，不属于
`render`/`video`；`rocminfo` 返回：

```text
Unable to open /dev/kfd read-write: Permission denied
```

该账号也不具备 sudo 权限。因此在管理员为运行用户授予 `/dev/kfd` 和
`/dev/dri/renderD128` 访问权限前，不能启动 HIP 引擎或完成端到端 GPU 推理验收。
`llama-server --version` 也因此同时输出 `failed to initialize ROCm: no ROCm-capable
device is detected`，但仍确认二进制版本为 b9330。此环境问题不会阻止包构建、
版本/校验和核验、HAL/catalog 检查、引擎下载解包和 dry-run。
