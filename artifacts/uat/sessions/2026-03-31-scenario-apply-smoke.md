# Scenario Apply Smoke UAT

**日期**：2026-03-31  
**构建**：`v0.2-dev` / commit `3db1e64`  
**分支**：`feat/uat-scenario-20260331`（from `develop`）  
**worktree**：`/Users/jguan/projects/AIMA-uat-scenario-20260331`  
**范围**：只验证当前 `develop` 上 `scenario list/show/apply` 的本机行为、已有自动化覆盖、以及一个隔离环境下的真实 `scenario apply` 行为。  
**限制**：本轮没有去匹配硬件真机上跑 `scenario.apply`，因此不能把结果外推成 GB10 / AIBook / M1000 真机结论。

## 执行命令

```bash
go test ./cmd/aima -run 'TestScenario|TestFindExistingDeploymentFallsBackToLabelMatch'
go test ./internal/knowledge -run 'TestScenario|TestLoadRealCatalog'
make build
./build/aima version
./build/aima hal detect
./build/aima engine plan
./build/aima model scan
./build/aima scenario list
./build/aima scenario show aibook-coldstart
./build/aima scenario show openclaw-multi
./build/aima scenario apply aibook-coldstart --dry-run
./build/aima scenario apply openclaw-multi --dry-run
HOME=/tmp/aima-scenario-home.xjwN2W \
  AIMA_DATA_DIR=/tmp/aima-scenario-home.xjwN2W/.aima \
  ./build/aima scenario apply openclaw-multi
```

## 结果

### 1. 自动化覆盖现状

- `go test ./cmd/aima -run 'TestScenario|TestFindExistingDeploymentFallsBackToLabelMatch'` 通过。
- `go test ./internal/knowledge -run 'TestScenario|TestLoadRealCatalog'` 通过。
- 现有自动化只覆盖两层：
- catalog 能读出 deployment scenario 字段；
- `scenarioWaitForReady()` 的几种基本分支；
- 没有 `scenario.apply` 的端到端测试，也没有 post-deploy / partial-failure / hardware-mismatch 的测试。

### 2. 本机知识展示链路

- 本机 `hal detect` 识别到的是 `darwin/arm64` / Apple M4，无离散 GPU。
- `scenario list` 能稳定列出两个 scenario：
- `aibook-coldstart`
- `openclaw-multi`
- `scenario show` 能完整展开 `deployments`、`startup_order`、`alternative_configs`、`post_deploy`、`verified` 等字段。

### 3. 本机 `scenario apply --dry-run`

- `aibook-coldstart --dry-run`
- `vllm-musa` 两个 deployment 都失败，错误是 `no engine asset for type "vllm-musa" gpu_arch ""`。
- `funasr-onnx` 和 `litetts` 能给出 dry-run 结果。
- `openclaw-multi --dry-run`
- 四个 deployment 全部失败，错误都是当前机器没有对应 Blackwell engine asset。

### 4. 隔离环境下真实 `scenario apply`

- 我在临时 `HOME` + 临时 `AIMA_DATA_DIR` 下执行了 `scenario apply openclaw-multi`，避免污染当前用户环境。
- 四个 deployment 全部失败后，`post_deploy` 里的 `openclaw_sync` 仍然继续执行。
- 最终输出里直接多出一条：
- `model=openclaw_sync`
- `status=error`
- `error=openclaw sync: read openclaw config: open /tmp/.../.openclaw/openclaw.json: no such file or directory`

## 结论

- `scenario` 目前作为 knowledge 浏览入口是可用的，`list/show` 没问题。
- `scenario.apply` 目前没有足够证据证明“经常 work”。
- 现有仓库里没有对 `scenario.apply` 的端到端自动化保护。
- 现有 UAT 资料主要覆盖 `deploy` / `run` 主链路，不覆盖 `scenario.apply`。
- 这轮实测已经确认一个真实稳定性风险：前序 deployment 全部失败时，`post_deploy` 仍会继续执行。
- 这意味着 `scenario.apply` 当前更接近“best effort 批量调用 deploy.apply”，还不能算一个收口严格的场景编排器。

## 代码对应点

- `cmd/aima/main.go:449-455`
- hardware mismatch 只在非 dry-run 且成功识别出当前 `HardwareProfile` 时才告警，不会阻断执行。
- `cmd/aima/main.go:496-549`
- 每个 deployment 独立执行；中间等待失败只记 warning，不中止后续 deployment。
- `cmd/aima/main.go:552-575`
- `post_deploy` 不看前面的 deployment 是否失败，始终继续执行。
- `cmd/aima/main.go:2677-2755`
- `scenarioWaitForReady()` 超时或状态查询异常时，调用方按 warning 处理，继续往下走。
- `cmd/aima/main_test.go:401-445`
- 只有 `scenarioWaitForReady()` 的分支单测。
- `internal/knowledge/loader_test.go:290-365`
- 只有 scenario catalog 字段解析测试。

## 本轮判断

- 如果你问的是“这个功能能不能拿来当稳定的多模型一键编排入口”，当前答案是：还不能下乐观结论。
- 如果你问的是“这个功能是不是已经完全坏掉”，也不是。知识面和基础串联已经存在，但缺少失败收口和真实矩阵验证。
- 下一步最值得做的不是继续猜，而是：
- 给 `scenario.apply` 补一组 end-to-end table-driven tests；
- 明确失败策略，至少在有 deployment 失败时禁止执行 `post_deploy`；
- 再去 GB10 / AIBook 这两类目标真机上各跑一轮真实 scenario。
