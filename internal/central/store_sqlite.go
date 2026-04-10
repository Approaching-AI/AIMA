package central

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteCentralStore implements CentralStore using modernc.org/sqlite (zero CGO).
type SQLiteCentralStore struct {
	db *sql.DB
}

// NewSQLiteCentralStore opens a SQLite database and returns a CentralStore.
func NewSQLiteCentralStore(dbPath string) (*SQLiteCentralStore, error) {
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	return &SQLiteCentralStore{db: db}, nil
}

func (s *SQLiteCentralStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteCentralStore) DB() *sql.DB {
	return s.db
}

func (s *SQLiteCentralStore) Migrate(ctx context.Context) error {
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
    cpu_usage_pct REAL,
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
    status TEXT NOT NULL DEFAULT 'pending',
    severity TEXT NOT NULL DEFAULT 'info',
    target_hardware TEXT,
    target_model TEXT,
    target_engine TEXT,
    content_json TEXT,
    reasoning TEXT,
    based_on_json TEXT,
    hardware TEXT,
    model TEXT,
    engine TEXT,
    title TEXT,
    summary TEXT,
    details TEXT,
    confidence TEXT,
    analysis_id TEXT,
    feedback TEXT,
    accepted BOOLEAN DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    delivered_at DATETIME,
    validated_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_advisory_type ON advisories(type);
CREATE INDEX IF NOT EXISTS idx_advisory_status ON advisories(status);
CREATE INDEX IF NOT EXISTS idx_advisory_target_hw ON advisories(target_hardware);

CREATE TABLE IF NOT EXISTS analysis_runs (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running',
    summary TEXT,
    input_json TEXT,
    output_json TEXT,
    advisories TEXT,
    advisory_count INTEGER DEFAULT 0,
    duration_ms INTEGER DEFAULT 0,
    error TEXT,
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS scenarios (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT,
    hardware_profile TEXT,
    scenario_yaml TEXT,
    advisory_id TEXT,
    version INTEGER DEFAULT 1,
    hardware TEXT,
    models TEXT,
    config TEXT,
    source TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_scenario_name ON scenarios(name);
CREATE INDEX IF NOT EXISTS idx_scenario_hw ON scenarios(hardware);
CREATE INDEX IF NOT EXISTS idx_scenario_hw_profile ON scenarios(hardware_profile);`

	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return err
	}

	for _, change := range []struct {
		table string
		col   string
		def   string
	}{
		{"advisories", "status", "TEXT NOT NULL DEFAULT 'pending'"},
		{"advisories", "target_hardware", "TEXT"},
		{"advisories", "target_model", "TEXT"},
		{"advisories", "target_engine", "TEXT"},
		{"advisories", "content_json", "TEXT"},
		{"advisories", "reasoning", "TEXT"},
		{"advisories", "based_on_json", "TEXT"},
		{"advisories", "delivered_at", "DATETIME"},
		{"advisories", "validated_at", "DATETIME"},
		{"analysis_runs", "input_json", "TEXT"},
		{"analysis_runs", "output_json", "TEXT"},
		{"analysis_runs", "advisories", "TEXT"},
		{"analysis_runs", "started_at", "DATETIME"},
		{"analysis_runs", "completed_at", "DATETIME"},
		{"analysis_runs", "updated_at", "DATETIME"},
		{"scenarios", "hardware_profile", "TEXT"},
		{"scenarios", "scenario_yaml", "TEXT"},
		{"scenarios", "advisory_id", "TEXT"},
		{"scenarios", "version", "INTEGER DEFAULT 1"},
		{"scenarios", "updated_at", "DATETIME"},
		{"benchmark_results", "cpu_usage_pct", "REAL"},
	} {
		if err := s.ensureColumn(ctx, change.table, change.col, change.def); err != nil {
			return err
		}
	}

	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_advisory_status ON advisories(status)`,
		`CREATE INDEX IF NOT EXISTS idx_advisory_target_hw ON advisories(target_hardware)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_scenario_name ON scenarios(name)`,
		`CREATE INDEX IF NOT EXISTS idx_scenario_hw_profile ON scenarios(hardware_profile)`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteCentralStore) ensureColumn(ctx context.Context, table, column, definition string) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, definition))
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return nil
	}
	return err
}

// --- Devices ---

func (s *SQLiteCentralStore) UpsertDevice(ctx context.Context, d Device) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO devices (id, hardware_profile, gpu_arch, last_seen)
		 VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(id) DO UPDATE SET
		   last_seen = datetime('now'),
		   gpu_arch = COALESCE(excluded.gpu_arch, devices.gpu_arch),
		   hardware_profile = COALESCE(excluded.hardware_profile, devices.hardware_profile)`,
		d.ID, d.HardwareProfile, d.GPUArch)
	return err
}

func (s *SQLiteCentralStore) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, COALESCE(hardware_profile,''), COALESCE(gpu_arch,''), COALESCE(last_seen,'') FROM devices ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var devs []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.HardwareProfile, &d.GPUArch, &d.LastSeen); err != nil {
			return nil, err
		}
		devs = append(devs, d)
	}
	return devs, rows.Err()
}

// --- Configurations ---

func (s *SQLiteCentralStore) InsertConfiguration(ctx context.Context, c Configuration) error {
	derivedFrom := sql.NullString{}
	if c.DerivedFrom != "" {
		derivedFrom = sql.NullString{String: c.DerivedFrom, Valid: true}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO configurations (id, device_id, hardware, engine_type, engine_version, model, slot, config, config_hash, status, derived_from, tags, source, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.DeviceID, c.Hardware, c.EngineType, c.EngineVersion, c.Model, c.Slot,
		c.Config, c.ConfigHash, c.Status, derivedFrom, c.Tags, c.Source, c.CreatedAt, c.UpdatedAt)
	return err
}

func (s *SQLiteCentralStore) ConfigExistsByHash(ctx context.Context, hash string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM configurations WHERE config_hash = ?`, hash).Scan(&count)
	return count > 0, err
}

func (s *SQLiteCentralStore) QueryConfigurations(ctx context.Context, f ConfigFilter) ([]Configuration, error) {
	query := `SELECT id, COALESCE(device_id,''), hardware, engine_type, COALESCE(engine_version,''), model,
	           COALESCE(slot,''), config, config_hash, COALESCE(status,''), COALESCE(derived_from,''),
	           COALESCE(tags,''), COALESCE(source,''), COALESCE(created_at,''), COALESCE(updated_at,'')
	          FROM configurations WHERE 1=1`
	var args []any
	if f.Hardware != "" {
		query += ` AND hardware = ?`
		args = append(args, f.Hardware)
	}
	if f.Engine != "" {
		query += ` AND engine_type = ?`
		args = append(args, f.Engine)
	}
	if f.Model != "" {
		query += ` AND model = ?`
		args = append(args, f.Model)
	}
	if f.Status != "" {
		query += ` AND status = ?`
		args = append(args, f.Status)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var configs []Configuration
	for rows.Next() {
		var c Configuration
		var derivedFrom sql.NullString
		if err := rows.Scan(&c.ID, &c.DeviceID, &c.Hardware, &c.EngineType, &c.EngineVersion,
			&c.Model, &c.Slot, &c.Config, &c.ConfigHash, &c.Status, &derivedFrom,
			&c.Tags, &c.Source, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if derivedFrom.Valid {
			c.DerivedFrom = derivedFrom.String
		}
		configs = append(configs, c)
	}
	return configs, rows.Err()
}

func (s *SQLiteCentralStore) ListConfigurationsForSync(ctx context.Context, f SyncFilter) ([]Configuration, error) {
	query := `SELECT id, COALESCE(device_id,''), hardware, engine_type, COALESCE(engine_version,''), model,
	           COALESCE(slot,''), config, config_hash, COALESCE(status,''), COALESCE(derived_from,''),
	           COALESCE(tags,''), COALESCE(source,''), COALESCE(created_at,''), COALESCE(updated_at,'')
	          FROM configurations WHERE 1=1`
	var args []any
	if f.Since != "" {
		query += ` AND created_at > ?`
		args = append(args, f.Since)
	}
	if f.Hardware != "" {
		query += ` AND hardware = ?`
		args = append(args, f.Hardware)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	query += ` ORDER BY created_at ASC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var configs []Configuration
	for rows.Next() {
		var c Configuration
		var derivedFrom sql.NullString
		if err := rows.Scan(&c.ID, &c.DeviceID, &c.Hardware, &c.EngineType, &c.EngineVersion,
			&c.Model, &c.Slot, &c.Config, &c.ConfigHash, &c.Status, &derivedFrom,
			&c.Tags, &c.Source, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if derivedFrom.Valid {
			c.DerivedFrom = derivedFrom.String
		}
		configs = append(configs, c)
	}
	return configs, rows.Err()
}

// --- Benchmarks ---

func (s *SQLiteCentralStore) InsertBenchmark(ctx context.Context, b BenchmarkResult) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO benchmark_results (id, config_id, device_id, concurrency, input_len_bucket, output_len_bucket, modality,
		 throughput_tps, ttft_p50_ms, ttft_p95_ms, ttft_p99_ms, tpot_p50_ms, tpot_p95_ms, qps, vram_usage_mib, ram_usage_mib,
		 power_draw_watts, gpu_utilization_pct, cpu_usage_pct, error_rate, oom_occurred, stability, duration_s, sample_count, tested_at, agent_model, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.ConfigID, b.DeviceID, b.Concurrency, b.InputLenBucket, b.OutputLenBucket, b.Modality,
		b.ThroughputTPS, b.TTFTP50ms, b.TTFTP95ms, b.TTFTP99ms, b.TPOTP50ms, b.TPOTP95ms, b.QPS, b.VRAMUsageMiB, b.RAMUsageMiB,
		b.PowerDrawWatts, b.GPUUtilPct, b.CPUUsagePct, b.ErrorRate, b.OOMOccurred, b.Stability, b.DurationS, b.SampleCount, b.TestedAt, b.AgentModel, b.Notes)
	return err
}

func (s *SQLiteCentralStore) ListBenchmarksForSync(ctx context.Context, configIDs []string, since string) ([]BenchmarkResult, error) {
	query := `SELECT id, config_id, COALESCE(device_id,''), COALESCE(concurrency,0),
	           COALESCE(input_len_bucket,''), COALESCE(output_len_bucket,''), COALESCE(modality,''),
	           COALESCE(throughput_tps,0), COALESCE(ttft_p50_ms,0), COALESCE(ttft_p95_ms,0), COALESCE(ttft_p99_ms,0),
	           COALESCE(tpot_p50_ms,0), COALESCE(tpot_p95_ms,0), COALESCE(qps,0),
	           COALESCE(vram_usage_mib,0), COALESCE(ram_usage_mib,0), COALESCE(power_draw_watts,0),
	           COALESCE(gpu_utilization_pct,0), COALESCE(cpu_usage_pct,0), COALESCE(error_rate,0), COALESCE(oom_occurred,0),
	           COALESCE(stability,''), COALESCE(duration_s,0), COALESCE(sample_count,0),
	           COALESCE(agent_model,''), COALESCE(notes,''), COALESCE(tested_at,'')
	          FROM benchmark_results WHERE 1=1`
	var args []any
	var conditions []string
	if len(configIDs) > 0 {
		placeholders := strings.Repeat("?,", len(configIDs))
		placeholders = placeholders[:len(placeholders)-1]
		conditions = append(conditions, fmt.Sprintf("config_id IN (%s)", placeholders))
		for _, id := range configIDs {
			args = append(args, id)
		}
	}
	if since != "" {
		conditions = append(conditions, "tested_at > ?")
		args = append(args, since)
	}
	if len(conditions) > 0 {
		query += " AND (" + strings.Join(conditions, " OR ") + ")"
	}
	query += " ORDER BY tested_at ASC LIMIT 1000"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBenchmarkRows(rows)
}

func (s *SQLiteCentralStore) QueryBenchmarks(ctx context.Context, f BenchmarkFilter) ([]BenchmarkResult, error) {
	query := `SELECT br.id, br.config_id, COALESCE(br.device_id,''), COALESCE(br.concurrency,0),
	           COALESCE(br.input_len_bucket,''), COALESCE(br.output_len_bucket,''), COALESCE(br.modality,''),
	           COALESCE(br.throughput_tps,0), COALESCE(br.ttft_p50_ms,0), COALESCE(br.ttft_p95_ms,0), COALESCE(br.ttft_p99_ms,0),
	           COALESCE(br.tpot_p50_ms,0), COALESCE(br.tpot_p95_ms,0), COALESCE(br.qps,0),
	           COALESCE(br.vram_usage_mib,0), COALESCE(br.ram_usage_mib,0), COALESCE(br.power_draw_watts,0),
	           COALESCE(br.gpu_utilization_pct,0), COALESCE(br.cpu_usage_pct,0), COALESCE(br.error_rate,0), COALESCE(br.oom_occurred,0),
	           COALESCE(br.stability,''), COALESCE(br.duration_s,0), COALESCE(br.sample_count,0),
	           COALESCE(br.agent_model,''), COALESCE(br.notes,''), COALESCE(br.tested_at,'')
	          FROM benchmark_results br`
	var args []any
	var wheres []string

	if f.ConfigID != "" {
		wheres = append(wheres, "br.config_id = ?")
		args = append(args, f.ConfigID)
	}
	if f.Hardware != "" || f.Model != "" {
		query += " JOIN configurations c ON br.config_id = c.id"
		if f.Hardware != "" {
			wheres = append(wheres, "c.hardware = ?")
			args = append(args, f.Hardware)
		}
		if f.Model != "" {
			wheres = append(wheres, "c.model = ?")
			args = append(args, f.Model)
		}
	}
	if len(wheres) > 0 {
		query += " WHERE " + strings.Join(wheres, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query += fmt.Sprintf(" ORDER BY br.tested_at DESC LIMIT %d", limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBenchmarkRows(rows)
}

func scanBenchmarkRows(rows *sql.Rows) ([]BenchmarkResult, error) {
	var results []BenchmarkResult
	for rows.Next() {
		var b BenchmarkResult
		if err := rows.Scan(&b.ID, &b.ConfigID, &b.DeviceID, &b.Concurrency,
			&b.InputLenBucket, &b.OutputLenBucket, &b.Modality,
			&b.ThroughputTPS, &b.TTFTP50ms, &b.TTFTP95ms, &b.TTFTP99ms,
			&b.TPOTP50ms, &b.TPOTP95ms, &b.QPS, &b.VRAMUsageMiB, &b.RAMUsageMiB,
			&b.PowerDrawWatts, &b.GPUUtilPct, &b.CPUUsagePct, &b.ErrorRate, &b.OOMOccurred,
			&b.Stability, &b.DurationS, &b.SampleCount,
			&b.AgentModel, &b.Notes, &b.TestedAt); err != nil {
			return nil, err
		}
		results = append(results, b)
	}
	return results, rows.Err()
}

// --- Knowledge Notes ---

func (s *SQLiteCentralStore) UpsertKnowledgeNote(ctx context.Context, n KnowledgeNote) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO knowledge_notes (id, title, tags, hardware_profile, model, engine, content, confidence, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.Title, n.Tags, n.HardwareProfile, n.Model, n.Engine, n.Content, n.Confidence, n.CreatedAt)
	return err
}

func (s *SQLiteCentralStore) ListKnowledgeNotes(ctx context.Context) ([]KnowledgeNote, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, COALESCE(tags,''), COALESCE(hardware_profile,''), COALESCE(model,''),
		 COALESCE(engine,''), content, COALESCE(confidence,''), COALESCE(created_at,'')
		 FROM knowledge_notes ORDER BY created_at ASC LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notes []KnowledgeNote
	for rows.Next() {
		var n KnowledgeNote
		if err := rows.Scan(&n.ID, &n.Title, &n.Tags, &n.HardwareProfile, &n.Model, &n.Engine, &n.Content, &n.Confidence, &n.CreatedAt); err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	return notes, rows.Err()
}

// --- Advisories ---

func (s *SQLiteCentralStore) InsertAdvisory(ctx context.Context, a Advisory) error {
	a = normalizeAdvisory(a)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO advisories (
		 id, type, status, severity, target_hardware, target_model, target_engine,
		 content_json, reasoning, based_on_json, hardware, model, engine,
		 title, summary, details, confidence, analysis_id, feedback, accepted,
		 created_at, delivered_at, validated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Type, a.Status, a.Severity, a.TargetHardware, a.TargetModel, a.TargetEngine,
		string(a.ContentJSON), a.Reasoning, string(a.BasedOnJSON), a.Hardware, a.Model, a.Engine,
		a.Title, a.Summary, a.Details, a.Confidence, a.AnalysisID, a.Feedback, a.Accepted,
		a.CreatedAt, nullString(a.DeliveredAt), nullString(a.ValidatedAt))
	return err
}

func (s *SQLiteCentralStore) ListAdvisories(ctx context.Context, f AdvisoryFilter) ([]Advisory, error) {
	query := `SELECT id, type, COALESCE(status,''), COALESCE(severity,''),
	           COALESCE(target_hardware, hardware, ''), COALESCE(target_model, model, ''), COALESCE(target_engine, engine, ''),
	           COALESCE(content_json, details, ''), COALESCE(reasoning, summary, ''), COALESCE(confidence, ''),
	           COALESCE(based_on_json, '[]'), COALESCE(analysis_id, ''), COALESCE(created_at, ''),
	           COALESCE(delivered_at, ''), COALESCE(validated_at, ''),
	           COALESCE(title, ''), COALESCE(summary, ''), COALESCE(hardware, ''), COALESCE(model, ''),
	           COALESCE(engine, ''), COALESCE(details, ''), COALESCE(feedback, ''), COALESCE(accepted, 0)
	          FROM advisories WHERE 1=1`
	var args []any
	if f.ID != "" {
		query += ` AND id = ?`
		args = append(args, f.ID)
	}
	if f.Type != "" {
		query += ` AND type = ?`
		args = append(args, f.Type)
	}
	if f.Status != "" {
		query += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.Severity != "" {
		query += ` AND severity = ?`
		args = append(args, f.Severity)
	}
	if f.Hardware != "" {
		query += ` AND COALESCE(target_hardware, hardware, '') = ?`
		args = append(args, f.Hardware)
	}
	if f.Model != "" {
		query += ` AND COALESCE(target_model, model, '') = ?`
		args = append(args, f.Model)
	}
	if f.Engine != "" {
		query += ` AND COALESCE(target_engine, engine, '') = ?`
		args = append(args, f.Engine)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query += ` ORDER BY datetime(created_at) DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var advs []Advisory
	for rows.Next() {
		var a Advisory
		var contentJSON string
		var basedOnJSON string
		if err := rows.Scan(
			&a.ID, &a.Type, &a.Status, &a.Severity,
			&a.TargetHardware, &a.TargetModel, &a.TargetEngine,
			&contentJSON, &a.Reasoning, &a.Confidence,
			&basedOnJSON, &a.AnalysisID, &a.CreatedAt,
			&a.DeliveredAt, &a.ValidatedAt,
			&a.Title, &a.Summary, &a.Hardware, &a.Model,
			&a.Engine, &a.Details, &a.Feedback, &a.Accepted,
		); err != nil {
			return nil, err
		}
		if contentJSON != "" {
			a.ContentJSON = []byte(contentJSON)
		}
		if basedOnJSON != "" {
			a.BasedOnJSON = []byte(basedOnJSON)
		}
		advs = append(advs, normalizeAdvisory(a))
	}
	return advs, rows.Err()
}

func (s *SQLiteCentralStore) UpdateAdvisoryStatus(ctx context.Context, id string, update AdvisoryStatusUpdate) error {
	current, err := s.getAdvisory(ctx, id)
	if err != nil {
		return err
	}
	if !advisoryTransitionAllowed(current.Status, update.Status) {
		return fmt.Errorf("advisory %q cannot transition from %s to %s", id, current.Status, update.Status)
	}

	status := current.Status
	if update.Status != "" {
		status = update.Status
	}
	feedback := current.Feedback
	if update.Feedback != "" {
		feedback = update.Feedback
	}
	deliveredAt := current.DeliveredAt
	if update.DeliveredAt != "" {
		deliveredAt = update.DeliveredAt
	}
	validatedAt := current.ValidatedAt
	if update.ValidatedAt != "" {
		validatedAt = update.ValidatedAt
	}
	if status == AdvisoryStatusDelivered && deliveredAt == "" {
		deliveredAt = nowRFC3339()
	}
	if (status == AdvisoryStatusValidated || status == AdvisoryStatusRejected) && validatedAt == "" {
		validatedAt = nowRFC3339()
	}
	accepted := status == AdvisoryStatusValidated

	res, err := s.db.ExecContext(ctx,
		`UPDATE advisories
		    SET status = ?, feedback = ?, accepted = ?, delivered_at = ?, validated_at = ?
		  WHERE id = ?`,
		status, feedback, accepted, nullString(deliveredAt), nullString(validatedAt), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("advisory %q not found", id)
	}
	return nil
}

func (s *SQLiteCentralStore) ExpireAdvisories(ctx context.Context, before time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE advisories
		    SET status = ?
		  WHERE status IN (?, ?)
		    AND datetime(created_at) < datetime(?)`,
		AdvisoryStatusExpired, AdvisoryStatusPending, AdvisoryStatusDelivered, before.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// --- Analysis Runs ---

func (s *SQLiteCentralStore) InsertAnalysisRun(ctx context.Context, r AnalysisRun) error {
	r = normalizeAnalysisRun(r)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO analysis_runs (
		 id, type, status, summary, input_json, output_json, advisories,
		 advisory_count, duration_ms, error, started_at, completed_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Type, r.Status, r.Summary, string(r.InputJSON), string(r.OutputJSON), string(r.Advisories),
		r.AdvisoryCount, r.DurationMs, r.Error, r.StartedAt, nullString(r.CompletedAt), r.CreatedAt, r.UpdatedAt)
	return err
}

func (s *SQLiteCentralStore) UpdateAnalysisRun(ctx context.Context, id string, update AnalysisRunUpdate) error {
	current, err := s.getAnalysisRun(ctx, id)
	if err != nil {
		return err
	}

	if update.Status != "" {
		current.Status = update.Status
	}
	if update.Summary != "" {
		current.Summary = update.Summary
	}
	if len(update.InputJSON) > 0 {
		current.InputJSON = update.InputJSON
	}
	if len(update.OutputJSON) > 0 {
		current.OutputJSON = update.OutputJSON
	}
	if len(update.Advisories) > 0 {
		current.Advisories = update.Advisories
	}
	if update.AdvisoryCount != 0 || len(current.Advisories) == 0 {
		current.AdvisoryCount = update.AdvisoryCount
	}
	if update.DurationMs != 0 {
		current.DurationMs = update.DurationMs
	}
	if update.Error != "" {
		current.Error = update.Error
	}
	if update.StartedAt != "" {
		current.StartedAt = update.StartedAt
	}
	if update.CompletedAt != "" {
		current.CompletedAt = update.CompletedAt
	}
	current.UpdatedAt = nowRFC3339()
	current = normalizeAnalysisRun(current)

	res, err := s.db.ExecContext(ctx,
		`UPDATE analysis_runs
		    SET status = ?, summary = ?, input_json = ?, output_json = ?, advisories = ?,
		        advisory_count = ?, duration_ms = ?, error = ?, started_at = ?, completed_at = ?, updated_at = ?
		  WHERE id = ?`,
		current.Status, current.Summary, string(current.InputJSON), string(current.OutputJSON), string(current.Advisories),
		current.AdvisoryCount, current.DurationMs, current.Error, current.StartedAt, nullString(current.CompletedAt), current.UpdatedAt, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("analysis run %q not found", id)
	}
	return nil
}

func (s *SQLiteCentralStore) ListAnalysisRuns(ctx context.Context, limit int) ([]AnalysisRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, status, COALESCE(summary,''), COALESCE(input_json,''), COALESCE(output_json,''),
		 COALESCE(advisories,''), COALESCE(advisory_count,0), COALESCE(duration_ms,0), COALESCE(error,''),
		 COALESCE(started_at, created_at, ''), COALESCE(completed_at, ''), COALESCE(created_at, ''), COALESCE(updated_at, '')
		 FROM analysis_runs ORDER BY datetime(COALESCE(started_at, created_at)) DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []AnalysisRun
	for rows.Next() {
		var r AnalysisRun
		var inputJSON string
		var outputJSON string
		var advisories string
		if err := rows.Scan(&r.ID, &r.Type, &r.Status, &r.Summary, &inputJSON, &outputJSON, &advisories,
			&r.AdvisoryCount, &r.DurationMs, &r.Error, &r.StartedAt, &r.CompletedAt, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if inputJSON != "" {
			r.InputJSON = []byte(inputJSON)
		}
		if outputJSON != "" {
			r.OutputJSON = []byte(outputJSON)
		}
		if advisories != "" {
			r.Advisories = []byte(advisories)
		}
		runs = append(runs, normalizeAnalysisRun(r))
	}
	return runs, rows.Err()
}

// --- Scenarios ---

func (s *SQLiteCentralStore) InsertScenario(ctx context.Context, s2 Scenario) error {
	s2 = normalizeScenario(s2)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO scenarios (
		 id, name, description, hardware_profile, scenario_yaml, source, advisory_id,
		 version, created_at, updated_at, hardware, models, config
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
		 description = excluded.description,
		 hardware_profile = excluded.hardware_profile,
		 scenario_yaml = excluded.scenario_yaml,
		 source = excluded.source,
		 advisory_id = excluded.advisory_id,
		 version = excluded.version,
		 updated_at = excluded.updated_at,
		 hardware = excluded.hardware,
		 models = excluded.models,
		 config = excluded.config`,
		s2.ID, s2.Name, s2.Description, s2.HardwareProfile, s2.ScenarioYAML, s2.Source, s2.AdvisoryID,
		s2.Version, s2.CreatedAt, s2.UpdatedAt, s2.Hardware, s2.Models, s2.Config)
	return err
}

func (s *SQLiteCentralStore) ListScenarios(ctx context.Context, f ScenarioFilter) ([]Scenario, error) {
	query := `SELECT id, name, COALESCE(description,''), COALESCE(hardware_profile, hardware, ''),
	           COALESCE(scenario_yaml, config, ''), COALESCE(source,''), COALESCE(advisory_id,''),
	           COALESCE(version, 1), COALESCE(created_at,''), COALESCE(updated_at, created_at, ''),
	           COALESCE(hardware,''), COALESCE(models,''), COALESCE(config,'')
	          FROM scenarios WHERE 1=1`
	var args []any
	if f.Name != "" {
		query += ` AND name = ?`
		args = append(args, f.Name)
	}
	if f.Hardware != "" {
		query += ` AND COALESCE(hardware_profile, hardware, '') = ?`
		args = append(args, f.Hardware)
	}
	if f.Source != "" {
		query += ` AND source = ?`
		args = append(args, f.Source)
	}
	if f.AdvisoryID != "" {
		query += ` AND advisory_id = ?`
		args = append(args, f.AdvisoryID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query += ` ORDER BY datetime(COALESCE(updated_at, created_at)) DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var scenarios []Scenario
	for rows.Next() {
		var sc Scenario
		if err := rows.Scan(&sc.ID, &sc.Name, &sc.Description, &sc.HardwareProfile,
			&sc.ScenarioYAML, &sc.Source, &sc.AdvisoryID, &sc.Version,
			&sc.CreatedAt, &sc.UpdatedAt, &sc.Hardware, &sc.Models, &sc.Config); err != nil {
			return nil, err
		}
		scenarios = append(scenarios, normalizeScenario(sc))
	}
	return scenarios, rows.Err()
}

func (s *SQLiteCentralStore) getAdvisory(ctx context.Context, id string) (Advisory, error) {
	advs, err := s.ListAdvisories(ctx, AdvisoryFilter{ID: id, Limit: 1})
	if err != nil {
		return Advisory{}, err
	}
	if len(advs) == 0 {
		return Advisory{}, fmt.Errorf("advisory %q not found", id)
	}
	return advs[0], nil
}

func (s *SQLiteCentralStore) getAnalysisRun(ctx context.Context, id string) (AnalysisRun, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, type, status, COALESCE(summary,''), COALESCE(input_json,''), COALESCE(output_json,''),
		 COALESCE(advisories,''), COALESCE(advisory_count,0), COALESCE(duration_ms,0), COALESCE(error,''),
		 COALESCE(started_at, created_at, ''), COALESCE(completed_at, ''), COALESCE(created_at, ''), COALESCE(updated_at, '')
		 FROM analysis_runs WHERE id = ?`, id)

	var run AnalysisRun
	var inputJSON string
	var outputJSON string
	var advisories string
	if err := row.Scan(&run.ID, &run.Type, &run.Status, &run.Summary, &inputJSON, &outputJSON,
		&advisories, &run.AdvisoryCount, &run.DurationMs, &run.Error, &run.StartedAt,
		&run.CompletedAt, &run.CreatedAt, &run.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return AnalysisRun{}, fmt.Errorf("analysis run %q not found", id)
		}
		return AnalysisRun{}, err
	}
	if inputJSON != "" {
		run.InputJSON = []byte(inputJSON)
	}
	if outputJSON != "" {
		run.OutputJSON = []byte(outputJSON)
	}
	if advisories != "" {
		run.Advisories = []byte(advisories)
	}
	return normalizeAnalysisRun(run), nil
}

func nullString(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

// --- Stats ---

func (s *SQLiteCentralStore) Stats(ctx context.Context) (StoreStats, error) {
	var st StoreStats
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices`).Scan(&st.Devices)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM configurations`).Scan(&st.Configurations)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM benchmark_results`).Scan(&st.Benchmarks)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_notes`).Scan(&st.KnowledgeNotes)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM advisories`).Scan(&st.Advisories)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scenarios`).Scan(&st.Scenarios)
	return st, nil
}

func (s *SQLiteCentralStore) CoverageMatrix(ctx context.Context) ([]CoverageEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT hardware, engine_type, COUNT(DISTINCT model) as models
		 FROM configurations GROUP BY hardware, engine_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []CoverageEntry
	for rows.Next() {
		var e CoverageEntry
		if err := rows.Scan(&e.Hardware, &e.Engine, &e.Models); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
