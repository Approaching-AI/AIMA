# Central Server 升级实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 升级 Central Server，增加 CentralStore 抽象（SQLite/PostgreSQL 双实现）、Advisor Engine（LLM 推理推荐配置/优化/生成 Scenario）、Periodic Analyzer（定期全局分析），以及 6 个新 API 端点。

**Architecture:** 通过 `CentralStore` 接口解耦数据库实现，运行时通过 `CENTRAL_DB_DRIVER` 环境变量选择 SQLite 或 PostgreSQL。Advisor Engine 调用 LLM 生成结构化推荐。Periodic Analyzer 以后台定时循环运行全局分析。

**Tech Stack:** Go 1.22+, zero CGO, `modernc.org/sqlite`, `github.com/jackc/pgx/v5/stdlib`, `log/slog`, `net/http`, `encoding/json`

**Design Spec:** `docs/superpowers/specs/2026-04-07-v0.4-knowledge-automation-design.md` §4

---

## File Structure

```
internal/central/
  store.go                    # CentralStore 接口 + 共享类型
  store_test.go               # 接口一致性测试（SQLite + PG 共用）
  store_sqlite.go             # SQLite 实现（重构现有 server.go 数据库逻辑）
  store_sqlite_test.go        # SQLite 单元测试
  store_postgres.go           # PostgreSQL 实现 (pgx/v5)
  store_postgres_test.go      # PostgreSQL 单元测试（需 PG 可用，否则 skip）
  advisor.go                  # Advisor Engine：Recommend / OptimizeScenario / GenerateScenario
  advisor_test.go
  advisor_prompts.go          # LLM prompt 模板
  analyzer.go                 # Periodic Analyzer：gaps / patterns / scenario health
  analyzer_test.go
  server.go                   # MODIFY：重构为使用 CentralStore，新增路由
  server_test.go              # MODIFY：基于 CentralStore mock 测试

cmd/central/
  main.go                     # MODIFY：DB driver 选择 + Advisor/Analyzer 初始化
```

---

### Task 1: CentralStore 接口 + 共享类型

**Files:**
- Create: `internal/central/store.go`
- Create: `internal/central/store_test.go`

- [ ] **Step 1: 定义 CentralStore 接口和所有关联类型**

```go
// store.go
package central

import (
	"context"
	"encoding/json"
	"time"
)

// CentralStore abstracts the central server's database operations.
// Implementations: SQLiteCentralStore (dev/small) and PostgresCentralStore (production).
type CentralStore interface {
	// 数据写入
	IngestConfigurations(ctx context.Context, deviceID string, configs []IngestConfig) (*IngestResult, error)
	IngestBenchmarks(ctx context.Context, deviceID string, benchmarks []IngestBenchmark) (*IngestResult, error)
	IngestNotes(ctx context.Context, notes []IngestNote) (*IngestResult, error)
	UpsertDevice(ctx context.Context, device DeviceInfo) error

	// 数据查询
	QueryConfigs(ctx context.Context, params ConfigQuery) ([]ConfigRow, error)
	QueryBenchmarks(ctx context.Context, params BenchmarkQuery) ([]BenchmarkRow, error)
	QueryGaps(ctx context.Context, params GapsQuery) ([]GapEntry, error)

	// Advisory 管理
	InsertAdvisory(ctx context.Context, adv Advisory) error
	ListAdvisories(ctx context.Context, params AdvisoryQuery) ([]Advisory, error)
	UpdateAdvisoryStatus(ctx context.Context, id string, status string) error

	// Scenario 管理
	UpsertScenario(ctx context.Context, s Scenario) error
	ListScenarios(ctx context.Context, hardware string) ([]Scenario, error)

	// 分析任务
	InsertAnalysisRun(ctx context.Context, run AnalysisRun) error
	UpdateAnalysisRun(ctx context.Context, id string, updates AnalysisRunUpdate) error
	ListAnalysisRuns(ctx context.Context, limit int) ([]AnalysisRun, error)

	// 统计
	Stats(ctx context.Context) (*StoreStats, error)

	// 生命周期
	Close() error
}

// IngestResult reports how many records were ingested vs skipped.
type IngestResult struct {
	Ingested   int `json:"ingested"`
	Duplicates int `json:"duplicates"`
}

// DeviceInfo represents an edge device registering with central.
type DeviceInfo struct {
	ID              string `json:"id"`
	HardwareProfile string `json:"hardware_profile"`
	GPUArch         string `json:"gpu_arch"`
}

// ConfigQuery filters for configuration lookups.
type ConfigQuery struct {
	Hardware string `json:"hardware"`
	Engine   string `json:"engine"`
	Model    string `json:"model"`
	Status   string `json:"status"`
	Limit    int    `json:"limit"`
}

// BenchmarkQuery filters for benchmark lookups.
type BenchmarkQuery struct {
	ConfigID string `json:"config_id"`
	Hardware string `json:"hardware"`
	Model    string `json:"model"`
	Engine   string `json:"engine"`
	Limit    int    `json:"limit"`
}

// GapsQuery identifies missing HW×Engine×Model combinations.
type GapsQuery struct {
	Hardware string `json:"hardware"`
}

// GapEntry represents a missing configuration/benchmark combination.
type GapEntry struct {
	Hardware string `json:"hardware"`
	Model    string `json:"model"`
	Engine   string `json:"engine"`
	Reason   string `json:"reason"` // "no_config", "no_benchmark", "stale_benchmark"
}

// Advisory is a recommendation from Central to an edge device.
type Advisory struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"` // config_recommend / scenario_optimization / scenario_generation / gap_alert
	TargetHardware string          `json:"target_hardware"`
	TargetModel    string          `json:"target_model"`
	TargetEngine   string          `json:"target_engine"`
	ContentJSON    json.RawMessage `json:"content"`
	Reasoning      string          `json:"reasoning"`
	Confidence     string          `json:"confidence"` // low / medium / high
	BasedOnJSON    json.RawMessage `json:"based_on"`
	Status         string          `json:"status"` // pending / delivered / validated / rejected / expired
	CreatedAt      time.Time       `json:"created_at"`
	DeliveredAt    *time.Time      `json:"delivered_at,omitempty"`
	ValidatedAt    *time.Time      `json:"validated_at,omitempty"`
}

// AdvisoryQuery filters for advisory lookups.
type AdvisoryQuery struct {
	Hardware string `json:"hardware"`
	Status   string `json:"status"`
	Type     string `json:"type"`
	Limit    int    `json:"limit"`
}

// Scenario is a centrally-managed deployment scenario.
type Scenario struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	HardwareProfile string    `json:"hardware_profile"`
	ScenarioYAML    string    `json:"scenario_yaml"`
	Source          string    `json:"source"` // generated / optimized / manual
	AdvisoryID      string    `json:"advisory_id,omitempty"`
	Version         int       `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// AnalysisRun records a periodic analysis execution.
type AnalysisRun struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"` // gap_scan / pattern_discovery / scenario_health
	Status       string          `json:"status"`
	InputJSON    json.RawMessage `json:"input,omitempty"`
	OutputJSON   json.RawMessage `json:"output,omitempty"`
	Advisories   []string        `json:"advisories,omitempty"`
	StartedAt    time.Time       `json:"started_at"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
	Error        string          `json:"error,omitempty"`
}

// AnalysisRunUpdate carries partial updates for an analysis run.
type AnalysisRunUpdate struct {
	Status      string          `json:"status"`
	OutputJSON  json.RawMessage `json:"output,omitempty"`
	Advisories  []string        `json:"advisories,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// StoreStats returns aggregate counts for the central dashboard.
type StoreStats struct {
	Devices        int `json:"devices"`
	Configurations int `json:"configurations"`
	Benchmarks     int `json:"benchmarks"`
	Notes          int `json:"notes"`
	Advisories     int `json:"advisories"`
	Scenarios      int `json:"scenarios"`
}

// ConfigRow is a configuration record returned by queries.
type ConfigRow struct {
	ID            string          `json:"id"`
	DeviceID      string          `json:"device_id"`
	Hardware      string          `json:"hardware"`
	EngineType    string          `json:"engine_type"`
	EngineVersion string          `json:"engine_version"`
	Model         string          `json:"model"`
	Slot          string          `json:"slot"`
	Config        json.RawMessage `json:"config"`
	ConfigHash    string          `json:"config_hash"`
	Status        string          `json:"status"`
	CreatedAt     time.Time       `json:"created_at"`
}

// BenchmarkRow is a benchmark result returned by queries.
type BenchmarkRow struct {
	ID            string  `json:"id"`
	ConfigID      string  `json:"config_id"`
	DeviceID      string  `json:"device_id"`
	Hardware      string  `json:"hardware"`
	Model         string  `json:"model"`
	Engine        string  `json:"engine"`
	Concurrency   int     `json:"concurrency"`
	ThroughputTPS float64 `json:"throughput_tps"`
	TTFTP50ms     float64 `json:"ttft_p50_ms"`
	TTFTP95ms     float64 `json:"ttft_p95_ms"`
	VRAMUsageMiB  int     `json:"vram_usage_mib"`
	TestedAt      string  `json:"tested_at"`
}
```

- [ ] **Step 2: 写接口一致性测试骨架**

```go
// store_test.go
package central

import (
	"context"
	"testing"
)

// storeTestSuite runs the same tests against any CentralStore implementation.
func storeTestSuite(t *testing.T, newStore func(t *testing.T) CentralStore) {
	t.Run("UpsertDevice", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()
		err := s.UpsertDevice(context.Background(), DeviceInfo{
			ID: "dev-1", HardwareProfile: "nvidia-rtx4090-x86", GPUArch: "Ada",
		})
		if err != nil {
			t.Fatalf("UpsertDevice: %v", err)
		}
		// Upsert again — should not error
		err = s.UpsertDevice(context.Background(), DeviceInfo{
			ID: "dev-1", HardwareProfile: "nvidia-rtx4090-x86", GPUArch: "Ada",
		})
		if err != nil {
			t.Fatalf("UpsertDevice (again): %v", err)
		}
	})

	t.Run("IngestAndQueryConfigs", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()
		_ = s.UpsertDevice(context.Background(), DeviceInfo{ID: "dev-1", GPUArch: "Ada"})

		res, err := s.IngestConfigurations(context.Background(), "dev-1", []IngestConfig{{
			ID: "cfg-1", Hardware: "nvidia-rtx4090-x86", EngineType: "vllm",
			Model: "qwen3-8b", Config: []byte(`{"gmu":0.8}`), ConfigHash: "abc123",
			Status: "golden",
		}})
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if res.Ingested != 1 {
			t.Errorf("ingested = %d, want 1", res.Ingested)
		}

		// Duplicate should be skipped
		res2, _ := s.IngestConfigurations(context.Background(), "dev-1", []IngestConfig{{
			ID: "cfg-1-dup", Hardware: "nvidia-rtx4090-x86", EngineType: "vllm",
			Model: "qwen3-8b", Config: []byte(`{"gmu":0.8}`), ConfigHash: "abc123",
			Status: "golden",
		}})
		if res2.Duplicates != 1 {
			t.Errorf("duplicates = %d, want 1", res2.Duplicates)
		}

		// Query back
		rows, err := s.QueryConfigs(context.Background(), ConfigQuery{Model: "qwen3-8b", Limit: 10})
		if err != nil {
			t.Fatalf("QueryConfigs: %v", err)
		}
		if len(rows) != 1 {
			t.Errorf("rows = %d, want 1", len(rows))
		}
	})

	t.Run("AdvisoryLifecycle", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()
		adv := Advisory{
			ID: "adv-1", Type: "config_recommend",
			TargetHardware: "nvidia-rtx4090-x86", TargetModel: "qwen3-8b",
			ContentJSON: []byte(`{"engine":"vllm","gmu":0.78}`),
			Reasoning: "test", Confidence: "medium", Status: "pending",
		}
		if err := s.InsertAdvisory(context.Background(), adv); err != nil {
			t.Fatalf("InsertAdvisory: %v", err)
		}

		list, _ := s.ListAdvisories(context.Background(), AdvisoryQuery{Status: "pending", Limit: 10})
		if len(list) != 1 {
			t.Fatalf("list = %d, want 1", len(list))
		}
		if list[0].ID != "adv-1" {
			t.Errorf("id = %q, want adv-1", list[0].ID)
		}

		_ = s.UpdateAdvisoryStatus(context.Background(), "adv-1", "delivered")
		list2, _ := s.ListAdvisories(context.Background(), AdvisoryQuery{Status: "pending", Limit: 10})
		if len(list2) != 0 {
			t.Errorf("pending after deliver = %d, want 0", len(list2))
		}
	})

	t.Run("ScenarioUpsert", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()
		sc := Scenario{
			ID: "sc-1", Name: "test-scenario", HardwareProfile: "nvidia-rtx4090-x86",
			ScenarioYAML: "models:\n  - qwen3-8b", Source: "generated", Version: 1,
		}
		if err := s.UpsertScenario(context.Background(), sc); err != nil {
			t.Fatalf("UpsertScenario: %v", err)
		}
		list, _ := s.ListScenarios(context.Background(), "nvidia-rtx4090-x86")
		if len(list) != 1 {
			t.Errorf("scenarios = %d, want 1", len(list))
		}
	})

	t.Run("Stats", func(t *testing.T) {
		s := newStore(t)
		defer s.Close()
		stats, err := s.Stats(context.Background())
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		if stats == nil {
			t.Fatal("stats is nil")
		}
	})
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/central/ -run TestStore -v`
Expected: FAIL — no implementations exist yet

- [ ] **Step 4: Commit**

```bash
git add internal/central/store.go internal/central/store_test.go
git commit -m "feat(central): define CentralStore interface and shared types"
```

---

### Task 2: SQLite CentralStore 实现

**Files:**
- Create: `internal/central/store_sqlite.go`
- Create: `internal/central/store_sqlite_test.go`

- [ ] **Step 1: 实现 SQLiteCentralStore**

```go
// store_sqlite.go
package central

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteCentralStore implements CentralStore using SQLite.
type SQLiteCentralStore struct {
	db *sql.DB
}

// NewSQLiteCentralStore opens a SQLite database and runs migrations.
func NewSQLiteCentralStore(dbPath string) (*SQLiteCentralStore, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &SQLiteCentralStore{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteCentralStore) migrate(ctx context.Context) error {
	ddl := `
CREATE TABLE IF NOT EXISTS devices (
    id TEXT PRIMARY KEY,
    hardware_profile TEXT,
    gpu_arch TEXT,
    last_seen DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS configurations (
    id TEXT PRIMARY KEY,
    device_id TEXT REFERENCES devices(id),
    hardware TEXT NOT NULL,
    engine_type TEXT NOT NULL,
    engine_version TEXT,
    model TEXT NOT NULL,
    slot TEXT,
    config TEXT NOT NULL,
    config_hash TEXT NOT NULL,
    status TEXT DEFAULT 'experiment',
    derived_from TEXT,
    tags TEXT,
    source TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    ingested_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_config_hash ON configurations(config_hash);
CREATE INDEX IF NOT EXISTS idx_config_hw ON configurations(hardware, engine_type, model);
CREATE TABLE IF NOT EXISTS benchmark_results (
    id TEXT PRIMARY KEY,
    config_id TEXT NOT NULL REFERENCES configurations(id),
    device_id TEXT REFERENCES devices(id),
    concurrency INTEGER,
    input_len_bucket TEXT,
    output_len_bucket TEXT,
    modality TEXT,
    throughput_tps REAL,
    ttft_p50_ms REAL,
    ttft_p95_ms REAL,
    ttft_p99_ms REAL,
    tpot_p50_ms REAL,
    tpot_p95_ms REAL,
    qps REAL,
    vram_usage_mib INTEGER,
    ram_usage_mib INTEGER,
    power_draw_watts REAL,
    gpu_utilization_pct REAL,
    error_rate REAL,
    oom_occurred BOOLEAN,
    stability TEXT,
    duration_s INTEGER,
    sample_count INTEGER,
    agent_model TEXT,
    notes TEXT,
    tested_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    ingested_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_bench_config ON benchmark_results(config_id);
CREATE TABLE IF NOT EXISTS knowledge_notes (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    tags TEXT,
    hardware_profile TEXT,
    model TEXT,
    engine TEXT,
    content TEXT NOT NULL,
    confidence TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    ingested_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS advisories (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    target_hardware TEXT,
    target_model TEXT,
    target_engine TEXT,
    content_json TEXT NOT NULL,
    reasoning TEXT,
    confidence TEXT DEFAULT 'medium',
    based_on_json TEXT,
    status TEXT DEFAULT 'pending',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    delivered_at DATETIME,
    validated_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_adv_status ON advisories(status);
CREATE INDEX IF NOT EXISTS idx_adv_hw ON advisories(target_hardware);
CREATE TABLE IF NOT EXISTS analysis_runs (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    status TEXT DEFAULT 'running',
    input_json TEXT,
    output_json TEXT,
    advisories TEXT,
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    error TEXT
);
CREATE TABLE IF NOT EXISTS scenarios (
    id TEXT PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    hardware_profile TEXT NOT NULL,
    scenario_yaml TEXT NOT NULL,
    source TEXT NOT NULL,
    advisory_id TEXT,
    version INTEGER DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_sc_hw ON scenarios(hardware_profile);`
	_, err := s.db.ExecContext(ctx, ddl)
	return err
}

func (s *SQLiteCentralStore) Close() error { return s.db.Close() }

func (s *SQLiteCentralStore) UpsertDevice(ctx context.Context, d DeviceInfo) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO devices (id, hardware_profile, gpu_arch, last_seen) VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(id) DO UPDATE SET last_seen = datetime('now'),
		 hardware_profile = COALESCE(excluded.hardware_profile, devices.hardware_profile),
		 gpu_arch = COALESCE(excluded.gpu_arch, devices.gpu_arch)`,
		d.ID, d.HardwareProfile, d.GPUArch)
	return err
}

func (s *SQLiteCentralStore) IngestConfigurations(ctx context.Context, deviceID string, configs []IngestConfig) (*IngestResult, error) {
	res := &IngestResult{}
	for _, c := range configs {
		configHash := c.ConfigHash
		if configHash == "" {
			h := sha256.Sum256(c.Config)
			configHash = fmt.Sprintf("%x", h)[:16]
		}
		var existing int
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM configurations WHERE config_hash = ?`, configHash).Scan(&existing)
		if existing > 0 {
			res.Duplicates++
			continue
		}
		derivedFrom := sql.NullString{}
		if c.DerivedFrom != "" {
			derivedFrom = sql.NullString{String: c.DerivedFrom, Valid: true}
		}
		did := c.DeviceID
		if did == "" {
			did = deviceID
		}
		tagsJSON, _ := json.Marshal(c.Tags)
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO configurations (id, device_id, hardware, engine_type, engine_version, model, slot, config, config_hash, status, derived_from, tags, source, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, did, c.Hardware, c.EngineType, c.EngineVersion, c.Model, c.Slot,
			string(c.Config), configHash, c.Status, derivedFrom, string(tagsJSON), c.Source,
			coalesceTime(c.CreatedAt), coalesceTime(c.UpdatedAt))
		if err != nil {
			continue
		}
		res.Ingested++
	}
	return res, nil
}

func (s *SQLiteCentralStore) IngestBenchmarks(ctx context.Context, deviceID string, benchmarks []IngestBenchmark) (*IngestResult, error) {
	res := &IngestResult{}
	for _, b := range benchmarks {
		_, err := s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO benchmark_results (id, config_id, device_id, concurrency, input_len_bucket, output_len_bucket, modality,
			 throughput_tps, ttft_p50_ms, ttft_p95_ms, ttft_p99_ms, tpot_p50_ms, tpot_p95_ms, qps, vram_usage_mib, ram_usage_mib,
			 power_draw_watts, gpu_utilization_pct, error_rate, oom_occurred, stability, duration_s, sample_count, tested_at, agent_model, notes)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			b.ID, b.ConfigID, deviceID, b.Concurrency, b.InputLenBucket, b.OutputLenBucket, b.Modality,
			b.ThroughputTPS, b.TTFTP50ms, b.TTFTP95ms, b.TTFTP99ms, b.TPOTP50ms, b.TPOTP95ms, b.QPS,
			b.VRAMUsageMiB, b.RAMUsageMiB, b.PowerDrawWatts, b.GPUUtilPct, b.ErrorRate, b.OOMOccurred,
			b.Stability, b.DurationS, b.SampleCount, coalesceTime(b.TestedAt), b.AgentModel, b.Notes)
		if err != nil {
			res.Duplicates++
			continue
		}
		res.Ingested++
	}
	return res, nil
}

func (s *SQLiteCentralStore) IngestNotes(ctx context.Context, notes []IngestNote) (*IngestResult, error) {
	res := &IngestResult{}
	for _, n := range notes {
		tagsJSON, _ := json.Marshal(n.Tags)
		_, err := s.db.ExecContext(ctx,
			`INSERT OR REPLACE INTO knowledge_notes (id, title, tags, hardware_profile, model, engine, content, confidence, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			n.ID, n.Title, string(tagsJSON), n.HardwareProfile, n.Model, n.Engine, n.Content, n.Confidence, coalesceTime(n.CreatedAt))
		if err != nil {
			continue
		}
		res.Ingested++
	}
	return res, nil
}

func (s *SQLiteCentralStore) QueryConfigs(ctx context.Context, p ConfigQuery) ([]ConfigRow, error) {
	q := `SELECT id, COALESCE(device_id,''), hardware, engine_type, COALESCE(engine_version,''), model, COALESCE(slot,''), config, config_hash, status, created_at
		  FROM configurations WHERE 1=1`
	var args []any
	if p.Hardware != "" {
		q += ` AND hardware = ?`
		args = append(args, p.Hardware)
	}
	if p.Engine != "" {
		q += ` AND engine_type = ?`
		args = append(args, p.Engine)
	}
	if p.Model != "" {
		q += ` AND model = ?`
		args = append(args, p.Model)
	}
	if p.Status != "" {
		q += ` AND status = ?`
		args = append(args, p.Status)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ConfigRow
	for rows.Next() {
		var r ConfigRow
		if err := rows.Scan(&r.ID, &r.DeviceID, &r.Hardware, &r.EngineType, &r.EngineVersion,
			&r.Model, &r.Slot, &r.Config, &r.ConfigHash, &r.Status, &r.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *SQLiteCentralStore) QueryBenchmarks(ctx context.Context, p BenchmarkQuery) ([]BenchmarkRow, error) {
	q := `SELECT br.id, br.config_id, COALESCE(br.device_id,''), c.hardware, c.model, c.engine_type,
		  br.concurrency, br.throughput_tps, COALESCE(br.ttft_p50_ms,0), COALESCE(br.ttft_p95_ms,0),
		  COALESCE(br.vram_usage_mib,0), br.tested_at
		  FROM benchmark_results br JOIN configurations c ON br.config_id = c.id WHERE 1=1`
	var args []any
	if p.ConfigID != "" {
		q += ` AND br.config_id = ?`
		args = append(args, p.ConfigID)
	}
	if p.Hardware != "" {
		q += ` AND c.hardware = ?`
		args = append(args, p.Hardware)
	}
	if p.Model != "" {
		q += ` AND c.model = ?`
		args = append(args, p.Model)
	}
	if p.Engine != "" {
		q += ` AND c.engine_type = ?`
		args = append(args, p.Engine)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}
	q += ` ORDER BY br.tested_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []BenchmarkRow
	for rows.Next() {
		var r BenchmarkRow
		if err := rows.Scan(&r.ID, &r.ConfigID, &r.DeviceID, &r.Hardware, &r.Model, &r.Engine,
			&r.Concurrency, &r.ThroughputTPS, &r.TTFTP50ms, &r.TTFTP95ms, &r.VRAMUsageMiB, &r.TestedAt); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *SQLiteCentralStore) QueryGaps(ctx context.Context, p GapsQuery) ([]GapEntry, error) {
	// Find HW×Model combos in configs that have no benchmarks
	q := `SELECT DISTINCT c.hardware, c.model, c.engine_type
		  FROM configurations c
		  LEFT JOIN benchmark_results br ON c.id = br.config_id
		  WHERE br.id IS NULL`
	var args []any
	if p.Hardware != "" {
		q += ` AND c.hardware = ?`
		args = append(args, p.Hardware)
	}
	q += ` LIMIT 200`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []GapEntry
	for rows.Next() {
		var g GapEntry
		if err := rows.Scan(&g.Hardware, &g.Model, &g.Engine); err != nil {
			return nil, err
		}
		g.Reason = "no_benchmark"
		result = append(result, g)
	}
	return result, rows.Err()
}

func (s *SQLiteCentralStore) InsertAdvisory(ctx context.Context, adv Advisory) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO advisories (id, type, target_hardware, target_model, target_engine, content_json, reasoning, confidence, based_on_json, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		adv.ID, adv.Type, adv.TargetHardware, adv.TargetModel, adv.TargetEngine,
		string(adv.ContentJSON), adv.Reasoning, adv.Confidence, string(adv.BasedOnJSON),
		adv.Status, adv.CreatedAt)
	return err
}

func (s *SQLiteCentralStore) ListAdvisories(ctx context.Context, p AdvisoryQuery) ([]Advisory, error) {
	q := `SELECT id, type, COALESCE(target_hardware,''), COALESCE(target_model,''), COALESCE(target_engine,''),
		  content_json, COALESCE(reasoning,''), confidence, COALESCE(based_on_json,'[]'), status, created_at, delivered_at, validated_at
		  FROM advisories WHERE 1=1`
	var args []any
	if p.Hardware != "" {
		q += ` AND target_hardware = ?`
		args = append(args, p.Hardware)
	}
	if p.Status != "" {
		q += ` AND status = ?`
		args = append(args, p.Status)
	}
	if p.Type != "" {
		q += ` AND type = ?`
		args = append(args, p.Type)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Advisory
	for rows.Next() {
		var a Advisory
		var deliveredAt, validatedAt sql.NullTime
		var contentJSON, basedOnJSON string
		if err := rows.Scan(&a.ID, &a.Type, &a.TargetHardware, &a.TargetModel, &a.TargetEngine,
			&contentJSON, &a.Reasoning, &a.Confidence, &basedOnJSON, &a.Status,
			&a.CreatedAt, &deliveredAt, &validatedAt); err != nil {
			return nil, err
		}
		a.ContentJSON = json.RawMessage(contentJSON)
		a.BasedOnJSON = json.RawMessage(basedOnJSON)
		if deliveredAt.Valid {
			a.DeliveredAt = &deliveredAt.Time
		}
		if validatedAt.Valid {
			a.ValidatedAt = &validatedAt.Time
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func (s *SQLiteCentralStore) UpdateAdvisoryStatus(ctx context.Context, id string, status string) error {
	q := `UPDATE advisories SET status = ?`
	switch status {
	case "delivered":
		q += `, delivered_at = datetime('now')`
	case "validated", "rejected":
		q += `, validated_at = datetime('now')`
	}
	q += ` WHERE id = ?`
	_, err := s.db.ExecContext(ctx, q, status, id)
	return err
}

func (s *SQLiteCentralStore) UpsertScenario(ctx context.Context, sc Scenario) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO scenarios (id, name, hardware_profile, scenario_yaml, source, advisory_id, version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET scenario_yaml = excluded.scenario_yaml, source = excluded.source,
		 advisory_id = excluded.advisory_id, version = scenarios.version + 1, updated_at = datetime('now')`,
		sc.ID, sc.Name, sc.HardwareProfile, sc.ScenarioYAML, sc.Source, sc.AdvisoryID, sc.Version,
		sc.CreatedAt, sc.UpdatedAt)
	return err
}

func (s *SQLiteCentralStore) ListScenarios(ctx context.Context, hardware string) ([]Scenario, error) {
	q := `SELECT id, name, hardware_profile, scenario_yaml, source, COALESCE(advisory_id,''), version, created_at, COALESCE(updated_at, created_at)
		  FROM scenarios WHERE 1=1`
	var args []any
	if hardware != "" {
		q += ` AND hardware_profile = ?`
		args = append(args, hardware)
	}
	q += ` ORDER BY updated_at DESC LIMIT 50`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Scenario
	for rows.Next() {
		var sc Scenario
		if err := rows.Scan(&sc.ID, &sc.Name, &sc.HardwareProfile, &sc.ScenarioYAML, &sc.Source,
			&sc.AdvisoryID, &sc.Version, &sc.CreatedAt, &sc.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, sc)
	}
	return result, rows.Err()
}

func (s *SQLiteCentralStore) InsertAnalysisRun(ctx context.Context, run AnalysisRun) error {
	advJSON, _ := json.Marshal(run.Advisories)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO analysis_runs (id, type, status, input_json, started_at, advisories) VALUES (?, ?, ?, ?, ?, ?)`,
		run.ID, run.Type, run.Status, string(run.InputJSON), run.StartedAt, string(advJSON))
	return err
}

func (s *SQLiteCentralStore) UpdateAnalysisRun(ctx context.Context, id string, u AnalysisRunUpdate) error {
	advJSON, _ := json.Marshal(u.Advisories)
	_, err := s.db.ExecContext(ctx,
		`UPDATE analysis_runs SET status=?, output_json=?, advisories=?, completed_at=?, error=? WHERE id=?`,
		u.Status, string(u.OutputJSON), string(advJSON), u.CompletedAt, u.Error, id)
	return err
}

func (s *SQLiteCentralStore) ListAnalysisRuns(ctx context.Context, limit int) ([]AnalysisRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, status, COALESCE(input_json,''), COALESCE(output_json,''), COALESCE(advisories,'[]'), started_at, completed_at, COALESCE(error,'')
		 FROM analysis_runs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AnalysisRun
	for rows.Next() {
		var r AnalysisRun
		var inputJSON, outputJSON, advJSON string
		var completedAt sql.NullTime
		if err := rows.Scan(&r.ID, &r.Type, &r.Status, &inputJSON, &outputJSON, &advJSON, &r.StartedAt, &completedAt, &r.Error); err != nil {
			return nil, err
		}
		r.InputJSON = json.RawMessage(inputJSON)
		r.OutputJSON = json.RawMessage(outputJSON)
		_ = json.Unmarshal([]byte(advJSON), &r.Advisories)
		if completedAt.Valid {
			r.CompletedAt = &completedAt.Time
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *SQLiteCentralStore) Stats(ctx context.Context) (*StoreStats, error) {
	st := &StoreStats{}
	queries := []struct {
		q   string
		dst *int
	}{
		{"SELECT COUNT(*) FROM devices", &st.Devices},
		{"SELECT COUNT(*) FROM configurations", &st.Configurations},
		{"SELECT COUNT(*) FROM benchmark_results", &st.Benchmarks},
		{"SELECT COUNT(*) FROM knowledge_notes", &st.Notes},
		{"SELECT COUNT(*) FROM advisories", &st.Advisories},
		{"SELECT COUNT(*) FROM scenarios", &st.Scenarios},
	}
	for _, qr := range queries {
		_ = s.db.QueryRowContext(ctx, qr.q).Scan(qr.dst)
	}
	return st, nil
}

func coalesceTime(t string) string {
	if t == "" {
		return time.Now().UTC().Format(time.RFC3339)
	}
	return t
}

// Ensure SQLiteCentralStore implements CentralStore at compile time.
var _ CentralStore = (*SQLiteCentralStore)(nil)
```

- [ ] **Step 2: 写 SQLite 测试**

```go
// store_sqlite_test.go
package central

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteCentralStore(t *testing.T) {
	storeTestSuite(t, func(t *testing.T) CentralStore {
		t.Helper()
		dir := t.TempDir()
		s, err := NewSQLiteCentralStore(filepath.Join(dir, "test.db"))
		if err != nil {
			t.Fatalf("NewSQLiteCentralStore: %v", err)
		}
		return s
	})
}

func TestSQLiteCentralStore_FileCreated(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "central.db")
	s, err := NewSQLiteCentralStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteCentralStore: %v", err)
	}
	defer s.Close()
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("expected db file to exist")
	}
}
```

- [ ] **Step 3: Run tests**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/central/ -run TestSQLite -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/central/store_sqlite.go internal/central/store_sqlite_test.go
git commit -m "feat(central): implement SQLiteCentralStore with full interface coverage"
```

---

### Task 3: PostgreSQL CentralStore 实现

**Files:**
- Create: `internal/central/store_postgres.go`
- Create: `internal/central/store_postgres_test.go`

- [ ] **Step 1: 添加 pgx 依赖**

```bash
cd /Users/jguan/projects/AIMA && go get github.com/jackc/pgx/v5/stdlib
```

- [ ] **Step 2: 实现 PostgresCentralStore**

```go
// store_postgres.go
package central

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresCentralStore implements CentralStore using PostgreSQL.
type PostgresCentralStore struct {
	db *sql.DB
}

// NewPostgresCentralStore connects to PostgreSQL and runs migrations.
func NewPostgresCentralStore(dsn string) (*PostgresCentralStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	s := &PostgresCentralStore{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *PostgresCentralStore) migrate(ctx context.Context) error {
	ddl := `
CREATE TABLE IF NOT EXISTS devices (
    id TEXT PRIMARY KEY,
    hardware_profile TEXT,
    gpu_arch TEXT,
    last_seen TIMESTAMPTZ DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS configurations (
    id TEXT PRIMARY KEY,
    device_id TEXT REFERENCES devices(id),
    hardware TEXT NOT NULL,
    engine_type TEXT NOT NULL,
    engine_version TEXT,
    model TEXT NOT NULL,
    slot TEXT,
    config JSONB NOT NULL,
    config_hash TEXT NOT NULL,
    status TEXT DEFAULT 'experiment',
    derived_from TEXT,
    tags JSONB,
    source TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    ingested_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_pg_config_hash ON configurations(config_hash);
CREATE INDEX IF NOT EXISTS idx_pg_config_hw ON configurations(hardware, engine_type, model);
CREATE TABLE IF NOT EXISTS benchmark_results (
    id TEXT PRIMARY KEY,
    config_id TEXT NOT NULL REFERENCES configurations(id),
    device_id TEXT REFERENCES devices(id),
    concurrency INTEGER,
    input_len_bucket TEXT,
    output_len_bucket TEXT,
    modality TEXT,
    throughput_tps DOUBLE PRECISION,
    ttft_p50_ms DOUBLE PRECISION,
    ttft_p95_ms DOUBLE PRECISION,
    ttft_p99_ms DOUBLE PRECISION,
    tpot_p50_ms DOUBLE PRECISION,
    tpot_p95_ms DOUBLE PRECISION,
    qps DOUBLE PRECISION,
    vram_usage_mib INTEGER,
    ram_usage_mib INTEGER,
    power_draw_watts DOUBLE PRECISION,
    gpu_utilization_pct DOUBLE PRECISION,
    error_rate DOUBLE PRECISION,
    oom_occurred BOOLEAN,
    stability TEXT,
    duration_s INTEGER,
    sample_count INTEGER,
    agent_model TEXT,
    notes TEXT,
    tested_at TIMESTAMPTZ DEFAULT NOW(),
    ingested_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_pg_bench_config ON benchmark_results(config_id);
CREATE TABLE IF NOT EXISTS knowledge_notes (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    tags JSONB,
    hardware_profile TEXT,
    model TEXT,
    engine TEXT,
    content TEXT NOT NULL,
    confidence TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    ingested_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS advisories (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    target_hardware TEXT,
    target_model TEXT,
    target_engine TEXT,
    content_json JSONB NOT NULL,
    reasoning TEXT,
    confidence TEXT DEFAULT 'medium',
    based_on_json JSONB,
    status TEXT DEFAULT 'pending',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    delivered_at TIMESTAMPTZ,
    validated_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_pg_adv_status ON advisories(status);
CREATE INDEX IF NOT EXISTS idx_pg_adv_hw ON advisories(target_hardware);
CREATE TABLE IF NOT EXISTS analysis_runs (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    status TEXT DEFAULT 'running',
    input_json JSONB,
    output_json JSONB,
    advisories JSONB,
    started_at TIMESTAMPTZ DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    error TEXT
);
CREATE TABLE IF NOT EXISTS scenarios (
    id TEXT PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    hardware_profile TEXT NOT NULL,
    scenario_yaml TEXT NOT NULL,
    source TEXT NOT NULL,
    advisory_id TEXT,
    version INTEGER DEFAULT 1,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_pg_sc_hw ON scenarios(hardware_profile);`
	_, err := s.db.ExecContext(ctx, ddl)
	return err
}

func (s *PostgresCentralStore) Close() error { return s.db.Close() }

// UpsertDevice, IngestConfigurations, IngestBenchmarks, IngestNotes, QueryConfigs,
// QueryBenchmarks, QueryGaps, InsertAdvisory, ListAdvisories, UpdateAdvisoryStatus,
// UpsertScenario, ListScenarios, InsertAnalysisRun, UpdateAnalysisRun, ListAnalysisRuns, Stats
// — all follow the same logic as SQLiteCentralStore but use PostgreSQL syntax:
//   - datetime('now') → NOW()
//   - INSERT OR IGNORE → INSERT ... ON CONFLICT DO NOTHING
//   - INSERT OR REPLACE → INSERT ... ON CONFLICT DO UPDATE
//   - config column uses JSONB type (transparent — Go sees string/[]byte)
//   - COALESCE patterns identical
//
// Implementation follows the exact same structure as store_sqlite.go.
// The full implementation is provided below for each method.

func (s *PostgresCentralStore) UpsertDevice(ctx context.Context, d DeviceInfo) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO devices (id, hardware_profile, gpu_arch, last_seen) VALUES ($1, $2, $3, NOW())
		 ON CONFLICT(id) DO UPDATE SET last_seen = NOW(),
		 hardware_profile = COALESCE(EXCLUDED.hardware_profile, devices.hardware_profile),
		 gpu_arch = COALESCE(EXCLUDED.gpu_arch, devices.gpu_arch)`,
		d.ID, d.HardwareProfile, d.GPUArch)
	return err
}

func (s *PostgresCentralStore) IngestConfigurations(ctx context.Context, deviceID string, configs []IngestConfig) (*IngestResult, error) {
	res := &IngestResult{}
	for _, c := range configs {
		configHash := c.ConfigHash
		if configHash == "" {
			h := sha256.Sum256(c.Config)
			configHash = fmt.Sprintf("%x", h)[:16]
		}
		var existing int
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM configurations WHERE config_hash = $1`, configHash).Scan(&existing)
		if existing > 0 {
			res.Duplicates++
			continue
		}
		did := c.DeviceID
		if did == "" {
			did = deviceID
		}
		tagsJSON, _ := json.Marshal(c.Tags)
		derivedFrom := sql.NullString{}
		if c.DerivedFrom != "" {
			derivedFrom = sql.NullString{String: c.DerivedFrom, Valid: true}
		}
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO configurations (id, device_id, hardware, engine_type, engine_version, model, slot, config, config_hash, status, derived_from, tags, source, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			c.ID, did, c.Hardware, c.EngineType, c.EngineVersion, c.Model, c.Slot,
			string(c.Config), configHash, c.Status, derivedFrom, string(tagsJSON), c.Source,
			coalesceTime(c.CreatedAt), coalesceTime(c.UpdatedAt))
		if err != nil {
			continue
		}
		res.Ingested++
	}
	return res, nil
}

func (s *PostgresCentralStore) IngestBenchmarks(ctx context.Context, deviceID string, benchmarks []IngestBenchmark) (*IngestResult, error) {
	res := &IngestResult{}
	for _, b := range benchmarks {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO benchmark_results (id, config_id, device_id, concurrency, input_len_bucket, output_len_bucket, modality,
			 throughput_tps, ttft_p50_ms, ttft_p95_ms, ttft_p99_ms, tpot_p50_ms, tpot_p95_ms, qps, vram_usage_mib, ram_usage_mib,
			 power_draw_watts, gpu_utilization_pct, error_rate, oom_occurred, stability, duration_s, sample_count, tested_at, agent_model, notes)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)
			 ON CONFLICT(id) DO NOTHING`,
			b.ID, b.ConfigID, deviceID, b.Concurrency, b.InputLenBucket, b.OutputLenBucket, b.Modality,
			b.ThroughputTPS, b.TTFTP50ms, b.TTFTP95ms, b.TTFTP99ms, b.TPOTP50ms, b.TPOTP95ms, b.QPS,
			b.VRAMUsageMiB, b.RAMUsageMiB, b.PowerDrawWatts, b.GPUUtilPct, b.ErrorRate, b.OOMOccurred,
			b.Stability, b.DurationS, b.SampleCount, coalesceTime(b.TestedAt), b.AgentModel, b.Notes)
		if err != nil {
			res.Duplicates++
			continue
		}
		res.Ingested++
	}
	return res, nil
}

func (s *PostgresCentralStore) IngestNotes(ctx context.Context, notes []IngestNote) (*IngestResult, error) {
	res := &IngestResult{}
	for _, n := range notes {
		tagsJSON, _ := json.Marshal(n.Tags)
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO knowledge_notes (id, title, tags, hardware_profile, model, engine, content, confidence, created_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			 ON CONFLICT(id) DO UPDATE SET title=EXCLUDED.title, tags=EXCLUDED.tags, content=EXCLUDED.content`,
			n.ID, n.Title, string(tagsJSON), n.HardwareProfile, n.Model, n.Engine, n.Content, n.Confidence, coalesceTime(n.CreatedAt))
		if err != nil {
			continue
		}
		res.Ingested++
	}
	return res, nil
}

func (s *PostgresCentralStore) QueryConfigs(ctx context.Context, p ConfigQuery) ([]ConfigRow, error) {
	q := `SELECT id, COALESCE(device_id,''), hardware, engine_type, COALESCE(engine_version,''), model, COALESCE(slot,''), config::text, config_hash, status, created_at
		  FROM configurations WHERE true`
	var args []any
	n := 0
	if p.Hardware != "" {
		n++
		q += fmt.Sprintf(` AND hardware = $%d`, n)
		args = append(args, p.Hardware)
	}
	if p.Engine != "" {
		n++
		q += fmt.Sprintf(` AND engine_type = $%d`, n)
		args = append(args, p.Engine)
	}
	if p.Model != "" {
		n++
		q += fmt.Sprintf(` AND model = $%d`, n)
		args = append(args, p.Model)
	}
	if p.Status != "" {
		n++
		q += fmt.Sprintf(` AND status = $%d`, n)
		args = append(args, p.Status)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}
	n++
	q += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d`, n)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ConfigRow
	for rows.Next() {
		var r ConfigRow
		if err := rows.Scan(&r.ID, &r.DeviceID, &r.Hardware, &r.EngineType, &r.EngineVersion,
			&r.Model, &r.Slot, &r.Config, &r.ConfigHash, &r.Status, &r.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *PostgresCentralStore) QueryBenchmarks(ctx context.Context, p BenchmarkQuery) ([]BenchmarkRow, error) {
	q := `SELECT br.id, br.config_id, COALESCE(br.device_id,''), c.hardware, c.model, c.engine_type,
		  br.concurrency, br.throughput_tps, COALESCE(br.ttft_p50_ms,0), COALESCE(br.ttft_p95_ms,0),
		  COALESCE(br.vram_usage_mib,0), br.tested_at::text
		  FROM benchmark_results br JOIN configurations c ON br.config_id = c.id WHERE true`
	var args []any
	n := 0
	if p.ConfigID != "" {
		n++
		q += fmt.Sprintf(` AND br.config_id = $%d`, n)
		args = append(args, p.ConfigID)
	}
	if p.Hardware != "" {
		n++
		q += fmt.Sprintf(` AND c.hardware = $%d`, n)
		args = append(args, p.Hardware)
	}
	if p.Model != "" {
		n++
		q += fmt.Sprintf(` AND c.model = $%d`, n)
		args = append(args, p.Model)
	}
	if p.Engine != "" {
		n++
		q += fmt.Sprintf(` AND c.engine_type = $%d`, n)
		args = append(args, p.Engine)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}
	n++
	q += fmt.Sprintf(` ORDER BY br.tested_at DESC LIMIT $%d`, n)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []BenchmarkRow
	for rows.Next() {
		var r BenchmarkRow
		if err := rows.Scan(&r.ID, &r.ConfigID, &r.DeviceID, &r.Hardware, &r.Model, &r.Engine,
			&r.Concurrency, &r.ThroughputTPS, &r.TTFTP50ms, &r.TTFTP95ms, &r.VRAMUsageMiB, &r.TestedAt); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *PostgresCentralStore) QueryGaps(ctx context.Context, p GapsQuery) ([]GapEntry, error) {
	q := `SELECT DISTINCT c.hardware, c.model, c.engine_type
		  FROM configurations c
		  LEFT JOIN benchmark_results br ON c.id = br.config_id
		  WHERE br.id IS NULL`
	var args []any
	if p.Hardware != "" {
		q += ` AND c.hardware = $1`
		args = append(args, p.Hardware)
	}
	q += ` LIMIT 200`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []GapEntry
	for rows.Next() {
		var g GapEntry
		if err := rows.Scan(&g.Hardware, &g.Model, &g.Engine); err != nil {
			return nil, err
		}
		g.Reason = "no_benchmark"
		result = append(result, g)
	}
	return result, rows.Err()
}

func (s *PostgresCentralStore) InsertAdvisory(ctx context.Context, adv Advisory) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO advisories (id, type, target_hardware, target_model, target_engine, content_json, reasoning, confidence, based_on_json, status, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		adv.ID, adv.Type, adv.TargetHardware, adv.TargetModel, adv.TargetEngine,
		string(adv.ContentJSON), adv.Reasoning, adv.Confidence, string(adv.BasedOnJSON),
		adv.Status, adv.CreatedAt)
	return err
}

func (s *PostgresCentralStore) ListAdvisories(ctx context.Context, p AdvisoryQuery) ([]Advisory, error) {
	q := `SELECT id, type, COALESCE(target_hardware,''), COALESCE(target_model,''), COALESCE(target_engine,''),
		  content_json::text, COALESCE(reasoning,''), confidence, COALESCE(based_on_json::text,'[]'), status, created_at, delivered_at, validated_at
		  FROM advisories WHERE true`
	var args []any
	n := 0
	if p.Hardware != "" {
		n++
		q += fmt.Sprintf(` AND target_hardware = $%d`, n)
		args = append(args, p.Hardware)
	}
	if p.Status != "" {
		n++
		q += fmt.Sprintf(` AND status = $%d`, n)
		args = append(args, p.Status)
	}
	if p.Type != "" {
		n++
		q += fmt.Sprintf(` AND type = $%d`, n)
		args = append(args, p.Type)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}
	n++
	q += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d`, n)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Advisory
	for rows.Next() {
		var a Advisory
		var deliveredAt, validatedAt sql.NullTime
		var contentJSON, basedOnJSON string
		if err := rows.Scan(&a.ID, &a.Type, &a.TargetHardware, &a.TargetModel, &a.TargetEngine,
			&contentJSON, &a.Reasoning, &a.Confidence, &basedOnJSON, &a.Status,
			&a.CreatedAt, &deliveredAt, &validatedAt); err != nil {
			return nil, err
		}
		a.ContentJSON = json.RawMessage(contentJSON)
		a.BasedOnJSON = json.RawMessage(basedOnJSON)
		if deliveredAt.Valid {
			a.DeliveredAt = &deliveredAt.Time
		}
		if validatedAt.Valid {
			a.ValidatedAt = &validatedAt.Time
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func (s *PostgresCentralStore) UpdateAdvisoryStatus(ctx context.Context, id string, status string) error {
	q := `UPDATE advisories SET status = $1`
	switch status {
	case "delivered":
		q += `, delivered_at = NOW()`
	case "validated", "rejected":
		q += `, validated_at = NOW()`
	}
	q += ` WHERE id = $2`
	_, err := s.db.ExecContext(ctx, q, status, id)
	return err
}

func (s *PostgresCentralStore) UpsertScenario(ctx context.Context, sc Scenario) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO scenarios (id, name, hardware_profile, scenario_yaml, source, advisory_id, version, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT(name) DO UPDATE SET scenario_yaml = EXCLUDED.scenario_yaml, source = EXCLUDED.source,
		 advisory_id = EXCLUDED.advisory_id, version = scenarios.version + 1, updated_at = NOW()`,
		sc.ID, sc.Name, sc.HardwareProfile, sc.ScenarioYAML, sc.Source, sc.AdvisoryID, sc.Version,
		sc.CreatedAt, sc.UpdatedAt)
	return err
}

func (s *PostgresCentralStore) ListScenarios(ctx context.Context, hardware string) ([]Scenario, error) {
	q := `SELECT id, name, hardware_profile, scenario_yaml, source, COALESCE(advisory_id,''), version, created_at, COALESCE(updated_at, created_at)
		  FROM scenarios WHERE true`
	var args []any
	if hardware != "" {
		q += ` AND hardware_profile = $1`
		args = append(args, hardware)
	}
	q += ` ORDER BY updated_at DESC LIMIT 50`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Scenario
	for rows.Next() {
		var sc Scenario
		if err := rows.Scan(&sc.ID, &sc.Name, &sc.HardwareProfile, &sc.ScenarioYAML, &sc.Source,
			&sc.AdvisoryID, &sc.Version, &sc.CreatedAt, &sc.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, sc)
	}
	return result, rows.Err()
}

func (s *PostgresCentralStore) InsertAnalysisRun(ctx context.Context, run AnalysisRun) error {
	advJSON, _ := json.Marshal(run.Advisories)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO analysis_runs (id, type, status, input_json, started_at, advisories) VALUES ($1,$2,$3,$4,$5,$6)`,
		run.ID, run.Type, run.Status, string(run.InputJSON), run.StartedAt, string(advJSON))
	return err
}

func (s *PostgresCentralStore) UpdateAnalysisRun(ctx context.Context, id string, u AnalysisRunUpdate) error {
	advJSON, _ := json.Marshal(u.Advisories)
	_, err := s.db.ExecContext(ctx,
		`UPDATE analysis_runs SET status=$1, output_json=$2, advisories=$3, completed_at=$4, error=$5 WHERE id=$6`,
		u.Status, string(u.OutputJSON), string(advJSON), u.CompletedAt, u.Error, id)
	return err
}

func (s *PostgresCentralStore) ListAnalysisRuns(ctx context.Context, limit int) ([]AnalysisRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, status, COALESCE(input_json::text,''), COALESCE(output_json::text,''), COALESCE(advisories::text,'[]'), started_at, completed_at, COALESCE(error,'')
		 FROM analysis_runs ORDER BY started_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AnalysisRun
	for rows.Next() {
		var r AnalysisRun
		var inputJSON, outputJSON, advJSON string
		var completedAt sql.NullTime
		if err := rows.Scan(&r.ID, &r.Type, &r.Status, &inputJSON, &outputJSON, &advJSON, &r.StartedAt, &completedAt, &r.Error); err != nil {
			return nil, err
		}
		r.InputJSON = json.RawMessage(inputJSON)
		r.OutputJSON = json.RawMessage(outputJSON)
		_ = json.Unmarshal([]byte(advJSON), &r.Advisories)
		if completedAt.Valid {
			r.CompletedAt = &completedAt.Time
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *PostgresCentralStore) Stats(ctx context.Context) (*StoreStats, error) {
	st := &StoreStats{}
	queries := []struct {
		q   string
		dst *int
	}{
		{"SELECT COUNT(*) FROM devices", &st.Devices},
		{"SELECT COUNT(*) FROM configurations", &st.Configurations},
		{"SELECT COUNT(*) FROM benchmark_results", &st.Benchmarks},
		{"SELECT COUNT(*) FROM knowledge_notes", &st.Notes},
		{"SELECT COUNT(*) FROM advisories", &st.Advisories},
		{"SELECT COUNT(*) FROM scenarios", &st.Scenarios},
	}
	for _, qr := range queries {
		_ = s.db.QueryRowContext(ctx, qr.q).Scan(qr.dst)
	}
	return st, nil
}

var _ CentralStore = (*PostgresCentralStore)(nil)
```

- [ ] **Step 3: 写 PostgreSQL 测试（需要 PG 可用否则 skip）**

```go
// store_postgres_test.go
package central

import (
	"os"
	"testing"
)

func TestPostgresCentralStore(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set, skipping PostgreSQL tests")
	}
	storeTestSuite(t, func(t *testing.T) CentralStore {
		t.Helper()
		s, err := NewPostgresCentralStore(dsn)
		if err != nil {
			t.Fatalf("NewPostgresCentralStore: %v", err)
		}
		// Clean tables for test isolation
		for _, table := range []string{"analysis_runs", "advisories", "scenarios", "benchmark_results", "knowledge_notes", "configurations", "devices"} {
			s.db.Exec("DELETE FROM " + table)
		}
		return s
	})
}
```

- [ ] **Step 4: Run SQLite tests (always) + PG tests (if available)**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/central/ -run TestSQLite -v && go test ./internal/central/ -run TestPostgres -v`
Expected: SQLite PASS, PostgreSQL SKIP (unless TEST_POSTGRES_DSN set)

- [ ] **Step 5: Commit**

```bash
git add internal/central/store_postgres.go internal/central/store_postgres_test.go
go mod tidy
git add go.mod go.sum
git commit -m "feat(central): implement PostgresCentralStore with pgx/v5"
```

---

### Task 4: 重构 Server 使用 CentralStore

**Files:**
- Modify: `internal/central/server.go`

- [ ] **Step 1: 读取当前 server.go**

读取完整的 `internal/central/server.go`，理解所有现有 handler 逻辑。

- [ ] **Step 2: 重构 Server 使用 CentralStore 接口**

```go
// server.go — 重构后
type Server struct {
	store  CentralStore
	config Config
	mux    *http.ServeMux
}

type Config struct {
	Addr     string
	APIKey   string
	DBDriver string // "sqlite" or "postgres"
	DBPath   string // SQLite path
	DBDSN    string // PostgreSQL DSN
}

func New(cfg Config) (*Server, error) {
	var store CentralStore
	var err error
	switch cfg.DBDriver {
	case "postgres":
		store, err = NewPostgresCentralStore(cfg.DBDSN)
	default:
		store, err = NewSQLiteCentralStore(cfg.DBPath)
	}
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	s := &Server{store: store, config: cfg}
	s.mux = http.NewServeMux()
	s.routes()
	return s, nil
}
```

将所有 handler 方法从直接 `s.db.ExecContext/QueryContext` 改为调用 `s.store.XXX` 方法。

新增路由：
```go
func (s *Server) routes() {
	// 现有
	s.mux.HandleFunc("POST /api/v1/ingest", s.authMiddleware(s.handleIngest))
	s.mux.HandleFunc("GET /api/v1/query", s.authMiddleware(s.handleQuery))
	s.mux.HandleFunc("GET /api/v1/sync", s.authMiddleware(s.handleSync))
	s.mux.HandleFunc("GET /api/v1/stats", s.handleStats)
	// 新增
	s.mux.HandleFunc("POST /api/v1/advise", s.authMiddleware(s.handleAdvise))
	s.mux.HandleFunc("GET /api/v1/advisories", s.authMiddleware(s.handleListAdvisories))
	s.mux.HandleFunc("POST /api/v1/advisory/feedback", s.authMiddleware(s.handleAdvisoryFeedback))
	s.mux.HandleFunc("POST /api/v1/scenario/generate", s.authMiddleware(s.handleScenarioGenerate))
	s.mux.HandleFunc("GET /api/v1/scenarios", s.authMiddleware(s.handleListScenarios))
	s.mux.HandleFunc("GET /api/v1/analysis", s.authMiddleware(s.handleListAnalysis))
}
```

- [ ] **Step 3: 更新 handleIngest 使用 store**

```go
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	var payload IngestPayload
	limited := http.MaxBytesReader(w, r.Body, 10<<20)
	if err := json.NewDecoder(limited).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if payload.DeviceID != "" {
		_ = s.store.UpsertDevice(r.Context(), DeviceInfo{
			ID: payload.DeviceID, GPUArch: payload.GPUArch,
		})
	}

	cfgRes, _ := s.store.IngestConfigurations(r.Context(), payload.DeviceID, payload.Configurations)
	benchRes, _ := s.store.IngestBenchmarks(r.Context(), payload.DeviceID, payload.Benchmarks)
	noteRes, _ := s.store.IngestNotes(r.Context(), payload.KnowledgeNotes)

	writeJSON(w, map[string]any{
		"ingested":   cfgRes.Ingested,
		"duplicates": cfgRes.Duplicates,
		"benchmarks": benchRes.Ingested,
		"notes":      noteRes.Ingested,
	})

	// Post-ingest event for Analyzer (wired in Task 7)
	if s.onIngest != nil {
		go s.onIngest(payload.DeviceID)
	}
}
```

- [ ] **Step 4: Run existing tests**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/central/ -v -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/central/server.go
git commit -m "refactor(central): server uses CentralStore interface instead of raw sql.DB"
```

---

### Task 5: Advisor Engine — LLM Prompt Templates

**Files:**
- Create: `internal/central/advisor_prompts.go`

- [ ] **Step 1: 定义 Advisor prompt 模板**

```go
// advisor_prompts.go
package central

const recommendSystemPrompt = `You are an AI inference configuration advisor. Given hardware specifications, benchmark history, and user intent, recommend optimal inference engine configuration.

Output strictly as JSON:
{
  "engine": "string",
  "config": {"gpu_memory_utilization": 0.0, "quantization": "string", "max_model_len": 0, "tensor_parallel_size": 0},
  "confidence": "low|medium|high",
  "reasoning": "string explaining the recommendation",
  "suggested_validation": {"kind": "tune|benchmark", "params": [{"key": "string", "values": []}]}
}

Rules:
- Choose quantization based on VRAM: BF16 if fits, AWQ/GPTQ if tight, 4-bit if very tight
- tensor_parallel_size = min(gpu_count, what the model architecture supports)
- gpu_memory_utilization: start conservative (0.75-0.80), suggest validation range
- Reference similar hardware benchmarks when available
- If uncertain, set confidence=low and suggest validation`

const optimizeScenarioSystemPrompt = `You are an AI deployment scenario optimizer. Given a deployment scenario YAML, benchmark data from multiple devices, and performance metrics, suggest parameter optimizations.

Output strictly as JSON:
{
  "optimized_params": [{"model": "string", "key": "string", "old_value": "any", "new_value": "any", "reasoning": "string"}],
  "confidence": "low|medium|high",
  "overall_reasoning": "string"
}

Rules:
- Only suggest changes backed by benchmark evidence
- Conservative changes: prefer smaller adjustments that are safe across devices
- If gmu can be increased safely (3+ devices stable at higher gmu), recommend it
- Never recommend changes that would cause OOM on any known device`

const generateScenarioSystemPrompt = `You are an AI deployment scenario generator. Given hardware profile, modality requirements, and constraints, generate a complete deployment scenario.

Output strictly as JSON:
{
  "name": "string",
  "models": [{"model": "string", "engine": "string", "config": {}, "priority": 0}],
  "resource_allocation": {"strategy": "string", "details": "string"},
  "open_questions": ["string"],
  "confidence": "low|medium|high",
  "reasoning": "string"
}

Rules:
- Respect VRAM budget: sum of all model VRAM must fit in available GPU memory
- Higher priority models get more resources
- Include open_questions for anything you're uncertain about
- Generate scenario name as: <hardware>-<primary-modality>-<model-count>m`

const gapAnalysisSystemPrompt = `You are analyzing cross-device benchmark data to identify patterns, anomalies, and optimization opportunities.

Output strictly as JSON:
{
  "patterns": [{"description": "string", "evidence": "string", "confidence": "low|medium|high"}],
  "anomalies": [{"device": "string", "description": "string", "severity": "low|medium|high"}],
  "recommendations": [{"type": "config_recommend|gap_alert", "target_hardware": "string", "target_model": "string", "reasoning": "string"}]
}`
```

- [ ] **Step 2: Commit**

```bash
git add internal/central/advisor_prompts.go
git commit -m "feat(central): add LLM prompt templates for Advisor Engine"
```

---

### Task 6: Advisor Engine 实现

**Files:**
- Create: `internal/central/advisor.go`
- Create: `internal/central/advisor_test.go`

- [ ] **Step 1: 写 Advisor 测试**

```go
// advisor_test.go
package central

import (
	"context"
	"testing"
)

type mockAdvisorLLM struct {
	response string
	err      error
}

func (m *mockAdvisorLLM) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}

func TestAdvisor_Recommend(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close()
	// Seed data
	_ = store.UpsertDevice(context.Background(), DeviceInfo{ID: "dev-1", HardwareProfile: "nvidia-rtx4090-x86", GPUArch: "Ada"})

	llm := &mockAdvisorLLM{
		response: `{"engine":"vllm","config":{"gpu_memory_utilization":0.78,"quantization":"awq","tensor_parallel_size":1},"confidence":"medium","reasoning":"AWQ fits 24GB","suggested_validation":{"kind":"tune","params":[]}}`,
	}
	adv := NewAdvisor(store, llm)

	req := RecommendRequest{
		HardwareProfile: "nvidia-rtx4090-x86",
		HardwareInfo:    HardwareSpec{GPUVRAMMiB: 24576, GPUCount: 1},
		Model:           "qwen3-30b-a3b",
		Intent:          "throughput",
	}
	resp, err := adv.Recommend(context.Background(), req)
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if resp.AdvisoryID == "" {
		t.Error("expected advisory ID")
	}
	if resp.Confidence != "medium" {
		t.Errorf("confidence = %q, want medium", resp.Confidence)
	}
}

func TestAdvisor_Recommend_LLMError(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close()
	llm := &mockAdvisorLLM{err: fmt.Errorf("LLM unavailable")}
	adv := NewAdvisor(store, llm)
	_, err := adv.Recommend(context.Background(), RecommendRequest{Model: "test"})
	if err == nil {
		t.Fatal("expected error when LLM fails")
	}
}

func newTestSQLiteStore(t *testing.T) *SQLiteCentralStore {
	t.Helper()
	s, err := NewSQLiteCentralStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/central/ -run TestAdvisor -v`
Expected: FAIL — Advisor not defined

- [ ] **Step 3: 实现 Advisor**

```go
// advisor.go
package central

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// LLMCompleter abstracts an LLM call for the Advisor.
type LLMCompleter interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// Advisor generates configuration recommendations using LLM + knowledge data.
type Advisor struct {
	store CentralStore
	llm   LLMCompleter
}

func NewAdvisor(store CentralStore, llm LLMCompleter) *Advisor {
	return &Advisor{store: store, llm: llm}
}

// RecommendRequest is the input for single-config recommendation.
type RecommendRequest struct {
	HardwareProfile string       `json:"hardware_profile"`
	HardwareInfo    HardwareSpec `json:"hardware_info"`
	Model           string       `json:"model"`
	Engine          string       `json:"engine"`
	Intent          string       `json:"intent"` // throughput / latency / balanced
}

type HardwareSpec struct {
	GPUVRAMMiB int `json:"gpu_vram_mib"`
	GPUCount   int `json:"gpu_count"`
}

// RecommendResponse is the Advisor's recommendation.
type RecommendResponse struct {
	AdvisoryID          string          `json:"advisory_id"`
	Engine              string          `json:"engine"`
	Config              json.RawMessage `json:"config"`
	Reasoning           string          `json:"reasoning"`
	Confidence          string          `json:"confidence"`
	BasedOn             []string        `json:"based_on"`
	SuggestedValidation json.RawMessage `json:"suggested_validation"`
}

func (a *Advisor) Recommend(ctx context.Context, req RecommendRequest) (*RecommendResponse, error) {
	// Gather context: similar hardware benchmarks + golden configs
	benchmarks, _ := a.store.QueryBenchmarks(ctx, BenchmarkQuery{
		Hardware: req.HardwareProfile, Model: req.Model, Limit: 20,
	})
	configs, _ := a.store.QueryConfigs(ctx, ConfigQuery{
		Hardware: req.HardwareProfile, Status: "golden", Limit: 10,
	})

	// Build LLM prompt
	prompt := buildRecommendPrompt(req, benchmarks, configs)
	llmResp, err := a.llm.Complete(ctx, recommendSystemPrompt, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM recommend: %w", err)
	}

	// Parse LLM response
	var parsed struct {
		Engine              string          `json:"engine"`
		Config              json.RawMessage `json:"config"`
		Confidence          string          `json:"confidence"`
		Reasoning           string          `json:"reasoning"`
		SuggestedValidation json.RawMessage `json:"suggested_validation"`
	}
	if err := json.Unmarshal([]byte(llmResp), &parsed); err != nil {
		return nil, fmt.Errorf("parse LLM response: %w", err)
	}

	// Store advisory
	advID := fmt.Sprintf("adv-%x", sha256.Sum256([]byte(fmt.Sprintf("%s-%s-%d", req.HardwareProfile, req.Model, time.Now().UnixNano()))))[:12]
	basedOnIDs := extractConfigIDs(configs)
	basedOnJSON, _ := json.Marshal(basedOnIDs)
	adv := Advisory{
		ID: advID, Type: "config_recommend",
		TargetHardware: req.HardwareProfile, TargetModel: req.Model, TargetEngine: parsed.Engine,
		ContentJSON: parsed.Config, Reasoning: parsed.Reasoning,
		Confidence: parsed.Confidence, BasedOnJSON: basedOnJSON,
		Status: "pending", CreatedAt: time.Now(),
	}
	if err := a.store.InsertAdvisory(ctx, adv); err != nil {
		slog.Warn("advisor: store advisory failed", "error", err)
	}

	return &RecommendResponse{
		AdvisoryID:          advID,
		Engine:              parsed.Engine,
		Config:              parsed.Config,
		Reasoning:           parsed.Reasoning,
		Confidence:          parsed.Confidence,
		BasedOn:             basedOnIDs,
		SuggestedValidation: parsed.SuggestedValidation,
	}, nil
}

// OptimizeScenarioRequest is the input for scenario optimization.
type OptimizeScenarioRequest struct {
	ScenarioName string `json:"scenario_name"`
	ScenarioYAML string `json:"scenario_yaml"`
}

// OptimizeScenarioResponse returns optimization suggestions.
type OptimizeScenarioResponse struct {
	AdvisoryID string          `json:"advisory_id"`
	Changes    json.RawMessage `json:"changes"`
	Reasoning  string          `json:"reasoning"`
	Confidence string          `json:"confidence"`
}

func (a *Advisor) OptimizeScenario(ctx context.Context, req OptimizeScenarioRequest) (*OptimizeScenarioResponse, error) {
	// Gather benchmarks for models in this scenario
	prompt := buildOptimizePrompt(req)
	llmResp, err := a.llm.Complete(ctx, optimizeScenarioSystemPrompt, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM optimize: %w", err)
	}

	var parsed struct {
		OptimizedParams json.RawMessage `json:"optimized_params"`
		Confidence      string          `json:"confidence"`
		OverallReasoning string         `json:"overall_reasoning"`
	}
	if err := json.Unmarshal([]byte(llmResp), &parsed); err != nil {
		return nil, fmt.Errorf("parse LLM response: %w", err)
	}

	advID := fmt.Sprintf("adv-%x", sha256.Sum256([]byte(fmt.Sprintf("opt-%s-%d", req.ScenarioName, time.Now().UnixNano()))))[:12]
	adv := Advisory{
		ID: advID, Type: "scenario_optimization",
		ContentJSON: parsed.OptimizedParams, Reasoning: parsed.OverallReasoning,
		Confidence: parsed.Confidence, Status: "pending", CreatedAt: time.Now(),
	}
	_ = a.store.InsertAdvisory(ctx, adv)

	return &OptimizeScenarioResponse{
		AdvisoryID: advID, Changes: parsed.OptimizedParams,
		Reasoning: parsed.OverallReasoning, Confidence: parsed.Confidence,
	}, nil
}

// GenerateScenarioRequest is the input for new scenario generation.
type GenerateScenarioRequest struct {
	HardwareProfile string   `json:"hardware_profile"`
	HardwareInfo    HardwareSpec `json:"hardware_info"`
	Modalities      []string `json:"modalities"` // text, tts, image, etc.
	Constraints     string   `json:"constraints"`
}

// GenerateScenarioResponse returns a newly generated scenario.
type GenerateScenarioResponse struct {
	AdvisoryID   string `json:"advisory_id"`
	ScenarioYAML string `json:"scenario_yaml"`
	Reasoning    string `json:"reasoning"`
	Confidence   string `json:"confidence"`
}

func (a *Advisor) GenerateScenario(ctx context.Context, req GenerateScenarioRequest) (*GenerateScenarioResponse, error) {
	// Gather existing configs for this hardware
	configs, _ := a.store.QueryConfigs(ctx, ConfigQuery{Hardware: req.HardwareProfile, Limit: 20})

	prompt := buildGeneratePrompt(req, configs)
	llmResp, err := a.llm.Complete(ctx, generateScenarioSystemPrompt, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM generate: %w", err)
	}

	var parsed struct {
		Name             string          `json:"name"`
		Models           json.RawMessage `json:"models"`
		ResourceAlloc    json.RawMessage `json:"resource_allocation"`
		OpenQuestions    []string        `json:"open_questions"`
		Confidence       string          `json:"confidence"`
		Reasoning        string          `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(llmResp), &parsed); err != nil {
		return nil, fmt.Errorf("parse LLM response: %w", err)
	}

	// Convert to YAML-like format (JSON representation for now)
	scenarioContent, _ := json.MarshalIndent(parsed, "", "  ")

	advID := fmt.Sprintf("adv-%x", sha256.Sum256([]byte(fmt.Sprintf("gen-%s-%d", req.HardwareProfile, time.Now().UnixNano()))))[:12]
	scID := fmt.Sprintf("sc-%x", sha256.Sum256([]byte(parsed.Name)))[:12]

	// Store scenario
	_ = a.store.UpsertScenario(ctx, Scenario{
		ID: scID, Name: parsed.Name, HardwareProfile: req.HardwareProfile,
		ScenarioYAML: string(scenarioContent), Source: "generated", AdvisoryID: advID,
		Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	// Store advisory
	adv := Advisory{
		ID: advID, Type: "scenario_generation",
		TargetHardware: req.HardwareProfile,
		ContentJSON: scenarioContent, Reasoning: parsed.Reasoning,
		Confidence: parsed.Confidence, Status: "pending", CreatedAt: time.Now(),
	}
	_ = a.store.InsertAdvisory(ctx, adv)

	return &GenerateScenarioResponse{
		AdvisoryID: advID, ScenarioYAML: string(scenarioContent),
		Reasoning: parsed.Reasoning, Confidence: parsed.Confidence,
	}, nil
}

func buildRecommendPrompt(req RecommendRequest, benchmarks []BenchmarkRow, configs []ConfigRow) string {
	data, _ := json.MarshalIndent(map[string]any{
		"request":    req,
		"benchmarks": benchmarks,
		"golden_configs": configs,
	}, "", "  ")
	return string(data)
}

func buildOptimizePrompt(req OptimizeScenarioRequest) string {
	data, _ := json.MarshalIndent(map[string]any{
		"scenario_name": req.ScenarioName,
		"scenario_yaml": req.ScenarioYAML,
	}, "", "  ")
	return string(data)
}

func buildGeneratePrompt(req GenerateScenarioRequest, configs []ConfigRow) string {
	data, _ := json.MarshalIndent(map[string]any{
		"hardware":    req.HardwareProfile,
		"hw_info":     req.HardwareInfo,
		"modalities":  req.Modalities,
		"constraints": req.Constraints,
		"existing_configs": configs,
	}, "", "  ")
	return string(data)
}

func extractConfigIDs(configs []ConfigRow) []string {
	ids := make([]string, 0, len(configs))
	for _, c := range configs {
		ids = append(ids, c.ID)
	}
	return ids
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/central/ -run TestAdvisor -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/central/advisor.go internal/central/advisor_test.go
git commit -m "feat(central): implement Advisor Engine with Recommend/OptimizeScenario/GenerateScenario"
```

---

### Task 7: Periodic Analyzer 实现

**Files:**
- Create: `internal/central/analyzer.go`
- Create: `internal/central/analyzer_test.go`

- [ ] **Step 1: 写 Analyzer 测试**

```go
// analyzer_test.go
package central

import (
	"context"
	"testing"
	"time"
)

func TestAnalyzer_GapScan(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close()

	// Seed: device + config without benchmark = gap
	_ = store.UpsertDevice(context.Background(), DeviceInfo{ID: "dev-1", GPUArch: "Ada"})
	_, _ = store.IngestConfigurations(context.Background(), "dev-1", []IngestConfig{{
		ID: "cfg-1", Hardware: "nvidia-rtx4090-x86", EngineType: "vllm",
		Model: "qwen3-8b", Config: []byte(`{}`), ConfigHash: "hash1", Status: "experiment",
	}})

	llm := &mockAdvisorLLM{response: `{"patterns":[],"anomalies":[],"recommendations":[{"type":"gap_alert","target_hardware":"nvidia-rtx4090-x86","target_model":"qwen3-8b","reasoning":"no benchmark"}]}`}
	analyzer := NewAnalyzer(store, llm, AnalyzerConfig{
		GapScanInterval:  24 * time.Hour,
		PatternInterval:  7 * 24 * time.Hour,
		PostIngestDelay:  0,
	})

	runID, err := analyzer.RunGapScan(context.Background())
	if err != nil {
		t.Fatalf("RunGapScan: %v", err)
	}
	if runID == "" {
		t.Error("expected analysis run ID")
	}

	// Verify analysis run was stored
	runs, _ := store.ListAnalysisRuns(context.Background(), 5)
	if len(runs) == 0 {
		t.Fatal("expected analysis run in store")
	}
	if runs[0].Status != "completed" {
		t.Errorf("status = %q, want completed", runs[0].Status)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

- [ ] **Step 3: 实现 Analyzer**

```go
// analyzer.go
package central

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

type AnalyzerConfig struct {
	GapScanInterval time.Duration
	PatternInterval time.Duration
	PostIngestDelay time.Duration
}

func DefaultAnalyzerConfig() AnalyzerConfig {
	return AnalyzerConfig{
		GapScanInterval: 24 * time.Hour,
		PatternInterval: 7 * 24 * time.Hour,
		PostIngestDelay: 5 * time.Minute,
	}
}

type Analyzer struct {
	store  CentralStore
	llm    LLMCompleter
	config AnalyzerConfig
	cancel context.CancelFunc
}

func NewAnalyzer(store CentralStore, llm LLMCompleter, config AnalyzerConfig) *Analyzer {
	return &Analyzer{store: store, llm: llm, config: config}
}

// Start launches background analysis loops.
func (a *Analyzer) Start(ctx context.Context) {
	ctx, a.cancel = context.WithCancel(ctx)

	go a.loop(ctx, "gap_scan", a.config.GapScanInterval, func(ctx context.Context) {
		if _, err := a.RunGapScan(ctx); err != nil {
			slog.Warn("analyzer gap scan failed", "error", err)
		}
	})
	go a.loop(ctx, "pattern_discovery", a.config.PatternInterval, func(ctx context.Context) {
		if _, err := a.RunPatternDiscovery(ctx); err != nil {
			slog.Warn("analyzer pattern discovery failed", "error", err)
		}
	})
	slog.Info("analyzer started", "gap_interval", a.config.GapScanInterval, "pattern_interval", a.config.PatternInterval)
}

func (a *Analyzer) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *Analyzer) loop(ctx context.Context, name string, interval time.Duration, fn func(context.Context)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			slog.Info("analyzer: running", "type", name)
			fn(ctx)
		}
	}
}

// RunGapScan finds HW×Model gaps and generates advisories.
func (a *Analyzer) RunGapScan(ctx context.Context) (string, error) {
	runID := genID("ar")
	now := time.Now()
	_ = a.store.InsertAnalysisRun(ctx, AnalysisRun{
		ID: runID, Type: "gap_scan", Status: "running", StartedAt: now,
	})

	gaps, err := a.store.QueryGaps(ctx, GapsQuery{})
	if err != nil {
		a.failRun(ctx, runID, err)
		return runID, err
	}

	if len(gaps) == 0 {
		a.completeRun(ctx, runID, []byte(`{"gaps":0}`), nil)
		return runID, nil
	}

	// Use LLM to analyze gaps and generate recommendations
	gapsJSON, _ := json.Marshal(gaps)
	prompt := fmt.Sprintf("Analyze these knowledge gaps and recommend which to fill first:\n%s", string(gapsJSON))
	llmResp, err := a.llm.Complete(ctx, gapAnalysisSystemPrompt, prompt)
	if err != nil {
		// Fallback: generate simple gap_alert advisories without LLM
		var advIDs []string
		for _, g := range gaps {
			advID := genID("adv")
			content, _ := json.Marshal(g)
			_ = a.store.InsertAdvisory(ctx, Advisory{
				ID: advID, Type: "gap_alert",
				TargetHardware: g.Hardware, TargetModel: g.Model, TargetEngine: g.Engine,
				ContentJSON: content, Reasoning: g.Reason,
				Confidence: "high", Status: "pending", CreatedAt: time.Now(),
			})
			advIDs = append(advIDs, advID)
		}
		output, _ := json.Marshal(map[string]any{"gaps": len(gaps), "llm_fallback": true})
		a.completeRun(ctx, runID, output, advIDs)
		return runID, nil
	}

	// Parse LLM recommendations and create advisories
	var parsed struct {
		Recommendations []struct {
			Type           string `json:"type"`
			TargetHardware string `json:"target_hardware"`
			TargetModel    string `json:"target_model"`
			Reasoning      string `json:"reasoning"`
		} `json:"recommendations"`
	}
	_ = json.Unmarshal([]byte(llmResp), &parsed)

	var advIDs []string
	for _, rec := range parsed.Recommendations {
		advID := genID("adv")
		content, _ := json.Marshal(rec)
		_ = a.store.InsertAdvisory(ctx, Advisory{
			ID: advID, Type: rec.Type,
			TargetHardware: rec.TargetHardware, TargetModel: rec.TargetModel,
			ContentJSON: content, Reasoning: rec.Reasoning,
			Confidence: "medium", Status: "pending", CreatedAt: time.Now(),
		})
		advIDs = append(advIDs, advID)
	}

	output, _ := json.Marshal(map[string]any{"gaps": len(gaps), "advisories_created": len(advIDs)})
	a.completeRun(ctx, runID, output, advIDs)
	return runID, nil
}

// RunPatternDiscovery analyzes cross-device benchmarks for patterns.
func (a *Analyzer) RunPatternDiscovery(ctx context.Context) (string, error) {
	runID := genID("ar")
	now := time.Now()
	_ = a.store.InsertAnalysisRun(ctx, AnalysisRun{
		ID: runID, Type: "pattern_discovery", Status: "running", StartedAt: now,
	})

	benchmarks, err := a.store.QueryBenchmarks(ctx, BenchmarkQuery{Limit: 200})
	if err != nil {
		a.failRun(ctx, runID, err)
		return runID, err
	}

	if len(benchmarks) < 3 {
		a.completeRun(ctx, runID, []byte(`{"skipped":"insufficient data"}`), nil)
		return runID, nil
	}

	benchJSON, _ := json.Marshal(benchmarks)
	prompt := fmt.Sprintf("Analyze these cross-device benchmark results for patterns and anomalies:\n%s", string(benchJSON))
	llmResp, err := a.llm.Complete(ctx, gapAnalysisSystemPrompt, prompt)
	if err != nil {
		a.failRun(ctx, runID, err)
		return runID, err
	}

	a.completeRun(ctx, runID, json.RawMessage(llmResp), nil)
	return runID, nil
}

// OnIngest triggers a delayed post-ingest analysis.
func (a *Analyzer) OnIngest(deviceID string) {
	if a.config.PostIngestDelay > 0 {
		time.Sleep(a.config.PostIngestDelay)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := a.RunGapScan(ctx); err != nil {
		slog.Warn("post-ingest gap scan failed", "device", deviceID, "error", err)
	}
}

func (a *Analyzer) completeRun(ctx context.Context, id string, output json.RawMessage, advIDs []string) {
	now := time.Now()
	_ = a.store.UpdateAnalysisRun(ctx, id, AnalysisRunUpdate{
		Status: "completed", OutputJSON: output, Advisories: advIDs, CompletedAt: &now,
	})
}

func (a *Analyzer) failRun(ctx context.Context, id string, err error) {
	now := time.Now()
	_ = a.store.UpdateAnalysisRun(ctx, id, AnalysisRunUpdate{
		Status: "failed", CompletedAt: &now, Error: err.Error(),
	})
}

func genID(prefix string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())))
	return fmt.Sprintf("%s-%x", prefix, h)[:len(prefix)+1+12]
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/central/ -run TestAnalyzer -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/central/analyzer.go internal/central/analyzer_test.go
git commit -m "feat(central): implement Periodic Analyzer with gap scan and pattern discovery"
```

---

### Task 8: 新增 API 端点 Handler

**Files:**
- Modify: `internal/central/server.go`

- [ ] **Step 1: 添加 Advisor 和 Analyzer 到 Server**

```go
// 在 Server struct 中添加:
type Server struct {
	store    CentralStore
	config   Config
	mux      *http.ServeMux
	advisor  *Advisor
	analyzer *Analyzer
	onIngest func(deviceID string)
}
```

- [ ] **Step 2: 实现 handleAdvise**

```go
func (s *Server) handleAdvise(w http.ResponseWriter, r *http.Request) {
	var req RecommendRequest
	limited := http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(limited).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if s.advisor == nil {
		http.Error(w, "advisor not configured", http.StatusServiceUnavailable)
		return
	}
	resp, err := s.advisor.Recommend(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}
```

- [ ] **Step 3: 实现 handleListAdvisories**

```go
func (s *Server) handleListAdvisories(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := s.store.ListAdvisories(r.Context(), AdvisoryQuery{
		Hardware: q.Get("hardware"),
		Status:   q.Get("status"),
		Type:     q.Get("type"),
		Limit:    50,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, list)
}
```

- [ ] **Step 4: 实现 handleAdvisoryFeedback**

```go
func (s *Server) handleAdvisoryFeedback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AdvisoryID string `json:"advisory_id"`
		Status     string `json:"status"` // validated / rejected
		Reason     string `json:"reason"`
	}
	limited := http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(limited).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Status != "validated" && req.Status != "rejected" {
		http.Error(w, "status must be 'validated' or 'rejected'", http.StatusBadRequest)
		return
	}
	if err := s.store.UpdateAdvisoryStatus(r.Context(), req.AdvisoryID, req.Status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}
```

- [ ] **Step 5: 实现 handleScenarioGenerate 和 handleListScenarios**

```go
func (s *Server) handleScenarioGenerate(w http.ResponseWriter, r *http.Request) {
	var req GenerateScenarioRequest
	limited := http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(limited).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if s.advisor == nil {
		http.Error(w, "advisor not configured", http.StatusServiceUnavailable)
		return
	}
	resp, err := s.advisor.GenerateScenario(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleListScenarios(w http.ResponseWriter, r *http.Request) {
	hw := r.URL.Query().Get("hardware")
	list, err := s.store.ListScenarios(r.Context(), hw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, list)
}
```

- [ ] **Step 6: 实现 handleListAnalysis**

```go
func (s *Server) handleListAnalysis(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.ListAnalysisRuns(r.Context(), 20)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, runs)
}
```

- [ ] **Step 7: Run tests**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/central/ -v -count=1`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/central/server.go
git commit -m "feat(central): add advise/advisories/feedback/scenario/analysis API endpoints"
```

---

### Task 9: cmd/central/main.go Wiring

**Files:**
- Modify: `cmd/central/main.go`

- [ ] **Step 1: 读取现有 cmd/central/main.go**

- [ ] **Step 2: 添加 DB driver 选择 + Advisor/Analyzer 初始化**

```go
// cmd/central/main.go — 扩展

func main() {
	cfg := central.Config{
		Addr:     envOr("CENTRAL_ADDR", ":8080"),
		APIKey:   os.Getenv("CENTRAL_API_KEY"),
		DBDriver: envOr("CENTRAL_DB_DRIVER", "sqlite"),
		DBPath:   envOr("CENTRAL_DB", "central.db"),
		DBDSN:    os.Getenv("CENTRAL_DB_DSN"),
	}

	srv, err := central.New(cfg)
	if err != nil {
		slog.Error("failed to start", "error", err)
		os.Exit(1)
	}
	defer srv.Close()

	// Setup LLM client for Advisor (if configured)
	llmEndpoint := os.Getenv("CENTRAL_LLM_ENDPOINT")
	llmModel := os.Getenv("CENTRAL_LLM_MODEL")
	llmKey := os.Getenv("CENTRAL_LLM_API_KEY")
	if llmEndpoint != "" && llmKey != "" {
		llm := central.NewOpenAICompleter(llmEndpoint, llmModel, llmKey)
		srv.SetAdvisor(central.NewAdvisor(srv.Store(), llm))

		if os.Getenv("CENTRAL_ANALYZER_ENABLED") == "true" {
			analyzer := central.NewAnalyzer(srv.Store(), llm, central.DefaultAnalyzerConfig())
			srv.SetAnalyzer(analyzer)
			go analyzer.Start(context.Background())
		}
	}

	slog.Info("central server starting", "addr", cfg.Addr, "db_driver", cfg.DBDriver)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server error", "error", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 3: 添加 Server 辅助方法**

在 `server.go` 添加:

```go
func (s *Server) Store() CentralStore { return s.store }
func (s *Server) SetAdvisor(a *Advisor) { s.advisor = a }
func (s *Server) SetAnalyzer(a *Analyzer) {
	s.analyzer = a
	s.onIngest = a.OnIngest
}
```

- [ ] **Step 4: 添加 OpenAICompleter 简单实现**

在 `advisor.go` 末尾添加:

```go
// OpenAICompleter implements LLMCompleter using OpenAI-compatible API.
type OpenAICompleter struct {
	endpoint string
	model    string
	apiKey   string
	client   *http.Client
}

func NewOpenAICompleter(endpoint, model, apiKey string) *OpenAICompleter {
	return &OpenAICompleter{
		endpoint: endpoint, model: model, apiKey: apiKey,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *OpenAICompleter) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0.3,
	})
	req, _ := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("LLM status %d", resp.StatusCode)
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode LLM response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in LLM response")
	}
	return result.Choices[0].Message.Content, nil
}
```

- [ ] **Step 5: Build verification**

Run: `cd /Users/jguan/projects/AIMA && go build ./cmd/central`
Expected: BUILD SUCCESS

- [ ] **Step 6: Commit**

```bash
git add cmd/central/main.go internal/central/server.go internal/central/advisor.go
git commit -m "feat(central): wire Advisor + Analyzer in cmd/central with DB driver selection"
```

---

### Task 10: 最终验证

- [ ] **Step 1: Build both binaries**

```bash
cd /Users/jguan/projects/AIMA
go build ./cmd/aima
go build ./cmd/central
go vet ./...
```
Expected: BUILD SUCCESS, no vet warnings

- [ ] **Step 2: Run full test suite**

```bash
go test ./internal/central/ -v -count=1
go test -race ./internal/central/ -v -count=1
```
Expected: PASS, no race conditions

- [ ] **Step 3: Verify new API endpoints compile**

Run the central server briefly to confirm routes are registered:

```bash
CENTRAL_ADDR=:0 go run ./cmd/central &
sleep 1 && kill %1
```
Expected: Server starts and logs "central server starting"
