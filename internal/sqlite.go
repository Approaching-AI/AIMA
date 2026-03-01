package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	db *sql.DB
}

// RawDB exposes the underlying *sql.DB for packages that need direct SQL access
// (e.g., knowledge query engine).
func (d *DB) RawDB() *sql.DB {
	return d.db
}

type Model struct {
	ID               string
	Name             string
	Type             string
	Path             string
	Format           string
	SizeBytes        int64
	DetectedArch     string
	DetectedParams   string
	ModelClass       string
	TotalParams      int64
	ActiveParams     int64
	Quantization     string
	QuantSrc         string
	Status           string
	DownloadProgress float64
	CreatedAt        time.Time
}

type Engine struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	Image       string    `json:"image"` // container image name (container engines) or empty (native)
	Tag         string    `json:"tag"`   // container image tag (container engines) or empty (native)
	SizeBytes   int64     `json:"size_bytes"`
	Platform    string    `json:"platform"`
	RuntimeType string    `json:"runtime_type"` // "container" or "native"
	BinaryPath  string    `json:"binary_path"`  // path to native binary (native engines only)
	Available   bool      `json:"available"`
	CreatedAt   time.Time `json:"created_at"`
}

type KnowledgeNote struct {
	ID              string
	Title           string
	Tags            []string
	HardwareProfile string
	Model           string
	Engine          string
	Content         string
	Confidence      string
	CreatedAt       time.Time
}

type NoteFilter struct {
	HardwareProfile string
	Model           string
	Engine          string
}

type AuditEntry struct {
	AgentType     string
	ToolName      string
	Arguments     string
	ResultSummary string
}

// Configuration represents a tested Hardware×Engine×Model×Config combination.
type Configuration struct {
	ID          string
	HardwareID  string
	EngineID    string
	ModelID     string
	Slot        string
	Config      string // JSON
	ConfigHash  string
	DerivedFrom string
	Status      string
	Tags        []string
	Source      string
	DeviceID    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// BenchmarkResult stores multi-dimensional performance data for a configuration.
type BenchmarkResult struct {
	ID              string
	ConfigID        string
	Concurrency     int
	InputLenBucket  string
	OutputLenBucket string
	Modality        string
	TTFTP50ms       float64
	TTFTP95ms       float64
	TTFTP99ms       float64
	TPOTP50ms       float64
	TPOTP95ms       float64
	ThroughputTPS   float64
	QPS             float64
	VRAMUsageMiB    int
	RAMUsageMiB     int
	PowerDrawWatts  float64
	GPUUtilPct      float64
	ErrorRate       float64
	OOMOccurred     bool
	Stability       string
	DurationS       int
	SampleCount     int
	TestedAt        time.Time
	AgentModel      string
	Notes           string
}

func Open(ctx context.Context, dbPath string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	// Keep one long-lived connection so PRAGMA settings are stable and access is
	// serialized per process (SQLite is optimized for this pattern).
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	// busy_timeout is a per-connection setting that needs no lock — set it
	// first so all subsequent operations benefit from SQLite's built-in retry.
	if _, err := sqlDB.ExecContext(ctx, "PRAGMA busy_timeout=3000"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	d := &DB{db: sqlDB}
	// journal_mode=WAL requires a write lock, so it goes inside retryBusy
	// together with migrate (which uses BEGIN IMMEDIATE).
	if err := retryBusy(ctx, 8, func() error {
		if _, err := sqlDB.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
			return fmt.Errorf("set WAL mode: %w", err)
		}
		if _, err := sqlDB.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
			return fmt.Errorf("enable foreign keys: %w", err)
		}
		return d.migrate(ctx)
	}); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

func retryBusy(ctx context.Context, maxAttempts int, fn func() error) error {
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		if err := fn(); err == nil {
			return nil
		} else if !isSQLiteBusy(err) {
			return err
		} else {
			lastErr = err
		}

		delay := time.Duration(50*(i+1)) * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w (last busy error: %v)", ctx.Err(), lastErr)
		case <-timer.C:
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("sqlite busy retry exhausted")
}

func isSQLiteBusy(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sqlite_busy") || strings.Contains(msg, "database is locked")
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) migrate(ctx context.Context) error {
	// Use raw "BEGIN IMMEDIATE" instead of db.BeginTx because database/sql
	// doesn't support SQLite's IMMEDIATE lock level. Safe because
	// SetMaxOpenConns(1) guarantees all statements use the same connection.
	if _, err := d.db.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin migration lock: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = d.db.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// v1: system tables (models, engines, config, audit_log, knowledge_notes)
	if err := d.migrateV1(ctx); err != nil {
		return fmt.Errorf("migrate v1: %w", err)
	}
	// v2: knowledge architecture tables (static + dynamic)
	if err := d.migrateV2(ctx); err != nil {
		return fmt.Errorf("migrate v2: %w", err)
	}
	// v3: enhanced model metadata
	if err := d.migrateV3(ctx); err != nil {
		return fmt.Errorf("migrate v3: %w", err)
	}
	// v4: unified engine scan (container + native)
	if err := d.migrateV4(ctx); err != nil {
		return fmt.Errorf("migrate v4: %w", err)
	}
	// v5: vendor-neutral GPU fields (gpu_compute_cap → gpu_compute_id)
	if err := d.migrateV5(ctx); err != nil {
		return fmt.Errorf("migrate v5: %w", err)
	}
	// v6: rollback snapshots for agent safety guardrails
	if err := d.migrateV6(ctx); err != nil {
		return fmt.Errorf("migrate v6: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	committed = true
	return nil
}

func (d *DB) migrateV1(ctx context.Context) error {
	var version int
	_ = d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)

	if version < 1 {
		// Old table schemas may be incomplete (e.g. missing size_bytes column).
		// These are all scan caches that can be safely rebuilt.
		for _, t := range []string{"models", "engines", "knowledge_notes", "config", "audit_log"} {
			if _, err := d.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+t); err != nil {
				return fmt.Errorf("drop old table %s: %w", t, err)
			}
		}
	}

	ddl := `
CREATE TABLE IF NOT EXISTS models (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    path TEXT NOT NULL,
    format TEXT,
    size_bytes INTEGER,
    detected_arch TEXT,
    detected_params TEXT,
    status TEXT DEFAULT 'registered',
    download_progress REAL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS engines (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    image TEXT NOT NULL,
    tag TEXT NOT NULL,
    size_bytes INTEGER,
    platform TEXT,
    available BOOLEAN DEFAULT TRUE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS knowledge_notes (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    tags TEXT,
    hardware_profile TEXT,
    model TEXT,
    engine TEXT,
    content TEXT NOT NULL,
    confidence TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS config (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS audit_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_type TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    arguments TEXT,
    result_summary TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);`
	if _, err := d.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("migrate v1 schema: %w", err)
	}

	if _, err := d.db.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

func (d *DB) migrateV2(ctx context.Context) error {
	// Check if v2 migration already applied
	var count int
	err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='hardware_profiles'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check v2 migration: %w", err)
	}
	if count > 0 {
		return nil // already migrated
	}

	ddl := `
-- ====================================================================
-- Static knowledge tables (rebuilt on startup from go:embed YAML)
-- ====================================================================

CREATE TABLE hardware_profiles (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    gpu_arch TEXT NOT NULL,
    gpu_vram_mib INTEGER,
    gpu_compute_id TEXT,
    cpu_arch TEXT,
    cpu_cores INTEGER,
    ram_mib INTEGER,
    unified_memory BOOLEAN DEFAULT FALSE,
    tdp_watts INTEGER,
    power_modes TEXT,
    gpu_tools TEXT,
    raw_yaml TEXT
);
CREATE INDEX idx_hp_gpu ON hardware_profiles(gpu_arch);

CREATE TABLE engine_assets (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    version TEXT,
    image_name TEXT,
    image_tag TEXT,
    image_size_mb INTEGER,
    api_protocol TEXT,
    cold_start_s_min INTEGER,
    cold_start_s_max INTEGER,
    power_watts_min INTEGER,
    power_watts_max INTEGER,
    perf_gain_desc TEXT,
    raw_yaml TEXT
);

CREATE TABLE engine_features (
    engine_id TEXT NOT NULL REFERENCES engine_assets(id),
    feature TEXT NOT NULL,
    PRIMARY KEY (engine_id, feature)
);
CREATE INDEX idx_ef_feature ON engine_features(feature);

CREATE TABLE engine_hardware_compat (
    engine_id TEXT NOT NULL REFERENCES engine_assets(id),
    hardware_id TEXT NOT NULL REFERENCES hardware_profiles(id),
    vram_min_mib INTEGER,
    cpu_offload BOOLEAN DEFAULT FALSE,
    ssd_offload BOOLEAN DEFAULT FALSE,
    npu_offload BOOLEAN DEFAULT FALSE,
    min_gpu_mem_mib INTEGER,
    recommended_cores_pct INTEGER,
    PRIMARY KEY (engine_id, hardware_id)
);

CREATE TABLE model_assets (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    family TEXT,
    param_count TEXT,
    formats TEXT,
    sources TEXT,
    raw_yaml TEXT
);
CREATE INDEX idx_ma_type ON model_assets(type);
CREATE INDEX idx_ma_family ON model_assets(family);

CREATE TABLE model_variants (
    id TEXT PRIMARY KEY,
    model_id TEXT NOT NULL REFERENCES model_assets(id),
    hardware_id TEXT NOT NULL REFERENCES hardware_profiles(id),
    engine_type TEXT NOT NULL,
    format TEXT,
    default_config TEXT NOT NULL,
    expected_perf TEXT,
    vram_min_mib INTEGER
);
CREATE INDEX idx_mv_lookup ON model_variants(model_id, hardware_id, engine_type);

CREATE TABLE partition_strategies (
    id TEXT PRIMARY KEY,
    hardware_id TEXT NOT NULL,
    workload_pattern TEXT NOT NULL,
    slots TEXT NOT NULL,
    raw_yaml TEXT
);

-- ====================================================================
-- Dynamic knowledge tables (Agent exploration, persisted across restarts)
-- ====================================================================

CREATE TABLE configurations (
    id TEXT PRIMARY KEY,
    hardware_id TEXT NOT NULL,
    engine_id TEXT NOT NULL,
    model_id TEXT NOT NULL,
    partition_slot TEXT,
    config TEXT NOT NULL,
    config_hash TEXT NOT NULL,
    derived_from TEXT REFERENCES configurations(id),
    status TEXT DEFAULT 'experiment',
    tags TEXT,
    source TEXT DEFAULT 'local',
    device_id TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_cfg_4d ON configurations(hardware_id, engine_id, model_id);
CREATE INDEX idx_cfg_status ON configurations(status);
CREATE INDEX idx_cfg_hash ON configurations(config_hash);

CREATE TABLE benchmark_results (
    id TEXT PRIMARY KEY,
    config_id TEXT NOT NULL REFERENCES configurations(id),
    concurrency INTEGER NOT NULL DEFAULT 1,
    input_len_bucket TEXT,
    output_len_bucket TEXT,
    modality TEXT DEFAULT 'text',
    ttft_ms_p50 REAL,
    ttft_ms_p95 REAL,
    ttft_ms_p99 REAL,
    tpot_ms_p50 REAL,
    tpot_ms_p95 REAL,
    throughput_tps REAL,
    qps REAL,
    vram_usage_mib INTEGER,
    ram_usage_mib INTEGER,
    power_draw_watts REAL,
    gpu_utilization_pct REAL,
    error_rate REAL DEFAULT 0,
    oom_occurred BOOLEAN DEFAULT FALSE,
    stability TEXT,
    duration_s INTEGER,
    sample_count INTEGER,
    tested_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    agent_model TEXT,
    notes TEXT
);
CREATE INDEX idx_br_config ON benchmark_results(config_id);
CREATE INDEX idx_br_perf ON benchmark_results(throughput_tps DESC);
CREATE INDEX idx_br_load ON benchmark_results(concurrency, input_len_bucket);

CREATE TABLE perf_vectors (
    config_id TEXT PRIMARY KEY REFERENCES configurations(id),
    norm_ttft_p95 REAL,
    norm_tpot_p95 REAL,
    norm_throughput REAL,
    norm_qps REAL,
    norm_vram REAL,
    norm_power REAL,
    avg_throughput REAL,
    avg_ttft_p95 REAL,
    avg_vram_mib REAL,
    benchmark_count INTEGER,
    updated_at DATETIME
);`

	_, err = d.db.ExecContext(ctx, ddl)
	if err != nil {
		return fmt.Errorf("migrate v2 schema: %w", err)
	}
	return nil
}

func (d *DB) migrateV3(ctx context.Context) error {
	var version int
	_ = d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if version >= 3 {
		return nil
	}

	// Add new columns to models table for enhanced metadata
	// Use ALTER TABLE with IF NOT EXISTS pattern by checking column existence first
	var count int
	err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('models') WHERE name='model_class'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check model_class column: %w", err)
	}
	if count == 0 {
		_, err = d.db.ExecContext(ctx, `ALTER TABLE models ADD COLUMN model_class TEXT DEFAULT ''`)
		if err != nil {
			return fmt.Errorf("add model_class column: %w", err)
		}
	}

	err = d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('models') WHERE name='total_params'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check total_params column: %w", err)
	}
	if count == 0 {
		_, err = d.db.ExecContext(ctx, `ALTER TABLE models ADD COLUMN total_params INTEGER DEFAULT 0`)
		if err != nil {
			return fmt.Errorf("add total_params column: %w", err)
		}
	}

	err = d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('models') WHERE name='active_params'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check active_params column: %w", err)
	}
	if count == 0 {
		_, err = d.db.ExecContext(ctx, `ALTER TABLE models ADD COLUMN active_params INTEGER DEFAULT 0`)
		if err != nil {
			return fmt.Errorf("add active_params column: %w", err)
		}
	}

	err = d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('models') WHERE name='quantization'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check quantization column: %w", err)
	}
	if count == 0 {
		_, err = d.db.ExecContext(ctx, `ALTER TABLE models ADD COLUMN quantization TEXT DEFAULT ''`)
		if err != nil {
			return fmt.Errorf("add quantization column: %w", err)
		}
	}

	err = d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('models') WHERE name='quant_src'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check quant_src column: %w", err)
	}
	if count == 0 {
		_, err = d.db.ExecContext(ctx, `ALTER TABLE models ADD COLUMN quant_src TEXT DEFAULT ''`)
		if err != nil {
			return fmt.Errorf("add quant_src column: %w", err)
		}
	}

	if _, err := d.db.ExecContext(ctx, "PRAGMA user_version = 3"); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

func (d *DB) migrateV4(ctx context.Context) error {
	var version int
	_ = d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if version >= 4 {
		return nil
	}

	// Add runtime_type column to engines table
	var count int
	err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('engines') WHERE name='runtime_type'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check runtime_type column: %w", err)
	}
	if count == 0 {
		_, err = d.db.ExecContext(ctx, `ALTER TABLE engines ADD COLUMN runtime_type TEXT DEFAULT 'container'`)
		if err != nil {
			return fmt.Errorf("add runtime_type column: %w", err)
		}
	}

	// Add binary_path column to engines table
	err = d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('engines') WHERE name='binary_path'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check binary_path column: %w", err)
	}
	if count == 0 {
		_, err = d.db.ExecContext(ctx, `ALTER TABLE engines ADD COLUMN binary_path TEXT`)
		if err != nil {
			return fmt.Errorf("add binary_path column: %w", err)
		}
	}

	if _, err := d.db.ExecContext(ctx, "PRAGMA user_version = 4"); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

func (d *DB) migrateV5(ctx context.Context) error {
	var version int
	_ = d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if version >= 5 {
		return nil
	}

	// Rename gpu_compute_cap → gpu_compute_id (vendor-neutral)
	var count int
	err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('hardware_profiles') WHERE name='gpu_compute_cap'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check gpu_compute_cap column: %w", err)
	}
	if count > 0 {
		_, err = d.db.ExecContext(ctx, `ALTER TABLE hardware_profiles RENAME COLUMN gpu_compute_cap TO gpu_compute_id`)
		if err != nil {
			return fmt.Errorf("rename gpu_compute_cap: %w", err)
		}
	}

	if _, err := d.db.ExecContext(ctx, "PRAGMA user_version = 5"); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

func (d *DB) migrateV6(ctx context.Context) error {
	var version int
	_ = d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if version >= 6 {
		return nil
	}

	ddl := `CREATE TABLE IF NOT EXISTS rollback_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tool_name TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_name TEXT NOT NULL,
    snapshot TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);`
	if _, err := d.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create rollback_snapshots table: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, "PRAGMA user_version = 6"); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

// RollbackSnapshot stores pre-deletion state for agent safety recovery.
type RollbackSnapshot struct {
	ID           int64     `json:"id"`
	ToolName     string    `json:"tool_name"`
	ResourceType string    `json:"resource_type"`
	ResourceName string    `json:"resource_name"`
	Snapshot     string    `json:"snapshot"`
	CreatedAt    time.Time `json:"created_at"`
}

// SaveSnapshot writes a rollback snapshot and prunes old entries (keeps last 10).
func (d *DB) SaveSnapshot(ctx context.Context, s *RollbackSnapshot) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO rollback_snapshots (tool_name, resource_type, resource_name, snapshot) VALUES (?, ?, ?, ?)`,
		s.ToolName, s.ResourceType, s.ResourceName, s.Snapshot)
	if err != nil {
		return fmt.Errorf("save snapshot for %s: %w", s.ResourceName, err)
	}
	// Prune: keep only the 10 most recent
	_, _ = d.db.ExecContext(ctx,
		`DELETE FROM rollback_snapshots WHERE id NOT IN (SELECT id FROM rollback_snapshots ORDER BY id DESC LIMIT 10)`)
	return nil
}

// ListSnapshots returns the most recent rollback snapshots (up to 10).
func (d *DB) ListSnapshots(ctx context.Context) ([]*RollbackSnapshot, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, tool_name, resource_type, resource_name, snapshot, created_at
		 FROM rollback_snapshots ORDER BY id DESC LIMIT 10`)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	defer rows.Close()
	var snapshots []*RollbackSnapshot
	for rows.Next() {
		s := &RollbackSnapshot{}
		if err := rows.Scan(&s.ID, &s.ToolName, &s.ResourceType, &s.ResourceName, &s.Snapshot, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan snapshot row: %w", err)
		}
		snapshots = append(snapshots, s)
	}
	return snapshots, rows.Err()
}

// GetSnapshot returns a single rollback snapshot by ID.
func (d *DB) GetSnapshot(ctx context.Context, id int64) (*RollbackSnapshot, error) {
	s := &RollbackSnapshot{}
	err := d.db.QueryRowContext(ctx,
		`SELECT id, tool_name, resource_type, resource_name, snapshot, created_at
		 FROM rollback_snapshots WHERE id = ?`, id).Scan(
		&s.ID, &s.ToolName, &s.ResourceType, &s.ResourceName, &s.Snapshot, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("snapshot %d not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get snapshot %d: %w", id, err)
	}
	return s, nil
}

// ClearStaticKnowledge deletes all rows from static knowledge tables.
// Called on startup before reloading from go:embed YAML.
func (d *DB) ClearStaticKnowledge(ctx context.Context) error {
	// Order matters: child tables first (foreign keys)
	tables := []string{
		"engine_hardware_compat",
		"engine_features",
		"model_variants",
		"partition_strategies",
		"engine_assets",
		"model_assets",
		"hardware_profiles",
	}
	for _, t := range tables {
		if _, err := d.db.ExecContext(ctx, "DELETE FROM "+t); err != nil {
			return fmt.Errorf("clear %s: %w", t, err)
		}
	}
	return nil
}

// Analyze updates SQLite's index statistics for the query optimizer.
func (d *DB) Analyze(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "ANALYZE")
	if err != nil {
		return fmt.Errorf("analyze: %w", err)
	}
	return nil
}

// Models CRUD

func (d *DB) InsertModel(ctx context.Context, m *Model) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO models (id, name, type, path, format, size_bytes, detected_arch, detected_params,
		                    model_class, total_params, active_params, quantization, quant_src, status, download_progress)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Name, m.Type, m.Path, m.Format, m.SizeBytes, m.DetectedArch, m.DetectedParams,
		m.ModelClass, m.TotalParams, m.ActiveParams, m.Quantization, m.QuantSrc, m.Status, m.DownloadProgress)
	if err != nil {
		return fmt.Errorf("insert model %s: %w", m.ID, err)
	}
	return nil
}

// UpsertScannedModel inserts a new model or updates metadata of an existing one.
// If a model with the same path exists, update that record instead of creating a duplicate.
// Status defaults to 'registered' if not set.
func (d *DB) UpsertScannedModel(ctx context.Context, m *Model) error {
	// First check if a model with this path already exists
	var existingID string
	var existingStatus string
	err := d.db.QueryRowContext(ctx, `SELECT id, COALESCE(status,'registered') FROM models WHERE path = ?`, m.Path).Scan(&existingID, &existingStatus)
	if err == nil {
		// Existing model found with same path, use its ID for update
		m.ID = existingID
		// Preserve existing status if new status is empty
		if m.Status == "" {
			m.Status = existingStatus
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check existing model by path %s: %w", m.Path, err)
	}
	// else: no existing model, use the scanned hash ID

	// Default status to 'registered' if not set
	if m.Status == "" {
		m.Status = "registered"
	}

	_, err = d.db.ExecContext(ctx,
		`INSERT INTO models (id, name, type, path, format, size_bytes, detected_arch, detected_params,
		                    model_class, total_params, active_params, quantization, quant_src, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name=excluded.name, type=excluded.type, path=excluded.path,
		   format=excluded.format, size_bytes=excluded.size_bytes,
		   detected_arch=excluded.detected_arch, detected_params=excluded.detected_params,
		   model_class=excluded.model_class, total_params=excluded.total_params,
		   active_params=excluded.active_params, quantization=excluded.quantization,
		   quant_src=excluded.quant_src, status=excluded.status`,
		m.ID, m.Name, m.Type, m.Path, m.Format, m.SizeBytes, m.DetectedArch, m.DetectedParams,
		m.ModelClass, m.TotalParams, m.ActiveParams, m.Quantization, m.QuantSrc, m.Status)
	if err != nil {
		return fmt.Errorf("upsert scanned model %s: %w", m.ID, err)
	}
	return nil
}

func (d *DB) GetModel(ctx context.Context, id string) (*Model, error) {
	m := &Model{}
	err := d.db.QueryRowContext(ctx,
		`SELECT id, name, type, path, COALESCE(format,''), COALESCE(size_bytes,0),
		        COALESCE(detected_arch,''), COALESCE(detected_params,''),
		        COALESCE(model_class,''), COALESCE(total_params,0), COALESCE(active_params,0),
		        COALESCE(quantization,''), COALESCE(quant_src,''),
		        COALESCE(status,'registered'), COALESCE(download_progress,0), created_at
		 FROM models WHERE id = ? OR name = ?
		 ORDER BY CASE WHEN id = ? THEN 0 ELSE 1 END
		 LIMIT 1`, id, id, id).Scan(
		&m.ID, &m.Name, &m.Type, &m.Path, &m.Format, &m.SizeBytes,
		&m.DetectedArch, &m.DetectedParams, &m.ModelClass, &m.TotalParams, &m.ActiveParams,
		&m.Quantization, &m.QuantSrc, &m.Status, &m.DownloadProgress, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("model %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get model %s: %w", id, err)
	}
	return m, nil
}

func (d *DB) ListModels(ctx context.Context) ([]*Model, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, name, type, path, COALESCE(format,''), COALESCE(size_bytes,0),
		        COALESCE(detected_arch,''), COALESCE(detected_params,''),
		        COALESCE(model_class,''), COALESCE(total_params,0), COALESCE(active_params,0),
		        COALESCE(quantization,''), COALESCE(quant_src,''),
		        COALESCE(status,'registered'), COALESCE(download_progress,0), created_at
		 FROM models ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer rows.Close()
	models := make([]*Model, 0)
	for rows.Next() {
		m := &Model{}
		if err := rows.Scan(&m.ID, &m.Name, &m.Type, &m.Path, &m.Format, &m.SizeBytes,
			&m.DetectedArch, &m.DetectedParams, &m.ModelClass, &m.TotalParams, &m.ActiveParams,
			&m.Quantization, &m.QuantSrc, &m.Status, &m.DownloadProgress, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan model row: %w", err)
		}
		models = append(models, m)
	}
	return models, rows.Err()
}

func (d *DB) UpdateModelStatus(ctx context.Context, id, status string) error {
	res, err := d.db.ExecContext(ctx, `UPDATE models SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return fmt.Errorf("update model status %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("model %s not found", id)
	}
	return nil
}

// FindModelByName searches for a model by name with prioritized matching:
// 1. Exact name  2. Case-insensitive  3. Substring match
func (d *DB) FindModelByName(ctx context.Context, name string) (*Model, error) {
	queries := []string{
		`SELECT id, name, type, path, COALESCE(format,''), COALESCE(size_bytes,0),
		        COALESCE(detected_arch,''), COALESCE(detected_params,''),
		        COALESCE(model_class,''), COALESCE(total_params,0), COALESCE(active_params,0),
		        COALESCE(quantization,''), COALESCE(quant_src,''),
		        COALESCE(status,'registered'), COALESCE(download_progress,0), created_at
		 FROM models WHERE name = ?`,
		`SELECT id, name, type, path, COALESCE(format,''), COALESCE(size_bytes,0),
		        COALESCE(detected_arch,''), COALESCE(detected_params,''),
		        COALESCE(model_class,''), COALESCE(total_params,0), COALESCE(active_params,0),
		        COALESCE(quantization,''), COALESCE(quant_src,''),
		        COALESCE(status,'registered'), COALESCE(download_progress,0), created_at
		 FROM models WHERE LOWER(name) = LOWER(?)`,
		`SELECT id, name, type, path, COALESCE(format,''), COALESCE(size_bytes,0),
		        COALESCE(detected_arch,''), COALESCE(detected_params,''),
		        COALESCE(model_class,''), COALESCE(total_params,0), COALESCE(active_params,0),
		        COALESCE(quantization,''), COALESCE(quant_src,''),
		        COALESCE(status,'registered'), COALESCE(download_progress,0), created_at
		 FROM models WHERE LOWER(name) LIKE '%' || LOWER(?) || '%'`,
	}
	for _, q := range queries {
		m := &Model{}
		err := d.db.QueryRowContext(ctx, q, name).Scan(
			&m.ID, &m.Name, &m.Type, &m.Path, &m.Format, &m.SizeBytes,
			&m.DetectedArch, &m.DetectedParams, &m.ModelClass, &m.TotalParams, &m.ActiveParams,
			&m.Quantization, &m.QuantSrc, &m.Status, &m.DownloadProgress, &m.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("find model by name %q: %w", name, err)
		}
		return m, nil
	}
	return nil, fmt.Errorf("model %q not found", name)
}

func (d *DB) DeleteModel(ctx context.Context, id string) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM models WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete model %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("model %s not found", id)
	}
	return nil
}

// Engines CRUD

func (d *DB) InsertEngine(ctx context.Context, e *Engine) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO engines (id, type, image, tag, size_bytes, platform, runtime_type, binary_path, available)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Type, e.Image, e.Tag, e.SizeBytes, e.Platform, e.RuntimeType, e.BinaryPath, e.Available)
	if err != nil {
		return fmt.Errorf("insert engine %s: %w", e.ID, err)
	}
	return nil
}

// UpsertScannedEngine inserts a new engine or updates an existing one.
func (d *DB) UpsertScannedEngine(ctx context.Context, e *Engine) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO engines (id, type, image, tag, size_bytes, platform, runtime_type, binary_path, available)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   type=excluded.type, image=excluded.image, tag=excluded.tag,
		   size_bytes=excluded.size_bytes, platform=excluded.platform,
		   runtime_type=excluded.runtime_type, binary_path=excluded.binary_path,
		   available=excluded.available`,
		e.ID, e.Type, e.Image, e.Tag, e.SizeBytes, e.Platform, e.RuntimeType, e.BinaryPath, e.Available)
	if err != nil {
		return fmt.Errorf("upsert scanned engine %s: %w", e.ID, err)
	}
	return nil
}

func (d *DB) GetEngine(ctx context.Context, id string) (*Engine, error) {
	e := &Engine{}
	err := d.db.QueryRowContext(ctx,
		`SELECT id, type, image, tag, COALESCE(size_bytes,0), COALESCE(platform,''),
		        COALESCE(runtime_type,'container'), COALESCE(binary_path,''),
		        available, created_at
		 FROM engines WHERE id = ?`, id).Scan(
		&e.ID, &e.Type, &e.Image, &e.Tag, &e.SizeBytes, &e.Platform,
		&e.RuntimeType, &e.BinaryPath, &e.Available, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("engine %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get engine %s: %w", id, err)
	}
	return e, nil
}

func (d *DB) ListEngines(ctx context.Context) ([]*Engine, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, type, image, tag, COALESCE(size_bytes,0), COALESCE(platform,''),
		        COALESCE(runtime_type,'container'), COALESCE(binary_path,''),
		        available, created_at
		 FROM engines WHERE available = 1 ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list engines: %w", err)
	}
	defer rows.Close()
	engines := make([]*Engine, 0)
	for rows.Next() {
		e := &Engine{}
		if err := rows.Scan(&e.ID, &e.Type, &e.Image, &e.Tag, &e.SizeBytes,
			&e.Platform, &e.RuntimeType, &e.BinaryPath, &e.Available, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan engine row: %w", err)
		}
		engines = append(engines, e)
	}
	return engines, rows.Err()
}

// MarkEnginesUnavailableExcept sets available=false for all engines whose ID is not in keepIDs.
// Called after a full scan to clean stale entries (deleted images, renamed patterns, etc.).
func (d *DB) MarkEnginesUnavailableExcept(ctx context.Context, keepIDs []string) error {
	if len(keepIDs) == 0 {
		// No scan results — don't wipe everything (might be a permission issue)
		return nil
	}
	placeholders := make([]string, len(keepIDs))
	args := make([]any, len(keepIDs))
	for i, id := range keepIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`UPDATE engines SET available = 0 WHERE id NOT IN (%s)`,
		strings.Join(placeholders, ","))
	_, err := d.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("mark stale engines unavailable: %w", err)
	}
	return nil
}

func (d *DB) DeleteEngine(ctx context.Context, id string) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM engines WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete engine %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("engine %s not found", id)
	}
	return nil
}

// Knowledge Notes CRUD

func (d *DB) InsertNote(ctx context.Context, n *KnowledgeNote) error {
	tagsJSON, err := json.Marshal(n.Tags)
	if err != nil {
		return fmt.Errorf("marshal tags for note %s: %w", n.ID, err)
	}
	_, err = d.db.ExecContext(ctx,
		`INSERT INTO knowledge_notes (id, title, tags, hardware_profile, model, engine, content, confidence)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.Title, string(tagsJSON), n.HardwareProfile, n.Model, n.Engine, n.Content, n.Confidence)
	if err != nil {
		return fmt.Errorf("insert note %s: %w", n.ID, err)
	}
	return nil
}

func (d *DB) SearchNotes(ctx context.Context, filter NoteFilter) ([]*KnowledgeNote, error) {
	query := `SELECT id, title, COALESCE(tags,'[]'), COALESCE(hardware_profile,''),
	                 COALESCE(model,''), COALESCE(engine,''), content,
	                 COALESCE(confidence,''), created_at
	          FROM knowledge_notes WHERE 1=1`
	var args []any

	if filter.HardwareProfile != "" {
		query += " AND hardware_profile = ?"
		args = append(args, filter.HardwareProfile)
	}
	if filter.Model != "" {
		query += " AND model = ?"
		args = append(args, filter.Model)
	}
	if filter.Engine != "" {
		query += " AND engine = ?"
		args = append(args, filter.Engine)
	}
	query += " ORDER BY created_at DESC"

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search notes: %w", err)
	}
	defer rows.Close()

	notes := make([]*KnowledgeNote, 0)
	for rows.Next() {
		n := &KnowledgeNote{}
		var tagsStr string
		if err := rows.Scan(&n.ID, &n.Title, &tagsStr, &n.HardwareProfile,
			&n.Model, &n.Engine, &n.Content, &n.Confidence, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan note row: %w", err)
		}
		if err := json.Unmarshal([]byte(tagsStr), &n.Tags); err != nil {
			n.Tags = splitTags(tagsStr)
		}
		notes = append(notes, n)
	}
	return notes, rows.Err()
}

func splitTags(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	tags := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

func (d *DB) DeleteNote(ctx context.Context, id string) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM knowledge_notes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete note %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("note %s not found", id)
	}
	return nil
}

// UpdateConfigStatus transitions a configuration's status (e.g., experiment → golden).
func (d *DB) UpdateConfigStatus(ctx context.Context, configID, status string) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE configurations SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, configID)
	if err != nil {
		return fmt.Errorf("update config status %s: %w", configID, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("configuration %q not found", configID)
	}
	return nil
}

// Configurations CRUD

func (d *DB) InsertConfiguration(ctx context.Context, c *Configuration) error {
	tagsJSON, _ := json.Marshal(c.Tags)
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO configurations (id, hardware_id, engine_id, model_id, partition_slot, config, config_hash, derived_from, status, tags, source, device_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.HardwareID, c.EngineID, c.ModelID, c.Slot, c.Config, c.ConfigHash,
		nullStr(c.DerivedFrom), c.Status, string(tagsJSON), c.Source, c.DeviceID)
	if err != nil {
		return fmt.Errorf("insert configuration %s: %w", c.ID, err)
	}
	return nil
}

func (d *DB) GetConfiguration(ctx context.Context, id string) (*Configuration, error) {
	c := &Configuration{}
	var tagsStr, derivedFrom sql.NullString
	err := d.db.QueryRowContext(ctx,
		`SELECT id, hardware_id, engine_id, model_id, COALESCE(partition_slot,''),
		        config, config_hash, derived_from, COALESCE(status,'experiment'),
		        COALESCE(tags,'[]'), COALESCE(source,'local'), COALESCE(device_id,''),
		        created_at, updated_at
		 FROM configurations WHERE id = ?`, id).Scan(
		&c.ID, &c.HardwareID, &c.EngineID, &c.ModelID, &c.Slot,
		&c.Config, &c.ConfigHash, &derivedFrom, &c.Status,
		&tagsStr, &c.Source, &c.DeviceID, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("configuration %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get configuration %s: %w", id, err)
	}
	if derivedFrom.Valid {
		c.DerivedFrom = derivedFrom.String
	}
	_ = json.Unmarshal([]byte(tagsStr.String), &c.Tags)
	return c, nil
}

// FindConfigByHash returns a configuration matching the given config_hash, or nil if not found.
func (d *DB) FindConfigByHash(ctx context.Context, hash string) (*Configuration, error) {
	c := &Configuration{}
	var tagsStr, derivedFrom sql.NullString
	err := d.db.QueryRowContext(ctx,
		`SELECT id, hardware_id, engine_id, model_id, COALESCE(partition_slot,''),
		        config, config_hash, derived_from, COALESCE(status,'experiment'),
		        COALESCE(tags,'[]'), COALESCE(source,'local'), COALESCE(device_id,''),
		        created_at, updated_at
		 FROM configurations WHERE config_hash = ?`, hash).Scan(
		&c.ID, &c.HardwareID, &c.EngineID, &c.ModelID, &c.Slot,
		&c.Config, &c.ConfigHash, &derivedFrom, &c.Status,
		&tagsStr, &c.Source, &c.DeviceID, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find config by hash: %w", err)
	}
	if derivedFrom.Valid {
		c.DerivedFrom = derivedFrom.String
	}
	_ = json.Unmarshal([]byte(tagsStr.String), &c.Tags)
	return c, nil
}

func (d *DB) InsertBenchmarkResult(ctx context.Context, b *BenchmarkResult) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO benchmark_results (id, config_id, concurrency, input_len_bucket, output_len_bucket, modality,
		    ttft_ms_p50, ttft_ms_p95, ttft_ms_p99, tpot_ms_p50, tpot_ms_p95,
		    throughput_tps, qps, vram_usage_mib, ram_usage_mib, power_draw_watts, gpu_utilization_pct,
		    error_rate, oom_occurred, stability, duration_s, sample_count, agent_model, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.ConfigID, b.Concurrency, b.InputLenBucket, b.OutputLenBucket, b.Modality,
		b.TTFTP50ms, b.TTFTP95ms, b.TTFTP99ms, b.TPOTP50ms, b.TPOTP95ms,
		b.ThroughputTPS, b.QPS, b.VRAMUsageMiB, b.RAMUsageMiB, b.PowerDrawWatts, b.GPUUtilPct,
		b.ErrorRate, b.OOMOccurred, b.Stability, b.DurationS, b.SampleCount, b.AgentModel, b.Notes)
	if err != nil {
		return fmt.Errorf("insert benchmark %s: %w", b.ID, err)
	}
	return nil
}

// Config

func (d *DB) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := d.db.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("config key %q not found", key)
	}
	if err != nil {
		return "", fmt.Errorf("get config %q: %w", key, err)
	}
	return value, nil
}

func (d *DB) SetConfig(ctx context.Context, key, value string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO config (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		key, value)
	if err != nil {
		return fmt.Errorf("set config %q: %w", key, err)
	}
	return nil
}

// Audit

func (d *DB) LogAction(ctx context.Context, entry *AuditEntry) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO audit_log (agent_type, tool_name, arguments, result_summary) VALUES (?, ?, ?, ?)`,
		entry.AgentType, entry.ToolName, entry.Arguments, entry.ResultSummary)
	if err != nil {
		return fmt.Errorf("log action %s: %w", entry.ToolName, err)
	}
	return nil
}

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
