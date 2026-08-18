# AMD395 Linux 构建与验收手册

本文适用于 AMD Ryzen AI Max+ 395 / Radeon 8060S（Strix Halo）的 Linux x86_64 交付包。
该平台分支以 `amd395-win` 的已验收能力为基线，并使用 Linux 原生 ROCm/HIP 引擎。

## 分支与构建

平台分支为 `amd395-linux`。从该分支创建修改分支时，Pull Request 目标仍应为
`amd395-linux`，以触发 `AMD395 Linux Build`。

本地构建和契约测试：

```bash
make amd395-linux-build-test
OUTPUT_DIR="$PWD/dist/amd395-linux" bash scripts/package-amd395-linux.sh
(cd dist/amd395-linux && sha256sum -c checksums.txt)
```

产物包括：

```text
aima-linux-amd64-v0.5-dev-amd-strix-halo-<12位提交SHA>
checksums.txt
build-metadata.json
```

`build-metadata.json.git_commit` 和 `aima version` 中的完整提交号必须一致。

## 运行环境

当前平台引擎为 `llamacpp-hip-linux`：

- llama.cpp：`b9330`
- 官方包：`ubuntu-rocm-7.2-x64.tar.gz`
- 运行方式：Linux native
- 目标 GPU：Radeon 8060S / `gfx1151` / RDNA3.5
- 目标 ROCm：7.2.x

运行 AIMA 的用户必须能够读写 `/dev/kfd` 和 `/dev/dri/render*`。Ubuntu 通常需要：

```bash
sudo usermod -aG render,video <运行用户>
```

修改组后应重新登录，并先确认：

```bash
id
rocminfo | grep -E 'gfx1151|Marketing Name'
rocm-smi --showuse --showmemuse
```

若 `rocminfo` 报 `/dev/kfd` `Permission denied`，禁止宣称 GPU 推理验收通过。

## 干净目录静态验收

不要覆盖用户正在使用的数据目录：

```bash
export AIMA_DATA_DIR="$(mktemp -d /tmp/aima-amd395-linux.XXXXXX)"
./aima-linux-amd64-v0.5-dev-amd-strix-halo-<SHA> version
./aima-linux-amd64-v0.5-dev-amd-strix-halo-<SHA> hal detect
./aima-linux-amd64-v0.5-dev-amd-strix-halo-<SHA> catalog validate
./aima-linux-amd64-v0.5-dev-amd-strix-halo-<SHA> catalog effective model_asset qwen3.6-35b-a3b
```

`hal detect` 应识别 RDNA3.5、`gfx1151` 和 unified memory。Linux native 解析必须选择
`llamacpp-hip-linux`，而不是 Windows HIP 包或旧的 Vulkan 包。

## 真机推理验收

先使用实际 GGUF 路径预览：

```bash
./aima-linux-amd64-v0.5-dev-amd-strix-halo-<SHA> \
  deploy qwen3.6-35b-a3b --engine llamacpp \
  --config model_path=/path/to/Qwen3.6-35B-A3B-UD-Q4_K_M.gguf \
  --dry-run
```

确认引擎为 `llamacpp-hip-linux`、默认参数包含 `ctx_size=262144`、`parallel=1`、
`cache_ram=0` 后再部署。首次部署会自动下载官方 b9330 ROCm 7.2 包。

最低验收门槛：

1. 引擎成功识别 `gfx1151`，模型全部层可卸载到 GPU。
2. 部署进入 ready，最小 chat completion 返回非空内容。
3. 连续部署、推理、undeploy 至少两轮，端口和进程均正确释放。
4. 日志没有 HIP device、ROCm library、OOM、COMGR 或 recurrent `seq_rm` 崩溃。
5. 验收期间 GPU 和机器没有其他研发、性能测试任务。

结束后执行 `deploy undeploy` 并再次确认 `rocm-smi`、`ps` 和监听端口已恢复空闲。
