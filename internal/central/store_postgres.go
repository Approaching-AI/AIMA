package central

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresCentralStore implements CentralStore using PostgreSQL via pgx.
type PostgresCentralStore struct {
	db *sql.DB
}

// NewPostgresCentralStore opens a PostgreSQL connection and returns a CentralStore.
func NewPostgresCentralStore(dsn string) (*PostgresCentralStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &PostgresCentralStore{db: db}, nil
}

func (p *PostgresCentralStore) Close() error {
	return p.db.Close()
}

func (p *PostgresCentralStore) DB() *sql.DB {
	return p.db
}

func (p *PostgresCentralStore) Migrate(ctx context.Context) error {
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
    tags TEXT,
    source TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    ingested_at TIMESTAMPTZ DEFAULT NOW()
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
    tested_at TIMESTAMPTZ DEFAULT NOW(),
    ingested_at TIMESTAMPTZ DEFAULT NOW()
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
    created_at TIMESTAMPTZ DEFAULT NOW(),
    ingested_at TIMESTAMPTZ DEFAULT NOW()
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
    accepted BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMPTZ DEFAULT NOW()
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
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS scenarios (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT,
    hardware TEXT,
    models TEXT,
    config JSONB,
    source TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_scenario_hw ON scenarios(hardware);`

	_, err := p.db.ExecContext(ctx, ddl)
	return err
}

// --- Devices ---

func (p *PostgresCentralStore) UpsertDevice(ctx context.Context, d Device) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO devices (id, hardware_profile, gpu_arch, last_seen)
		 VALUES ($1, $2, $3, NOW())
		 ON CONFLICT(id) DO UPDATE SET
		   last_seen = NOW(),
		   gpu_arch = COALESCE(EXCLUDED.gpu_arch, devices.gpu_arch),
		   hardware_profile = COALESCE(EXCLUDED.hardware_profile, devices.hardware_profile)`,
		d.ID, d.HardwareProfile, d.GPUArch)
	return err
}

func (p *PostgresCentralStore) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, COALESCE(hardware_profile,''), COALESCE(gpu_arch,''), COALESCE(last_seen::text,'') FROM devices ORDER BY last_seen DESC`)
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

func (p *PostgresCentralStore) InsertConfiguration(ctx context.Context, c Configuration) error {
	derivedFrom := sql.NullString{}
	if c.DerivedFrom != "" {
		derivedFrom = sql.NullString{String: c.DerivedFrom, Valid: true}
	}
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO configurations (id, device_id, hardware, engine_type, engine_version, model, slot, config, config_hash, status, derived_from, tags, source, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		c.ID, c.DeviceID, c.Hardware, c.EngineType, c.EngineVersion, c.Model, c.Slot,
		c.Config, c.ConfigHash, c.Status, derivedFrom, c.Tags, c.Source, c.CreatedAt, c.UpdatedAt)
	return err
}

func (p *PostgresCentralStore) ConfigExistsByHash(ctx context.Context, hash string) (bool, error) {
	var count int
	err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM configurations WHERE config_hash = $1`, hash).Scan(&count)
	return count > 0, err
}

func (p *PostgresCentralStore) QueryConfigurations(ctx context.Context, f ConfigFilter) ([]Configuration, error) {
	query := `SELECT id, COALESCE(device_id,''), hardware, engine_type, COALESCE(engine_version,''), model,
	           COALESCE(slot,''), config::text, config_hash, COALESCE(status,''), COALESCE(derived_from,''),
	           COALESCE(tags,''), COALESCE(source,''), COALESCE(created_at::text,''), COALESCE(updated_at::text,'')
	          FROM configurations WHERE 1=1`
	var args []any
	n := 1
	if f.Hardware != "" {
		query += fmt.Sprintf(` AND hardware = $%d`, n)
		args = append(args, f.Hardware)
		n++
	}
	if f.Engine != "" {
		query += fmt.Sprintf(` AND engine_type = $%d`, n)
		args = append(args, f.Engine)
		n++
	}
	if f.Model != "" {
		query += fmt.Sprintf(` AND model = $%d`, n)
		args = append(args, f.Model)
		n++
	}
	if f.Status != "" {
		query += fmt.Sprintf(` AND status = $%d`, n)
		args = append(args, f.Status)
		n++
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d`, n)
	args = append(args, limit)

	rows, err := p.db.QueryContext(ctx, query, args...)
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

func (p *PostgresCentralStore) ListConfigurationsForSync(ctx context.Context, f SyncFilter) ([]Configuration, error) {
	query := `SELECT id, COALESCE(device_id,''), hardware, engine_type, COALESCE(engine_version,''), model,
	           COALESCE(slot,''), config::text, config_hash, COALESCE(derived_from,''), COALESCE(status,''),
	           COALESCE(tags,''), COALESCE(source,''), COALESCE(created_at::text,''), COALESCE(updated_at::text,'')
	          FROM configurations WHERE 1=1`
	var args []any
	n := 1
	if f.Since != "" {
		query += fmt.Sprintf(` AND created_at > $%d`, n)
		args = append(args, f.Since)
		n++
	}
	if f.Hardware != "" {
		query += fmt.Sprintf(` AND hardware = $%d`, n)
		args = append(args, f.Hardware)
		n++
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	query += fmt.Sprintf(` ORDER BY created_at ASC LIMIT $%d`, n)
	args = append(args, limit)

	rows, err := p.db.QueryContext(ctx, query, args...)
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

func (p *PostgresCentralStore) InsertBenchmark(ctx context.Context, b BenchmarkResult) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO benchmark_results (id, config_id, device_id, concurrency, input_len_bucket, output_len_bucket, modality,
		 throughput_tps, ttft_p50_ms, ttft_p95_ms, ttft_p99_ms, tpot_p50_ms, tpot_p95_ms, qps, vram_usage_mib, ram_usage_mib,
		 power_draw_watts, gpu_utilization_pct, error_rate, oom_occurred, stability, duration_s, sample_count, tested_at, agent_model, notes)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)
		 ON CONFLICT (id) DO NOTHING`,
		b.ID, b.ConfigID, b.DeviceID, b.Concurrency, b.InputLenBucket, b.OutputLenBucket, b.Modality,
		b.ThroughputTPS, b.TTFTP50ms, b.TTFTP95ms, b.TTFTP99ms, b.TPOTP50ms, b.TPOTP95ms, b.QPS, b.VRAMUsageMiB, b.RAMUsageMiB,
		b.PowerDrawWatts, b.GPUUtilPct, b.ErrorRate, b.OOMOccurred, b.Stability, b.DurationS, b.SampleCount, b.TestedAt, b.AgentModel, b.Notes)
	return err
}

func (p *PostgresCentralStore) ListBenchmarksForSync(ctx context.Context, configIDs []string, since string) ([]BenchmarkResult, error) {
	query := `SELECT id, config_id, COALESCE(device_id,''), COALESCE(concurrency,0),
	           COALESCE(input_len_bucket,''), COALESCE(output_len_bucket,''), COALESCE(modality,''),
	           COALESCE(throughput_tps,0), COALESCE(ttft_p50_ms,0), COALESCE(ttft_p95_ms,0), COALESCE(ttft_p99_ms,0),
	           COALESCE(tpot_p50_ms,0), COALESCE(tpot_p95_ms,0), COALESCE(qps,0),
	           COALESCE(vram_usage_mib,0), COALESCE(ram_usage_mib,0), COALESCE(power_draw_watts,0),
	           COALESCE(gpu_utilization_pct,0), COALESCE(error_rate,0), COALESCE(oom_occurred,false),
	           COALESCE(stability,''), COALESCE(duration_s,0), COALESCE(sample_count,0),
	           COALESCE(agent_model,''), COALESCE(notes,''), COALESCE(tested_at::text,'')
	          FROM benchmark_results WHERE 1=1`
	var args []any
	var conditions []string
	n := 1
	if len(configIDs) > 0 {
		var placeholders []string
		for _, id := range configIDs {
			placeholders = append(placeholders, fmt.Sprintf("$%d", n))
			args = append(args, id)
			n++
		}
		conditions = append(conditions, fmt.Sprintf("config_id IN (%s)", strings.Join(placeholders, ",")))
	}
	if since != "" {
		conditions = append(conditions, fmt.Sprintf("tested_at > $%d", n))
		args = append(args, since)
		n++
	}
	if len(conditions) > 0 {
		query += " AND (" + strings.Join(conditions, " OR ") + ")"
	}
	query += " ORDER BY tested_at ASC LIMIT 1000"

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBenchmarkRows(rows)
}

func (p *PostgresCentralStore) QueryBenchmarks(ctx context.Context, f BenchmarkFilter) ([]BenchmarkResult, error) {
	query := `SELECT br.id, br.config_id, COALESCE(br.device_id,''), COALESCE(br.concurrency,0),
	           COALESCE(br.input_len_bucket,''), COALESCE(br.output_len_bucket,''), COALESCE(br.modality,''),
	           COALESCE(br.throughput_tps,0), COALESCE(br.ttft_p50_ms,0), COALESCE(br.ttft_p95_ms,0), COALESCE(br.ttft_p99_ms,0),
	           COALESCE(br.tpot_p50_ms,0), COALESCE(br.tpot_p95_ms,0), COALESCE(br.qps,0),
	           COALESCE(br.vram_usage_mib,0), COALESCE(br.ram_usage_mib,0), COALESCE(br.power_draw_watts,0),
	           COALESCE(br.gpu_utilization_pct,0), COALESCE(br.error_rate,0), COALESCE(br.oom_occurred,false),
	           COALESCE(br.stability,''), COALESCE(br.duration_s,0), COALESCE(br.sample_count,0),
	           COALESCE(br.agent_model,''), COALESCE(br.notes,''), COALESCE(br.tested_at::text,'')
	          FROM benchmark_results br`
	var args []any
	var wheres []string
	n := 1

	needsJoin := f.Hardware != "" || f.Model != ""
	if needsJoin {
		query += " JOIN configurations c ON br.config_id = c.id"
	}
	if f.ConfigID != "" {
		wheres = append(wheres, fmt.Sprintf("br.config_id = $%d", n))
		args = append(args, f.ConfigID)
		n++
	}
	if f.Hardware != "" {
		wheres = append(wheres, fmt.Sprintf("c.hardware = $%d", n))
		args = append(args, f.Hardware)
		n++
	}
	if f.Model != "" {
		wheres = append(wheres, fmt.Sprintf("c.model = $%d", n))
		args = append(args, f.Model)
		n++
	}
	if len(wheres) > 0 {
		query += " WHERE " + strings.Join(wheres, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query += fmt.Sprintf(" ORDER BY br.tested_at DESC LIMIT %d", limit)

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBenchmarkRows(rows)
}

// --- Knowledge Notes ---

func (p *PostgresCentralStore) UpsertKnowledgeNote(ctx context.Context, n KnowledgeNote) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO knowledge_notes (id, title, tags, hardware_profile, model, engine, content, confidence, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT(id) DO UPDATE SET
		   title = EXCLUDED.title,
		   tags = EXCLUDED.tags,
		   hardware_profile = EXCLUDED.hardware_profile,
		   model = EXCLUDED.model,
		   engine = EXCLUDED.engine,
		   content = EXCLUDED.content,
		   confidence = EXCLUDED.confidence`,
		n.ID, n.Title, n.Tags, n.HardwareProfile, n.Model, n.Engine, n.Content, n.Confidence, n.CreatedAt)
	return err
}

func (p *PostgresCentralStore) ListKnowledgeNotes(ctx context.Context) ([]KnowledgeNote, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, title, COALESCE(tags,''), COALESCE(hardware_profile,''), COALESCE(model,''),
		 COALESCE(engine,''), content, COALESCE(confidence,''), COALESCE(created_at::text,'')
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

func (p *PostgresCentralStore) InsertAdvisory(ctx context.Context, a Advisory) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO advisories (id, type, severity, hardware, model, engine, title, summary, details, confidence, analysis_id, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		a.ID, a.Type, a.Severity, a.Hardware, a.Model, a.Engine, a.Title, a.Summary, a.Details, a.Confidence, a.AnalysisID, a.CreatedAt)
	return err
}

func (p *PostgresCentralStore) ListAdvisories(ctx context.Context, f AdvisoryFilter) ([]Advisory, error) {
	query := `SELECT id, type, severity, COALESCE(hardware,''), COALESCE(model,''), COALESCE(engine,''),
	           title, COALESCE(summary,''), COALESCE(details,''), COALESCE(confidence,''),
	           COALESCE(analysis_id,''), COALESCE(feedback,''), COALESCE(accepted,false), COALESCE(created_at::text,'')
	          FROM advisories WHERE 1=1`
	var args []any
	n := 1
	if f.Type != "" {
		query += fmt.Sprintf(` AND type = $%d`, n)
		args = append(args, f.Type)
		n++
	}
	if f.Severity != "" {
		query += fmt.Sprintf(` AND severity = $%d`, n)
		args = append(args, f.Severity)
		n++
	}
	if f.Hardware != "" {
		query += fmt.Sprintf(` AND hardware = $%d`, n)
		args = append(args, f.Hardware)
		n++
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d`, n)
	args = append(args, limit)

	rows, err := p.db.QueryContext(ctx, query, args...)
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

func (p *PostgresCentralStore) UpdateAdvisoryFeedback(ctx context.Context, id string, feedback string, accepted bool) error {
	res, err := p.db.ExecContext(ctx,
		`UPDATE advisories SET feedback = $1, accepted = $2 WHERE id = $3`,
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

func (p *PostgresCentralStore) InsertAnalysisRun(ctx context.Context, r AnalysisRun) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO analysis_runs (id, type, status, summary, advisory_count, duration_ms, error, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		r.ID, r.Type, r.Status, r.Summary, r.AdvisoryCount, r.DurationMs, r.Error, r.CreatedAt)
	return err
}

func (p *PostgresCentralStore) ListAnalysisRuns(ctx context.Context, limit int) ([]AnalysisRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, type, status, COALESCE(summary,''), COALESCE(advisory_count,0),
		 COALESCE(duration_ms,0), COALESCE(error,''), COALESCE(created_at::text,'')
		 FROM analysis_runs ORDER BY created_at DESC LIMIT $1`, limit)
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

func (p *PostgresCentralStore) InsertScenario(ctx context.Context, s Scenario) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO scenarios (id, name, description, hardware, models, config, source, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		s.ID, s.Name, s.Description, s.Hardware, s.Models, s.Config, s.Source, s.CreatedAt)
	return err
}

func (p *PostgresCentralStore) ListScenarios(ctx context.Context, f ScenarioFilter) ([]Scenario, error) {
	query := `SELECT id, name, COALESCE(description,''), COALESCE(hardware,''),
	           COALESCE(models,''), COALESCE(config::text,''), COALESCE(source,''), COALESCE(created_at::text,'')
	          FROM scenarios WHERE 1=1`
	var args []any
	n := 1
	if f.Hardware != "" {
		query += fmt.Sprintf(` AND hardware = $%d`, n)
		args = append(args, f.Hardware)
		n++
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d`, n)
	args = append(args, limit)

	rows, err := p.db.QueryContext(ctx, query, args...)
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

func (p *PostgresCentralStore) Stats(ctx context.Context) (StoreStats, error) {
	var st StoreStats
	_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices`).Scan(&st.Devices)
	_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM configurations`).Scan(&st.Configurations)
	_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM benchmark_results`).Scan(&st.Benchmarks)
	_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_notes`).Scan(&st.KnowledgeNotes)
	_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM advisories`).Scan(&st.Advisories)
	_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scenarios`).Scan(&st.Scenarios)
	return st, nil
}

func (p *PostgresCentralStore) CoverageMatrix(ctx context.Context) ([]CoverageEntry, error) {
	rows, err := p.db.QueryContext(ctx,
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
