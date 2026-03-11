package central

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Config holds central server settings.
type Config struct {
	Addr   string
	APIKey string
	DBPath string
}

// Server is the central knowledge aggregation service.
type Server struct {
	db     *sql.DB
	config Config
	mux    *http.ServeMux
}

// New creates a central server with a SQLite database.
func New(cfg Config) (*Server, error) {
	db, err := sql.Open("sqlite", cfg.DBPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &Server{db: db, config: cfg}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	s.mux = http.NewServeMux()
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	s.mux.HandleFunc("POST /api/v1/ingest", s.authMiddleware(s.handleIngest))
	s.mux.HandleFunc("GET /api/v1/query", s.authMiddleware(s.handleQuery))
	s.mux.HandleFunc("GET /api/v1/sync", s.authMiddleware(s.handleSync))
	s.mux.HandleFunc("GET /api/v1/stats", s.handleStats)
}

func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.config.APIKey != "" {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(strings.ToLower(auth), "bearer ") ||
				subtle.ConstantTimeCompare([]byte(auth[7:]), []byte(s.config.APIKey)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) ListenAndServe() error {
	slog.Info("central server starting", "addr", s.config.Addr)
	srv := &http.Server{
		Addr:              s.config.Addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.ListenAndServe()
}

func (s *Server) Close() error {
	return s.db.Close()
}

func (s *Server) migrate(ctx context.Context) error {
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
);`

	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return err
	}

	alterStmts := []string{
		`ALTER TABLE configurations ADD COLUMN slot TEXT`,
		`ALTER TABLE configurations ADD COLUMN tags TEXT`,
		`ALTER TABLE configurations ADD COLUMN source TEXT`,
		`ALTER TABLE configurations ADD COLUMN updated_at DATETIME DEFAULT CURRENT_TIMESTAMP`,
		`ALTER TABLE benchmark_results ADD COLUMN input_len_bucket TEXT`,
		`ALTER TABLE benchmark_results ADD COLUMN output_len_bucket TEXT`,
		`ALTER TABLE benchmark_results ADD COLUMN modality TEXT`,
		`ALTER TABLE benchmark_results ADD COLUMN ttft_p50_ms REAL`,
		`ALTER TABLE benchmark_results ADD COLUMN ttft_p95_ms REAL`,
		`ALTER TABLE benchmark_results ADD COLUMN ttft_p99_ms REAL`,
		`ALTER TABLE benchmark_results ADD COLUMN tpot_p50_ms REAL`,
		`ALTER TABLE benchmark_results ADD COLUMN tpot_p95_ms REAL`,
		`ALTER TABLE benchmark_results ADD COLUMN qps REAL`,
		`ALTER TABLE benchmark_results ADD COLUMN vram_usage_mib INTEGER`,
		`ALTER TABLE benchmark_results ADD COLUMN ram_usage_mib INTEGER`,
		`ALTER TABLE benchmark_results ADD COLUMN gpu_utilization_pct REAL`,
		`ALTER TABLE benchmark_results ADD COLUMN error_rate REAL`,
		`ALTER TABLE benchmark_results ADD COLUMN oom_occurred BOOLEAN`,
		`ALTER TABLE benchmark_results ADD COLUMN stability TEXT`,
		`ALTER TABLE benchmark_results ADD COLUMN sample_count INTEGER`,
		`ALTER TABLE benchmark_results ADD COLUMN agent_model TEXT`,
		`ALTER TABLE benchmark_results ADD COLUMN notes TEXT`,
	}
	for _, stmt := range alterStmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	return nil
}

// IngestPayload is the expected JSON body for POST /api/v1/ingest.
// It mirrors the knowledge.export output format.
type IngestPayload struct {
	SchemaVersion  int               `json:"schema_version"`
	DeviceID       string            `json:"device_id"`
	GPUArch        string            `json:"gpu_arch"`
	Configurations []IngestConfig    `json:"configurations"`
	Benchmarks     []IngestBenchmark `json:"benchmarks"`
	KnowledgeNotes []IngestNote      `json:"knowledge_notes"`
}

type IngestConfig struct {
	ID            string          `json:"id"`
	Hardware      string          `json:"hardware_id"`
	EngineType    string          `json:"engine_id"`
	EngineVersion string          `json:"engine_version"`
	Model         string          `json:"model_id"`
	Config        json.RawMessage `json:"config"`
	ConfigHash    string          `json:"config_hash"`
	Status        string          `json:"status"`
	DerivedFrom   string          `json:"derived_from"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
	Slot          string          `json:"slot"`
	Source        string          `json:"source"`
	DeviceID      string          `json:"device_id"`
	Tags          []string        `json:"tags"`
}

type IngestBenchmark struct {
	ID              string  `json:"id"`
	ConfigID        string  `json:"config_id"`
	Concurrency     int     `json:"concurrency"`
	InputLenBucket  string  `json:"input_len_bucket"`
	OutputLenBucket string  `json:"output_len_bucket"`
	Modality        string  `json:"modality"`
	TTFTP50ms       float64 `json:"ttft_p50_ms"`
	TTFTP95ms       float64 `json:"ttft_p95_ms"`
	TTFTP99ms       float64 `json:"ttft_p99_ms"`
	TPOTP50ms       float64 `json:"tpot_p50_ms"`
	TPOTP95ms       float64 `json:"tpot_p95_ms"`
	ThroughputTPS   float64 `json:"throughput_tps"`
	QPS             float64 `json:"qps"`
	VRAMUsageMiB    int     `json:"vram_usage_mib"`
	RAMUsageMiB     int     `json:"ram_usage_mib"`
	PowerDrawWatts  float64 `json:"power_draw_watts"`
	GPUUtilPct      float64 `json:"gpu_util_pct"`
	ErrorRate       float64 `json:"error_rate"`
	OOMOccurred     bool    `json:"oom_occurred"`
	Stability       string  `json:"stability"`
	DurationS       int     `json:"duration_s"`
	SampleCount     int     `json:"sample_count"`
	TestedAt        string  `json:"tested_at"`
	AgentModel      string  `json:"agent_model"`
	Notes           string  `json:"notes"`
}

type IngestNote struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Tags            []string `json:"tags"`
	HardwareProfile string   `json:"hardware_profile"`
	Model           string   `json:"model"`
	Engine          string   `json:"engine"`
	Content         string   `json:"content"`
	Confidence      string   `json:"confidence"`
	CreatedAt       string   `json:"created_at"`
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	var payload IngestPayload
	limited := http.MaxBytesReader(w, r.Body, 10<<20) // 10 MiB
	if err := json.NewDecoder(limited).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Upsert device
	if payload.DeviceID != "" {
		_, _ = s.db.ExecContext(r.Context(),
			`INSERT INTO devices (id, gpu_arch, last_seen) VALUES (?, ?, datetime('now'))
			 ON CONFLICT(id) DO UPDATE SET last_seen = datetime('now'), gpu_arch = COALESCE(excluded.gpu_arch, devices.gpu_arch)`,
			payload.DeviceID, payload.GPUArch)
	}

	ingested, duplicates := 0, 0
	benchIngested := 0
	noteIngested := 0

	for _, c := range payload.Configurations {
		configHash := c.ConfigHash
		if configHash == "" {
			h := sha256.Sum256(c.Config)
			configHash = fmt.Sprintf("%x", h)[:16]
		}

		// Check for duplicates by config_hash
		var existing int
		_ = s.db.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM configurations WHERE config_hash = ?`, configHash).Scan(&existing)
		if existing > 0 {
			duplicates++
			continue
		}

		derivedFrom := sql.NullString{}
		if c.DerivedFrom != "" {
			derivedFrom = sql.NullString{String: c.DerivedFrom, Valid: true}
		}
		createdAt := c.CreatedAt
		if createdAt == "" {
			createdAt = time.Now().UTC().Format(time.RFC3339)
		}
		updatedAt := c.UpdatedAt
		if updatedAt == "" {
			updatedAt = createdAt
		}
		deviceID := c.DeviceID
		if deviceID == "" {
			deviceID = payload.DeviceID
		}
		tagsJSON, _ := json.Marshal(c.Tags)
		_, err := s.db.ExecContext(r.Context(),
			`INSERT INTO configurations (id, device_id, hardware, engine_type, engine_version, model, slot, config, config_hash, status, derived_from, tags, source, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, deviceID, c.Hardware, c.EngineType, c.EngineVersion, c.Model, c.Slot,
			string(c.Config), configHash, c.Status, derivedFrom, string(tagsJSON), c.Source, createdAt, updatedAt)
		if err != nil {
			slog.Warn("ingest config", "id", c.ID, "error", err)
			continue
		}
		ingested++
	}

	for _, b := range payload.Benchmarks {
		testedAt := b.TestedAt
		if testedAt == "" {
			testedAt = time.Now().UTC().Format(time.RFC3339)
		}
		_, err := s.db.ExecContext(r.Context(),
			`INSERT OR IGNORE INTO benchmark_results (id, config_id, device_id, concurrency, input_len_bucket, output_len_bucket, modality,
			 throughput_tps, ttft_p50_ms, ttft_p95_ms, ttft_p99_ms, tpot_p50_ms, tpot_p95_ms, qps, vram_usage_mib, ram_usage_mib,
			 power_draw_watts, gpu_utilization_pct, error_rate, oom_occurred, stability, duration_s, sample_count, tested_at, agent_model, notes)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			b.ID, b.ConfigID, payload.DeviceID, b.Concurrency, b.InputLenBucket, b.OutputLenBucket, b.Modality,
			b.ThroughputTPS, b.TTFTP50ms, b.TTFTP95ms, b.TTFTP99ms, b.TPOTP50ms, b.TPOTP95ms, b.QPS, b.VRAMUsageMiB, b.RAMUsageMiB,
			b.PowerDrawWatts, b.GPUUtilPct, b.ErrorRate, b.OOMOccurred, b.Stability, b.DurationS, b.SampleCount, testedAt, b.AgentModel, b.Notes)
		if err != nil {
			slog.Warn("ingest benchmark", "id", b.ID, "error", err)
			continue
		}
		benchIngested++
	}

	for _, n := range payload.KnowledgeNotes {
		createdAt := n.CreatedAt
		if createdAt == "" {
			createdAt = time.Now().UTC().Format(time.RFC3339)
		}
		tagsJSON, _ := json.Marshal(n.Tags)
		_, err := s.db.ExecContext(r.Context(),
			`INSERT OR REPLACE INTO knowledge_notes (id, title, tags, hardware_profile, model, engine, content, confidence, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			n.ID, n.Title, string(tagsJSON), n.HardwareProfile, n.Model, n.Engine, n.Content, n.Confidence, createdAt)
		if err != nil {
			slog.Warn("ingest note", "id", n.ID, "error", err)
			continue
		}
		noteIngested++
	}

	writeJSON(w, map[string]any{
		"ingested":   ingested,
		"duplicates": duplicates,
		"benchmarks": benchIngested,
		"notes":      noteIngested,
	})
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := `SELECT c.id, c.device_id, c.hardware, c.engine_type, c.model, c.config, c.status, c.created_at
			  FROM configurations c WHERE 1=1`
	var args []any

	if hw := q.Get("hardware"); hw != "" {
		query += ` AND c.hardware = ?`
		args = append(args, hw)
	}
	if eng := q.Get("engine"); eng != "" {
		query += ` AND c.engine_type = ?`
		args = append(args, eng)
	}
	if mdl := q.Get("model"); mdl != "" {
		query += ` AND c.model = ?`
		args = append(args, mdl)
	}
	if status := q.Get("status"); status != "" {
		query += ` AND c.status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY c.created_at DESC LIMIT 100`

	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id, hardware, engine, model, config, status, createdAt string
		var deviceID sql.NullString
		if err := rows.Scan(&id, &deviceID, &hardware, &engine, &model, &config, &status, &createdAt); err != nil {
			continue
		}
		r := map[string]any{
			"id": id, "hardware": hardware, "engine": engine, "model": model,
			"config": json.RawMessage(config), "status": status, "created_at": createdAt,
		}
		if deviceID.Valid {
			r["device_id"] = deviceID.String
		}
		results = append(results, r)
	}
	writeJSON(w, results)
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")
	hardware := r.URL.Query().Get("hardware")

	configQuery := `SELECT id, device_id, hardware, engine_type, model, slot, config, config_hash, derived_from, status, tags, source, created_at, updated_at
		FROM configurations WHERE 1=1`
	var configArgs []any
	if since != "" {
		configQuery += ` AND created_at > ?`
		configArgs = append(configArgs, since)
	}
	if hardware != "" {
		configQuery += ` AND hardware = ?`
		configArgs = append(configArgs, hardware)
	}
	configQuery += ` ORDER BY created_at ASC LIMIT 500`

	rows, err := s.db.QueryContext(r.Context(), configQuery, configArgs...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var configs []map[string]any
	var configIDs []string
	for rows.Next() {
		var id, hw, eng, mdl, slot, config, configHash, status, tagsJSON, source, createdAt, updatedAt string
		var deviceID, derivedFrom sql.NullString
		if err := rows.Scan(&id, &deviceID, &hw, &eng, &mdl, &slot, &config, &configHash, &derivedFrom, &status, &tagsJSON, &source, &createdAt, &updatedAt); err != nil {
			continue
		}
		var tags []string
		_ = json.Unmarshal([]byte(tagsJSON), &tags)
		entry := map[string]any{
			"id": id, "hardware_id": hw, "engine_id": eng, "model_id": mdl,
			"slot": slot, "config": json.RawMessage(config), "config_hash": configHash,
			"status": status, "tags": tags, "source": source, "created_at": createdAt, "updated_at": updatedAt,
		}
		if deviceID.Valid {
			entry["device_id"] = deviceID.String
		}
		if derivedFrom.Valid {
			entry["derived_from"] = derivedFrom.String
		}
		configs = append(configs, entry)
		configIDs = append(configIDs, id)
	}

	// Fetch benchmarks: for synced configs, plus any benchmarks added since last sync
	var benchmarks []map[string]any
	benchQuery := `SELECT id, config_id, concurrency, input_len_bucket, output_len_bucket, modality, throughput_tps, ttft_p50_ms, ttft_p95_ms, ttft_p99_ms,
		 tpot_p50_ms, tpot_p95_ms, qps, vram_usage_mib, ram_usage_mib, power_draw_watts, gpu_utilization_pct, error_rate, oom_occurred,
		 stability, duration_s, sample_count, tested_at, agent_model, notes
		 FROM benchmark_results WHERE 1=1`
	var benchArgs []any

	// Include benchmarks for synced configs OR benchmarks tested since last sync
	var conditions []string
	if len(configIDs) > 0 {
		placeholders := strings.Repeat("?,", len(configIDs))
		placeholders = placeholders[:len(placeholders)-1]
		conditions = append(conditions, fmt.Sprintf("config_id IN (%s)", placeholders))
		for _, id := range configIDs {
			benchArgs = append(benchArgs, id)
		}
	}
	if since != "" {
		conditions = append(conditions, "tested_at > ?")
		benchArgs = append(benchArgs, since)
	}
	if len(conditions) > 0 {
		benchQuery += " AND (" + strings.Join(conditions, " OR ") + ")"
	}
	benchQuery += " ORDER BY tested_at ASC LIMIT 1000"

	bRows, err := s.db.QueryContext(r.Context(), benchQuery, benchArgs...)
	if err == nil {
		defer bRows.Close()
		for bRows.Next() {
			var id, configID, inBucket, outBucket, modality, stability, testedAt, agentModel, notes string
			var conc, vramUsed, ramUsed, durationS, sampleCount int
			var tps, ttft50, ttft95, ttft99, tpot50, tpot95, qps, power, gpuUtil, errorRate float64
			var oomOccurred bool
			if err := bRows.Scan(&id, &configID, &conc, &inBucket, &outBucket, &modality, &tps, &ttft50, &ttft95, &ttft99,
				&tpot50, &tpot95, &qps, &vramUsed, &ramUsed, &power, &gpuUtil, &errorRate, &oomOccurred,
				&stability, &durationS, &sampleCount, &testedAt, &agentModel, &notes); err != nil {
				continue
			}
			benchmarks = append(benchmarks, map[string]any{
				"id": id, "config_id": configID, "concurrency": conc, "input_len_bucket": inBucket,
				"output_len_bucket": outBucket, "modality": modality, "throughput_tps": tps,
				"ttft_p50_ms": ttft50, "ttft_p95_ms": ttft95, "ttft_p99_ms": ttft99,
				"tpot_p50_ms": tpot50, "tpot_p95_ms": tpot95, "qps": qps,
				"vram_usage_mib": vramUsed, "ram_usage_mib": ramUsed, "power_draw_watts": power,
				"gpu_util_pct": gpuUtil, "error_rate": errorRate, "oom_occurred": oomOccurred,
				"stability": stability, "duration_s": durationS, "sample_count": sampleCount,
				"tested_at": testedAt, "agent_model": agentModel, "notes": notes,
			})
		}
	}

	var notes []map[string]any
	noteQuery := `SELECT id, title, tags, hardware_profile, model, engine, content, confidence, created_at FROM knowledge_notes ORDER BY created_at ASC LIMIT 1000`
	nRows, err := s.db.QueryContext(r.Context(), noteQuery)
	if err == nil {
		defer nRows.Close()
		for nRows.Next() {
			var id, title, tagsJSON, hardwareProfile, model, engine, content, confidence, createdAt string
			if err := nRows.Scan(&id, &title, &tagsJSON, &hardwareProfile, &model, &engine, &content, &confidence, &createdAt); err != nil {
				continue
			}
			var tags []string
			_ = json.Unmarshal([]byte(tagsJSON), &tags)
			notes = append(notes, map[string]any{
				"id": id, "title": title, "tags": tags, "hardware_profile": hardwareProfile,
				"model": model, "engine": engine, "content": content, "confidence": confidence, "created_at": createdAt,
			})
		}
	}

	// Return in the standard import envelope format so edge can import directly
	writeJSON(w, map[string]any{
		"schema_version": 1,
		"data": map[string]any{
			"configurations":    configs,
			"benchmark_results": benchmarks,
			"knowledge_notes":   notes,
		},
	})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	var deviceCount, configCount, benchCount, noteCount int
	_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM devices`).Scan(&deviceCount)
	_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM configurations`).Scan(&configCount)
	_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM benchmark_results`).Scan(&benchCount)
	_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM knowledge_notes`).Scan(&noteCount)

	// Coverage matrix: distinct hardware x engine x model combos
	coverageRows, _ := s.db.QueryContext(r.Context(),
		`SELECT hardware, engine_type, COUNT(DISTINCT model) as models FROM configurations GROUP BY hardware, engine_type`)
	var coverage []map[string]any
	if coverageRows != nil {
		defer coverageRows.Close()
		for coverageRows.Next() {
			var hw, eng string
			var models int
			if err := coverageRows.Scan(&hw, &eng, &models); err != nil {
				continue
			}
			coverage = append(coverage, map[string]any{"hardware": hw, "engine": eng, "models": models})
		}
	}

	writeJSON(w, map[string]any{
		"devices":         deviceCount,
		"configurations":  configCount,
		"benchmarks":      benchCount,
		"knowledge_notes": noteCount,
		"coverage":        coverage,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
