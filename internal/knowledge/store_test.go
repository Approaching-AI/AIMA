package knowledge

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func mustOpenDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Enable foreign keys and WAL
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("enable FK: %v", err)
	}

	// Create all tables (replicate state.migrate v1+v2)
	ddl := `
CREATE TABLE models (id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL, path TEXT NOT NULL, format TEXT, size_bytes INTEGER, detected_arch TEXT, detected_params TEXT, status TEXT DEFAULT 'registered', download_progress REAL, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE engines (id TEXT PRIMARY KEY, type TEXT NOT NULL, image TEXT NOT NULL, tag TEXT NOT NULL, size_bytes INTEGER, platform TEXT, available BOOLEAN DEFAULT TRUE, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE knowledge_notes (id TEXT PRIMARY KEY, title TEXT NOT NULL, tags TEXT, hardware_profile TEXT, model TEXT, engine TEXT, content TEXT NOT NULL, confidence TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE audit_log (id INTEGER PRIMARY KEY AUTOINCREMENT, agent_type TEXT NOT NULL, tool_name TEXT NOT NULL, arguments TEXT, result_summary TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE hardware_profiles (id TEXT PRIMARY KEY, name TEXT NOT NULL, gpu_arch TEXT NOT NULL, gpu_vram_mib INTEGER, gpu_compute_id TEXT, cpu_arch TEXT, cpu_cores INTEGER, ram_mib INTEGER, unified_memory BOOLEAN DEFAULT FALSE, tdp_watts INTEGER, power_modes TEXT, gpu_tools TEXT, raw_yaml TEXT);
CREATE INDEX idx_hp_gpu ON hardware_profiles(gpu_arch);
CREATE TABLE engine_assets (id TEXT PRIMARY KEY, type TEXT NOT NULL, version TEXT, image_name TEXT, image_tag TEXT, image_size_mb INTEGER, api_protocol TEXT, cold_start_s_min INTEGER, cold_start_s_max INTEGER, power_watts_min INTEGER, power_watts_max INTEGER, perf_gain_desc TEXT, raw_yaml TEXT);
CREATE TABLE engine_features (engine_id TEXT NOT NULL REFERENCES engine_assets(id), feature TEXT NOT NULL, PRIMARY KEY (engine_id, feature));
CREATE INDEX idx_ef_feature ON engine_features(feature);
CREATE TABLE engine_hardware_compat (engine_id TEXT NOT NULL REFERENCES engine_assets(id), hardware_id TEXT NOT NULL REFERENCES hardware_profiles(id), vram_min_mib INTEGER, cpu_offload BOOLEAN DEFAULT FALSE, ssd_offload BOOLEAN DEFAULT FALSE, npu_offload BOOLEAN DEFAULT FALSE, min_gpu_mem_mib INTEGER, recommended_cores_pct INTEGER, PRIMARY KEY (engine_id, hardware_id));
CREATE TABLE model_assets (id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL, family TEXT, param_count TEXT, formats TEXT, sources TEXT, raw_yaml TEXT);
CREATE INDEX idx_ma_type ON model_assets(type);
CREATE INDEX idx_ma_family ON model_assets(family);
CREATE TABLE model_variants (id TEXT PRIMARY KEY, model_id TEXT NOT NULL REFERENCES model_assets(id), hardware_id TEXT NOT NULL REFERENCES hardware_profiles(id), engine_type TEXT NOT NULL, format TEXT, default_config TEXT NOT NULL, expected_perf TEXT, vram_min_mib INTEGER);
CREATE INDEX idx_mv_lookup ON model_variants(model_id, hardware_id, engine_type);
CREATE TABLE partition_strategies (id TEXT PRIMARY KEY, hardware_id TEXT NOT NULL, workload_pattern TEXT NOT NULL, slots TEXT NOT NULL, raw_yaml TEXT);
CREATE TABLE configurations (id TEXT PRIMARY KEY, hardware_id TEXT NOT NULL, engine_id TEXT NOT NULL, model_id TEXT NOT NULL, partition_slot TEXT, config TEXT NOT NULL, config_hash TEXT NOT NULL, derived_from TEXT REFERENCES configurations(id), status TEXT DEFAULT 'experiment', tags TEXT, source TEXT DEFAULT 'local', device_id TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP);
CREATE INDEX idx_cfg_4d ON configurations(hardware_id, engine_id, model_id);
CREATE INDEX idx_cfg_status ON configurations(status);
CREATE INDEX idx_cfg_hash ON configurations(config_hash);
CREATE TABLE benchmark_results (id TEXT PRIMARY KEY, config_id TEXT NOT NULL REFERENCES configurations(id), concurrency INTEGER NOT NULL DEFAULT 1, input_len_bucket TEXT, output_len_bucket TEXT, modality TEXT DEFAULT 'text', ttft_ms_p50 REAL, ttft_ms_p95 REAL, ttft_ms_p99 REAL, tpot_ms_p50 REAL, tpot_ms_p95 REAL, throughput_tps REAL, qps REAL, vram_usage_mib INTEGER, ram_usage_mib INTEGER, power_draw_watts REAL, gpu_utilization_pct REAL, error_rate REAL DEFAULT 0, oom_occurred BOOLEAN DEFAULT FALSE, stability TEXT, duration_s INTEGER, sample_count INTEGER, tested_at DATETIME DEFAULT CURRENT_TIMESTAMP, agent_model TEXT, notes TEXT);
CREATE INDEX idx_br_config ON benchmark_results(config_id);
CREATE INDEX idx_br_perf ON benchmark_results(throughput_tps DESC);
CREATE TABLE perf_vectors (config_id TEXT PRIMARY KEY REFERENCES configurations(id), norm_ttft_p95 REAL, norm_tpot_p95 REAL, norm_throughput REAL, norm_qps REAL, norm_vram REAL, norm_power REAL, avg_throughput REAL, avg_ttft_p95 REAL, avg_vram_mib REAL, benchmark_count INTEGER, updated_at DATETIME);`

	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return db
}

func TestLoadToSQLite(t *testing.T) {
	db := mustOpenDB(t)
	cat := mustLoadCatalog(t)
	ctx := context.Background()

	if err := LoadToSQLite(ctx, db, cat); err != nil {
		t.Fatalf("LoadToSQLite: %v", err)
	}

	// Verify hardware profiles loaded
	var hpCount int
	db.QueryRow("SELECT COUNT(*) FROM hardware_profiles").Scan(&hpCount)
	if hpCount != 1 {
		t.Errorf("hardware_profiles count = %d, want 1", hpCount)
	}

	// Verify engine assets loaded
	var eaCount int
	db.QueryRow("SELECT COUNT(*) FROM engine_assets").Scan(&eaCount)
	if eaCount != 2 {
		t.Errorf("engine_assets count = %d, want 2", eaCount)
	}

	// Verify engine features
	var efCount int
	db.QueryRow("SELECT COUNT(*) FROM engine_features").Scan(&efCount)
	if efCount == 0 {
		t.Error("expected engine features to be loaded")
	}

	// Verify engine-hardware compat
	var ehcCount int
	db.QueryRow("SELECT COUNT(*) FROM engine_hardware_compat").Scan(&ehcCount)
	if ehcCount == 0 {
		t.Error("expected engine_hardware_compat to be loaded")
	}

	// Verify model assets loaded
	var maCount int
	db.QueryRow("SELECT COUNT(*) FROM model_assets").Scan(&maCount)
	if maCount != 1 {
		t.Errorf("model_assets count = %d, want 1", maCount)
	}

	// Verify model variants loaded
	var mvCount int
	db.QueryRow("SELECT COUNT(*) FROM model_variants").Scan(&mvCount)
	if mvCount == 0 {
		t.Error("expected model variants to be loaded")
	}

	// Verify partition strategies loaded
	var psCount int
	db.QueryRow("SELECT COUNT(*) FROM partition_strategies").Scan(&psCount)
	if psCount != 2 {
		t.Errorf("partition_strategies count = %d, want 2", psCount)
	}
}

func TestLoadToSQLiteIdempotent(t *testing.T) {
	db := mustOpenDB(t)
	cat := mustLoadCatalog(t)
	ctx := context.Background()

	// Load twice — should not error (second load clears and reinserts)
	if err := LoadToSQLite(ctx, db, cat); err != nil {
		t.Fatalf("first load: %v", err)
	}
	if err := LoadToSQLite(ctx, db, cat); err != nil {
		t.Fatalf("second load: %v", err)
	}

	// Counts should be same as single load
	var hpCount int
	db.QueryRow("SELECT COUNT(*) FROM hardware_profiles").Scan(&hpCount)
	if hpCount != 1 {
		t.Errorf("hardware_profiles count = %d, want 1 (idempotent)", hpCount)
	}
}

func TestStoreGaps(t *testing.T) {
	db := mustOpenDB(t)
	cat := mustLoadCatalog(t)
	ctx := context.Background()

	if err := LoadToSQLite(ctx, db, cat); err != nil {
		t.Fatalf("LoadToSQLite: %v", err)
	}

	store := NewStore(db)

	// All model variants should show as gaps (no configurations/benchmarks yet)
	gaps, err := store.Gaps(ctx, GapsParams{MinBenchmarks: 1})
	if err != nil {
		t.Fatalf("Gaps: %v", err)
	}
	if len(gaps) == 0 {
		t.Error("expected gaps with no configurations")
	}
}

func TestStoreListHardwareProfiles(t *testing.T) {
	db := mustOpenDB(t)
	cat := mustLoadCatalog(t)
	ctx := context.Background()

	if err := LoadToSQLite(ctx, db, cat); err != nil {
		t.Fatalf("LoadToSQLite: %v", err)
	}

	store := NewStore(db)
	profiles, err := store.ListHardwareProfiles(ctx)
	if err != nil {
		t.Fatalf("ListHardwareProfiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profiles count = %d, want 1", len(profiles))
	}
	if profiles[0]["gpu_arch"] != "TestArch" {
		t.Errorf("gpu_arch = %v, want TestArch", profiles[0]["gpu_arch"])
	}
}

func TestStoreListEngineAssets(t *testing.T) {
	db := mustOpenDB(t)
	cat := mustLoadCatalog(t)
	ctx := context.Background()

	if err := LoadToSQLite(ctx, db, cat); err != nil {
		t.Fatalf("LoadToSQLite: %v", err)
	}

	store := NewStore(db)
	assets, err := store.ListEngineAssets(ctx)
	if err != nil {
		t.Fatalf("ListEngineAssets: %v", err)
	}
	if len(assets) != 2 {
		t.Fatalf("engine assets count = %d, want 2", len(assets))
	}
}

func TestStoreSearchEmpty(t *testing.T) {
	db := mustOpenDB(t)
	ctx := context.Background()

	store := NewStore(db)

	// Search with no data should return empty results
	resp, err := store.Search(ctx, SearchParams{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Errorf("expected 0 results, got %d", len(resp.Results))
	}
}
