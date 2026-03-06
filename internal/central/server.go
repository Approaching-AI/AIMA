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
    config TEXT NOT NULL,
    config_hash TEXT NOT NULL,
    status TEXT DEFAULT 'experiment',
    derived_from TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    ingested_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_config_hash ON configurations(config_hash);
CREATE INDEX IF NOT EXISTS idx_config_hw ON configurations(hardware, engine_type, model);

CREATE TABLE IF NOT EXISTS benchmark_results (
    id TEXT PRIMARY KEY,
    config_id TEXT NOT NULL REFERENCES configurations(id),
    device_id TEXT REFERENCES devices(id),
    concurrency INTEGER,
    throughput_tps REAL,
    ttft_ms_p50 REAL,
    ttft_ms_p95 REAL,
    ttft_ms_p99 REAL,
    tpot_ms_p50 REAL,
    tpot_ms_p95 REAL,
    total_tokens INTEGER,
    duration_s REAL,
    power_draw_watts REAL,
    vram_used_mib INTEGER,
    tested_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    ingested_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_bench_config ON benchmark_results(config_id);`

	_, err := s.db.ExecContext(ctx, ddl)
	return err
}

// IngestPayload is the expected JSON body for POST /api/v1/ingest.
// It mirrors the knowledge.export output format.
type IngestPayload struct {
	SchemaVersion int                `json:"schema_version"`
	DeviceID      string             `json:"device_id"`
	GPUArch       string             `json:"gpu_arch"`
	Configurations []IngestConfig    `json:"configurations"`
	Benchmarks     []IngestBenchmark `json:"benchmarks"`
}

type IngestConfig struct {
	ID            string         `json:"id"`
	Hardware      string         `json:"hardware_id"`
	EngineType    string         `json:"engine_id"`
	EngineVersion string         `json:"engine_version"`
	Model         string         `json:"model_id"`
	Config        json.RawMessage `json:"config"`
	ConfigHash    string         `json:"config_hash"`
	Status        string         `json:"status"`
	DerivedFrom   string         `json:"derived_from"`
	CreatedAt     string         `json:"created_at"`
}

type IngestBenchmark struct {
	ID             string  `json:"id"`
	ConfigID       string  `json:"config_id"`
	Concurrency    int     `json:"concurrency"`
	ThroughputTPS  float64 `json:"throughput_tps"`
	TTFTP50ms      float64 `json:"ttft_ms_p50"`
	TTFTP95ms      float64 `json:"ttft_ms_p95"`
	TTFTP99ms      float64 `json:"ttft_ms_p99"`
	TPOTP50ms      float64 `json:"tpot_ms_p50"`
	TPOTP95ms      float64 `json:"tpot_ms_p95"`
	TotalTokens    int     `json:"total_tokens"`
	DurationS      float64 `json:"duration_s"`
	PowerDrawWatts float64 `json:"power_draw_watts"`
	VRAMUsedMiB    int     `json:"vram_used_mib"`
	TestedAt       string  `json:"tested_at"`
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
		_, err := s.db.ExecContext(r.Context(),
			`INSERT INTO configurations (id, device_id, hardware, engine_type, engine_version, model, config, config_hash, status, derived_from, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, payload.DeviceID, c.Hardware, c.EngineType, c.EngineVersion, c.Model,
			string(c.Config), configHash, c.Status, derivedFrom, createdAt)
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
			`INSERT OR IGNORE INTO benchmark_results (id, config_id, device_id, concurrency, throughput_tps, ttft_ms_p50, ttft_ms_p95, ttft_ms_p99, tpot_ms_p50, tpot_ms_p95, total_tokens, duration_s, power_draw_watts, vram_used_mib, tested_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			b.ID, b.ConfigID, payload.DeviceID, b.Concurrency, b.ThroughputTPS,
			b.TTFTP50ms, b.TTFTP95ms, b.TTFTP99ms, b.TPOTP50ms, b.TPOTP95ms,
			b.TotalTokens, b.DurationS, b.PowerDrawWatts, b.VRAMUsedMiB, testedAt)
		if err != nil {
			slog.Warn("ingest benchmark", "id", b.ID, "error", err)
			continue
		}
		benchIngested++
	}

	writeJSON(w, map[string]any{
		"ingested":        ingested,
		"duplicates":      duplicates,
		"benchmarks":      benchIngested,
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

	configQuery := `SELECT id, hardware, engine_type, model, config, config_hash, status, created_at FROM configurations WHERE 1=1`
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
		var id, hw, eng, mdl, config, configHash, status, createdAt string
		if err := rows.Scan(&id, &hw, &eng, &mdl, &config, &configHash, &status, &createdAt); err != nil {
			continue
		}
		configs = append(configs, map[string]any{
			"id": id, "hardware_id": hw, "engine_id": eng, "model_id": mdl,
			"config": json.RawMessage(config), "config_hash": configHash,
			"status": status, "created_at": createdAt,
		})
		configIDs = append(configIDs, id)
	}

	// Fetch benchmarks for synced configs
	var benchmarks []map[string]any
	if len(configIDs) > 0 {
		placeholders := strings.Repeat("?,", len(configIDs))
		placeholders = placeholders[:len(placeholders)-1]
		benchQuery := fmt.Sprintf(
			`SELECT id, config_id, concurrency, throughput_tps, ttft_ms_p50, ttft_ms_p95, ttft_ms_p99,
			 tpot_ms_p50, tpot_ms_p95, total_tokens, duration_s, power_draw_watts, vram_used_mib, tested_at
			 FROM benchmark_results WHERE config_id IN (%s)`, placeholders)
		benchArgs := make([]any, len(configIDs))
		for i, id := range configIDs {
			benchArgs[i] = id
		}
		bRows, err := s.db.QueryContext(r.Context(), benchQuery, benchArgs...)
		if err == nil {
			defer bRows.Close()
			for bRows.Next() {
				var id, configID, testedAt string
				var conc, totalTokens, vramUsed int
				var tps, ttft50, ttft95, ttft99, tpot50, tpot95, dur, power float64
				if err := bRows.Scan(&id, &configID, &conc, &tps, &ttft50, &ttft95, &ttft99,
					&tpot50, &tpot95, &totalTokens, &dur, &power, &vramUsed, &testedAt); err != nil {
					continue
				}
				benchmarks = append(benchmarks, map[string]any{
					"id": id, "config_id": configID, "concurrency": conc,
					"throughput_tps": tps, "ttft_ms_p50": ttft50, "ttft_ms_p95": ttft95,
					"ttft_ms_p99": ttft99, "tpot_ms_p50": tpot50, "tpot_ms_p95": tpot95,
					"total_tokens": totalTokens, "duration_s": dur,
					"power_draw_watts": power, "vram_used_mib": vramUsed, "tested_at": testedAt,
				})
			}
		}
	}

	writeJSON(w, map[string]any{
		"schema_version": 1,
		"configurations": configs,
		"benchmarks":     benchmarks,
	})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	var deviceCount, configCount, benchCount int
	_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM devices`).Scan(&deviceCount)
	_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM configurations`).Scan(&configCount)
	_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM benchmark_results`).Scan(&benchCount)

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
		"devices":        deviceCount,
		"configurations": configCount,
		"benchmarks":     benchCount,
		"coverage":       coverage,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
