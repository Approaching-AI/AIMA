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

## 真机 GPU 验收阻塞

当前登录账号 `baiying_algorithm_public` 只有自身用户组和 `users` 组，不属于
`render`/`video`；`rocminfo` 返回：

```text
Unable to open /dev/kfd read-write: Permission denied
```

该账号也不具备 sudo 权限。因此在管理员为运行用户授予 `/dev/kfd` 和
`/dev/dri/renderD128` 访问权限前，不能启动 HIP 引擎或完成端到端 GPU 推理验收。
此环境问题不会阻止包构建、版本/校验和核验、HAL/catalog 检查和 dry-run。
