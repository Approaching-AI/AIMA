package state

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	db *sql.DB
}

type EngineExecutionHints struct {
	CPUOffload bool `json:"cpu_offload"`
	SSDOffload bool `json:"ssd_offload"`
	NPUOffload bool `json:"npu_offload"`
}

// RawDB exposes the underlying *sql.DB for packages that need direct SQL access
// (e.g., knowledge query engine).
func (d *DB) RawDB() *sql.DB {
	return d.db
}

type Model struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Type             string    `json:"type"`
	Path             string    `json:"path"`
	Format           string    `json:"format"`
	SizeBytes        int64     `json:"size_bytes"`
	DetectedArch     string    `json:"detected_arch"`
	DetectedParams   string    `json:"detected_params"`
	ModelClass       string    `json:"model_class"`
	TotalParams      int64     `json:"total_params"`
	ActiveParams     int64     `json:"active_params"`
	Quantization     string    `json:"quantization"`
	QuantSrc         string    `json:"quant_src"`
	Status           string    `json:"status"`
	DownloadProgress float64   `json:"download_progress"`
	CreatedAt        time.Time `json:"created_at"`
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
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	Tags            []string  `json:"tags"`
	HardwareProfile string    `json:"hardware_profile"`
	Model           string    `json:"model"`
	Engine          string    `json:"engine"`
	Content         string    `json:"content"`
	Confidence      string    `json:"confidence"`
	CreatedAt       time.Time `json:"created_at"`
}

type NoteFilter struct {
	HardwareProfile string `json:"hardware_profile"`
	Model           string `json:"model"`
	Engine          string `json:"engine"`
}

type AuditEntry struct {
	AgentType     string `json:"agent_type"`
	ToolName      string `json:"tool_name"`
	Arguments     string `json:"arguments"`
	ResultSummary string `json:"result_summary"`
}

// Configuration represents a tested Hardware×Engine×Model×Config combination.
type Configuration struct {
	ID          string    `json:"id"`
	HardwareID  string    `json:"hardware_id"`
	EngineID    string    `json:"engine_id"`
	ModelID     string    `json:"model_id"`
	Slot        string    `json:"slot"`
	Config      string    `json:"config"` // JSON
	ConfigHash  string    `json:"config_hash"`
	DerivedFrom string    `json:"derived_from"`
	Status      string    `json:"status"`
	Tags        []string  `json:"tags"`
	Source      string    `json:"source"`
	DeviceID    string    `json:"device_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// BenchmarkResult stores multi-dimensional performance data for a configuration.
type BenchmarkResult struct {
	ID              string    `json:"id"`
	ConfigID        string    `json:"config_id"`
	Concurrency     int       `json:"concurrency"`
	InputLenBucket  string    `json:"input_len_bucket"`
	OutputLenBucket string    `json:"output_len_bucket"`
	Modality        string    `json:"modality"`
	TTFTP50ms       float64   `json:"ttft_p50_ms"`
	TTFTP95ms       float64   `json:"ttft_p95_ms"`
	TTFTP99ms       float64   `json:"ttft_p99_ms"`
	TPOTP50ms       float64   `json:"tpot_p50_ms"`
	TPOTP95ms       float64   `json:"tpot_p95_ms"`
	ThroughputTPS   float64   `json:"throughput_tps"`
	QPS             float64   `json:"qps"`
	VRAMUsageMiB    int       `json:"vram_usage_mib"`
	RAMUsageMiB     int       `json:"ram_usage_mib"`
	PowerDrawWatts  float64   `json:"power_draw_watts"`
	GPUUtilPct      float64   `json:"gpu_util_pct"`
	CPUUsagePct     float64   `json:"cpu_usage_pct"`
	ErrorRate       float64   `json:"error_rate"`
	OOMOccurred     bool      `json:"oom_occurred"`
	Stability       string    `json:"stability"`
	DurationS       int       `json:"duration_s"`
	SampleCount     int       `json:"sample_count"`
	TestedAt        time.Time `json:"tested_at"`
	AgentModel      string    `json:"agent_model"`
	Notes           string    `json:"notes"`

	// TTS/ASR shared
	RTFP50           *float64 `json:"rtf_p50,omitempty"`
	RTFP95           *float64 `json:"rtf_p95,omitempty"`
	RTFMean          *float64 `json:"rtf_mean,omitempty"`

	// TTS
	TTFAP50ms        *float64 `json:"ttfa_p50_ms,omitempty"`
	TTFAP95ms        *float64 `json:"ttfa_p95_ms,omitempty"`
	AudioThroughput  *float64 `json:"audio_throughput,omitempty"`
	AvgInputChars    *int     `json:"avg_input_chars,omitempty"`
	AvgAudioDurationS *float64 `json:"avg_audio_duration_s,omitempty"`

	// ASR
	ASRThroughput    *float64 `json:"asr_throughput,omitempty"`
	AvgInputAudioS   *float64 `json:"avg_input_audio_s,omitempty"`
	AvgOutputChars   *int     `json:"avg_output_chars,omitempty"`

	// T2I
	LatencyP50ms     *float64 `json:"latency_p50_ms,omitempty"`
	LatencyP95ms     *float64 `json:"latency_p95_ms,omitempty"`
	LatencyP99ms     *float64 `json:"latency_p99_ms,omitempty"`
	ImagesPerSec     *float64 `json:"images_per_sec,omitempty"`
	AvgSteps         *int     `json:"avg_steps,omitempty"`
	ImageWidth       *int     `json:"image_width,omitempty"`
	ImageHeight      *int     `json:"image_height,omitempty"`

	// T2V
	VideoLatencyP50s  *float64 `json:"video_latency_p50_s,omitempty"`
	VideoLatencyP95s  *float64 `json:"video_latency_p95_s,omitempty"`
	VideosPerHour     *float64 `json:"videos_per_hour,omitempty"`
	AvgVideoDurationS *float64 `json:"avg_video_duration_s,omitempty"`
	AvgFrames         *int     `json:"avg_frames,omitempty"`
	VideoFPS          *int     `json:"video_fps,omitempty"`
	VideoWidth        *int     `json:"video_width,omitempty"`
	VideoHeight       *int     `json:"video_height,omitempty"`
	VideoSteps        *int     `json:"video_steps,omitempty"`
}

type ExplorationRun struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Goal         string    `json:"goal"`
	RequestedBy  string    `json:"requested_by"`
	Executor     string    `json:"executor"`
	Planner      string    `json:"planner"`
	Status       string    `json:"status"`
	HardwareID   string    `json:"hardware_id,omitempty"`
	EngineID     string    `json:"engine_id,omitempty"`
	ModelID      string    `json:"model_id,omitempty"`
	SourceRef    string    `json:"source_ref,omitempty"`
	ApprovalMode string    `json:"approval_mode"`
	ApprovedAt   time.Time `json:"approved_at,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	CompletedAt  time.Time `json:"completed_at,omitempty"`
	Error        string    `json:"error,omitempty"`
	PlanJSON     string    `json:"plan_json"`
	SummaryJSON  string    `json:"summary_json,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type OpenQuestion struct {
	ID           string    `json:"id"`
	SourceAsset  string    `json:"source_asset"`
	Question     string    `json:"question"`
	TestCommand  string    `json:"test_command,omitempty"`
	Expected     string    `json:"expected,omitempty"`
	Status       string    `json:"status"`
	ActualResult string    `json:"actual_result,omitempty"`
	TestedAt     time.Time `json:"tested_at,omitempty"`
	Hardware     string    `json:"hardware,omitempty"`
}

type ExplorationEvent struct {
	ID           int64     `json:"id"`
	RunID        string    `json:"run_id"`
	StepIndex    int       `json:"step_index"`
	StepKind     string    `json:"step_kind"`
	Status       string    `json:"status"`
	ToolName     string    `json:"tool_name,omitempty"`
	RequestJSON  string    `json:"request_json,omitempty"`
	ResponseJSON string    `json:"response_json,omitempty"`
	ArtifactType string    `json:"artifact_type,omitempty"`
	ArtifactID   string    `json:"artifact_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type ExplorationPlanRow struct {
	ID          string
	Tier        int
	Trigger     string
	Status      string // "active", "paused", "completed", "archived"
	PlanJSON    string
	Progress    int
	Total       int
	CreatedAt   time.Time
	CompletedAt *time.Time
	SummaryJSON string
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
	// v7: patrol alerts, power samples, validation results, tuning sessions
	if err := d.migrateV7(ctx); err != nil {
		return fmt.Errorf("migrate v7: %w", err)
	}
	// v8: exploration runs and events
	if err := d.migrateV8(ctx); err != nil {
		return fmt.Errorf("migrate v8: %w", err)
	}
	// v9: model_variants.gpu_count_min for multi-GPU variant selection
	if err := d.migrateV9(ctx); err != nil {
		return fmt.Errorf("migrate v9: %w", err)
	}
	// v10: deleted deployment tombstones for cross-process redeploy consistency
	if err := d.migrateV10(ctx); err != nil {
		return fmt.Errorf("migrate v10: %w", err)
	}
	// v11: performance indexes for knowledge_notes, patrol_alerts, open_questions
	if err := d.migrateV11(ctx); err != nil {
		return fmt.Errorf("migrate v11: %w", err)
	}
	// v12: exploration_plans table for Explorer subsystem
	if err := d.migrateV12(ctx); err != nil {
		return fmt.Errorf("migrate v12: %w", err)
	}
	// v13: benchmark_results.cpu_usage_pct for heterogeneous-engine knowledge
	if err := d.migrateV13(ctx); err != nil {
		return fmt.Errorf("migrate v13: %w", err)
	}
	// v14: multi-modal benchmark columns (TTS/ASR/T2I/T2V)
	if err := d.migrateV14(ctx); err != nil {
		return fmt.Errorf("migrate v14: %w", err)
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
    vram_min_mib INTEGER,
    gpu_count_min INTEGER
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
    cpu_usage_pct REAL,
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
	if _, err := d.db.ExecContext(ctx, "PRAGMA user_version = 2"); err != nil {
		return fmt.Errorf("set user_version=2: %w", err)
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

func (d *DB) migrateV7(ctx context.Context) error {
	var version int
	_ = d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if version >= 7 {
		return nil
	}

	ddl := `
CREATE TABLE IF NOT EXISTS patrol_alerts (
    id TEXT PRIMARY KEY,
    severity TEXT NOT NULL,
    type TEXT NOT NULL,
    message TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at DATETIME,
    resolved BOOLEAN NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS power_samples (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    gpu_index INTEGER NOT NULL DEFAULT 0,
    power_watts REAL,
    temperature_c REAL,
    utilization_pct REAL,
    vram_used_mib INTEGER,
    vram_total_mib INTEGER
);
CREATE INDEX IF NOT EXISTS idx_power_samples_ts ON power_samples(timestamp);

CREATE TABLE IF NOT EXISTS validation_results (
    id TEXT PRIMARY KEY,
    config_id TEXT NOT NULL,
    hardware TEXT NOT NULL,
    engine TEXT NOT NULL,
    model TEXT NOT NULL,
    metric TEXT NOT NULL,
    predicted_value REAL,
    actual_value REAL,
    deviation_pct REAL,
    validated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (config_id) REFERENCES configurations(id)
);

CREATE TABLE IF NOT EXISTS tuning_sessions (
    id TEXT PRIMARY KEY,
    model TEXT NOT NULL,
    engine TEXT,
    status TEXT NOT NULL DEFAULT 'running',
    progress INTEGER DEFAULT 0,
    total INTEGER DEFAULT 0,
    best_config TEXT,
    best_score REAL DEFAULT 0,
    results TEXT,
    started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME
);

CREATE TABLE IF NOT EXISTS apps (
    id TEXT PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    spec TEXT NOT NULL,
    status TEXT DEFAULT 'pending',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS app_dependencies (
    app_id TEXT NOT NULL REFERENCES apps(id),
    need_type TEXT NOT NULL,
    model TEXT,
    deploy_name TEXT,
    satisfied BOOLEAN DEFAULT 0,
    PRIMARY KEY (app_id, need_type)
);

CREATE TABLE IF NOT EXISTS open_questions (
    id TEXT PRIMARY KEY,
    source_asset TEXT NOT NULL,
    question TEXT NOT NULL,
    test_command TEXT,
    expected TEXT,
    status TEXT NOT NULL DEFAULT 'untested',
    actual_result TEXT,
    tested_at DATETIME,
    hardware TEXT
);`
	if _, err := d.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create v7 tables: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, "PRAGMA user_version = 7"); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

func (d *DB) migrateV8(ctx context.Context) error {
	var version int
	_ = d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if version >= 8 {
		return nil
	}

	ddl := `
CREATE TABLE IF NOT EXISTS exploration_runs (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    goal TEXT NOT NULL,
    requested_by TEXT NOT NULL,
    executor TEXT NOT NULL,
    planner TEXT NOT NULL,
    status TEXT NOT NULL,
    hardware_id TEXT,
    engine_id TEXT,
    model_id TEXT,
    source_ref TEXT,
    approval_mode TEXT NOT NULL DEFAULT 'none',
    approved_at DATETIME,
    started_at DATETIME,
    completed_at DATETIME,
    error TEXT,
    plan_json TEXT NOT NULL,
    summary_json TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_er_status ON exploration_runs(status);
CREATE INDEX IF NOT EXISTS idx_er_kind ON exploration_runs(kind);
CREATE INDEX IF NOT EXISTS idx_er_lookup ON exploration_runs(hardware_id, engine_id, model_id);

CREATE TABLE IF NOT EXISTS exploration_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL REFERENCES exploration_runs(id),
    step_index INTEGER NOT NULL,
    step_kind TEXT NOT NULL,
    status TEXT NOT NULL,
    tool_name TEXT,
    request_json TEXT,
    response_json TEXT,
    artifact_type TEXT,
    artifact_id TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_ee_run ON exploration_events(run_id, step_index);`
	if _, err := d.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create v8 tables: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, "PRAGMA user_version = 8"); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

func (d *DB) migrateV9(ctx context.Context) error {
	var version int
	_ = d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if version >= 9 {
		return nil
	}

	rows, err := d.db.QueryContext(ctx, "PRAGMA table_info(model_variants)")
	if err != nil {
		return fmt.Errorf("inspect model_variants: %w", err)
	}
	defer rows.Close()

	hasGPUCountMin := false
	for rows.Next() {
		var (
			cid        int
			name       string
			typ        string
			notNull    int
			defaultV   any
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultV, &primaryKey); err != nil {
			return fmt.Errorf("scan model_variants column: %w", err)
		}
		if name == "gpu_count_min" {
			hasGPUCountMin = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate model_variants columns: %w", err)
	}
	// Explicitly close before ALTER TABLE — MaxOpenConns(1) means the rows
	// must release the connection before any other statement can execute.
	rows.Close()

	if !hasGPUCountMin {
		if _, err := d.db.ExecContext(ctx, `ALTER TABLE model_variants ADD COLUMN gpu_count_min INTEGER`); err != nil {
			return fmt.Errorf("add model_variants.gpu_count_min: %w", err)
		}
	}

	if _, err := d.db.ExecContext(ctx, "PRAGMA user_version = 9"); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

func (d *DB) migrateV10(ctx context.Context) error {
	var version int
	_ = d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if version >= 10 {
		return nil
	}

	ddl := `
CREATE TABLE IF NOT EXISTS deleted_deployments (
    key TEXT PRIMARY KEY,
    deleted_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_deleted_deployments_deleted_at
    ON deleted_deployments (deleted_at);`
	if _, err := d.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("migrate v10 schema: %w", err)
	}

	if _, err := d.db.ExecContext(ctx, "PRAGMA user_version = 10"); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

func (d *DB) migrateV11(ctx context.Context) error {
	var version int
	_ = d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if version >= 11 {
		return nil
	}

	ddl := `
CREATE INDEX IF NOT EXISTS idx_notes_hw_model_engine ON knowledge_notes(hardware_profile, model, engine);
CREATE INDEX IF NOT EXISTS idx_patrol_resolved ON patrol_alerts(resolved, created_at);
CREATE INDEX IF NOT EXISTS idx_questions_status ON open_questions(status);`
	if _, err := d.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create v11 indexes: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, "PRAGMA user_version = 11"); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

func (d *DB) migrateV12(ctx context.Context) error {
	var version int
	_ = d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if version >= 12 {
		return nil
	}

	ddl := `
CREATE TABLE IF NOT EXISTS exploration_plans (
    id            TEXT PRIMARY KEY,
    tier          INTEGER NOT NULL,
    trigger       TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'active',
    plan_json     TEXT NOT NULL,
    progress      INTEGER DEFAULT 0,
    total         INTEGER DEFAULT 0,
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at  DATETIME,
    summary_json  TEXT
);
CREATE INDEX IF NOT EXISTS idx_plans_status ON exploration_plans(status);`
	if _, err := d.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create exploration_plans table: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, "PRAGMA user_version = 12"); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

func (d *DB) migrateV13(ctx context.Context) error {
	var version int
	_ = d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if version >= 13 {
		return nil
	}

	var count int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('benchmark_results') WHERE name='cpu_usage_pct'`).Scan(&count); err != nil {
		return fmt.Errorf("check benchmark_results.cpu_usage_pct column: %w", err)
	}
	if count == 0 {
		if _, err := d.db.ExecContext(ctx, `ALTER TABLE benchmark_results ADD COLUMN cpu_usage_pct REAL`); err != nil {
			return fmt.Errorf("add benchmark_results.cpu_usage_pct: %w", err)
		}
	}
	if _, err := d.db.ExecContext(ctx, "PRAGMA user_version = 13"); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

func (d *DB) migrateV14(ctx context.Context) error {
	var version int
	_ = d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if version >= 14 {
		return nil
	}

	columns := []struct {
		name string
		typ  string
	}{
		// TTS/ASR shared
		{"rtf_p50", "REAL"},
		{"rtf_p95", "REAL"},
		{"rtf_mean", "REAL"},
		// TTS specific
		{"ttfa_p50_ms", "REAL"},
		{"ttfa_p95_ms", "REAL"},
		{"audio_throughput", "REAL"},
		{"avg_input_chars", "INTEGER"},
		{"avg_audio_duration_s", "REAL"},
		// ASR specific
		{"asr_throughput", "REAL"},
		{"avg_input_audio_s", "REAL"},
		{"avg_output_chars", "INTEGER"},
		// T2I specific
		{"latency_p50_ms", "REAL"},
		{"latency_p95_ms", "REAL"},
		{"latency_p99_ms", "REAL"},
		{"images_per_sec", "REAL"},
		{"avg_steps", "INTEGER"},
		{"image_width", "INTEGER"},
		{"image_height", "INTEGER"},
		// T2V specific
		{"video_latency_p50_s", "REAL"},
		{"video_latency_p95_s", "REAL"},
		{"videos_per_hour", "REAL"},
		{"avg_video_duration_s", "REAL"},
		{"avg_frames", "INTEGER"},
		{"video_fps", "INTEGER"},
		{"video_width", "INTEGER"},
		{"video_height", "INTEGER"},
		{"video_steps", "INTEGER"},
	}

	for _, col := range columns {
		var count int
		if err := d.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('benchmark_results') WHERE name=?`, col.name).Scan(&count); err != nil {
			return fmt.Errorf("check benchmark_results.%s column: %w", col.name, err)
		}
		if count == 0 {
			stmt := fmt.Sprintf(`ALTER TABLE benchmark_results ADD COLUMN %s %s`, col.name, col.typ)
			if _, err := d.db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("add benchmark_results.%s: %w", col.name, err)
			}
		}
	}

	if _, err := d.db.ExecContext(ctx, "PRAGMA user_version = 14"); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

func (d *DB) InsertExplorationPlan(ctx context.Context, plan *ExplorationPlanRow) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO exploration_plans (id, tier, trigger, status, plan_json, progress, total, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		plan.ID, plan.Tier, plan.Trigger, plan.Status, plan.PlanJSON,
		plan.Progress, plan.Total, plan.CreatedAt)
	return err
}

func (d *DB) UpdateExplorationPlan(ctx context.Context, plan *ExplorationPlanRow) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE exploration_plans SET status=?, progress=?, completed_at=?, summary_json=? WHERE id=?`,
		plan.Status, plan.Progress, plan.CompletedAt, plan.SummaryJSON, plan.ID)
	return err
}

func (d *DB) ListExplorationPlans(ctx context.Context, status string) ([]*ExplorationPlanRow, error) {
	query := `SELECT id, tier, trigger, status, plan_json, progress, total, created_at, completed_at, summary_json FROM exploration_plans`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT 50`
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plans []*ExplorationPlanRow
	for rows.Next() {
		p := &ExplorationPlanRow{}
		var completedAt sql.NullTime
		var summaryJSON sql.NullString
		if err := rows.Scan(&p.ID, &p.Tier, &p.Trigger, &p.Status, &p.PlanJSON,
			&p.Progress, &p.Total, &p.CreatedAt, &completedAt, &summaryJSON); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			p.CompletedAt = &completedAt.Time
		}
		if summaryJSON.Valid {
			p.SummaryJSON = summaryJSON.String
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

// InsertPatrolAlert persists a patrol alert.
func (d *DB) InsertPatrolAlert(ctx context.Context, id, severity, typ, message string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO patrol_alerts (id, severity, type, message) VALUES (?, ?, ?, ?)`,
		id, severity, typ, message)
	return err
}

// ListPatrolAlerts returns alerts, optionally filtering by resolved status.
func (d *DB) ListPatrolAlerts(ctx context.Context, onlyActive bool) ([]map[string]any, error) {
	query := `SELECT id, severity, type, message, created_at, resolved_at, resolved FROM patrol_alerts`
	if onlyActive {
		query += ` WHERE resolved = 0`
	}
	query += ` ORDER BY created_at DESC LIMIT 100`
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	alerts := make([]map[string]any, 0)
	for rows.Next() {
		var id, severity, typ, message, createdAt string
		var resolvedAt sql.NullString
		var resolved bool
		if err := rows.Scan(&id, &severity, &typ, &message, &createdAt, &resolvedAt, &resolved); err != nil {
			return nil, fmt.Errorf("scan patrol alert: %w", err)
		}
		a := map[string]any{
			"id": id, "severity": severity, "type": typ, "message": message,
			"created_at": createdAt, "resolved": resolved,
		}
		if resolvedAt.Valid {
			a["resolved_at"] = resolvedAt.String
		}
		alerts = append(alerts, a)
	}
	return alerts, rows.Err()
}

// InsertPowerSample records a power/temp/util snapshot.
func (d *DB) InsertPowerSample(ctx context.Context, gpuIndex int, powerW, tempC, utilPct float64, vramUsed, vramTotal int) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO power_samples (gpu_index, power_watts, temperature_c, utilization_pct, vram_used_mib, vram_total_mib) VALUES (?, ?, ?, ?, ?, ?)`,
		gpuIndex, powerW, tempC, utilPct, vramUsed, vramTotal)
	return err
}

// QueryPowerHistory returns aggregated power samples in a time range.
func (d *DB) QueryPowerHistory(ctx context.Context, fromTime, toTime string, intervalS int) ([]map[string]any, error) {
	// Group by interval buckets using strftime
	query := `SELECT
		strftime('%Y-%m-%dT%H:%M:00', timestamp) as bucket,
		AVG(power_watts) as avg_power,
		MAX(power_watts) as max_power,
		AVG(temperature_c) as avg_temp,
		AVG(utilization_pct) as avg_util,
		AVG(vram_used_mib) as avg_vram_used
	FROM power_samples
	WHERE timestamp >= ? AND timestamp <= ?
	GROUP BY bucket
	ORDER BY bucket`
	rows, err := d.db.QueryContext(ctx, query, fromTime, toTime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]map[string]any, 0)
	for rows.Next() {
		var bucket string
		var avgPower, maxPower, avgTemp, avgUtil, avgVRAM float64
		if err := rows.Scan(&bucket, &avgPower, &maxPower, &avgTemp, &avgUtil, &avgVRAM); err != nil {
			return nil, fmt.Errorf("scan power history: %w", err)
		}
		results = append(results, map[string]any{
			"timestamp": bucket, "avg_power_watts": avgPower, "max_power_watts": maxPower,
			"avg_temperature_c": avgTemp, "avg_utilization_pct": avgUtil, "avg_vram_used_mib": int(avgVRAM),
		})
	}
	return results, rows.Err()
}

// PrunePowerSamples removes samples older than retentionDays.
func (d *DB) PrunePowerSamples(ctx context.Context, retentionDays int) error {
	_, err := d.db.ExecContext(ctx,
		`DELETE FROM power_samples WHERE timestamp < datetime('now', ? || ' days')`,
		fmt.Sprintf("-%d", retentionDays))
	return err
}

// InsertValidation records a predicted vs actual comparison.
func (d *DB) InsertValidation(ctx context.Context, id, configID, hardware, engine, model, metric string, predicted, actual, deviation float64) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO validation_results (id, config_id, hardware, engine, model, metric, predicted_value, actual_value, deviation_pct) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, configID, hardware, engine, model, metric, predicted, actual, deviation)
	return err
}

// ListValidations returns validation results for a hardware/engine/model combo.
func (d *DB) ListValidations(ctx context.Context, hardware, engine, model string) ([]map[string]any, error) {
	query := `SELECT id, config_id, hardware, engine, model, metric, predicted_value, actual_value, deviation_pct, validated_at FROM validation_results WHERE 1=1`
	var args []any
	if hardware != "" {
		query += ` AND hardware = ?`
		args = append(args, hardware)
	}
	if engine != "" {
		query += ` AND engine = ?`
		args = append(args, engine)
	}
	if model != "" {
		query += ` AND model = ?`
		args = append(args, model)
	}
	query += ` ORDER BY validated_at DESC LIMIT 50`
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]map[string]any, 0)
	for rows.Next() {
		var id, configID, hw, eng, mdl, metric, validatedAt string
		var predicted, actual, deviation float64
		if err := rows.Scan(&id, &configID, &hw, &eng, &mdl, &metric, &predicted, &actual, &deviation, &validatedAt); err != nil {
			return nil, fmt.Errorf("scan validation: %w", err)
		}
		status := "accurate"
		if deviation > 20 || deviation < -20 {
			status = "divergent"
		}
		results = append(results, map[string]any{
			"id": id, "config_id": configID, "hardware": hw, "engine": eng, "model": mdl,
			"metric": metric, "predicted": predicted, "actual": actual, "deviation_pct": deviation,
			"status": status, "validated_at": validatedAt,
		})
	}
	return results, rows.Err()
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
	if _, err := d.db.ExecContext(ctx,
		`DELETE FROM rollback_snapshots WHERE id NOT IN (SELECT id FROM rollback_snapshots ORDER BY id DESC LIMIT 10)`); err != nil {
		slog.Warn("prune old snapshots", "error", err)
	}
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
	snapshots := make([]*RollbackSnapshot, 0)
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
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin clear static knowledge: %w", err)
	}
	defer tx.Rollback()
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
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+t); err != nil {
			return fmt.Errorf("clear %s: %w", t, err)
		}
	}
	return tx.Commit()
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
// 1. Case-insensitive exact  2. Substring match
func (d *DB) FindModelByName(ctx context.Context, name string) (*Model, error) {
	queries := []string{
		`SELECT id, name, type, path, COALESCE(format,''), COALESCE(size_bytes,0),
		        COALESCE(detected_arch,''), COALESCE(detected_params,''),
		        COALESCE(model_class,''), COALESCE(total_params,0), COALESCE(active_params,0),
		        COALESCE(quantization,''), COALESCE(quant_src,''),
		        COALESCE(status,'registered'), COALESCE(download_progress,0), created_at
		 FROM models WHERE LOWER(name) = LOWER(?) ORDER BY created_at DESC LIMIT 1`,
		`SELECT id, name, type, path, COALESCE(format,''), COALESCE(size_bytes,0),
		        COALESCE(detected_arch,''), COALESCE(detected_params,''),
		        COALESCE(model_class,''), COALESCE(total_params,0), COALESCE(active_params,0),
		        COALESCE(quantization,''), COALESCE(quant_src,''),
		        COALESCE(status,'registered'), COALESCE(download_progress,0), created_at
		 FROM models WHERE LOWER(name) LIKE '%' || LOWER(?) || '%' ORDER BY created_at DESC LIMIT 1`,
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

// LookupEngineAssetMetadata resolves version/image for an engine reference.
// engineRef may be either an exact engine asset id or an engine type.
func (d *DB) LookupEngineAssetMetadata(ctx context.Context, engineRef, hardwareID string) (string, string, error) {
	if d == nil || strings.TrimSpace(engineRef) == "" {
		return "", "", nil
	}
	if version, image, found, err := queryEngineAssetMetadata(ctx, d.db,
		`SELECT COALESCE(version,''), COALESCE(image_name,''), COALESCE(image_tag,'')
		   FROM engine_assets
		  WHERE id = ?
		  LIMIT 1`,
		engineRef); err != nil {
		return "", "", fmt.Errorf("lookup engine asset %q by id: %w", engineRef, err)
	} else if found {
		return version, image, nil
	}

	if strings.TrimSpace(hardwareID) != "" {
		if version, image, found, err := queryEngineAssetMetadata(ctx, d.db,
			`SELECT COALESCE(e.version,''), COALESCE(e.image_name,''), COALESCE(e.image_tag,'')
			   FROM engine_assets e
			  WHERE e.type = ?
			    AND EXISTS (
			          SELECT 1
			            FROM engine_hardware_compat ehc
			           WHERE ehc.engine_id = e.id AND ehc.hardware_id = ?
			        )
			  ORDER BY e.id
			  LIMIT 1`,
			engineRef, hardwareID); err != nil {
			return "", "", fmt.Errorf("lookup engine asset %q for hardware %q: %w", engineRef, hardwareID, err)
		} else if found {
			return version, image, nil
		}
	}

	version, image, _, err := queryEngineAssetMetadata(ctx, d.db,
		`SELECT COALESCE(version,''), COALESCE(image_name,''), COALESCE(image_tag,'')
		   FROM engine_assets
		  WHERE type = ?
		  ORDER BY id
		  LIMIT 1`,
		engineRef)
	if err != nil {
		return "", "", fmt.Errorf("lookup engine asset %q by type: %w", engineRef, err)
	}
	return version, image, nil
}

func queryEngineAssetMetadata(ctx context.Context, db *sql.DB, query string, args ...any) (string, string, bool, error) {
	if db == nil {
		return "", "", false, nil
	}
	var (
		version sql.NullString
		name    sql.NullString
		tag     sql.NullString
	)
	err := db.QueryRowContext(ctx, query, args...).Scan(&version, &name, &tag)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	image := strings.TrimSpace(name.String)
	if image != "" && strings.TrimSpace(tag.String) != "" {
		image += ":" + strings.TrimSpace(tag.String)
	}
	return strings.TrimSpace(version.String), image, true, nil
}

func (d *DB) LookupHardwareGPUArch(ctx context.Context, hardwareID string) (string, error) {
	if d == nil || strings.TrimSpace(hardwareID) == "" {
		return "", nil
	}
	var gpuArch sql.NullString
	err := d.db.QueryRowContext(ctx,
		`SELECT COALESCE(gpu_arch,'') FROM hardware_profiles WHERE id = ?`,
		hardwareID).Scan(&gpuArch)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("lookup hardware profile %q: %w", hardwareID, err)
	}
	return strings.TrimSpace(gpuArch.String), nil
}

func (d *DB) LookupEngineExecutionHints(ctx context.Context, engineRef, hardwareID string) (EngineExecutionHints, error) {
	if d == nil || strings.TrimSpace(engineRef) == "" {
		return EngineExecutionHints{}, nil
	}
	if strings.TrimSpace(hardwareID) != "" {
		if hints, found, err := queryEngineExecutionHints(ctx, d.db,
			`SELECT COALESCE(cpu_offload,0), COALESCE(ssd_offload,0), COALESCE(npu_offload,0)
			   FROM engine_hardware_compat
			  WHERE engine_id = ? AND hardware_id = ?
			  LIMIT 1`,
			engineRef, hardwareID); err != nil {
			return EngineExecutionHints{}, fmt.Errorf("lookup engine execution hints %q/%q by id: %w", engineRef, hardwareID, err)
		} else if found {
			return hints, nil
		}
		if hints, found, err := queryEngineExecutionHints(ctx, d.db,
			`SELECT COALESCE(ehc.cpu_offload,0), COALESCE(ehc.ssd_offload,0), COALESCE(ehc.npu_offload,0)
			   FROM engine_assets e
			   JOIN engine_hardware_compat ehc ON ehc.engine_id = e.id
			  WHERE e.type = ? AND ehc.hardware_id = ?
			  ORDER BY e.id
			  LIMIT 1`,
			engineRef, hardwareID); err != nil {
			return EngineExecutionHints{}, fmt.Errorf("lookup engine execution hints %q/%q by type: %w", engineRef, hardwareID, err)
		} else if found {
			return hints, nil
		}
	}

	if hints, found, err := queryEngineExecutionHints(ctx, d.db,
		`SELECT COALESCE(cpu_offload,0), COALESCE(ssd_offload,0), COALESCE(npu_offload,0)
		   FROM engine_hardware_compat
		  WHERE engine_id = ?
		  ORDER BY hardware_id
		  LIMIT 1`,
		engineRef); err != nil {
		return EngineExecutionHints{}, fmt.Errorf("lookup engine execution hints %q fallback by id: %w", engineRef, err)
	} else if found {
		return hints, nil
	}

	hints, _, err := queryEngineExecutionHints(ctx, d.db,
		`SELECT COALESCE(ehc.cpu_offload,0), COALESCE(ehc.ssd_offload,0), COALESCE(ehc.npu_offload,0)
		   FROM engine_assets e
		   JOIN engine_hardware_compat ehc ON ehc.engine_id = e.id
		  WHERE e.type = ?
		  ORDER BY e.id, ehc.hardware_id
		  LIMIT 1`,
		engineRef)
	if err != nil {
		return EngineExecutionHints{}, fmt.Errorf("lookup engine execution hints %q fallback by type: %w", engineRef, err)
	}
	return hints, nil
}

func queryEngineExecutionHints(ctx context.Context, db *sql.DB, query string, args ...any) (EngineExecutionHints, bool, error) {
	if db == nil {
		return EngineExecutionHints{}, false, nil
	}
	var cpuOffload, ssdOffload, npuOffload int
	err := db.QueryRowContext(ctx, query, args...).Scan(&cpuOffload, &ssdOffload, &npuOffload)
	if errors.Is(err, sql.ErrNoRows) {
		return EngineExecutionHints{}, false, nil
	}
	if err != nil {
		return EngineExecutionHints{}, false, err
	}
	return EngineExecutionHints{
		CPUOffload: cpuOffload != 0,
		SSDOffload: ssdOffload != 0,
		NPUOffload: npuOffload != 0,
	}, true, nil
}

func BuildHeterogeneousObservation(hints EngineExecutionHints, config, resourceUsage map[string]any) map[string]any {
	if !hints.CPUOffload && !hints.SSDOffload && !hints.NPUOffload {
		if len(heterogeneousConfigKeys(config)) > 0 {
			_, hasCPU := positiveObservationFloat(resourceUsage["cpu_usage_pct"])
			_, hasRAM := positiveObservationInt(resourceUsage["ram_usage_mib"])
			_, hasVRAM := positiveObservationInt(resourceUsage["vram_usage_mib"])
			if hasCPU && hasRAM && hasVRAM {
				hints.CPUOffload = true
			}
		}
	}
	if !hints.CPUOffload && !hints.SSDOffload && !hints.NPUOffload {
		return nil
	}
	path := []string{"gpu"}
	observation := map[string]any{}
	if hints.CPUOffload {
		path = append(path, "cpu")
		observation["cpu_offload"] = true
	}
	if hints.SSDOffload {
		path = append(path, "ssd")
		observation["ssd_offload"] = true
	}
	if hints.NPUOffload {
		path = append(path, "npu")
		observation["npu_offload"] = true
	}
	observation["path"] = strings.Join(path, "+")

	for _, key := range heterogeneousConfigKeys(config) {
		if value := config[key]; value != nil {
			observation[key] = value
		}
	}
	if value, ok := positiveObservationInt(resourceUsage["ram_usage_mib"]); ok {
		observation["ram_usage_mib"] = value
	}
	if value, ok := positiveObservationFloat(resourceUsage["cpu_usage_pct"]); ok {
		observation["cpu_usage_pct"] = value
	}
	if value, ok := positiveObservationInt(resourceUsage["vram_usage_mib"]); ok {
		observation["vram_usage_mib"] = value
	}
	if value, ok := positiveObservationFloat(resourceUsage["gpu_utilization_pct"]); ok {
		observation["gpu_utilization_pct"] = value
	}
	if value, ok := positiveObservationFloat(resourceUsage["power_draw_watts"]); ok {
		observation["power_draw_watts"] = value
	}
	return observation
}

func heterogeneousConfigKeys(config map[string]any) []string {
	if len(config) == 0 {
		return nil
	}
	keys := make([]string, 0, len(config))
	for key := range config {
		if shouldIncludeHeterogeneousConfigKey(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func shouldIncludeHeterogeneousConfigKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "" {
		return false
	}
	switch lower {
	case "tp_size", "tensor_parallel_size", "pipeline_parallel_size":
		return true
	}
	for _, marker := range []string{"cpu", "offload", "thread", "expert", "layer", "npu", "ssd"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func positiveObservationInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, v > 0
	case int64:
		n := int(v)
		return n, n > 0
	case float64:
		n := int(v)
		return n, n > 0
	default:
		return 0, false
	}
}

func positiveObservationFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, v > 0
	case float32:
		n := float64(v)
		return n, n > 0
	case int:
		n := float64(v)
		return n, n > 0
	case int64:
		n := float64(v)
		return n, n > 0
	default:
		return 0, false
	}
}

// MarkEnginesUnavailableExcept sets available=false for engines whose ID is not in keepIDs.
// When runtimeType is non-empty, only engines of that runtime are affected (filtered scan).
// When runtimeType is empty, all engines not in keepIDs are marked unavailable (full scan).
func (d *DB) MarkEnginesUnavailableExcept(ctx context.Context, keepIDs []string, runtimeType string) error {
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
	if runtimeType != "" {
		query += ` AND runtime_type = ?`
		args = append(args, runtimeType)
	}
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
	if n.ID == "" {
		var buf [8]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return fmt.Errorf("generate note id: %w", err)
		}
		n.ID = hex.EncodeToString(buf[:])
	}
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

// FindGoldenBenchmark returns the golden configuration and its best benchmark result
// for the given hardware/engine/model triple. Uses a single JOIN query to avoid
// MaxOpenConns(1) deadlocks. Returns (nil, nil, nil) if no golden config exists.
func (d *DB) FindGoldenBenchmark(ctx context.Context, hardware, engine, model string) (*Configuration, *BenchmarkResult, error) {
	row := d.db.QueryRowContext(ctx,
		`SELECT c.id, c.hardware_id, c.engine_id, c.model_id, COALESCE(c.partition_slot,''),
		        c.config, c.config_hash, c.derived_from, c.status,
		        COALESCE(c.tags,'[]'), COALESCE(c.source,'local'), COALESCE(c.device_id,''),
		        c.created_at, c.updated_at,
		        b.id, b.throughput_tps
		 FROM configurations c
		 LEFT JOIN benchmark_results b ON b.config_id = c.id
		 WHERE c.status = 'golden'
		   AND c.hardware_id = ? AND c.engine_id = ? AND c.model_id = ?
		 ORDER BY b.throughput_tps DESC
		 LIMIT 1`,
		hardware, engine, model)

	cfg := &Configuration{}
	var tagsStr, derivedFrom, benchID sql.NullString
	var throughput sql.NullFloat64
	err := row.Scan(
		&cfg.ID, &cfg.HardwareID, &cfg.EngineID, &cfg.ModelID, &cfg.Slot,
		&cfg.Config, &cfg.ConfigHash, &derivedFrom, &cfg.Status,
		&tagsStr, &cfg.Source, &cfg.DeviceID, &cfg.CreatedAt, &cfg.UpdatedAt,
		&benchID, &throughput)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("find golden benchmark: %w", err)
	}
	if derivedFrom.Valid {
		cfg.DerivedFrom = derivedFrom.String
	}
	_ = json.Unmarshal([]byte(tagsStr.String), &cfg.Tags)

	var bench *BenchmarkResult
	if benchID.Valid {
		bench = &BenchmarkResult{ID: benchID.String, ConfigID: cfg.ID, ThroughputTPS: throughput.Float64}
	}
	return cfg, bench, nil
}

func (d *DB) InsertBenchmarkResult(ctx context.Context, b *BenchmarkResult) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO benchmark_results (id, config_id, concurrency, input_len_bucket, output_len_bucket, modality,
		    ttft_ms_p50, ttft_ms_p95, ttft_ms_p99, tpot_ms_p50, tpot_ms_p95,
		    throughput_tps, qps, vram_usage_mib, ram_usage_mib, power_draw_watts, gpu_utilization_pct, cpu_usage_pct,
		    error_rate, oom_occurred, stability, duration_s, sample_count, agent_model, notes,
		    rtf_p50, rtf_p95, rtf_mean,
		    ttfa_p50_ms, ttfa_p95_ms, audio_throughput, avg_input_chars, avg_audio_duration_s,
		    asr_throughput, avg_input_audio_s, avg_output_chars,
		    latency_p50_ms, latency_p95_ms, latency_p99_ms, images_per_sec, avg_steps, image_width, image_height,
		    video_latency_p50_s, video_latency_p95_s, videos_per_hour, avg_video_duration_s,
		    avg_frames, video_fps, video_width, video_height, video_steps)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		         ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		         ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.ConfigID, b.Concurrency, b.InputLenBucket, b.OutputLenBucket, b.Modality,
		b.TTFTP50ms, b.TTFTP95ms, b.TTFTP99ms, b.TPOTP50ms, b.TPOTP95ms,
		b.ThroughputTPS, b.QPS, b.VRAMUsageMiB, b.RAMUsageMiB, b.PowerDrawWatts, b.GPUUtilPct, b.CPUUsagePct,
		b.ErrorRate, b.OOMOccurred, b.Stability, b.DurationS, b.SampleCount, b.AgentModel, b.Notes,
		b.RTFP50, b.RTFP95, b.RTFMean,
		b.TTFAP50ms, b.TTFAP95ms, b.AudioThroughput, b.AvgInputChars, b.AvgAudioDurationS,
		b.ASRThroughput, b.AvgInputAudioS, b.AvgOutputChars,
		b.LatencyP50ms, b.LatencyP95ms, b.LatencyP99ms, b.ImagesPerSec, b.AvgSteps, b.ImageWidth, b.ImageHeight,
		b.VideoLatencyP50s, b.VideoLatencyP95s, b.VideosPerHour, b.AvgVideoDurationS,
		b.AvgFrames, b.VideoFPS, b.VideoWidth, b.VideoHeight, b.VideoSteps)
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

func (d *DB) MarkDeletedDeployments(ctx context.Context, deletedAt time.Time, keys ...string) error {
	stmt, err := d.db.PrepareContext(ctx,
		`INSERT INTO deleted_deployments (key, deleted_at) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET deleted_at = excluded.deleted_at`)
	if err != nil {
		return fmt.Errorf("prepare deleted deployment mark: %w", err)
	}
	defer stmt.Close()

	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, key, deletedAt.UTC()); err != nil {
			return fmt.Errorf("mark deleted deployment %q: %w", key, err)
		}
	}
	return nil
}

func (d *DB) ListDeletedDeploymentsSince(ctx context.Context, since time.Time) (map[string]time.Time, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT key, deleted_at FROM deleted_deployments WHERE deleted_at >= ?`, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("list deleted deployments since %s: %w", since.UTC().Format(time.RFC3339), err)
	}
	defer rows.Close()

	marks := make(map[string]time.Time)
	for rows.Next() {
		var key string
		var deletedAt time.Time
		if err := rows.Scan(&key, &deletedAt); err != nil {
			return nil, fmt.Errorf("scan deleted deployment mark: %w", err)
		}
		marks[key] = deletedAt
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate deleted deployment marks: %w", err)
	}
	return marks, nil
}

func (d *DB) PruneDeletedDeploymentsBefore(ctx context.Context, before time.Time) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM deleted_deployments WHERE deleted_at < ?`, before.UTC()); err != nil {
		return fmt.Errorf("prune deleted deployments before %s: %w", before.UTC().Format(time.RFC3339), err)
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

// ListConfigurations returns configurations matching optional filters.
// Empty filter values are ignored.
func (d *DB) ListConfigurations(ctx context.Context, hardware, model, engine string) ([]*Configuration, error) {
	query := `SELECT id, hardware_id, engine_id, model_id, COALESCE(partition_slot,''),
	                 config, config_hash, derived_from, COALESCE(status,'experiment'),
	                 COALESCE(tags,'[]'), COALESCE(source,'local'), COALESCE(device_id,''),
	                 created_at, updated_at
	          FROM configurations WHERE 1=1`
	var args []any
	if hardware != "" {
		query += ` AND hardware_id = ?`
		args = append(args, hardware)
	}
	if model != "" {
		query += ` AND model_id = ?`
		args = append(args, model)
	}
	if engine != "" {
		query += ` AND engine_id = ?`
		args = append(args, engine)
	}
	query += ` ORDER BY updated_at DESC`

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list configurations: %w", err)
	}
	defer rows.Close()

	configs := make([]*Configuration, 0)
	for rows.Next() {
		c := &Configuration{}
		var tagsStr, derivedFrom sql.NullString
		if err := rows.Scan(&c.ID, &c.HardwareID, &c.EngineID, &c.ModelID, &c.Slot,
			&c.Config, &c.ConfigHash, &derivedFrom, &c.Status,
			&tagsStr, &c.Source, &c.DeviceID, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan configuration row: %w", err)
		}
		if derivedFrom.Valid {
			c.DerivedFrom = derivedFrom.String
		}
		_ = json.Unmarshal([]byte(tagsStr.String), &c.Tags)
		configs = append(configs, c)
	}
	return configs, rows.Err()
}

// ListBenchmarkResults returns benchmark results, optionally filtered by config IDs.
func (d *DB) ListBenchmarkResults(ctx context.Context, configIDs []string, limit int) ([]*BenchmarkResult, error) {
	query := `SELECT id, config_id, concurrency, COALESCE(input_len_bucket,''),
	                 COALESCE(output_len_bucket,''), COALESCE(modality,'text'),
	                 ttft_ms_p50, ttft_ms_p95, COALESCE(ttft_ms_p99,0),
	                 COALESCE(tpot_ms_p50,0), COALESCE(tpot_ms_p95,0),
	                 throughput_tps, COALESCE(qps,0),
	                 COALESCE(vram_usage_mib,0), COALESCE(ram_usage_mib,0),
	                 COALESCE(power_draw_watts,0), COALESCE(gpu_utilization_pct,0), COALESCE(cpu_usage_pct,0),
	                 COALESCE(error_rate,0), COALESCE(oom_occurred,0),
	                 COALESCE(stability,''), COALESCE(duration_s,0), COALESCE(sample_count,0),
	                 tested_at, COALESCE(agent_model,''), COALESCE(notes,''),
	                 rtf_p50, rtf_p95, rtf_mean,
	                 ttfa_p50_ms, ttfa_p95_ms, audio_throughput, avg_input_chars, avg_audio_duration_s,
	                 asr_throughput, avg_input_audio_s, avg_output_chars,
	                 latency_p50_ms, latency_p95_ms, latency_p99_ms, images_per_sec, avg_steps, image_width, image_height,
	                 video_latency_p50_s, video_latency_p95_s, videos_per_hour, avg_video_duration_s,
	                 avg_frames, video_fps, video_width, video_height, video_steps
	          FROM benchmark_results WHERE 1=1`
	var args []any
	if len(configIDs) > 0 {
		placeholders := strings.Repeat("?,", len(configIDs))
		placeholders = placeholders[:len(placeholders)-1]
		query += fmt.Sprintf(` AND config_id IN (%s)`, placeholders)
		for _, id := range configIDs {
			args = append(args, id)
		}
	}
	query += ` ORDER BY tested_at DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list benchmark results: %w", err)
	}
	defer rows.Close()

	results := make([]*BenchmarkResult, 0)
	for rows.Next() {
		b := &BenchmarkResult{}
		var (
			rtfP50, rtfP95, rtfMean                     sql.NullFloat64
			ttfaP50ms, ttfaP95ms, audioThroughput        sql.NullFloat64
			avgInputChars                                 sql.NullInt64
			avgAudioDurationS                             sql.NullFloat64
			asrThroughput, avgInputAudioS                 sql.NullFloat64
			avgOutputChars                                sql.NullInt64
			latencyP50ms, latencyP95ms, latencyP99ms      sql.NullFloat64
			imagesPerSec                                  sql.NullFloat64
			avgSteps, imageWidth, imageHeight             sql.NullInt64
			videoLatencyP50s, videoLatencyP95s             sql.NullFloat64
			videosPerHour, avgVideoDurationS               sql.NullFloat64
			avgFrames, videoFPS, videoWidth, videoHeight  sql.NullInt64
			videoSteps                                    sql.NullInt64
		)
		if err := rows.Scan(&b.ID, &b.ConfigID, &b.Concurrency, &b.InputLenBucket,
			&b.OutputLenBucket, &b.Modality,
			&b.TTFTP50ms, &b.TTFTP95ms, &b.TTFTP99ms, &b.TPOTP50ms, &b.TPOTP95ms,
			&b.ThroughputTPS, &b.QPS,
			&b.VRAMUsageMiB, &b.RAMUsageMiB, &b.PowerDrawWatts, &b.GPUUtilPct, &b.CPUUsagePct,
			&b.ErrorRate, &b.OOMOccurred, &b.Stability, &b.DurationS, &b.SampleCount,
			&b.TestedAt, &b.AgentModel, &b.Notes,
			&rtfP50, &rtfP95, &rtfMean,
			&ttfaP50ms, &ttfaP95ms, &audioThroughput, &avgInputChars, &avgAudioDurationS,
			&asrThroughput, &avgInputAudioS, &avgOutputChars,
			&latencyP50ms, &latencyP95ms, &latencyP99ms, &imagesPerSec, &avgSteps, &imageWidth, &imageHeight,
			&videoLatencyP50s, &videoLatencyP95s, &videosPerHour, &avgVideoDurationS,
			&avgFrames, &videoFPS, &videoWidth, &videoHeight, &videoSteps); err != nil {
			return nil, fmt.Errorf("scan benchmark row: %w", err)
		}
		if rtfP50.Valid {
			b.RTFP50 = &rtfP50.Float64
		}
		if rtfP95.Valid {
			b.RTFP95 = &rtfP95.Float64
		}
		if rtfMean.Valid {
			b.RTFMean = &rtfMean.Float64
		}
		if ttfaP50ms.Valid {
			b.TTFAP50ms = &ttfaP50ms.Float64
		}
		if ttfaP95ms.Valid {
			b.TTFAP95ms = &ttfaP95ms.Float64
		}
		if audioThroughput.Valid {
			b.AudioThroughput = &audioThroughput.Float64
		}
		if avgInputChars.Valid {
			v := int(avgInputChars.Int64)
			b.AvgInputChars = &v
		}
		if avgAudioDurationS.Valid {
			b.AvgAudioDurationS = &avgAudioDurationS.Float64
		}
		if asrThroughput.Valid {
			b.ASRThroughput = &asrThroughput.Float64
		}
		if avgInputAudioS.Valid {
			b.AvgInputAudioS = &avgInputAudioS.Float64
		}
		if avgOutputChars.Valid {
			v := int(avgOutputChars.Int64)
			b.AvgOutputChars = &v
		}
		if latencyP50ms.Valid {
			b.LatencyP50ms = &latencyP50ms.Float64
		}
		if latencyP95ms.Valid {
			b.LatencyP95ms = &latencyP95ms.Float64
		}
		if latencyP99ms.Valid {
			b.LatencyP99ms = &latencyP99ms.Float64
		}
		if imagesPerSec.Valid {
			b.ImagesPerSec = &imagesPerSec.Float64
		}
		if avgSteps.Valid {
			v := int(avgSteps.Int64)
			b.AvgSteps = &v
		}
		if imageWidth.Valid {
			v := int(imageWidth.Int64)
			b.ImageWidth = &v
		}
		if imageHeight.Valid {
			v := int(imageHeight.Int64)
			b.ImageHeight = &v
		}
		if videoLatencyP50s.Valid {
			b.VideoLatencyP50s = &videoLatencyP50s.Float64
		}
		if videoLatencyP95s.Valid {
			b.VideoLatencyP95s = &videoLatencyP95s.Float64
		}
		if videosPerHour.Valid {
			b.VideosPerHour = &videosPerHour.Float64
		}
		if avgVideoDurationS.Valid {
			b.AvgVideoDurationS = &avgVideoDurationS.Float64
		}
		if avgFrames.Valid {
			v := int(avgFrames.Int64)
			b.AvgFrames = &v
		}
		if videoFPS.Valid {
			v := int(videoFPS.Int64)
			b.VideoFPS = &v
		}
		if videoWidth.Valid {
			v := int(videoWidth.Int64)
			b.VideoWidth = &v
		}
		if videoHeight.Valid {
			v := int(videoHeight.Int64)
			b.VideoHeight = &v
		}
		if videoSteps.Valid {
			v := int(videoSteps.Int64)
			b.VideoSteps = &v
		}
		results = append(results, b)
	}
	return results, rows.Err()
}

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}

// --- Open Questions ---

// UpsertOpenQuestion inserts or updates an open question.
func (d *DB) UpsertOpenQuestion(ctx context.Context, id, sourceAsset, question, testCommand, expected, status, actualResult string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO open_questions (id, source_asset, question, test_command, expected, status, actual_result)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		     source_asset = excluded.source_asset,
		     question = excluded.question,
		     test_command = excluded.test_command,
		     expected = excluded.expected,
		     status = CASE
		       WHEN open_questions.status IN ('tested', 'confirmed', 'confirmed_incompatible', 'rejected') THEN open_questions.status
		       WHEN excluded.status <> '' AND excluded.status <> 'untested' THEN excluded.status
		       ELSE open_questions.status
		     END,
		     actual_result = CASE
		       WHEN open_questions.status IN ('tested', 'confirmed', 'confirmed_incompatible', 'rejected')
		            AND COALESCE(open_questions.actual_result, '') <> '' THEN open_questions.actual_result
		       WHEN excluded.status <> '' AND excluded.status <> 'untested'
		            AND COALESCE(excluded.actual_result, '') <> '' THEN excluded.actual_result
		       ELSE open_questions.actual_result
		     END`,
		id, sourceAsset, question, testCommand, expected, status, actualResult)
	return err
}

// GetOpenQuestion returns a single open question by ID.
func (d *DB) GetOpenQuestion(ctx context.Context, id string) (*OpenQuestion, error) {
	row := d.db.QueryRowContext(ctx,
		`SELECT id, source_asset, question, test_command, expected, status, actual_result, tested_at, hardware
		   FROM open_questions
		  WHERE id = ?`,
		id)

	var q OpenQuestion
	var testCmd, expected, actualResult, testedAt, hardware sql.NullString
	if err := row.Scan(&q.ID, &q.SourceAsset, &q.Question, &testCmd, &expected, &q.Status, &actualResult, &testedAt, &hardware); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("open question %s not found", id)
		}
		return nil, fmt.Errorf("get open question %s: %w", id, err)
	}
	if testCmd.Valid {
		q.TestCommand = testCmd.String
	}
	if expected.Valid {
		q.Expected = expected.String
	}
	if actualResult.Valid {
		q.ActualResult = actualResult.String
	}
	if testedAt.Valid {
		if ts, err := time.Parse("2006-01-02 15:04:05", testedAt.String); err == nil {
			q.TestedAt = ts
		}
	}
	if hardware.Valid {
		q.Hardware = hardware.String
	}
	return &q, nil
}

// ListOpenQuestions returns open questions, optionally filtering by status.
func (d *DB) ListOpenQuestions(ctx context.Context, status string) ([]map[string]any, error) {
	query := `SELECT id, source_asset, question, test_command, expected, status, actual_result, tested_at, hardware FROM open_questions`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY CASE status
		WHEN 'untested' THEN 0
		WHEN 'tested' THEN 1
		WHEN 'confirmed' THEN 2
		WHEN 'confirmed_incompatible' THEN 3
		ELSE 4 END`
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]map[string]any, 0)
	for rows.Next() {
		var id, source, question, status string
		var testCmd, expected, actualResult, testedAt, hardware sql.NullString
		if err := rows.Scan(&id, &source, &question, &testCmd, &expected, &status, &actualResult, &testedAt, &hardware); err != nil {
			return nil, fmt.Errorf("scan open question: %w", err)
		}
		r := map[string]any{
			"id": id, "source_asset": source, "question": question, "status": status,
		}
		if testCmd.Valid {
			r["test_command"] = testCmd.String
		}
		if expected.Valid {
			r["expected"] = expected.String
		}
		if actualResult.Valid {
			r["actual_result"] = actualResult.String
		}
		if testedAt.Valid {
			r["tested_at"] = testedAt.String
		}
		if hardware.Valid {
			r["hardware"] = hardware.String
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// ResolveOpenQuestion marks a question as confirmed or rejected with the actual result.
func (d *DB) ResolveOpenQuestion(ctx context.Context, id, status, actualResult, hardware string) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE open_questions SET status = ?, actual_result = ?, tested_at = datetime('now'), hardware = ? WHERE id = ?`,
		status, actualResult, hardware, id)
	if err != nil {
		return fmt.Errorf("resolve open question %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("open question %s not found", id)
	}
	return nil
}

// --- Exploration Runs ---

func (d *DB) InsertExplorationRun(ctx context.Context, run *ExplorationRun) error {
	if run == nil {
		return fmt.Errorf("exploration run is nil")
	}
	_, err := d.db.ExecContext(ctx, `
INSERT INTO exploration_runs (
    id, kind, goal, requested_by, executor, planner, status,
    hardware_id, engine_id, model_id, source_ref, approval_mode,
    approved_at, started_at, completed_at, error, plan_json, summary_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.Kind, run.Goal, run.RequestedBy, run.Executor, run.Planner, run.Status,
		nullStr(run.HardwareID), nullStr(run.EngineID), nullStr(run.ModelID), nullStr(run.SourceRef), run.ApprovalMode,
		nullTime(run.ApprovedAt), nullTime(run.StartedAt), nullTime(run.CompletedAt), nullStr(run.Error), run.PlanJSON, nullStr(run.SummaryJSON))
	if err != nil {
		return fmt.Errorf("insert exploration run %s: %w", run.ID, err)
	}
	return nil
}

func (d *DB) UpdateExplorationRun(ctx context.Context, run *ExplorationRun) error {
	if run == nil {
		return fmt.Errorf("exploration run is nil")
	}
	_, err := d.db.ExecContext(ctx, `
UPDATE exploration_runs
SET kind = ?, goal = ?, requested_by = ?, executor = ?, planner = ?, status = ?,
    hardware_id = ?, engine_id = ?, model_id = ?, source_ref = ?, approval_mode = ?,
    approved_at = ?, started_at = ?, completed_at = ?, error = ?, plan_json = ?, summary_json = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?`,
		run.Kind, run.Goal, run.RequestedBy, run.Executor, run.Planner, run.Status,
		nullStr(run.HardwareID), nullStr(run.EngineID), nullStr(run.ModelID), nullStr(run.SourceRef), run.ApprovalMode,
		nullTime(run.ApprovedAt), nullTime(run.StartedAt), nullTime(run.CompletedAt), nullStr(run.Error), run.PlanJSON, nullStr(run.SummaryJSON),
		run.ID)
	if err != nil {
		return fmt.Errorf("update exploration run %s: %w", run.ID, err)
	}
	return nil
}

func (d *DB) GetExplorationRun(ctx context.Context, id string) (*ExplorationRun, error) {
	var run ExplorationRun
	var hardwareID, engineID, modelID, sourceRef, errStr, summary sql.NullString
	var approvedAt, startedAt, completedAt sql.NullTime
	err := d.db.QueryRowContext(ctx, `
SELECT id, kind, goal, requested_by, executor, planner, status,
       COALESCE(hardware_id,''), COALESCE(engine_id,''), COALESCE(model_id,''), COALESCE(source_ref,''),
       approval_mode, approved_at, started_at, completed_at, error,
       plan_json, summary_json, created_at, updated_at
FROM exploration_runs
WHERE id = ?`, id).Scan(
		&run.ID, &run.Kind, &run.Goal, &run.RequestedBy, &run.Executor, &run.Planner, &run.Status,
		&hardwareID, &engineID, &modelID, &sourceRef,
		&run.ApprovalMode, &approvedAt, &startedAt, &completedAt, &errStr,
		&run.PlanJSON, &summary, &run.CreatedAt, &run.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("exploration run %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get exploration run %s: %w", id, err)
	}
	run.HardwareID = hardwareID.String
	run.EngineID = engineID.String
	run.ModelID = modelID.String
	run.SourceRef = sourceRef.String
	run.Error = errStr.String
	run.SummaryJSON = summary.String
	if approvedAt.Valid {
		run.ApprovedAt = approvedAt.Time
	}
	if startedAt.Valid {
		run.StartedAt = startedAt.Time
	}
	if completedAt.Valid {
		run.CompletedAt = completedAt.Time
	}
	return &run, nil
}

func (d *DB) ListExplorationRuns(ctx context.Context, status string, limit int) ([]*ExplorationRun, error) {
	query := `
SELECT id, kind, goal, requested_by, executor, planner, status,
       COALESCE(hardware_id,''), COALESCE(engine_id,''), COALESCE(model_id,''), COALESCE(source_ref,''),
       approval_mode, approved_at, started_at, completed_at, error,
       plan_json, summary_json, created_at, updated_at
FROM exploration_runs`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list exploration runs: %w", err)
	}
	defer rows.Close()

	runs := make([]*ExplorationRun, 0)
	for rows.Next() {
		var run ExplorationRun
		var hardwareID, engineID, modelID, sourceRef, errStr, summary sql.NullString
		var approvedAt, startedAt, completedAt sql.NullTime
		if err := rows.Scan(
			&run.ID, &run.Kind, &run.Goal, &run.RequestedBy, &run.Executor, &run.Planner, &run.Status,
			&hardwareID, &engineID, &modelID, &sourceRef,
			&run.ApprovalMode, &approvedAt, &startedAt, &completedAt, &errStr,
			&run.PlanJSON, &summary, &run.CreatedAt, &run.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan exploration run: %w", err)
		}
		run.HardwareID = hardwareID.String
		run.EngineID = engineID.String
		run.ModelID = modelID.String
		run.SourceRef = sourceRef.String
		run.Error = errStr.String
		run.SummaryJSON = summary.String
		if approvedAt.Valid {
			run.ApprovedAt = approvedAt.Time
		}
		if startedAt.Valid {
			run.StartedAt = startedAt.Time
		}
		if completedAt.Valid {
			run.CompletedAt = completedAt.Time
		}
		cp := run
		runs = append(runs, &cp)
	}
	return runs, rows.Err()
}

func (d *DB) CountActiveExplorationRuns(ctx context.Context) (int, error) {
	var count int
	err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM exploration_runs WHERE status IN ('planning', 'needs_approval', 'queued', 'running')`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active exploration runs: %w", err)
	}
	return count, nil
}

// HasCompletedExploration checks if a model+engine combo has any completed exploration run.
func (d *DB) HasCompletedExploration(ctx context.Context, modelID, engineID string) (bool, error) {
	var exists int
	err := d.db.QueryRowContext(ctx,
		`SELECT 1 FROM exploration_runs WHERE model_id = ? AND engine_id = ? AND status = 'completed' LIMIT 1`,
		modelID, engineID).Scan(&exists)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// CountFailedExplorations counts how many times a model+engine combo has failed.
func (d *DB) CountFailedExplorations(ctx context.Context, modelID, engineID string) (int, error) {
	var count int
	err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM exploration_runs WHERE model_id = ? AND engine_id = ? AND status = 'failed'`,
		modelID, engineID).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// ExploredCombo summarizes the exploration status of a model+engine pair.
type ExploredCombo struct {
	Model     string
	Engine    string
	Completed bool
	FailCount int
}

// ListExploredCombos returns all model+engine pairs that have been explored,
// with their completion status and failure count, in a single query.
func (d *DB) ListExploredCombos(ctx context.Context) ([]ExploredCombo, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT model_id, engine_id,
			MAX(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) AS completed,
			SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) AS fail_count
		FROM exploration_runs
		GROUP BY model_id, engine_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var combos []ExploredCombo
	for rows.Next() {
		var c ExploredCombo
		var completed int
		if err := rows.Scan(&c.Model, &c.Engine, &completed, &c.FailCount); err != nil {
			return nil, err
		}
		c.Completed = completed == 1
		combos = append(combos, c)
	}
	return combos, rows.Err()
}

func (d *DB) InsertExplorationEvent(ctx context.Context, event *ExplorationEvent) error {
	if event == nil {
		return fmt.Errorf("exploration event is nil")
	}
	res, err := d.db.ExecContext(ctx, `
INSERT INTO exploration_events (
    run_id, step_index, step_kind, status, tool_name, request_json, response_json, artifact_type, artifact_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.RunID, event.StepIndex, event.StepKind, event.Status,
		nullStr(event.ToolName), nullStr(event.RequestJSON), nullStr(event.ResponseJSON), nullStr(event.ArtifactType), nullStr(event.ArtifactID))
	if err != nil {
		return fmt.Errorf("insert exploration event for run %s: %w", event.RunID, err)
	}
	if id, err := res.LastInsertId(); err == nil {
		event.ID = id
	}
	return nil
}

func (d *DB) ListExplorationEvents(ctx context.Context, runID string) ([]*ExplorationEvent, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, run_id, step_index, step_kind, status,
       COALESCE(tool_name,''), COALESCE(request_json,''), COALESCE(response_json,''),
       COALESCE(artifact_type,''), COALESCE(artifact_id,''), created_at
FROM exploration_events
WHERE run_id = ?
ORDER BY id ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list exploration events for run %s: %w", runID, err)
	}
	defer rows.Close()

	events := make([]*ExplorationEvent, 0)
	for rows.Next() {
		var event ExplorationEvent
		if err := rows.Scan(&event.ID, &event.RunID, &event.StepIndex, &event.StepKind, &event.Status,
			&event.ToolName, &event.RequestJSON, &event.ResponseJSON, &event.ArtifactType, &event.ArtifactID, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan exploration event: %w", err)
		}
		events = append(events, &event)
	}
	return events, rows.Err()
}

// --- Apps ---

// InsertApp registers an app with its spec.
func (d *DB) InsertApp(ctx context.Context, id, name, spec string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO apps (id, name, spec, status) VALUES (?, ?, ?, 'pending')`,
		id, name, spec)
	if err != nil {
		return fmt.Errorf("insert app %s: %w", name, err)
	}
	return nil
}

// ListApps returns all registered apps with their dependency satisfaction status.
func (d *DB) ListApps(ctx context.Context) ([]map[string]any, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT a.id, a.name, a.spec, a.status, a.created_at,
			COALESCE((SELECT COUNT(*) FROM app_dependencies WHERE app_id = a.id), 0) as total_deps,
			COALESCE((SELECT COUNT(*) FROM app_dependencies WHERE app_id = a.id AND satisfied = 1), 0) as satisfied_deps
		 FROM apps a ORDER BY a.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]map[string]any, 0)
	for rows.Next() {
		var id, name, spec, status, createdAt string
		var totalDeps, satisfiedDeps int
		if err := rows.Scan(&id, &name, &spec, &status, &createdAt, &totalDeps, &satisfiedDeps); err != nil {
			return nil, fmt.Errorf("scan app: %w", err)
		}
		results = append(results, map[string]any{
			"id": id, "name": name, "spec": json.RawMessage(spec), "status": status,
			"created_at": createdAt, "total_deps": totalDeps, "satisfied_deps": satisfiedDeps,
		})
	}
	return results, rows.Err()
}

// UpsertAppDependency records a dependency for an app.
func (d *DB) UpsertAppDependency(ctx context.Context, appID, needType, model, deployName string, satisfied bool) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO app_dependencies (app_id, need_type, model, deploy_name, satisfied) VALUES (?, ?, ?, ?, ?)`,
		appID, needType, model, deployName, satisfied)
	if err != nil {
		return fmt.Errorf("upsert app dependency %s/%s: %w", appID, needType, err)
	}
	return nil
}

// UpdateAppStatus updates an app's provisioning status.
func (d *DB) UpdateAppStatus(ctx context.Context, id, status string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE apps SET status = ? WHERE id = ?`, status, id)
	return err
}

// --- Sync Metadata ---

// GetSyncTimestamp returns the last sync timestamp for a direction (push/pull).
func (d *DB) GetSyncTimestamp(ctx context.Context, direction string) (string, error) {
	// Store sync metadata in the config table (already exists)
	var val string
	err := d.db.QueryRowContext(ctx,
		`SELECT value FROM config WHERE key = ?`, "sync_"+direction+"_at").Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return val, err
}

// SetSyncTimestamp records the last sync timestamp.
func (d *DB) SetSyncTimestamp(ctx context.Context, direction string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO config (key, value) VALUES (?, datetime('now'))`,
		"sync_"+direction+"_at")
	if err != nil {
		return fmt.Errorf("set sync timestamp %s: %w", direction, err)
	}
	return nil
}
