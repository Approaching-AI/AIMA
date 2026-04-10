package central

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Config holds central server settings.
type Config struct {
	Addr     string
	APIKey   string
	DBPath   string // used for SQLite (backward compat)
	DBDriver string // "sqlite" (default) or "postgres"
	DBDSN    string // full DSN for postgres; for sqlite, falls back to DBPath
}

// Server is the central knowledge aggregation service.
type Server struct {
	store    CentralStore
	config   Config
	mux      *http.ServeMux
	advisor  interface{} // set via SetAdvisor
	analyzer interface{} // set via SetAnalyzer
}

// New creates a central server with the configured database backend.
func New(cfg Config) (*Server, error) {
	var store CentralStore
	var err error

	switch cfg.DBDriver {
	case "postgres":
		dsn := cfg.DBDSN
		if dsn == "" {
			return nil, fmt.Errorf("postgres requires DBDSN")
		}
		store, err = NewPostgresCentralStore(dsn)
	default:
		// Default to SQLite for backward compatibility
		dbPath := cfg.DBPath
		if dbPath == "" && cfg.DBDSN != "" {
			dbPath = cfg.DBDSN
		}
		if dbPath == "" {
			dbPath = "central.db"
		}
		store, err = NewSQLiteCentralStore(dbPath)
	}
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	s := &Server{store: store, config: cfg}
	if err := store.Migrate(context.Background()); err != nil {
		store.Close()
		return nil, err
	}
	s.mux = http.NewServeMux()
	s.routes()
	return s, nil
}

// Store returns the underlying CentralStore.
func (s *Server) Store() CentralStore {
	return s.store
}

// SetAdvisor sets the advisor engine on the server.
func (s *Server) SetAdvisor(a interface{}) {
	s.advisor = a
}

// SetAnalyzer sets the analyzer on the server.
func (s *Server) SetAnalyzer(a interface{}) {
	s.analyzer = a
}

func (s *Server) routes() {
	// Existing v1 routes
	s.mux.HandleFunc("POST /api/v1/ingest", s.authMiddleware(s.handleIngest))
	s.mux.HandleFunc("GET /api/v1/query", s.authMiddleware(s.handleQuery))
	s.mux.HandleFunc("GET /api/v1/sync", s.authMiddleware(s.handleSync))
	s.mux.HandleFunc("GET /api/v1/stats", s.handleStats)

	// v0.4 advisor routes
	s.mux.HandleFunc("POST /api/v1/advise", s.authMiddleware(s.handleAdvise))
	s.mux.HandleFunc("GET /api/v1/advisories", s.authMiddleware(s.handleListAdvisories))
	s.mux.HandleFunc("POST /api/v1/advisory/feedback", s.authMiddleware(s.handleAdvisoryFeedback))
	s.mux.HandleFunc("POST /api/v1/advisories/{id}/feedback", s.authMiddleware(s.handleAdvisoryFeedback))
	s.mux.HandleFunc("POST /api/v1/scenario/generate", s.authMiddleware(s.handleScenarioGenerate))
	s.mux.HandleFunc("POST /api/v1/scenarios/generate", s.authMiddleware(s.handleScenarioGenerate))
	s.mux.HandleFunc("GET /api/v1/scenarios", s.authMiddleware(s.handleListScenarios))
	s.mux.HandleFunc("GET /api/v1/analysis", s.authMiddleware(s.handleListAnalysis))
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
	return s.store.Close()
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
	CPUUsagePct     float64 `json:"cpu_usage_pct"`
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

	ctx := r.Context()

	// Upsert device
	if payload.DeviceID != "" {
		_ = s.store.UpsertDevice(ctx, Device{
			ID:      payload.DeviceID,
			GPUArch: payload.GPUArch,
		})
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

		exists, _ := s.store.ConfigExistsByHash(ctx, configHash)
		if exists {
			duplicates++
			continue
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

		err := s.store.InsertConfiguration(ctx, Configuration{
			ID:            c.ID,
			DeviceID:      deviceID,
			Hardware:      c.Hardware,
			EngineType:    c.EngineType,
			EngineVersion: c.EngineVersion,
			Model:         c.Model,
			Slot:          c.Slot,
			Config:        string(c.Config),
			ConfigHash:    configHash,
			Status:        c.Status,
			DerivedFrom:   c.DerivedFrom,
			Tags:          string(tagsJSON),
			Source:        c.Source,
			CreatedAt:     createdAt,
			UpdatedAt:     updatedAt,
		})
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
		err := s.store.InsertBenchmark(ctx, BenchmarkResult{
			ID:              b.ID,
			ConfigID:        b.ConfigID,
			DeviceID:        payload.DeviceID,
			Concurrency:     b.Concurrency,
			InputLenBucket:  b.InputLenBucket,
			OutputLenBucket: b.OutputLenBucket,
			Modality:        b.Modality,
			ThroughputTPS:   b.ThroughputTPS,
			TTFTP50ms:       b.TTFTP50ms,
			TTFTP95ms:       b.TTFTP95ms,
			TTFTP99ms:       b.TTFTP99ms,
			TPOTP50ms:       b.TPOTP50ms,
			TPOTP95ms:       b.TPOTP95ms,
			QPS:             b.QPS,
			VRAMUsageMiB:    b.VRAMUsageMiB,
			RAMUsageMiB:     b.RAMUsageMiB,
			PowerDrawWatts:  b.PowerDrawWatts,
			GPUUtilPct:      b.GPUUtilPct,
			CPUUsagePct:     b.CPUUsagePct,
			ErrorRate:       b.ErrorRate,
			OOMOccurred:     b.OOMOccurred,
			Stability:       b.Stability,
			DurationS:       b.DurationS,
			SampleCount:     b.SampleCount,
			TestedAt:        testedAt,
			AgentModel:      b.AgentModel,
			Notes:           b.Notes,
		})
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
		err := s.store.UpsertKnowledgeNote(ctx, KnowledgeNote{
			ID:              n.ID,
			Title:           n.Title,
			Tags:            string(tagsJSON),
			HardwareProfile: n.HardwareProfile,
			Model:           n.Model,
			Engine:          n.Engine,
			Content:         n.Content,
			Confidence:      n.Confidence,
			CreatedAt:       createdAt,
		})
		if err != nil {
			slog.Warn("ingest note", "id", n.ID, "error", err)
			continue
		}
		noteIngested++
	}

	if analyzer, ok := s.analyzer.(*Analyzer); ok && analyzer != nil {
		analyzer.OnIngest(ctx, payload)
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
	configs, err := s.store.QueryConfigurations(r.Context(), ConfigFilter{
		Hardware: q.Get("hardware"),
		Engine:   q.Get("engine"),
		Model:    q.Get("model"),
		Status:   q.Get("status"),
		Limit:    100,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var results []map[string]any
	for _, c := range configs {
		entry := map[string]any{
			"id": c.ID, "hardware": c.Hardware, "engine": c.EngineType, "model": c.Model,
			"config": json.RawMessage(c.Config), "status": c.Status, "created_at": c.CreatedAt,
		}
		if c.DeviceID != "" {
			entry["device_id"] = c.DeviceID
		}
		results = append(results, entry)
	}
	writeJSON(w, results)
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")
	hardware := r.URL.Query().Get("hardware")

	syncConfigs, err := s.store.ListConfigurationsForSync(r.Context(), SyncFilter{
		Since:    since,
		Hardware: hardware,
		Limit:    500,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var configs []map[string]any
	var configIDs []string
	for _, c := range syncConfigs {
		var tags []string
		_ = json.Unmarshal([]byte(c.Tags), &tags)
		entry := map[string]any{
			"id": c.ID, "hardware_id": c.Hardware, "engine_id": c.EngineType, "model_id": c.Model,
			"slot": c.Slot, "config": json.RawMessage(c.Config), "config_hash": c.ConfigHash,
			"status": c.Status, "tags": tags, "source": c.Source, "created_at": c.CreatedAt, "updated_at": c.UpdatedAt,
		}
		if c.DeviceID != "" {
			entry["device_id"] = c.DeviceID
		}
		if c.DerivedFrom != "" {
			entry["derived_from"] = c.DerivedFrom
		}
		configs = append(configs, entry)
		configIDs = append(configIDs, c.ID)
	}

	benchResults, _ := s.store.ListBenchmarksForSync(r.Context(), configIDs, since)
	var benchmarks []map[string]any
	for _, b := range benchResults {
		benchmarks = append(benchmarks, map[string]any{
			"id": b.ID, "config_id": b.ConfigID, "concurrency": b.Concurrency, "input_len_bucket": b.InputLenBucket,
			"output_len_bucket": b.OutputLenBucket, "modality": b.Modality, "throughput_tps": b.ThroughputTPS,
			"ttft_p50_ms": b.TTFTP50ms, "ttft_p95_ms": b.TTFTP95ms, "ttft_p99_ms": b.TTFTP99ms,
			"tpot_p50_ms": b.TPOTP50ms, "tpot_p95_ms": b.TPOTP95ms, "qps": b.QPS,
			"vram_usage_mib": b.VRAMUsageMiB, "ram_usage_mib": b.RAMUsageMiB, "power_draw_watts": b.PowerDrawWatts,
			"gpu_util_pct": b.GPUUtilPct, "cpu_usage_pct": b.CPUUsagePct, "error_rate": b.ErrorRate, "oom_occurred": b.OOMOccurred,
			"stability": b.Stability, "duration_s": b.DurationS, "sample_count": b.SampleCount,
			"tested_at": b.TestedAt, "agent_model": b.AgentModel, "notes": b.Notes,
		})
	}

	noteResults, _ := s.store.ListKnowledgeNotes(r.Context())
	var notes []map[string]any
	for _, n := range noteResults {
		var tags []string
		_ = json.Unmarshal([]byte(n.Tags), &tags)
		notes = append(notes, map[string]any{
			"id": n.ID, "title": n.Title, "tags": tags, "hardware_profile": n.HardwareProfile,
			"model": n.Model, "engine": n.Engine, "content": n.Content, "confidence": n.Confidence, "created_at": n.CreatedAt,
		})
	}

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
	stats, _ := s.store.Stats(r.Context())
	coverage, _ := s.store.CoverageMatrix(r.Context())

	var coverageList []map[string]any
	for _, c := range coverage {
		coverageList = append(coverageList, map[string]any{
			"hardware": c.Hardware, "engine": c.Engine, "models": c.Models,
		})
	}

	writeJSON(w, map[string]any{
		"devices":         stats.Devices,
		"configurations":  stats.Configurations,
		"benchmarks":      stats.Benchmarks,
		"knowledge_notes": stats.KnowledgeNotes,
		"coverage":        coverageList,
	})
}

func (s *Server) handleAdvise(w http.ResponseWriter, r *http.Request) {
	adv, ok := s.advisor.(*Advisor)
	if !ok || adv == nil {
		http.Error(w, "advisor not configured", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Action          string   `json:"action"` // "recommend" or "optimize"
		Hardware        string   `json:"hardware"`
		HardwareProfile string   `json:"hardware_profile"`
		Model           string   `json:"model"`
		Engine          string   `json:"engine,omitempty"`
		ConfigID        string   `json:"config_id,omitempty"`
		Goal            string   `json:"goal,omitempty"`
		Models          []string `json:"models,omitempty"`
	}
	limited := http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(limited).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	switch req.Action {
	case "recommend":
		resp, advisory, err := adv.Recommend(ctx, RecommendRequest{
			Hardware:        req.Hardware,
			HardwareProfile: req.HardwareProfile,
			Model:           req.Model,
			Goal:            req.Goal,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"recommendation": resp, "advisory": advisory})

	case "optimize":
		resp, advisory, err := adv.OptimizeScenario(ctx, OptimizeRequest{
			ConfigID: req.ConfigID,
			Hardware: req.Hardware,
			Model:    req.Model,
			Engine:   req.Engine,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"optimization": resp, "advisory": advisory})

	default:
		http.Error(w, "unknown action: "+req.Action, http.StatusBadRequest)
	}
}

func (s *Server) handleListAdvisories(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseLimit(q.Get("limit"), 50)
	hardware := q.Get("hardware")
	if hardware == "" {
		hardware = q.Get("target_hardware")
	}
	_, _ = s.store.ExpireAdvisories(r.Context(), time.Now().UTC().Add(-30*24*time.Hour))
	advs, err := s.store.ListAdvisories(r.Context(), AdvisoryFilter{
		Type:     q.Get("type"),
		Status:   q.Get("status"),
		Severity: q.Get("severity"),
		Hardware: hardware,
		Model:    firstNonEmpty(q.Get("model"), q.Get("target_model")),
		Engine:   firstNonEmpty(q.Get("engine"), q.Get("target_engine")),
		Limit:    limit,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if q.Get("status") == AdvisoryStatusPending {
		deliveredAt := nowRFC3339()
		for i := range advs {
			if err := s.store.UpdateAdvisoryStatus(r.Context(), advs[i].ID, AdvisoryStatusUpdate{
				Status:      AdvisoryStatusDelivered,
				DeliveredAt: deliveredAt,
			}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			advs[i].Status = AdvisoryStatusDelivered
			advs[i].DeliveredAt = deliveredAt
		}
	}
	writeJSON(w, advs)
}

func (s *Server) handleAdvisoryFeedback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		AdvisoryID string `json:"advisory_id"`
		Status     string `json:"status"`
		Feedback   string `json:"feedback"`
		Reason     string `json:"reason"`
		Accepted   *bool  `json:"accepted"`
	}
	limited := http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(limited).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if id == "" {
		id = req.AdvisoryID
	}
	if id == "" {
		http.Error(w, "missing advisory id", http.StatusBadRequest)
		return
	}

	status := req.Status
	if status == "" && req.Accepted != nil {
		if *req.Accepted {
			status = AdvisoryStatusValidated
		} else {
			status = AdvisoryStatusRejected
		}
	}
	if status != AdvisoryStatusValidated && status != AdvisoryStatusRejected {
		http.Error(w, "status must be 'validated' or 'rejected'", http.StatusBadRequest)
		return
	}
	feedback := strings.TrimSpace(req.Feedback)
	if feedback == "" {
		feedback = strings.TrimSpace(req.Reason)
	}

	if err := s.store.UpdateAdvisoryStatus(r.Context(), id, AdvisoryStatusUpdate{
		Status:      status,
		Feedback:    feedback,
		ValidatedAt: nowRFC3339(),
	}); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else if strings.Contains(err.Error(), "cannot transition") {
			http.Error(w, err.Error(), http.StatusConflict)
		} else {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, map[string]any{"ok": true, "advisory_id": id, "status": status})
}

func (s *Server) handleScenarioGenerate(w http.ResponseWriter, r *http.Request) {
	adv, ok := s.advisor.(*Advisor)
	if !ok || adv == nil {
		http.Error(w, "advisor not configured", http.StatusServiceUnavailable)
		return
	}

	var req GenerateScenarioRequest
	limited := http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(limited).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	resp, scenario, err := adv.GenerateScenario(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"scenario": resp, "stored": scenario})
}

func (s *Server) handleListScenarios(w http.ResponseWriter, r *http.Request) {
	hardware := r.URL.Query().Get("hardware")
	if hardware == "" {
		hardware = r.URL.Query().Get("hardware_profile")
	}
	scenarios, err := s.store.ListScenarios(r.Context(), ScenarioFilter{
		Name:       r.URL.Query().Get("name"),
		Hardware:   hardware,
		Source:     r.URL.Query().Get("source"),
		AdvisoryID: r.URL.Query().Get("advisory_id"),
		Limit:      parseLimit(r.URL.Query().Get("limit"), 100),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, scenarios)
}

func (s *Server) handleListAnalysis(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.ListAnalysisRuns(r.Context(), parseLimit(r.URL.Query().Get("limit"), 20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, runs)
}

func writeJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func parseLimit(v string, fallback int) int {
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
