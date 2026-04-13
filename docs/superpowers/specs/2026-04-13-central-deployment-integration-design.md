# Central Knowledge Server 生产部署集成设计

> 日期: 2026-04-13
> 状态: 已确认
> 涉及 Repo: aima-central-knowledge, aima-service, AIMA (edge)

## 1. 背景与目标

Central Knowledge Server 已从 AIMA 主 repo 拆分为独立 repo（`aima-central-knowledge`），
v0.4.0 功能开发完成（18 个 REST 端点），代码 review 和多平台 smoke test 均通过。

**目标：** 将 Central 部署到 `aima-oversea` 生产服务器，集成进现有 aima-service 技术栈，
使国内 Edge 设备通过 `https://aimaservice.ai/central` 访问知识聚合服务。

**约束：**
- 不影响 aima-service 现有服务的运行和 CI/CD 流程
- 不引入新的外部端口或域名
- 后续升级维护简单，Central 可独立升级不影响其他组件

## 2. 整体架构

```
Edge 设备 (gb10, linux-1, amd395, ...)
  │
  │ HTTPS (TLS, Let's Encrypt)
  ▼
aima-gateway (Rust/Axum, :443)
  ├── /api/v1/*            → platform:8000   (现有，不动)
  ├── /central/*           → central:8081    (新增，strip /central 前缀)
  ├── /doctor/*            → platform:8000   (现有，不动)
  ├── /ws/*, /sse/*        → platform:8000   (现有，不动)
  └── /healthz             → gateway 本地    (现有，不动)

Docker Compose 内部网络:
  ┌─────────────────────────────────────────┐
  │  postgres (:5432)                       │
  │    ├── database: aima          (platform)│
  │    └── database: aima_central  (central) │
  │                                         │
  │  platform (:8000) ← 不动               │
  │  gateway  (:80/:443) ← 加 central slice │
  │  central  (:8081) ← 新增               │
  │  redis, temporal, ... ← 不动            │
  └─────────────────────────────────────────┘
```

## 3. 数据库策略

- **独立 database**：在同一 PostgreSQL 实例中创建 `aima_central` 数据库
- DSN: `postgresql://aima:${AIMA_PG_PASSWORD}@postgres:5432/aima_central`
- Central 的 `Migrate()` 自动建表（7 张表：devices, configurations, benchmark_results, knowledge_notes, advisories, analysis_runs, scenarios）
- 与 platform 的 `aima` 数据库完全隔离，零冲突

**初始化（一次性）：**
```bash
docker compose exec postgres psql -U aima -c "CREATE DATABASE aima_central;"
```

## 4. Gateway 改动

### 4.1 config.rs

新增一个可选字段：
```rust
pub central_upstream_url: Option<String>,
```
从环境变量 `CENTRAL_UPSTREAM_URL` 读取。未设置时 central slice 不注册。

### 4.2 proxy.rs

新增通用代理函数：
```rust
pub async fn proxy_to_url(
    url_base: &str,
    upstream_path: &str,
    inbound_method: axum::http::Method,
    inbound_headers: &HeaderMap,
    forward_header_names: &[&str],
    inbound_body: Body,
    client: &reqwest::Client,
) -> Response
```

现有 `proxy_to_upstream()` 内部改为调用 `proxy_to_url(state.config.platform_upstream_url, ...)`，
保持完全向后兼容。

### 4.3 routes/slices/central.rs（新增）

- 路由: `/central/{*rest}`
- 行为: strip `/central` 前缀，保留 query string，代理到 `CENTRAL_UPSTREAM_URL`
- 转发 Headers: `authorization`, `content-type`, `content-length`, `accept`, `user-agent`, `x-request-id`
- 可选禁用: `CENTRAL_UPSTREAM_URL` 未设置时不注册路由

路由映射示例：
```
GET  /central/healthz                    → GET  http://central:8081/healthz
POST /central/api/v1/ingest              → POST http://central:8081/api/v1/ingest
GET  /central/api/v1/sync?since=2026-04  → GET  http://central:8081/api/v1/sync?since=2026-04
GET  /central/api/v1/gaps                → GET  http://central:8081/api/v1/gaps
```

### 4.4 注册位置

central slice 在 `slices/mod.rs` 中注册，与 doctor/growth 同级。
`/central/*` 前缀不与 `/api/v1/*` catch-all 冲突。

## 5. Docker Compose 改动

### 5.1 central 服务定义

```yaml
central:
  image: aima-central:latest
  environment:
    - CENTRAL_ADDR=:8081
    - CENTRAL_DB_DRIVER=postgres
    - CENTRAL_DB_DSN=postgresql://aima:${AIMA_PG_PASSWORD}@postgres:5432/aima_central
    - CENTRAL_API_KEY=${CENTRAL_API_KEY:?Set CENTRAL_API_KEY in .env}
  depends_on:
    postgres:
      condition: service_healthy
  healthcheck:
    test: ["CMD", "wget", "-qO-", "http://localhost:8081/healthz"]
    interval: 10s
    timeout: 3s
    retries: 3
    start_period: 5s
  restart: unless-stopped
  logging:
    driver: json-file
    options:
      max-size: "50m"
      max-file: "3"
```

**不影响现有服务的保证：**
- 无 `ports:` — 不暴露宿主端口
- 无额外 `networks:` — 用默认 compose 网络
- `image:` 非 `build:` — 不介入 aima-service 构建流程
- Central 升级只需 `docker compose up -d central`

### 5.2 gateway 环境变量新增

```yaml
- CENTRAL_UPSTREAM_URL=${CENTRAL_UPSTREAM_URL:-}
```

### 5.3 .env 文件新增

```
CENTRAL_API_KEY=<生成一个强随机 token>
CENTRAL_UPSTREAM_URL=http://central:8081
```

## 6. AIMA Edge 改动

### 6.1 默认 endpoint

`tooldeps_integration.go` 中 9 个 sync 函数的 endpoint 读取逻辑改为：

```go
const defaultCentralEndpoint = "https://aimaservice.ai/central"

func centralEndpoint(ctx context.Context, getConfig func(context.Context, string) (string, error)) string {
    ep, _ := getConfig(ctx, "central.endpoint")
    if ep == "" {
        return defaultCentralEndpoint
    }
    return ep
}
```

用户可通过 `system.config set central.endpoint <url>` 覆盖默认值。

## 7. Central 容器化

### 7.1 Dockerfile（aima-central-knowledge repo 新增）

```dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /central ./cmd/central/

FROM alpine:3.20
RUN apk add --no-cache wget
COPY --from=build /central /usr/local/bin/central
ENTRYPOINT ["central"]
```

零 CGO，多阶段构建，最终镜像 ~15 MB。
`wget` 用于 healthcheck（alpine 无 curl）。

### 7.2 构建与升级流程

```bash
# 首次部署
cd /root/aima-central-knowledge
docker build -t aima-central:latest .

# 升级 Central（不影响其他服务）
cd /root/aima-central-knowledge
git pull
docker build -t aima-central:latest .
cd /root/aima-service
docker compose up -d central

# 如果 gateway 也有改动
docker compose build gateway
docker compose up -d gateway
```

## 8. 跨 Repo 文档策略

### 8.1 aima-central-knowledge

| 文件 | 内容 |
|------|------|
| `deploy/README.md`（新增） | 构建镜像、环境变量、升级/回滚步骤、健康检查 |
| `CLAUDE.md` | 新增"生产部署"段落，指向 deploy/README.md |

### 8.2 aima-service

| 位置 | 内容 |
|------|------|
| `docker-compose.prod.yml` 注释 | 源码指向 aima-central-knowledge repo，升级步骤 |
| `central.rs` 文件头注释 | 说明 Central 是独立 Go 服务，API 契约位置 |

### 8.3 AIMA (edge)

| 位置 | 内容 |
|------|------|
| `CLAUDE.md` | 更新 Central 部署段落：生产 URL、默认值、覆盖方式 |

### 8.4 版本关联约定

- **API 契约文件** `aima-central-knowledge/api/openapi.yaml` 是唯一真相
- Central 发布 git tag 时，commit message 注明 Edge 兼容性要求
- 不引入复杂的版本锁定机制

## 9. 改动矩阵

| Repo | 文件 | 类型 | 行数 |
|------|------|------|------|
| aima-central-knowledge | `Dockerfile` | 新增 | ~10 |
| aima-central-knowledge | `deploy/README.md` | 新增 | ~60 |
| aima-central-knowledge | `CLAUDE.md` | 修改 | ~10 |
| aima-service | `apps/gateway/src/config.rs` | 修改 | ~5 |
| aima-service | `apps/gateway/src/proxy.rs` | 修改 | ~20 |
| aima-service | `apps/gateway/src/routes/slices/central.rs` | 新增 | ~70 |
| aima-service | `apps/gateway/src/routes/slices/mod.rs` | 修改 | ~5 |
| aima-service | `apps/gateway/src/lib.rs` | 修改 | ~5 |
| aima-service | `docker-compose.prod.yml` | 修改 | ~25 |
| AIMA | `cmd/aima/tooldeps_integration.go` | 修改 | ~20 |
| AIMA | `CLAUDE.md` | 修改 | ~10 |
| **合计** | | | **~240** |

## 10. 验证计划

1. **本地验证**: Central 容器 + 独立 PostgreSQL 跑 smoke test
2. **Gateway 验证**: gateway unit test 确认 central slice 路由正确
3. **端到端验证**: Edge 设备通过 `https://aimaservice.ai/central/healthz` 访问
4. **回归验证**: aima-service 现有端点 (`/api/v1/*`, `/healthz`, `/ws/*`) 不受影响
