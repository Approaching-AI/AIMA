package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Store provides knowledge query operations against the SQLite database.
type Store struct {
	db *sql.DB
}

// NewStore creates a knowledge Store backed by the given database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// --- Search ---

// SearchParams defines multi-dimensional search criteria.
type SearchParams struct {
	Hardware        string   `json:"hardware"`
	Model           string   `json:"model"`
	Engine          string   `json:"engine"`
	EngineFeatures  []string `json:"engine_features"`
	Concurrency     int      `json:"concurrency"`
	Status          string   `json:"status"`
	SortBy          string   `json:"sort_by"`
	SortOrder       string   `json:"sort_order"`
	IncludeBenchmarks bool   `json:"include_benchmarks"`
	Limit           int      `json:"limit"`
	// Performance constraints
	TTFTp95Max      float64 `json:"ttft_ms_p95_max"`
	ThroughputMin   float64 `json:"throughput_tps_min"`
	VRAMMiBMax      int     `json:"vram_mib_max"`
	PowerWattsMax   float64 `json:"power_watts_max"`
}

// SearchResult is a single configuration with its best benchmark data.
type SearchResult struct {
	ConfigID      string         `json:"config_id"`
	Hardware      string         `json:"hardware"`
	Engine        EngineRef      `json:"engine"`
	Model         string         `json:"model"`
	Config        json.RawMessage `json:"config"`
	Status        string         `json:"status"`
	DerivedFrom   string         `json:"derived_from,omitempty"`
	BestBenchmark *BenchmarkRef  `json:"best_benchmark,omitempty"`
	BenchmarkCount int           `json:"benchmark_count"`
	TestedAt      string         `json:"tested_at,omitempty"`
}

// EngineRef is a compact engine reference in search results.
type EngineRef struct {
	Type    string `json:"type"`
	Version string `json:"version"`
}

// BenchmarkRef is the best benchmark data for a config.
type BenchmarkRef struct {
	Concurrency   int     `json:"concurrency"`
	InputLen      string  `json:"input_len,omitempty"`
	ThroughputTPS float64 `json:"throughput_tps"`
	TTFTp95ms     float64 `json:"ttft_ms_p95"`
	VRAMMiB       int     `json:"vram_mib"`
	PowerWatts    float64 `json:"power_watts"`
}

// SearchSummary provides aggregate info about search results.
type SearchSummary struct {
	TotalMatching  int      `json:"total_matching"`
	Returned       int      `json:"returned"`
	AvgThroughput  float64  `json:"avg_throughput,omitempty"`
	BestThroughput float64  `json:"best_throughput,omitempty"`
	EngineTypes    []string `json:"engine_types,omitempty"`
}

// SearchResponse wraps results with summary.
type SearchResponse struct {
	Results []SearchResult `json:"results"`
	Summary SearchSummary  `json:"summary"`
}

// Search finds configurations matching multi-dimensional criteria.
func (s *Store) Search(ctx context.Context, p SearchParams) (*SearchResponse, error) {
	if p.Limit <= 0 {
		p.Limit = 10
	}
	if p.SortOrder == "" {
		p.SortOrder = "desc"
	}

	query := `
SELECT
    c.id, c.hardware_id, c.engine_id, c.model_id, c.config, c.status,
    COALESCE(c.derived_from, ''),
    ea.type, COALESCE(ea.version, ''),
    COALESCE(b.throughput_tps, 0), COALESCE(b.ttft_ms_p95, 0),
    COALESCE(b.vram_usage_mib, 0), COALESCE(b.power_draw_watts, 0),
    COALESCE(b.concurrency, 0), COALESCE(b.input_len_bucket, ''),
    (SELECT COUNT(*) FROM benchmark_results b2 WHERE b2.config_id = c.id) AS bench_count,
    COALESCE(b.tested_at, c.created_at)
FROM configurations c
JOIN engine_assets ea ON c.engine_id = ea.id
LEFT JOIN benchmark_results b ON b.config_id = c.id
    AND b.throughput_tps = (SELECT MAX(b3.throughput_tps) FROM benchmark_results b3 WHERE b3.config_id = c.id)`

	var conditions []string
	var args []any

	if p.Hardware != "" {
		conditions = append(conditions, "(c.hardware_id = ? OR c.hardware_id IN (SELECT id FROM hardware_profiles WHERE gpu_arch = ?))")
		args = append(args, p.Hardware, p.Hardware)
	}
	if p.Model != "" {
		conditions = append(conditions, "(c.model_id = ? OR c.model_id IN (SELECT id FROM model_assets WHERE family = ?))")
		args = append(args, p.Model, p.Model)
	}
	if p.Engine != "" {
		conditions = append(conditions, "(ea.type = ? OR c.engine_id = ?)")
		args = append(args, p.Engine, p.Engine)
	}
	if p.Status != "" {
		conditions = append(conditions, "c.status = ?")
		args = append(args, p.Status)
	}
	if len(p.EngineFeatures) > 0 {
		placeholders := make([]string, len(p.EngineFeatures))
		for i, f := range p.EngineFeatures {
			placeholders[i] = "?"
			args = append(args, f)
		}
		conditions = append(conditions, fmt.Sprintf(
			"c.engine_id IN (SELECT engine_id FROM engine_features WHERE feature IN (%s) GROUP BY engine_id HAVING COUNT(DISTINCT feature) = %d)",
			strings.Join(placeholders, ","), len(p.EngineFeatures)))
	}
	if p.Concurrency > 0 {
		conditions = append(conditions, "b.concurrency = ?")
		args = append(args, p.Concurrency)
	}
	if p.TTFTp95Max > 0 {
		conditions = append(conditions, "b.ttft_ms_p95 <= ?")
		args = append(args, p.TTFTp95Max)
	}
	if p.ThroughputMin > 0 {
		conditions = append(conditions, "b.throughput_tps >= ?")
		args = append(args, p.ThroughputMin)
	}
	if p.VRAMMiBMax > 0 {
		conditions = append(conditions, "b.vram_usage_mib <= ?")
		args = append(args, p.VRAMMiBMax)
	}
	if p.PowerWattsMax > 0 {
		conditions = append(conditions, "b.power_draw_watts <= ?")
		args = append(args, p.PowerWattsMax)
	}

	if len(conditions) > 0 {
		query += "\nWHERE " + strings.Join(conditions, " AND ")
	}

	// Dedup by config id (LEFT JOIN may produce duplicates)
	query += "\nGROUP BY c.id"

	// Sort — use MAX() aggregates since we GROUP BY c.id
	orderCol := "MAX(b.throughput_tps)"
	switch p.SortBy {
	case "latency":
		orderCol = "MIN(b.ttft_ms_p95)"
	case "vram":
		orderCol = "MIN(b.vram_usage_mib)"
	case "power":
		orderCol = "MIN(b.power_draw_watts)"
	case "created":
		orderCol = "c.created_at"
	}
	dir := "DESC"
	if p.SortOrder == "asc" {
		dir = "ASC"
	}
	query += fmt.Sprintf("\nORDER BY %s %s\nLIMIT ?", orderCol, dir)
	args = append(args, p.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search configurations: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	var totalThroughput, bestThroughput float64
	engineSet := make(map[string]bool)

	for rows.Next() {
		var r SearchResult
		var configJSON string
		var bThroughput, bTTFT, bPower float64
		var bVRAM, bConcurrency int
		var engineID, bInputLen, derivedFrom, eType, eVersion, testedAt string

		if err := rows.Scan(
			&r.ConfigID, &r.Hardware, &engineID, &r.Model, &configJSON, &r.Status,
			&derivedFrom, &eType, &eVersion,
			&bThroughput, &bTTFT, &bVRAM, &bPower, &bConcurrency, &bInputLen,
			&r.BenchmarkCount, &testedAt,
		); err != nil {
			return nil, fmt.Errorf("scan search row: %w", err)
		}
		_ = engineID // used for JOIN, engine info comes from ea.type/version
		r.Config = json.RawMessage(configJSON)
		r.Engine = EngineRef{Type: eType, Version: eVersion}
		r.DerivedFrom = derivedFrom
		r.TestedAt = testedAt

		if bThroughput > 0 || bTTFT > 0 {
			r.BestBenchmark = &BenchmarkRef{
				Concurrency:   bConcurrency,
				InputLen:      bInputLen,
				ThroughputTPS: bThroughput,
				TTFTp95ms:     bTTFT,
				VRAMMiB:       bVRAM,
				PowerWatts:    bPower,
			}
		}

		totalThroughput += bThroughput
		if bThroughput > bestThroughput {
			bestThroughput = bThroughput
		}
		engineSet[eType] = true
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate search rows: %w", err)
	}

	engines := make([]string, 0, len(engineSet))
	for e := range engineSet {
		engines = append(engines, e)
	}

	var avgThroughput float64
	if len(results) > 0 {
		avgThroughput = totalThroughput / float64(len(results))
	}

	return &SearchResponse{
		Results: results,
		Summary: SearchSummary{
			TotalMatching:  len(results),
			Returned:       len(results),
			AvgThroughput:  avgThroughput,
			BestThroughput: bestThroughput,
			EngineTypes:    engines,
		},
	}, nil
}

// --- Compare ---

// CompareParams specifies which configurations to compare.
type CompareParams struct {
	ConfigIDs   []string `json:"config_ids"`
	Metrics     []string `json:"metrics"`
	Concurrency int      `json:"concurrency"`
}

// CompareEntry is one row in the comparison table.
type CompareEntry struct {
	ConfigID      string          `json:"config_id"`
	Hardware      string          `json:"hardware"`
	Engine        string          `json:"engine"`
	Model         string          `json:"model"`
	Status        string          `json:"status"`
	Metrics       map[string]any  `json:"metrics"`
}

// Compare returns side-by-side performance data for multiple configurations.
func (s *Store) Compare(ctx context.Context, p CompareParams) ([]CompareEntry, error) {
	if len(p.ConfigIDs) < 2 {
		return nil, fmt.Errorf("compare requires at least 2 config_ids")
	}
	if len(p.Metrics) == 0 {
		p.Metrics = []string{"throughput_tps", "ttft_ms_p95", "vram_usage_mib"}
	}

	placeholders := make([]string, len(p.ConfigIDs))
	args := make([]any, len(p.ConfigIDs))
	for i, id := range p.ConfigIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(`
SELECT
    c.id, c.hardware_id, ea.type, c.model_id, c.status,
    AVG(b.throughput_tps), AVG(b.ttft_ms_p95), AVG(b.tpot_ms_p95),
    AVG(b.vram_usage_mib), AVG(b.power_draw_watts), AVG(b.error_rate)
FROM configurations c
JOIN engine_assets ea ON c.engine_id = ea.id
LEFT JOIN benchmark_results b ON b.config_id = c.id
WHERE c.id IN (%s)`, strings.Join(placeholders, ","))

	if p.Concurrency > 0 {
		query += " AND (b.concurrency = ? OR b.concurrency IS NULL)"
		args = append(args, p.Concurrency)
	}
	query += "\nGROUP BY c.id"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("compare configurations: %w", err)
	}
	defer rows.Close()

	var entries []CompareEntry
	for rows.Next() {
		var e CompareEntry
		var throughput, ttft, tpot, vram, power, errRate sql.NullFloat64

		if err := rows.Scan(&e.ConfigID, &e.Hardware, &e.Engine, &e.Model, &e.Status,
			&throughput, &ttft, &tpot, &vram, &power, &errRate); err != nil {
			return nil, fmt.Errorf("scan compare row: %w", err)
		}
		e.Metrics = map[string]any{
			"throughput_tps":  nullFloat(throughput),
			"ttft_ms_p95":    nullFloat(ttft),
			"tpot_ms_p95":    nullFloat(tpot),
			"vram_usage_mib": nullFloat(vram),
			"power_watts":    nullFloat(power),
			"error_rate":     nullFloat(errRate),
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// --- Lineage ---

// LineageEntry represents one node in the configuration evolution chain.
type LineageEntry struct {
	ConfigID   string  `json:"config_id"`
	Status     string  `json:"status"`
	Depth      int     `json:"depth"`
	BestTPS    float64 `json:"best_tps,omitempty"`
}

// Lineage returns the derivation chain for a configuration (ancestors + descendants).
func (s *Store) Lineage(ctx context.Context, configID string) ([]LineageEntry, error) {
	query := `
WITH RECURSIVE chain AS (
    SELECT id, status, derived_from, 0 AS depth FROM configurations WHERE id = ?
    UNION ALL
    SELECT c.id, c.status, c.derived_from, ch.depth + 1
    FROM configurations c JOIN chain ch ON c.derived_from = ch.id
    WHERE ch.depth < 10
)
SELECT
    chain.id, chain.status, chain.depth,
    COALESCE((SELECT MAX(b.throughput_tps) FROM benchmark_results b WHERE b.config_id = chain.id), 0)
FROM chain ORDER BY depth`

	rows, err := s.db.QueryContext(ctx, query, configID)
	if err != nil {
		return nil, fmt.Errorf("lineage query: %w", err)
	}
	defer rows.Close()

	var entries []LineageEntry
	for rows.Next() {
		var e LineageEntry
		if err := rows.Scan(&e.ConfigID, &e.Status, &e.Depth, &e.BestTPS); err != nil {
			return nil, fmt.Errorf("scan lineage row: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// --- Gaps ---

// GapEntry identifies a Hardware×Engine×Model combination with insufficient testing.
type GapEntry struct {
	HardwareID     string `json:"hardware_id"`
	EngineType     string `json:"engine_type"`
	ModelID        string `json:"model_id"`
	BenchmarkCount int    `json:"benchmark_count"`
}

// GapsParams controls gap discovery.
type GapsParams struct {
	Hardware      string `json:"hardware"`
	MinBenchmarks int    `json:"min_benchmarks"`
}

// Gaps finds Hardware×Engine×Model combinations that lack sufficient benchmark data.
func (s *Store) Gaps(ctx context.Context, p GapsParams) ([]GapEntry, error) {
	if p.MinBenchmarks <= 0 {
		p.MinBenchmarks = 3
	}

	// Find all valid HW×Engine×Model combinations from static knowledge,
	// then LEFT JOIN with actual configurations to find gaps.
	query := `
SELECT
    mv.hardware_id, mv.engine_type, mv.model_id,
    COALESCE(bench_counts.cnt, 0) AS benchmark_count
FROM model_variants mv
LEFT JOIN (
    SELECT c.hardware_id, ea.type AS engine_type, c.model_id, COUNT(b.id) AS cnt
    FROM configurations c
    JOIN engine_assets ea ON c.engine_id = ea.id
    LEFT JOIN benchmark_results b ON b.config_id = c.id
    GROUP BY c.hardware_id, ea.type, c.model_id
) bench_counts ON bench_counts.hardware_id = mv.hardware_id
    AND bench_counts.engine_type = mv.engine_type
    AND bench_counts.model_id = mv.model_id
WHERE COALESCE(bench_counts.cnt, 0) < ?`

	args := []any{p.MinBenchmarks}
	if p.Hardware != "" {
		query += " AND mv.hardware_id = ?"
		args = append(args, p.Hardware)
	}
	query += "\nORDER BY benchmark_count ASC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("gaps query: %w", err)
	}
	defer rows.Close()

	var entries []GapEntry
	for rows.Next() {
		var e GapEntry
		if err := rows.Scan(&e.HardwareID, &e.EngineType, &e.ModelID, &e.BenchmarkCount); err != nil {
			return nil, fmt.Errorf("scan gap row: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// --- Aggregate ---

// AggregateParams controls aggregation queries.
type AggregateParams struct {
	Hardware string `json:"hardware"`
	Model    string `json:"model"`
	GroupBy  string `json:"group_by"` // "engine", "hardware", "model"
}

// AggregateEntry is one group in the aggregation.
type AggregateEntry struct {
	GroupKey       string  `json:"group_key"`
	GroupValue     string  `json:"group_value"`
	ConfigCount    int     `json:"config_count"`
	AvgThroughput  float64 `json:"avg_throughput"`
	AvgLatency     float64 `json:"avg_latency"`
	MinThroughput  float64 `json:"min_throughput"`
	MaxThroughput  float64 `json:"max_throughput"`
}

// Aggregate groups and summarizes performance data.
func (s *Store) Aggregate(ctx context.Context, p AggregateParams) ([]AggregateEntry, error) {
	groupCol := "ea.type"
	groupLabel := "engine"
	switch p.GroupBy {
	case "hardware":
		groupCol = "c.hardware_id"
		groupLabel = "hardware"
	case "model":
		groupCol = "c.model_id"
		groupLabel = "model"
	}

	query := fmt.Sprintf(`
SELECT
    '%s', %s,
    COUNT(DISTINCT c.id),
    AVG(b.throughput_tps), AVG(b.ttft_ms_p95),
    MIN(b.throughput_tps), MAX(b.throughput_tps)
FROM benchmark_results b
JOIN configurations c ON b.config_id = c.id
JOIN engine_assets ea ON c.engine_id = ea.id
WHERE b.throughput_tps IS NOT NULL`, groupLabel, groupCol)

	var args []any
	if p.Hardware != "" {
		query += " AND c.hardware_id = ?"
		args = append(args, p.Hardware)
	}
	if p.Model != "" {
		query += " AND c.model_id = ?"
		args = append(args, p.Model)
	}
	query += fmt.Sprintf("\nGROUP BY %s\nORDER BY AVG(b.throughput_tps) DESC", groupCol)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("aggregate query: %w", err)
	}
	defer rows.Close()

	var entries []AggregateEntry
	for rows.Next() {
		var e AggregateEntry
		if err := rows.Scan(&e.GroupKey, &e.GroupValue, &e.ConfigCount,
			&e.AvgThroughput, &e.AvgLatency, &e.MinThroughput, &e.MaxThroughput); err != nil {
			return nil, fmt.Errorf("scan aggregate row: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// --- Static Knowledge Queries ---

// ListHardwareProfiles returns all hardware profiles from the static knowledge tables.
func (s *Store) ListHardwareProfiles(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, gpu_arch, COALESCE(gpu_vram_mib,0), COALESCE(cpu_arch,''),
		        COALESCE(cpu_cores,0), COALESCE(ram_mib,0), unified_memory,
		        COALESCE(tdp_watts,0)
		 FROM hardware_profiles ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list hardware profiles: %w", err)
	}
	defer rows.Close()

	var profiles []map[string]any
	for rows.Next() {
		var id, name, gpuArch, cpuArch string
		var vram, cpuCores, ram, tdp int
		var unified bool
		if err := rows.Scan(&id, &name, &gpuArch, &vram, &cpuArch, &cpuCores, &ram, &unified, &tdp); err != nil {
			return nil, fmt.Errorf("scan hardware profile: %w", err)
		}
		profiles = append(profiles, map[string]any{
			"id": id, "name": name, "gpu_arch": gpuArch, "gpu_vram_mib": vram,
			"cpu_arch": cpuArch, "cpu_cores": cpuCores, "ram_mib": ram,
			"unified_memory": unified, "tdp_watts": tdp,
		})
	}
	return profiles, rows.Err()
}

// ListEngineAssets returns all engine assets from the static knowledge tables.
func (s *Store) ListEngineAssets(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ea.id, ea.type, COALESCE(ea.version,''), COALESCE(ea.image_name,''),
		        COALESCE(ea.image_tag,''), COALESCE(ea.image_size_mb,0), COALESCE(ea.api_protocol,''),
		        COALESCE(ea.perf_gain_desc,''),
		        COALESCE(GROUP_CONCAT(ef.feature), '') AS features
		 FROM engine_assets ea
		 LEFT JOIN engine_features ef ON ea.id = ef.engine_id
		 GROUP BY ea.id
		 ORDER BY ea.type, ea.version`)
	if err != nil {
		return nil, fmt.Errorf("list engine assets: %w", err)
	}
	defer rows.Close()

	var assets []map[string]any
	for rows.Next() {
		var id, typ, version, imgName, imgTag, protocol, perf, featStr string
		var sizeMB int
		if err := rows.Scan(&id, &typ, &version, &imgName, &imgTag, &sizeMB, &protocol, &perf, &featStr); err != nil {
			return nil, fmt.Errorf("scan engine asset: %w", err)
		}
		var features []string
		if featStr != "" {
			features = strings.Split(featStr, ",")
		}
		assets = append(assets, map[string]any{
			"id": id, "type": typ, "version": version,
			"image": imgName + ":" + imgTag, "size_mb": sizeMB,
			"protocol": protocol, "perf_gain": perf, "features": features,
		})
	}
	return assets, rows.Err()
}

func nullFloat(n sql.NullFloat64) float64 {
	if n.Valid {
		return n.Float64
	}
	return 0
}
