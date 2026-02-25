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
	Status           string
	DownloadProgress float64
	CreatedAt        time.Time
}

type Engine struct {
	ID        string
	Type      string
	Image     string
	Tag       string
	SizeBytes int64
	Platform  string
	Available bool
	CreatedAt time.Time
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
	if _, err := sqlDB.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := sqlDB.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	d := &DB{db: sqlDB}
	if err := d.migrate(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) migrate(ctx context.Context) error {
	// v1: system tables (models, engines, config, audit_log, knowledge_notes)
	if err := d.migrateV1(ctx); err != nil {
		return err
	}
	// v2: knowledge architecture tables (static + dynamic)
	if err := d.migrateV2(ctx); err != nil {
		return err
	}
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
    gpu_compute_cap TEXT,
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
		`INSERT INTO models (id, name, type, path, format, size_bytes, detected_arch, detected_params, status, download_progress)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Name, m.Type, m.Path, m.Format, m.SizeBytes, m.DetectedArch, m.DetectedParams, m.Status, m.DownloadProgress)
	if err != nil {
		return fmt.Errorf("insert model %s: %w", m.ID, err)
	}
	return nil
}

func (d *DB) GetModel(ctx context.Context, id string) (*Model, error) {
	m := &Model{}
	err := d.db.QueryRowContext(ctx,
		`SELECT id, name, type, path, COALESCE(format,''), COALESCE(size_bytes,0),
		        COALESCE(detected_arch,''), COALESCE(detected_params,''),
		        COALESCE(status,'registered'), COALESCE(download_progress,0), created_at
		 FROM models WHERE id = ?`, id).Scan(
		&m.ID, &m.Name, &m.Type, &m.Path, &m.Format, &m.SizeBytes,
		&m.DetectedArch, &m.DetectedParams, &m.Status, &m.DownloadProgress, &m.CreatedAt)
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
		        COALESCE(status,'registered'), COALESCE(download_progress,0), created_at
		 FROM models ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer rows.Close()
	var models []*Model
	for rows.Next() {
		m := &Model{}
		if err := rows.Scan(&m.ID, &m.Name, &m.Type, &m.Path, &m.Format, &m.SizeBytes,
			&m.DetectedArch, &m.DetectedParams, &m.Status, &m.DownloadProgress, &m.CreatedAt); err != nil {
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
		`INSERT INTO engines (id, type, image, tag, size_bytes, platform, available)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Type, e.Image, e.Tag, e.SizeBytes, e.Platform, e.Available)
	if err != nil {
		return fmt.Errorf("insert engine %s: %w", e.ID, err)
	}
	return nil
}

func (d *DB) GetEngine(ctx context.Context, id string) (*Engine, error) {
	e := &Engine{}
	err := d.db.QueryRowContext(ctx,
		`SELECT id, type, image, tag, COALESCE(size_bytes,0), COALESCE(platform,''),
		        available, created_at
		 FROM engines WHERE id = ?`, id).Scan(
		&e.ID, &e.Type, &e.Image, &e.Tag, &e.SizeBytes, &e.Platform, &e.Available, &e.CreatedAt)
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
		        available, created_at
		 FROM engines ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list engines: %w", err)
	}
	defer rows.Close()
	var engines []*Engine
	for rows.Next() {
		e := &Engine{}
		if err := rows.Scan(&e.ID, &e.Type, &e.Image, &e.Tag, &e.SizeBytes,
			&e.Platform, &e.Available, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan engine row: %w", err)
		}
		engines = append(engines, e)
	}
	return engines, rows.Err()
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

	var notes []*KnowledgeNote
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
