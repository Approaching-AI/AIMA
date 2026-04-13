# Central Knowledge Server 生产部署集成 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Central Knowledge Server 部署到 aima-oversea 生产服务器，通过 Rust gateway 的 `/central/*` 路径代理，使 Edge 设备默认访问 `https://aimaservice.ai/central`。

**Architecture:** Gateway 新增 central route slice，strip `/central` 前缀后代理到独立 Central 容器。Central 用独立 PostgreSQL database (`aima_central`)，通过 docker-compose 集成。Edge 设备默认 endpoint 改为生产 URL，用户可覆盖。

**Tech Stack:** Rust/Axum (gateway), Go (central), PostgreSQL, Docker, aima-service docker-compose

**跨 Repo 说明：** 本计划涉及三个 repo 的改动。每个 Task 标注了所属 repo。

| Repo | 本地路径 | 分支策略 |
|------|----------|----------|
| AIMA (edge) | `/Users/jguan/projects/AIMA` (当前 worktree) | `worktree-feat+central-deployment-integration` |
| aima-central-knowledge | `/Users/jguan/projects/aima-central-knowledge` | `feat/docker-deploy` (新建) |
| aima-service | `/Users/jguan/projects/aima-service` | `feat/central-proxy` (新建) |

---

### Task 1: Central Dockerfile（aima-central-knowledge repo）

**Files:**
- Create: `/Users/jguan/projects/aima-central-knowledge/Dockerfile`
- Create: `/Users/jguan/projects/aima-central-knowledge/.dockerignore`

- [ ] **Step 1: 创建分支**

```bash
cd /Users/jguan/projects/aima-central-knowledge
git checkout develop
git pull origin develop
git checkout -b feat/docker-deploy
```

- [ ] **Step 2: 创建 .dockerignore**

```
# /Users/jguan/projects/aima-central-knowledge/.dockerignore
.git
*.db
central.db
docs/
```

- [ ] **Step 3: 创建 Dockerfile**

```dockerfile
# /Users/jguan/projects/aima-central-knowledge/Dockerfile
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

- [ ] **Step 4: 验证构建**

```bash
cd /Users/jguan/projects/aima-central-knowledge
docker build -t aima-central:test .
```

Expected: 构建成功，镜像产出。

- [ ] **Step 5: 验证镜像可启动（PostgreSQL 不可用时应报错退出，不崩溃）**

```bash
docker run --rm -e CENTRAL_DB_DRIVER=postgres -e CENTRAL_DB_DSN=postgresql://x:x@localhost:5432/x aima-central:test 2>&1 || true
```

Expected: 输出包含 `ping postgres` 错误信息，非 panic。

- [ ] **Step 6: Commit**

```bash
cd /Users/jguan/projects/aima-central-knowledge
git add Dockerfile .dockerignore
git commit -m "feat: add Dockerfile for production deployment

Multi-stage build, zero CGO, alpine-based (~15MB).
Used by aima-service docker-compose as 'aima-central:latest'."
```

---

### Task 2: Central 部署文档（aima-central-knowledge repo）

**Files:**
- Create: `/Users/jguan/projects/aima-central-knowledge/deploy/README.md`
- Modify: `/Users/jguan/projects/aima-central-knowledge/CLAUDE.md`

- [ ] **Step 1: 创建 deploy/README.md**

```markdown
# Central Knowledge Server 生产部署

## 构建镜像

```bash
cd /root/aima-central-knowledge
docker build -t aima-central:latest .
```

## 环境变量

| 变量 | 必填 | 默认值 | 说明 |
|------|------|--------|------|
| `CENTRAL_ADDR` | 否 | `:8080` | 监听地址（compose 中用 `:8081`） |
| `CENTRAL_DB_DRIVER` | 否 | `sqlite` | 生产用 `postgres` |
| `CENTRAL_DB_DSN` | 是(pg) | — | PostgreSQL DSN |
| `CENTRAL_API_KEY` | 是 | — | Bearer token（timing-safe） |
| `CENTRAL_LLM_ENDPOINT` | 否 | — | LLM 地址（启用 Advisor） |
| `CENTRAL_LLM_API_KEY` | 否 | — | LLM API Key |
| `CENTRAL_LLM_MODEL` | 否 | `gpt-4` | LLM 模型名 |

完整环境变量列表见 CLAUDE.md 的"部署配置"段。

## 数据库初始化（一次性）

```bash
# 在 aima-service 目录
docker compose exec postgres psql -U aima -c "CREATE DATABASE aima_central;"
```

Central 启动时自动执行 `Migrate()` 建表。

## 升级流程

```bash
# 1. 拉取最新代码并构建镜像
cd /root/aima-central-knowledge
git pull
docker build -t aima-central:latest .

# 2. 滚动更新（不影响其他服务）
cd /root/aima-service
docker compose up -d central
```

## 回滚

```bash
# 1. 查看可用镜像
docker images aima-central

# 2. 重新构建旧版本
cd /root/aima-central-knowledge
git checkout <previous-tag-or-commit>
docker build -t aima-central:latest .

# 3. 重启
cd /root/aima-service
docker compose up -d central
```

## 健康检查

```bash
# 容器内部
docker compose exec central wget -qO- http://localhost:8081/healthz

# 通过 gateway（生产）
curl https://aimaservice.ai/central/healthz
```

## 架构位置

```
Edge → HTTPS → aima-gateway (:443) → /central/* → central (:8081)
                                                       │
                                                  PostgreSQL (aima_central db)
```

Central 是 aima-service docker-compose 中的独立容器。
Gateway 通过 CENTRAL_UPSTREAM_URL 环境变量指向 central 服务。
API 契约文档: `api/openapi.yaml`
```

- [ ] **Step 2: 更新 CLAUDE.md 添加生产部署段落**

在 CLAUDE.md 的"部署配置"段后添加：

```markdown
## 生产部署

Central 部署在 `aima-oversea` 服务器，作为 aima-service docker-compose 中的独立容器运行。

- **访问地址**: `https://aimaservice.ai/central`（通过 Rust gateway 代理）
- **数据库**: PostgreSQL `aima_central` database（与 platform 的 `aima` database 隔离）
- **镜像构建**: `docker build -t aima-central:latest .`（本 repo 根目录）
- **详细部署文档**: `deploy/README.md`

Gateway 配置: `CENTRAL_UPSTREAM_URL=http://central:8081`（aima-service 的 `.env` 文件中）
```

- [ ] **Step 3: Commit**

```bash
cd /Users/jguan/projects/aima-central-knowledge
git add deploy/README.md CLAUDE.md
git commit -m "docs: add production deployment guide and update CLAUDE.md

deploy/README.md: build, env vars, upgrade/rollback, health check.
CLAUDE.md: add production deployment section."
```

---

### Task 3: Gateway config 新增 central_upstream_url（aima-service repo）

**Files:**
- Modify: `/Users/jguan/projects/aima-service/apps/gateway/src/config.rs:23-43` (struct) + `:98-131` (from_env)

- [ ] **Step 1: 创建分支**

```bash
cd /Users/jguan/projects/aima-service
git checkout develop
git pull origin develop
git checkout -b feat/central-proxy
```

- [ ] **Step 2: 在 GatewayConfig struct 添加字段**

在 `config.rs:42`（`rate_limit` 字段后）添加：

```rust
    /// Optional upstream URL for the Central Knowledge Server.
    /// When set, the gateway proxies `/central/*` to this URL.
    /// Example: `http://central:8081`
    pub central_upstream_url: Option<String>,
```

- [ ] **Step 3: 在 from_env() 中解析新字段**

在 `config.rs:129`（`rate_limit: GatewayRateLimitConfig::from_env(),` 之后）添加：

```rust
            central_upstream_url: read_optional_env("CENTRAL_UPSTREAM_URL"),
```

- [ ] **Step 4: 更新 lib.rs 中的 test_config()**

在 `lib.rs` 的 `test_config()` 函数中（`rate_limit` 字段后）添加：

```rust
            central_upstream_url: None,
```

- [ ] **Step 5: 更新 routes/api.rs 中的 test_config()**

在 `routes/api.rs` 的 `test_config()` 函数中（`rate_limit` 字段后）添加：

```rust
            central_upstream_url: None,
```

- [ ] **Step 6: 验证编译**

```bash
cd /Users/jguan/projects/aima-service
cargo build -p aima-gateway 2>&1 | tail -5
```

Expected: 编译成功。

- [ ] **Step 7: 运行现有测试确保无回归**

```bash
cd /Users/jguan/projects/aima-service
cargo test -p aima-gateway 2>&1 | tail -20
```

Expected: 所有现有测试通过。

- [ ] **Step 8: Commit**

```bash
cd /Users/jguan/projects/aima-service
git add apps/gateway/src/config.rs apps/gateway/src/lib.rs apps/gateway/src/routes/api.rs
git commit -m "feat(gateway): add central_upstream_url config field

Optional env var CENTRAL_UPSTREAM_URL. When set, enables /central/* proxy.
No behavioral change when unset — existing routes unaffected."
```

---

### Task 4: proxy.rs 新增 proxy_to_url 通用函数（aima-service repo）

**Files:**
- Modify: `/Users/jguan/projects/aima-service/apps/gateway/src/proxy.rs`

- [ ] **Step 1: 提取 proxy_to_url 通用函数**

在 `proxy.rs` 中，将 `proxy_to_upstream` 的核心逻辑提取为 `proxy_to_url`。在 `proxy_to_upstream` 函数之前添加：

```rust
/// Forward an inbound request to an arbitrary upstream URL and stream the
/// response back. This is the generic version of [`proxy_to_upstream`] —
/// callers supply the full base URL instead of reading it from state.
pub async fn proxy_to_url(
    url_base: &str,
    upstream_path: &str,
    inbound_method: axum::http::Method,
    inbound_headers: &HeaderMap,
    forward_header_names: &[&str],
    inbound_body: Body,
    client: &reqwest::Client,
) -> Response {
    let upstream_url = format!(
        "{}{}",
        url_base.trim_end_matches('/'),
        upstream_path,
    );

    let method: reqwest::Method = match inbound_method.as_str().parse() {
        Ok(m) => m,
        Err(_) => return bad_gateway("unsupported HTTP method"),
    };

    let mut req = client.request(method, &upstream_url);

    // Forward selected headers.
    for &name in forward_header_names {
        if is_hop_by_hop(name) {
            continue;
        }
        if let Some(value) = inbound_headers.get(name) {
            if let Ok(s) = value.to_str() {
                req = req.header(name, s);
            }
        }
    }

    // Collect the inbound body and forward it.
    let body_bytes = match axum::body::to_bytes(inbound_body, 10 * 1024 * 1024).await {
        Ok(bytes) => bytes,
        Err(err) => {
            warn!(?err, "failed to read inbound body");
            return bad_gateway("failed to read request body");
        }
    };
    if !body_bytes.is_empty() {
        req = req.body(body_bytes);
    }

    match req.send().await {
        Ok(upstream_resp) => convert_upstream_response(upstream_resp).await,
        Err(err) => {
            warn!(?err, upstream_url, "upstream request failed");
            if err.is_timeout() {
                (StatusCode::GATEWAY_TIMEOUT, "upstream timeout").into_response()
            } else {
                bad_gateway("upstream unreachable")
            }
        }
    }
}
```

- [ ] **Step 2: 验证编译**

```bash
cd /Users/jguan/projects/aima-service
cargo build -p aima-gateway 2>&1 | tail -5
```

Expected: 编译成功。

- [ ] **Step 3: 运行现有测试确保无回归**

```bash
cd /Users/jguan/projects/aima-service
cargo test -p aima-gateway 2>&1 | tail -20
```

Expected: 所有现有测试通过（proxy_to_upstream 未改，行为不变）。

- [ ] **Step 4: Commit**

```bash
cd /Users/jguan/projects/aima-service
git add apps/gateway/src/proxy.rs
git commit -m "feat(gateway): add generic proxy_to_url function

Extracted from proxy_to_upstream — accepts url_base parameter instead of
reading from state. Used by the upcoming central proxy slice.
Existing proxy_to_upstream is unchanged."
```

---

### Task 5: central route slice（aima-service repo）

**Files:**
- Create: `/Users/jguan/projects/aima-service/apps/gateway/src/routes/slices/central.rs`
- Modify: `/Users/jguan/projects/aima-service/apps/gateway/src/routes/slices/mod.rs`
- Modify: `/Users/jguan/projects/aima-service/apps/gateway/src/lib.rs`

- [ ] **Step 1: 创建 central.rs**

```rust
//! Central Knowledge Server proxy slice.
//!
//! Proxies `/central/*` requests to the Central Knowledge Server upstream,
//! stripping the `/central` prefix before forwarding.
//! Central is a standalone Go service (repo: aima-central-knowledge).
//! API contract: aima-central-knowledge/api/openapi.yaml

use axum::{
    extract::{Request, State},
    http::{header, HeaderValue},
    response::{IntoResponse, Response},
    routing::any,
    Router,
};
use tracing::{info, warn};

use crate::{proxy, state::GatewayState};

const ROUTE_OWNER_HEADER: &str = "x-aima-route-owner";
const ROUTE_TRANSPORT_HEADER: &str = "x-aima-route-transport";
const ROUTE_OWNER_RUST_GATEWAY: &str = "rust-gateway";
const ROUTE_TRANSPORT_HTTP_PROXY: &str = "http-proxy";

/// Headers forwarded for Central proxy requests.
const FORWARDED_HEADERS: &[&str] = &[
    "authorization",
    "content-type",
    "content-length",
    "accept",
    "user-agent",
    "x-request-id",
];

/// Build the central proxy router. Returns an empty router when
/// `CENTRAL_UPSTREAM_URL` is not configured.
pub fn router() -> Router<GatewayState> {
    Router::new().route("/central/{*rest}", any(proxy_central))
}

async fn proxy_central(State(state): State<GatewayState>, request: Request) -> Response {
    let central_url = match &state.config.central_upstream_url {
        Some(url) => url.clone(),
        None => {
            return (
                axum::http::StatusCode::NOT_FOUND,
                "central proxy not configured",
            )
                .into_response();
        }
    };

    let original_path = request
        .uri()
        .path_and_query()
        .map(|pq| pq.as_str().to_string())
        .unwrap_or_else(|| request.uri().path().to_string());

    // Strip the /central prefix, keeping the rest of the path + query string.
    let stripped_path = original_path
        .strip_prefix("/central")
        .unwrap_or(&original_path);

    // Ensure the stripped path starts with /
    let upstream_path = if stripped_path.starts_with('/') {
        stripped_path.to_string()
    } else {
        format!("/{stripped_path}")
    };

    let method = request.method().clone();
    let headers = request.headers().clone();
    let body = request.into_body();

    info!(
        method = %method,
        original_path = %original_path,
        upstream_path = %upstream_path,
        "proxying central request"
    );

    let mut response = proxy::proxy_to_url(
        &central_url,
        &upstream_path,
        method,
        &headers,
        FORWARDED_HEADERS,
        body,
        &state.http_client,
    )
    .await;

    attach_route_diagnostics(&mut response);
    response
}

fn attach_route_diagnostics(response: &mut Response) {
    response.headers_mut().insert(
        header::HeaderName::from_static(ROUTE_OWNER_HEADER),
        HeaderValue::from_static(ROUTE_OWNER_RUST_GATEWAY),
    );
    response.headers_mut().insert(
        header::HeaderName::from_static(ROUTE_TRANSPORT_HEADER),
        HeaderValue::from_static(ROUTE_TRANSPORT_HTTP_PROXY),
    );
}
```

- [ ] **Step 2: 注册 central module in slices/mod.rs**

在 `slices/mod.rs` 的 module 声明中（`pub mod static_assets;` 之后）添加：

```rust
pub mod central;
```

在 `PLANNED_ROUTE_SLICES` 数组末尾（`sse` 条目之后）添加：

```rust
    PlannedRouteSlice {
        name: "central",
        prefix: "/central/*",
        status: "active",
    },
```

在 `pub fn router()` 函数中（`// Static assets` 注释之前）添加：

```rust
        // Central Knowledge Server: `/central/*` proxied to central upstream.
        .merge(central::router())
```

- [ ] **Step 3: 在 lib.rs 的 build_app 中注册 central router**

在 `lib.rs:80`（`.merge(routes::api::router())` 之前）添加：

```rust
        // Central Knowledge Server proxy: /central/* → central upstream.
        .merge(routes::slices::central::router())
```

- [ ] **Step 4: 验证编译**

```bash
cd /Users/jguan/projects/aima-service
cargo build -p aima-gateway 2>&1 | tail -5
```

Expected: 编译成功。

- [ ] **Step 5: 更新 lib.rs 测试中 planned_route_slices 的数量断言**

在 `lib.rs` 的 `config_endpoint_lists_all_slices` 测试中，更新：

```rust
        assert_eq!(slices.len(), 13);  // was 12, now includes "central"
```

和 `metadata_routes_expose_version_and_config` 测试中：

```rust
            Some(13)  // was 12
```

在 `config_endpoint_lists_all_slices` 的 names 检查中添加：

```rust
        assert!(names.contains(&"central"));
```

- [ ] **Step 6: 运行全量测试**

```bash
cd /Users/jguan/projects/aima-service
cargo test -p aima-gateway 2>&1 | tail -30
```

Expected: 所有测试通过。

- [ ] **Step 7: Commit**

```bash
cd /Users/jguan/projects/aima-service
git add apps/gateway/src/routes/slices/central.rs apps/gateway/src/routes/slices/mod.rs apps/gateway/src/lib.rs
git commit -m "feat(gateway): add /central/* proxy to Central Knowledge Server

New route slice strips /central prefix and forwards to CENTRAL_UPSTREAM_URL.
Returns 404 when CENTRAL_UPSTREAM_URL is not configured.
Forwards: authorization, content-type, content-length, accept, user-agent, x-request-id.
Central is a standalone Go service (aima-central-knowledge repo).
API contract: aima-central-knowledge/api/openapi.yaml"
```

---

### Task 6: docker-compose 集成（aima-service repo）

**Files:**
- Modify: `/Users/jguan/projects/aima-service/docker-compose.prod.yml`

- [ ] **Step 1: 添加 central 服务定义**

在 `docker-compose.prod.yml` 的 `gateway:` 服务定义之前，添加 `central:` 服务：

```yaml
  # Central Knowledge Server — 独立 repo: aima-central-knowledge
  # 镜像本地构建: cd /root/aima-central-knowledge && docker build -t aima-central:latest .
  # 升级: git pull && docker build -t aima-central:latest . && docker compose up -d central
  # API 契约: aima-central-knowledge/api/openapi.yaml
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

- [ ] **Step 2: 添加 gateway 的 CENTRAL_UPSTREAM_URL 环境变量**

在 gateway 服务的 `environment:` 列表中（`AIMA_SITE_HOST_LOCALE_MAP` 之后）添加：

```yaml
      - CENTRAL_UPSTREAM_URL=${CENTRAL_UPSTREAM_URL:-}
```

- [ ] **Step 3: 添加 central 到 gateway 和 platform 的 NO_PROXY 列表**

在 gateway 服务的 `NO_PROXY` 和 `no_proxy` 行中，在末尾 `gateway` 后追加 `,central`：

```yaml
      - NO_PROXY=${NO_PROXY:-localhost,127.0.0.1,::1},platform,postgres,redis,temporal,temporal-ui,gateway,central
      - no_proxy=${no_proxy:-localhost,127.0.0.1,::1},platform,postgres,redis,temporal,temporal-ui,gateway,central
```

对 platform、temporal-worker 服务做同样的修改。

- [ ] **Step 4: Commit**

```bash
cd /Users/jguan/projects/aima-service
git add docker-compose.prod.yml
git commit -m "feat: add Central Knowledge Server to docker-compose

Independent container (aima-central:latest), built from aima-central-knowledge repo.
Uses separate PostgreSQL database (aima_central).
Gateway proxies /central/* via CENTRAL_UPSTREAM_URL env var.
No new host ports exposed — internal network only."
```

---

### Task 7: AIMA Edge 默认 endpoint（AIMA repo — 当前 worktree）

**Files:**
- Modify: `cmd/aima/tooldeps_integration.go`

- [ ] **Step 1: 添加 centralEndpoint helper 函数**

在 `tooldeps_integration.go` 文件末尾（最后一个函数之后）添加：

```go
const defaultCentralEndpoint = "https://aimaservice.ai/central"

// centralEndpoint returns the configured central endpoint, falling back to the
// production default (https://aimaservice.ai/central) when not explicitly set.
// Users can override via: system.config set central.endpoint <url>
func centralEndpoint(ctx context.Context, getConfig func(context.Context, string) (string, error)) string {
	ep, _ := getConfig(ctx, "central.endpoint")
	if ep == "" {
		return defaultCentralEndpoint
	}
	return ep
}
```

- [ ] **Step 2: 替换 SyncPush 中的 endpoint 读取**

将 `tooldeps_integration.go:103-106`：

```go
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured — use system.config set central.endpoint <url>")
		}
```

替换为：

```go
		endpoint := centralEndpoint(ctx, deps.GetConfig)
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
```

- [ ] **Step 3: 替换 SyncPull 中的 endpoint 读取**

将 `tooldeps_integration.go:173-176`：

```go
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured — use system.config set central.endpoint <url>")
		}
```

替换为：

```go
		endpoint := centralEndpoint(ctx, deps.GetConfig)
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
```

- [ ] **Step 4: 替换 SyncStatus 中的 endpoint 读取**

将 `tooldeps_integration.go:264`：

```go
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
```

替换为：

```go
		endpoint := centralEndpoint(ctx, deps.GetConfig)
```

- [ ] **Step 5: 替换 SyncPullAdvisories 中的 endpoint 读取**

将 `tooldeps_integration.go:288-291`：

```go
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured")
		}
```

替换为：

```go
		endpoint := centralEndpoint(ctx, deps.GetConfig)
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
```

- [ ] **Step 6: 替换 SyncPullScenarios 中的 endpoint 读取**

将 `tooldeps_integration.go:332-335`：

```go
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured")
		}
```

替换为：

```go
		endpoint := centralEndpoint(ctx, deps.GetConfig)
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
```

- [ ] **Step 7: 替换 AdvisoryFeedback 中的 endpoint 读取**

将 `tooldeps_integration.go:374-377`：

```go
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured")
		}
```

替换为：

```go
		endpoint := centralEndpoint(ctx, deps.GetConfig)
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
```

- [ ] **Step 8: 替换 RequestAdvise 中的 endpoint 读取**

将 `tooldeps_integration.go:417-420`：

```go
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured")
		}
```

替换为：

```go
		endpoint := centralEndpoint(ctx, deps.GetConfig)
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
```

- [ ] **Step 9: 替换 RequestScenario 中的 endpoint 读取**

将 `tooldeps_integration.go:461-464`：

```go
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured")
		}
```

替换为：

```go
		endpoint := centralEndpoint(ctx, deps.GetConfig)
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
```

- [ ] **Step 10: 替换 ListCentralScenarios 中的 endpoint 读取**

将 `tooldeps_integration.go:497-500`：

```go
		endpoint, _ := deps.GetConfig(ctx, "central.endpoint")
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
		if endpoint == "" {
			return nil, fmt.Errorf("central.endpoint not configured")
		}
```

替换为：

```go
		endpoint := centralEndpoint(ctx, deps.GetConfig)
		apiKey, _ := deps.GetConfig(ctx, "central.api_key")
```

- [ ] **Step 11: 验证编译**

```bash
go build ./cmd/aima/
```

Expected: 编译成功。

- [ ] **Step 12: 运行现有测试**

```bash
go test ./cmd/aima/ -run TestV040 -v 2>&1 | tail -20
```

Expected: 所有 v040 测试通过（它们显式 SetConfig central.endpoint，不受默认值影响）。

- [ ] **Step 13: Commit**

```bash
git add cmd/aima/tooldeps_integration.go
git commit -m "feat: default central endpoint to https://aimaservice.ai/central

All 9 sync functions now use centralEndpoint() helper that falls back
to the production URL when central.endpoint is not configured.
Users can still override via: system.config set central.endpoint <url>"
```

---

### Task 8: 更新 AIMA CLAUDE.md（AIMA repo — 当前 worktree）

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: 更新 Central 拆分段落**

在 CLAUDE.md 的 "====== Central Knowledge Server 已拆分 ======" 段中，在"开发警告"之前添加：

```markdown
### 生产部署

Central 已部署到 `aima-oversea` 服务器，作为 aima-service docker-compose 中的独立容器。

| 项目 | 说明 |
|------|------|
| **生产 URL** | `https://aimaservice.ai/central` |
| **默认 endpoint** | Edge 代码默认使用此 URL（`defaultCentralEndpoint` in `tooldeps_integration.go`） |
| **覆盖方式** | `system.config set central.endpoint <url>` |
| **Gateway 代理** | Rust gateway `/central/*` → strip 前缀 → `http://central:8081` |
| **数据库** | PostgreSQL `aima_central` database（与 platform 隔离） |
| **升级** | `cd aima-central-knowledge && git pull && docker build -t aima-central:latest . && docker compose up -d central` |
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: add Central production deployment info to CLAUDE.md"
```

---

### Task 9: 提交设计文档（AIMA repo — 当前 worktree）

**Files:**
- Already created: `docs/superpowers/specs/2026-04-13-central-deployment-integration-design.md`
- Already created: `docs/superpowers/plans/2026-04-13-central-deployment-integration.md`

- [ ] **Step 1: Commit 设计文档和实施计划**

```bash
git add docs/superpowers/specs/2026-04-13-central-deployment-integration-design.md
git add docs/superpowers/plans/2026-04-13-central-deployment-integration.md
git commit -m "docs: Central deployment integration design spec and implementation plan"
```
