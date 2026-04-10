package central

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

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
    status TEXT NOT NULL DEFAULT 'pending',
    severity TEXT NOT NULL DEFAULT 'info',
    target_hardware TEXT,
    target_model TEXT,
    target_engine TEXT,
    content_json JSONB,
    reasoning TEXT,
    based_on_json JSONB,
    hardware TEXT,
    model TEXT,
    engine TEXT,
    title TEXT,
    summary TEXT,
    details TEXT,
    confidence TEXT,
    analysis_id TEXT,
    feedback TEXT,
    accepted BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    delivered_at TIMESTAMPTZ,
    validated_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_advisory_type ON advisories(type);
CREATE INDEX IF NOT EXISTS idx_advisory_status ON advisories(status);
CREATE INDEX IF NOT EXISTS idx_advisory_target_hw ON advisories(target_hardware);

CREATE TABLE IF NOT EXISTS analysis_runs (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running',
    summary TEXT,
    input_json JSONB,
    output_json JSONB,
    advisories JSONB,
    advisory_count INTEGER DEFAULT 0,
    duration_ms INTEGER DEFAULT 0,
    error TEXT,
    started_at TIMESTAMPTZ DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
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
    config JSONB,
    source TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_scenario_name ON scenarios(name);
CREATE INDEX IF NOT EXISTS idx_scenario_hw ON scenarios(hardware);
CREATE INDEX IF NOT EXISTS idx_scenario_hw_profile ON scenarios(hardware_profile);`

	if _, err := p.db.ExecContext(ctx, ddl); err != nil {
		return err
	}
	for _, stmt := range []string{
		`ALTER TABLE advisories ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending'`,
		`ALTER TABLE advisories ADD COLUMN IF NOT EXISTS target_hardware TEXT`,
		`ALTER TABLE advisories ADD COLUMN IF NOT EXISTS target_model TEXT`,
		`ALTER TABLE advisories ADD COLUMN IF NOT EXISTS target_engine TEXT`,
		`ALTER TABLE advisories ADD COLUMN IF NOT EXISTS content_json JSONB`,
		`ALTER TABLE advisories ADD COLUMN IF NOT EXISTS reasoning TEXT`,
		`ALTER TABLE advisories ADD COLUMN IF NOT EXISTS based_on_json JSONB`,
		`ALTER TABLE advisories ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ`,
		`ALTER TABLE advisories ADD COLUMN IF NOT EXISTS validated_at TIMESTAMPTZ`,
		`ALTER TABLE analysis_runs ADD COLUMN IF NOT EXISTS input_json JSONB`,
		`ALTER TABLE analysis_runs ADD COLUMN IF NOT EXISTS output_json JSONB`,
		`ALTER TABLE analysis_runs ADD COLUMN IF NOT EXISTS advisories JSONB`,
		`ALTER TABLE analysis_runs ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ`,
		`ALTER TABLE analysis_runs ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ`,
		`ALTER TABLE analysis_runs ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ`,
		`ALTER TABLE scenarios ADD COLUMN IF NOT EXISTS hardware_profile TEXT`,
		`ALTER TABLE scenarios ADD COLUMN IF NOT EXISTS scenario_yaml TEXT`,
		`ALTER TABLE scenarios ADD COLUMN IF NOT EXISTS advisory_id TEXT`,
		`ALTER TABLE scenarios ADD COLUMN IF NOT EXISTS version INTEGER DEFAULT 1`,
		`ALTER TABLE scenarios ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ`,
		`ALTER TABLE benchmark_results ADD COLUMN IF NOT EXISTS cpu_usage_pct REAL`,
		`CREATE INDEX IF NOT EXISTS idx_advisory_status ON advisories(status)`,
		`CREATE INDEX IF NOT EXISTS idx_advisory_target_hw ON advisories(target_hardware)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_scenario_name ON scenarios(name)`,
		`CREATE INDEX IF NOT EXISTS idx_scenario_hw_profile ON scenarios(hardware_profile)`,
	} {
		if _, err := p.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
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
	           COALESCE(slot,''), config::text, config_hash, COALESCE(status,''), COALESCE(derived_from,''),
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

func (p *PostgresCentralStore) InsertBenchmark(ctx context.Context, b BenchmarkResult) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO benchmark_results (id, config_id, device_id, concurrency, input_len_bucket, output_len_bucket, modality,
		 throughput_tps, ttft_p50_ms, ttft_p95_ms, ttft_p99_ms, tpot_p50_ms, tpot_p95_ms, qps, vram_usage_mib, ram_usage_mib,
		 power_draw_watts, gpu_utilization_pct, cpu_usage_pct, error_rate, oom_occurred, stability, duration_s, sample_count, tested_at, agent_model, notes)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)
		 ON CONFLICT (id) DO NOTHING`,
		b.ID, b.ConfigID, b.DeviceID, b.Concurrency, b.InputLenBucket, b.OutputLenBucket, b.Modality,
		b.ThroughputTPS, b.TTFTP50ms, b.TTFTP95ms, b.TTFTP99ms, b.TPOTP50ms, b.TPOTP95ms, b.QPS, b.VRAMUsageMiB, b.RAMUsageMiB,
		b.PowerDrawWatts, b.GPUUtilPct, b.CPUUsagePct, b.ErrorRate, b.OOMOccurred, b.Stability, b.DurationS, b.SampleCount, b.TestedAt, b.AgentModel, b.Notes)
	return err
}

func (p *PostgresCentralStore) ListBenchmarksForSync(ctx context.Context, configIDs []string, since string) ([]BenchmarkResult, error) {
	query := `SELECT id, config_id, COALESCE(device_id,''), COALESCE(concurrency,0),
	           COALESCE(input_len_bucket,''), COALESCE(output_len_bucket,''), COALESCE(modality,''),
	           COALESCE(throughput_tps,0), COALESCE(ttft_p50_ms,0), COALESCE(ttft_p95_ms,0), COALESCE(ttft_p99_ms,0),
	           COALESCE(tpot_p50_ms,0), COALESCE(tpot_p95_ms,0), COALESCE(qps,0),
	           COALESCE(vram_usage_mib,0), COALESCE(ram_usage_mib,0), COALESCE(power_draw_watts,0),
	           COALESCE(gpu_utilization_pct,0), COALESCE(cpu_usage_pct,0), COALESCE(error_rate,0), COALESCE(oom_occurred,false),
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
	           COALESCE(br.gpu_utilization_pct,0), COALESCE(br.cpu_usage_pct,0), COALESCE(br.error_rate,0), COALESCE(br.oom_occurred,false),
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
	a = normalizeAdvisory(a)
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO advisories (
		 id, type, status, severity, target_hardware, target_model, target_engine,
		 content_json, reasoning, based_on_json, hardware, model, engine,
		 title, summary, details, confidence, analysis_id, feedback, accepted,
		 created_at, delivered_at, validated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::jsonb, $9, NULLIF($10, '')::jsonb, $11, $12, $13,
		          $14, $15, $16, $17, $18, $19, $20, $21, NULLIF($22, '')::timestamptz, NULLIF($23, '')::timestamptz)`,
		a.ID, a.Type, a.Status, a.Severity, a.TargetHardware, a.TargetModel, a.TargetEngine,
		string(a.ContentJSON), a.Reasoning, string(a.BasedOnJSON), a.Hardware, a.Model, a.Engine,
		a.Title, a.Summary, a.Details, a.Confidence, a.AnalysisID, a.Feedback, a.Accepted,
		a.CreatedAt, a.DeliveredAt, a.ValidatedAt)
	return err
}

func (p *PostgresCentralStore) ListAdvisories(ctx context.Context, f AdvisoryFilter) ([]Advisory, error) {
	query := `SELECT id, type, COALESCE(status,''), COALESCE(severity,''),
	           COALESCE(target_hardware, hardware, ''), COALESCE(target_model, model, ''), COALESCE(target_engine, engine, ''),
	           COALESCE(content_json::text, details, ''), COALESCE(reasoning, summary, ''), COALESCE(confidence, ''),
	           COALESCE(based_on_json::text, '[]'), COALESCE(analysis_id, ''), COALESCE(created_at::text, ''),
	           COALESCE(delivered_at::text, ''), COALESCE(validated_at::text, ''),
	           COALESCE(title, ''), COALESCE(summary, ''), COALESCE(hardware, ''), COALESCE(model, ''),
	           COALESCE(engine, ''), COALESCE(details, ''), COALESCE(feedback, ''), COALESCE(accepted, false)
	          FROM advisories WHERE 1=1`
	var args []any
	n := 1
	if f.ID != "" {
		query += fmt.Sprintf(` AND id = $%d`, n)
		args = append(args, f.ID)
		n++
	}
	if f.Type != "" {
		query += fmt.Sprintf(` AND type = $%d`, n)
		args = append(args, f.Type)
		n++
	}
	if f.Status != "" {
		query += fmt.Sprintf(` AND status = $%d`, n)
		args = append(args, f.Status)
		n++
	}
	if f.Severity != "" {
		query += fmt.Sprintf(` AND severity = $%d`, n)
		args = append(args, f.Severity)
		n++
	}
	if f.Hardware != "" {
		query += fmt.Sprintf(` AND COALESCE(target_hardware, hardware, '') = $%d`, n)
		args = append(args, f.Hardware)
		n++
	}
	if f.Model != "" {
		query += fmt.Sprintf(` AND COALESCE(target_model, model, '') = $%d`, n)
		args = append(args, f.Model)
		n++
	}
	if f.Engine != "" {
		query += fmt.Sprintf(` AND COALESCE(target_engine, engine, '') = $%d`, n)
		args = append(args, f.Engine)
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

func (p *PostgresCentralStore) UpdateAdvisoryStatus(ctx context.Context, id string, update AdvisoryStatusUpdate) error {
	current, err := p.getAdvisory(ctx, id)
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

	res, err := p.db.ExecContext(ctx,
		`UPDATE advisories
		    SET status = $1, feedback = $2, accepted = $3,
		        delivered_at = NULLIF($4, '')::timestamptz,
		        validated_at = NULLIF($5, '')::timestamptz
		  WHERE id = $6`,
		status, feedback, accepted, deliveredAt, validatedAt, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("advisory %q not found", id)
	}
	return nil
}

func (p *PostgresCentralStore) ExpireAdvisories(ctx context.Context, before time.Time) (int, error) {
	res, err := p.db.ExecContext(ctx,
		`UPDATE advisories
		    SET status = $1
		  WHERE status IN ($2, $3)
		    AND created_at < $4`,
		AdvisoryStatusExpired, AdvisoryStatusPending, AdvisoryStatusDelivered, before.UTC())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// --- Analysis Runs ---

func (p *PostgresCentralStore) InsertAnalysisRun(ctx context.Context, r AnalysisRun) error {
	r = normalizeAnalysisRun(r)
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO analysis_runs (
		 id, type, status, summary, input_json, output_json, advisories,
		 advisory_count, duration_ms, error, started_at, completed_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, NULLIF($5, '')::jsonb, NULLIF($6, '')::jsonb, NULLIF($7, '')::jsonb,
		          $8, $9, $10, $11, NULLIF($12, '')::timestamptz, $13, $14)`,
		r.ID, r.Type, r.Status, r.Summary, string(r.InputJSON), string(r.OutputJSON), string(r.Advisories),
		r.AdvisoryCount, r.DurationMs, r.Error, r.StartedAt, r.CompletedAt, r.CreatedAt, r.UpdatedAt)
	return err
}

func (p *PostgresCentralStore) UpdateAnalysisRun(ctx context.Context, id string, update AnalysisRunUpdate) error {
	current, err := p.getAnalysisRun(ctx, id)
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

	res, err := p.db.ExecContext(ctx,
		`UPDATE analysis_runs
		    SET status = $1, summary = $2, input_json = NULLIF($3, '')::jsonb, output_json = NULLIF($4, '')::jsonb,
		        advisories = NULLIF($5, '')::jsonb, advisory_count = $6, duration_ms = $7, error = $8,
		        started_at = $9, completed_at = NULLIF($10, '')::timestamptz, updated_at = $11
		  WHERE id = $12`,
		current.Status, current.Summary, string(current.InputJSON), string(current.OutputJSON), string(current.Advisories),
		current.AdvisoryCount, current.DurationMs, current.Error, current.StartedAt, current.CompletedAt, current.UpdatedAt, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("analysis run %q not found", id)
	}
	return nil
}

func (p *PostgresCentralStore) ListAnalysisRuns(ctx context.Context, limit int) ([]AnalysisRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, type, status, COALESCE(summary,''), COALESCE(input_json::text,''), COALESCE(output_json::text,''),
		 COALESCE(advisories::text,''), COALESCE(advisory_count,0), COALESCE(duration_ms,0), COALESCE(error,''),
		 COALESCE(started_at::text, created_at::text, ''), COALESCE(completed_at::text, ''), COALESCE(created_at::text, ''), COALESCE(updated_at::text, '')
		 FROM analysis_runs ORDER BY COALESCE(started_at, created_at) DESC LIMIT $1`, limit)
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

func (p *PostgresCentralStore) InsertScenario(ctx context.Context, s Scenario) error {
	s = normalizeScenario(s)
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO scenarios (
		 id, name, description, hardware_profile, scenario_yaml, source, advisory_id,
		 version, created_at, updated_at, hardware, models, config
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULLIF($13, '')::jsonb)
		ON CONFLICT(name) DO UPDATE SET
		 description = EXCLUDED.description,
		 hardware_profile = EXCLUDED.hardware_profile,
		 scenario_yaml = EXCLUDED.scenario_yaml,
		 source = EXCLUDED.source,
		 advisory_id = EXCLUDED.advisory_id,
		 version = EXCLUDED.version,
		 updated_at = EXCLUDED.updated_at,
		 hardware = EXCLUDED.hardware,
		 models = EXCLUDED.models,
		 config = EXCLUDED.config`,
		s.ID, s.Name, s.Description, s.HardwareProfile, s.ScenarioYAML, s.Source, s.AdvisoryID,
		s.Version, s.CreatedAt, s.UpdatedAt, s.Hardware, s.Models, s.Config)
	return err
}

func (p *PostgresCentralStore) ListScenarios(ctx context.Context, f ScenarioFilter) ([]Scenario, error) {
	query := `SELECT id, name, COALESCE(description,''), COALESCE(hardware_profile, hardware, ''),
	           COALESCE(scenario_yaml, config::text, ''), COALESCE(source,''), COALESCE(advisory_id,''),
	           COALESCE(version, 1), COALESCE(created_at::text,''), COALESCE(updated_at::text, created_at::text, ''),
	           COALESCE(hardware,''), COALESCE(models,''), COALESCE(config::text,'')
	          FROM scenarios WHERE 1=1`
	var args []any
	n := 1
	if f.Name != "" {
		query += fmt.Sprintf(` AND name = $%d`, n)
		args = append(args, f.Name)
		n++
	}
	if f.Hardware != "" {
		query += fmt.Sprintf(` AND COALESCE(hardware_profile, hardware, '') = $%d`, n)
		args = append(args, f.Hardware)
		n++
	}
	if f.Source != "" {
		query += fmt.Sprintf(` AND source = $%d`, n)
		args = append(args, f.Source)
		n++
	}
	if f.AdvisoryID != "" {
		query += fmt.Sprintf(` AND advisory_id = $%d`, n)
		args = append(args, f.AdvisoryID)
		n++
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query += fmt.Sprintf(` ORDER BY COALESCE(updated_at, created_at) DESC LIMIT $%d`, n)
	args = append(args, limit)

	rows, err := p.db.QueryContext(ctx, query, args...)
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

func (p *PostgresCentralStore) getAdvisory(ctx context.Context, id string) (Advisory, error) {
	advs, err := p.ListAdvisories(ctx, AdvisoryFilter{ID: id, Limit: 1})
	if err != nil {
		return Advisory{}, err
	}
	if len(advs) == 0 {
		return Advisory{}, fmt.Errorf("advisory %q not found", id)
	}
	return advs[0], nil
}

func (p *PostgresCentralStore) getAnalysisRun(ctx context.Context, id string) (AnalysisRun, error) {
	row := p.db.QueryRowContext(ctx,
		`SELECT id, type, status, COALESCE(summary,''), COALESCE(input_json::text,''), COALESCE(output_json::text,''),
		 COALESCE(advisories::text,''), COALESCE(advisory_count,0), COALESCE(duration_ms,0), COALESCE(error,''),
		 COALESCE(started_at::text, created_at::text, ''), COALESCE(completed_at::text, ''), COALESCE(created_at::text, ''), COALESCE(updated_at::text, '')
		 FROM analysis_runs WHERE id = $1`, id)

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
