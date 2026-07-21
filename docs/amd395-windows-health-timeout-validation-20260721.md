# AMD395 Windows 模型加载超时修复验证记录（2026-07-21）

## 构建来源

- 基线交付：`dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260715.exe`（打包提交 `04dfd941d1e0ab8266bd00aefb5b635b8d0102da`）
- 修复源码提交：`32b5ee3e6e7470e196dd4a8f3a39dcbd8ae34514`
- 新交付文件：`dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260721.exe`
- 构建时间：`2026-07-21T10:20:55Z`
- SHA-256：`649a74e2d160b0aca470db2365b4d83094662acd16571145d0a45063d12b2c30`

## 根因与修复

合作方两次失败都发生在 llama.cpp 正常加载模型期间。原 AMD395 Windows HIP 引擎继承通用 llama.cpp 的 60 秒健康检查超时；首次加载或系统繁忙时，`fitting params to device memory` 可能超过该边界。部署编排随后把仍存活、仍在加载的进程按超时清理，因此日志中的 `exit status 1` 是清理终止进程的结果，不是模型或 HIP 的原始崩溃。

`schtasks` 失败后回退直接启动会增加约 30 秒总体部署延迟，但健康检查计时从直接启动完成后开始，因此它不是本次 60 秒健康检查耗尽的直接原因。

修复在 `llamacpp-hip-windows` engine asset 中将健康检查超时覆盖为 180 秒。其他平台和引擎继续使用原有超时策略；CLI 外层等待和 native runtime 均从同一 catalog 值读取超时。

## 自动化验证

以下命令均通过：

```text
go test ./...
go vet ./...
make amd395-build-test
```

回归测试同时确认 Windows HIP engine asset 继承 `/health` 路径，并最终解析出 `timeout_s: 180`。

## AMD395 Windows 真机验证

- 测试机：Lenovo Baiying AMD Ryzen AI Max+ 395 / Radeon 8060S
- 引擎：llama.cpp b9180 HIP
- 验证方式：独立 `AIMA_DATA_DIR`，核对候选 EXE SHA-256、版本和完整源码提交后执行

| 模型 | Ready 与参数 | 最小推理 | 清理 |
| --- | --- | --- | --- |
| `Qwen3-Embedding-4B-Q8_0` | Ready；元数据 `health_check_timeout_s=180` | `/v1/embeddings` 返回 2560 维向量 | undeploy 成功 |
| `Qwen3.6-35B-A3B-UD-Q4_K_M` | 冷启动约 40 秒后 Ready；`ctx_size=262144`；元数据 `health_check_timeout_s=180` | chat completion 返回 227 字符 | undeploy 成功 |

验证结束后确认无残留 `llama-server.exe`，8080 无监听；隔离测试目录已清理，模型文件未改动。
