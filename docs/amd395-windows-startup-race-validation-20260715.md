# AMD395 Windows 启动竞态修复验证记录（2026-07-15）

## 构建来源

- 源码提交：`50465375ad2b9417c70a6b4a5776fe8e81c35d4b`
- 交付文件：`dist/aima-windows-amd64-v0.5-dev-amd-strix-halo-20260715.exe`
- 构建时间：`2026-07-15T09:51:00Z`
- SHA-256：`77a70ba0dd98ef865cf4767838f1ac095ef42924b27fc753a2a5ebefcf341e05`
- Go 自动 VCS 字段已关闭，版本来源统一以 AIMA 的完整 `GitCommit` 字段为准，避免构建元数据互相矛盾。

## 本地自动化验证

以下命令均通过：

```text
go test ./...
go test -race ./internal/runtime ./cmd/aima
go vet ./...
make amd395-build-test
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/runtime
```

另外完成了 Windows AMD64 交叉构建、PE 文件类型检查、包内提交来源检查和 SHA-256 校验。

## AMD395 Windows 真机

- 机型：Lenovo Baiying NUC AI Max+ 395
- CPU：AMD Ryzen AI Max+ 395 with Radeon 8060S
- GPU：AMD Radeon 8060S，统一内存，AIMA 检测可用显存约 110456 MiB
- 系统：Windows 10.0.26200.8655，AMD 驱动 32.0.31021.5001
- 引擎：`llamacpp-hip-windows`

最终 dated EXE 在真机上重新核对 SHA-256、版本、完整提交号后执行部署。结果：

| 模型 | 参数/模型文件 | Ready | 最小推理 | 清理 |
| --- | --- | ---: | --- | --- |
| `Qwen3-Embedding-4B-Q4_K_M` | 设备已有 Q8_0 GGUF；`ctx_size=40960`、`embedding=true` | 16.2 秒 | 1 条 embedding，2560 维 | 端口释放 |
| `Qwen3.6-35B-A3B-UD-Q4_K_M` | UD-Q4_K_M；`ctx_size=262144`、`parallel=1`、`cache_ram=0` | 30.6 秒 | chat completion 返回非空输出 | 端口释放 |

同一源码提交的候选构建还额外执行两轮：

| 模型 | 第 2 轮 | 第 3 轮 | 结果 |
| --- | ---: | ---: | --- |
| Embedding | 16.5 秒 | 16.0 秒 | 两轮均 ready，均返回 2560 维向量 |
| Qwen3.6 35B | 30.5 秒 | 24.4 秒 | 两轮均 ready，均返回非空输出 |

合计两个模型各 3 轮部署均成功；每轮结束后端口均释放，最终等待 5 秒确认 `llama-server` 进程数和 8080/8081 监听数均为 0。

部署元数据清理后再次执行 `aima deploy logs <name>`，两个模型都能从确定性日志路径读取尾部日志，验证失败清理后日志仍可访问。

## 覆盖边界

测试机执行验证时没有登录中的 Windows 交互桌面会话，因此真机使用 native direct-launch 路径。此次故障的核心修复——进程存活但尚未监听端口时保持 `starting`、不让持久化误判覆盖内存状态——已由真机冷启动覆盖；`schtasks` 新进程 PID 差分选择由 Windows 交叉编译和纯逻辑单元测试覆盖。
