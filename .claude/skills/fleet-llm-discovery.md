# Fleet LLM 端点自动发现

## 场景
设备无本地 LLM 模型（如 Windows 开发机），需要自动找到 Fleet 中运行推理服务的远程设备。

## 架构模式：DiscoverFunc 注入

`internal/agent` 包不能直接依赖 `internal/proxy`（会造成循环依赖方向）。
解法：定义 `DiscoverFunc` 函数类型，在 `cmd/aima/main.go` 组装注入。

```
agent.OpenAIClient  ←(DiscoverFunc)←  main.go  →(proxy.Discover)→  proxy 包
```

- `agent` 包只知道 `DiscoverFunc func(ctx, apiKey) []FleetEndpoint`
- `main.go` 创建 `discoverFleetLLM()` 桥接 `proxy.Discover` + `proxy.QueryRemoteModels`
- 这是标准的 "wire at main" 依赖注入模式

## 降级链设计

```
resolveModel():
  1. 显式 model 名 → 直接返回
  2. 缓存 <30s → 返回缓存
  3. GET baseURL/models
     ├─ 连接失败 → discoverFleetEndpoint()
     └─ 返回空   → discoverFleetEndpoint()
  4. mDNS 扫描 → 查询远程 /v1/models → hot-swap baseURL
```

关键设计决策：
- **懒触发**: 只在实际需要 LLM 时发现，不在启动时做 I/O
- **不持久化**: 发现结果存内存（30s 缓存），进程重启重新发现
- **hot-swap**: 成功后直接修改 `baseURL`/`cachedModel`，后续请求自动走远程

## 踩坑记录

1. **mDNS 默认关闭**: `aima serve --mdns` 原默认 false，远程设备不广播就扫不到。改为默认 true。
2. **导出函数**: `queryRemoteModels` 是 unexported，跨包调用需要大写导出。
3. **自发现过滤**: `proxy.IsLocalIP(addr)` 过滤本机 mDNS 广播，避免自己连自己。
4. **已部署 systemd unit 不会自动获取新默认值**: 需要重新 `aima init`。

## 测试要点

- 本地有模型 → 不触发发现（resolveModel 第 3 步成功）
- 本地无模型 + 有远程 → hot-swap 到远程
- 本地不可达 + 有远程 → hot-swap 到远程
- 无 DiscoverFunc → 原始错误
- DiscoverFunc 返回空 → 原始错误
