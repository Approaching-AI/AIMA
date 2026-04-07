package central

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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
    severity TEXT NOT NULL DEFAULT 'info',
    hardware TEXT,
    model TEXT,
    engine TEXT,
    title TEXT NOT NULL,
    summary TEXT,
    details TEXT,
    confidence TEXT,
    analysis_id TEXT,
    feedback TEXT,
    accepted BOOLEAN DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_advisory_type ON advisories(type);

CREATE TABLE IF NOT EXISTS analysis_runs (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running',
    summary TEXT,
    advisory_count INTEGER DEFAULT 0,
    duration_ms INTEGER DEFAULT 0,
    error TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS scenarios (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT,
    hardware TEXT,
    models TEXT,
    config TEXT,
    source TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_scenario_hw ON scenarios(hardware);`

	_, err := s.db.ExecContext(ctx, ddl)
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
	           COALESCE(slot,''), config, config_hash, COALESCE(derived_from,''), COALESCE(status,''),
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
			&c.Model, &c.Slot, &c.Config, &c.ConfigHash, &derivedFrom, &c.Status,
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
		 power_draw_watts, gpu_utilization_pct, error_rate, oom_occurred, stability, duration_s, sample_count, tested_at, agent_model, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.ConfigID, b.DeviceID, b.Concurrency, b.InputLenBucket, b.OutputLenBucket, b.Modality,
		b.ThroughputTPS, b.TTFTP50ms, b.TTFTP95ms, b.TTFTP99ms, b.TPOTP50ms, b.TPOTP95ms, b.QPS, b.VRAMUsageMiB, b.RAMUsageMiB,
		b.PowerDrawWatts, b.GPUUtilPct, b.ErrorRate, b.OOMOccurred, b.Stability, b.DurationS, b.SampleCount, b.TestedAt, b.AgentModel, b.Notes)
	return err
}

func (s *SQLiteCentralStore) ListBenchmarksForSync(ctx context.Context, configIDs []string, since string) ([]BenchmarkResult, error) {
	query := `SELECT id, config_id, COALESCE(device_id,''), COALESCE(concurrency,0),
	           COALESCE(input_len_bucket,''), COALESCE(output_len_bucket,''), COALESCE(modality,''),
	           COALESCE(throughput_tps,0), COALESCE(ttft_p50_ms,0), COALESCE(ttft_p95_ms,0), COALESCE(ttft_p99_ms,0),
	           COALESCE(tpot_p50_ms,0), COALESCE(tpot_p95_ms,0), COALESCE(qps,0),
	           COALESCE(vram_usage_mib,0), COALESCE(ram_usage_mib,0), COALESCE(power_draw_watts,0),
	           COALESCE(gpu_utilization_pct,0), COALESCE(error_rate,0), COALESCE(oom_occurred,0),
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
	           COALESCE(br.gpu_utilization_pct,0), COALESCE(br.error_rate,0), COALESCE(br.oom_occurred,0),
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
			&b.PowerDrawWatts, &b.GPUUtilPct, &b.ErrorRate, &b.OOMOccurred,
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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO advisories (id, type, severity, hardware, model, engine, title, summary, details, confidence, analysis_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Type, a.Severity, a.Hardware, a.Model, a.Engine, a.Title, a.Summary, a.Details, a.Confidence, a.AnalysisID, a.CreatedAt)
	return err
}

func (s *SQLiteCentralStore) ListAdvisories(ctx context.Context, f AdvisoryFilter) ([]Advisory, error) {
	query := `SELECT id, type, severity, COALESCE(hardware,''), COALESCE(model,''), COALESCE(engine,''),
	           title, COALESCE(summary,''), COALESCE(details,''), COALESCE(confidence,''),
	           COALESCE(analysis_id,''), COALESCE(feedback,''), COALESCE(accepted,0), COALESCE(created_at,'')
	          FROM advisories WHERE 1=1`
	var args []any
	if f.Type != "" {
		query += ` AND type = ?`
		args = append(args, f.Type)
	}
	if f.Severity != "" {
		query += ` AND severity = ?`
		args = append(args, f.Severity)
	}
	if f.Hardware != "" {
		query += ` AND hardware = ?`
		args = append(args, f.Hardware)
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
	var advs []Advisory
	for rows.Next() {
		var a Advisory
		if err := rows.Scan(&a.ID, &a.Type, &a.Severity, &a.Hardware, &a.Model, &a.Engine,
			&a.Title, &a.Summary, &a.Details, &a.Confidence, &a.AnalysisID, &a.Feedback, &a.Accepted, &a.CreatedAt); err != nil {
			return nil, err
		}
		advs = append(advs, a)
	}
	return advs, rows.Err()
}

func (s *SQLiteCentralStore) UpdateAdvisoryFeedback(ctx context.Context, id string, feedback string, accepted bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE advisories SET feedback = ?, accepted = ? WHERE id = ?`,
		feedback, accepted, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("advisory %q not found", id)
	}
	return nil
}

// --- Analysis Runs ---

func (s *SQLiteCentralStore) InsertAnalysisRun(ctx context.Context, r AnalysisRun) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO analysis_runs (id, type, status, summary, advisory_count, duration_ms, error, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Type, r.Status, r.Summary, r.AdvisoryCount, r.DurationMs, r.Error, r.CreatedAt)
	return err
}

func (s *SQLiteCentralStore) ListAnalysisRuns(ctx context.Context, limit int) ([]AnalysisRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, status, COALESCE(summary,''), COALESCE(advisory_count,0),
		 COALESCE(duration_ms,0), COALESCE(error,''), COALESCE(created_at,'')
		 FROM analysis_runs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []AnalysisRun
	for rows.Next() {
		var r AnalysisRun
		if err := rows.Scan(&r.ID, &r.Type, &r.Status, &r.Summary, &r.AdvisoryCount, &r.DurationMs, &r.Error, &r.CreatedAt); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// --- Scenarios ---

func (s *SQLiteCentralStore) InsertScenario(ctx context.Context, s2 Scenario) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO scenarios (id, name, description, hardware, models, config, source, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s2.ID, s2.Name, s2.Description, s2.Hardware, s2.Models, s2.Config, s2.Source, s2.CreatedAt)
	return err
}

func (s *SQLiteCentralStore) ListScenarios(ctx context.Context, f ScenarioFilter) ([]Scenario, error) {
	query := `SELECT id, name, COALESCE(description,''), COALESCE(hardware,''),
	           COALESCE(models,''), COALESCE(config,''), COALESCE(source,''), COALESCE(created_at,'')
	          FROM scenarios WHERE 1=1`
	var args []any
	if f.Hardware != "" {
		query += ` AND hardware = ?`
		args = append(args, f.Hardware)
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
	var scenarios []Scenario
	for rows.Next() {
		var sc Scenario
		if err := rows.Scan(&sc.ID, &sc.Name, &sc.Description, &sc.Hardware, &sc.Models, &sc.Config, &sc.Source, &sc.CreatedAt); err != nil {
			return nil, err
		}
		scenarios = append(scenarios, sc)
	}
	return scenarios, rows.Err()
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
