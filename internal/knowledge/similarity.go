package knowledge

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// SimilarParams controls similarity search.
type SimilarParams struct {
	ConfigID        string             `json:"config_id"`
	Weights         map[string]float64 `json:"weights"`
	FilterHardware  string             `json:"filter_hardware"`
	ExcludeSame     bool               `json:"exclude_same_config"`
	Limit           int                `json:"limit"`
}

// SimilarResult is a configuration similar to the query config.
type SimilarResult struct {
	ConfigID   string  `json:"config_id"`
	HardwareID string  `json:"hardware_id"`
	EngineID   string  `json:"engine_id"`
	ModelID    string  `json:"model_id"`
	Distance   float64 `json:"distance"`
	Throughput float64 `json:"avg_throughput"`
	TTFTp95    float64 `json:"avg_ttft_p95"`
	VRAMMiB    float64 `json:"avg_vram_mib"`
}

// perfVector is an in-memory performance vector with 6 normalized dimensions.
type perfVector struct {
	configID   string
	hardwareID string
	engineID   string
	modelID    string
	dims       [6]float64 // ttft, tpot, throughput, qps, vram, power
	// Raw values for display
	throughput float64
	ttft       float64
	vram       float64
}

// Similar finds configurations with the most similar performance profile.
// Uses weighted Euclidean distance on normalized performance vectors.
func (s *Store) Similar(ctx context.Context, p SimilarParams) ([]SimilarResult, error) {
	if p.Limit <= 0 {
		p.Limit = 5
	}
	if p.ExcludeSame && p.ConfigID == "" {
		return nil, fmt.Errorf("config_id is required")
	}

	// Default weights
	weights := [6]float64{0.2, 0.1, 0.3, 0.1, 0.2, 0.1}
	if len(p.Weights) > 0 {
		if v, ok := p.Weights["latency"]; ok {
			weights[0] = v
		}
		if v, ok := p.Weights["tpot"]; ok {
			weights[1] = v
		}
		if v, ok := p.Weights["throughput"]; ok {
			weights[2] = v
		}
		if v, ok := p.Weights["qps"]; ok {
			weights[3] = v
		}
		if v, ok := p.Weights["vram"]; ok {
			weights[4] = v
		}
		if v, ok := p.Weights["power"]; ok {
			weights[5] = v
		}
	}

	// Load all perf_vectors into memory
	query := `
SELECT pv.config_id, c.hardware_id, c.engine_id, c.model_id,
       COALESCE(pv.norm_ttft_p95, 0), COALESCE(pv.norm_tpot_p95, 0),
       COALESCE(pv.norm_throughput, 0), COALESCE(pv.norm_qps, 0),
       COALESCE(pv.norm_vram, 0), COALESCE(pv.norm_power, 0),
       COALESCE(pv.avg_throughput, 0), COALESCE(pv.avg_ttft_p95, 0), COALESCE(pv.avg_vram_mib, 0)
FROM perf_vectors pv
JOIN configurations c ON pv.config_id = c.id`
	var args []any
	if p.FilterHardware != "" {
		query += " WHERE c.hardware_id = ?"
		args = append(args, p.FilterHardware)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load perf vectors: %w", err)
	}
	defer rows.Close()

	var vectors []perfVector
	for rows.Next() {
		var v perfVector
		if err := rows.Scan(&v.configID, &v.hardwareID, &v.engineID, &v.modelID,
			&v.dims[0], &v.dims[1], &v.dims[2], &v.dims[3], &v.dims[4], &v.dims[5],
			&v.throughput, &v.ttft, &v.vram); err != nil {
			return nil, fmt.Errorf("scan perf vector: %w", err)
		}
		vectors = append(vectors, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(vectors) == 0 {
		return nil, nil
	}

	// Find the query vector
	var queryVec *perfVector
	for i := range vectors {
		if vectors[i].configID == p.ConfigID {
			queryVec = &vectors[i]
			break
		}
	}
	if queryVec == nil {
		return nil, fmt.Errorf("config %s not found in perf_vectors", p.ConfigID)
	}

	// Compute weighted Euclidean distance for all others
	type distEntry struct {
		idx  int
		dist float64
	}
	var dists []distEntry
	for i := range vectors {
		if p.ExcludeSame && vectors[i].configID == p.ConfigID {
			continue
		}
		d := weightedEuclidean(queryVec.dims, vectors[i].dims, weights)
		dists = append(dists, distEntry{idx: i, dist: d})
	}

	sort.Slice(dists, func(i, j int) bool { return dists[i].dist < dists[j].dist })

	limit := p.Limit
	if limit > len(dists) {
		limit = len(dists)
	}

	results := make([]SimilarResult, limit)
	for i := 0; i < limit; i++ {
		v := vectors[dists[i].idx]
		results[i] = SimilarResult{
			ConfigID:   v.configID,
			HardwareID: v.hardwareID,
			EngineID:   v.engineID,
			ModelID:    v.modelID,
			Distance:   dists[i].dist,
			Throughput: v.throughput,
			TTFTp95:    v.ttft,
			VRAMMiB:    v.vram,
		}
	}
	return results, nil
}

// RefreshPerfVectors recalculates normalized performance vectors from benchmark data.
func (s *Store) RefreshPerfVectors(ctx context.Context) error {
	// Get global min/max for each metric
	var minTTFT, maxTTFT, minTPOT, maxTPOT float64
	var minTP, maxTP, minQPS, maxQPS float64
	var minVRAM, maxVRAM, minPower, maxPower float64

	err := s.db.QueryRowContext(ctx, `
SELECT
    COALESCE(MIN(ttft_ms_p95), 0), COALESCE(MAX(ttft_ms_p95), 1),
    COALESCE(MIN(tpot_ms_p95), 0), COALESCE(MAX(tpot_ms_p95), 1),
    COALESCE(MIN(throughput_tps), 0), COALESCE(MAX(throughput_tps), 1),
    COALESCE(MIN(qps), 0), COALESCE(MAX(qps), 1),
    COALESCE(MIN(vram_usage_mib), 0), COALESCE(MAX(vram_usage_mib), 1),
    COALESCE(MIN(power_draw_watts), 0), COALESCE(MAX(power_draw_watts), 1)
FROM benchmark_results
WHERE throughput_tps IS NOT NULL`).Scan(
		&minTTFT, &maxTTFT, &minTPOT, &maxTPOT,
		&minTP, &maxTP, &minQPS, &maxQPS,
		&minVRAM, &maxVRAM, &minPower, &maxPower)
	if err != nil {
		return fmt.Errorf("get benchmark ranges: %w", err)
	}

	// Aggregate per-config and insert/replace into perf_vectors
	_, err = s.db.ExecContext(ctx, `
INSERT OR REPLACE INTO perf_vectors
    (config_id, norm_ttft_p95, norm_tpot_p95, norm_throughput, norm_qps, norm_vram, norm_power,
     avg_throughput, avg_ttft_p95, avg_vram_mib, benchmark_count, updated_at)
SELECT
    config_id,
    CASE WHEN ? = ? THEN 0.5 ELSE (AVG(ttft_ms_p95) - ?) / (? - ?) END,
    CASE WHEN ? = ? THEN 0.5 ELSE (AVG(tpot_ms_p95) - ?) / (? - ?) END,
    CASE WHEN ? = ? THEN 0.5 ELSE (AVG(throughput_tps) - ?) / (? - ?) END,
    CASE WHEN ? = ? THEN 0.5 ELSE (AVG(qps) - ?) / (? - ?) END,
    CASE WHEN ? = ? THEN 0.5 ELSE (AVG(vram_usage_mib) - ?) / (? - ?) END,
    CASE WHEN ? = ? THEN 0.5 ELSE (AVG(power_draw_watts) - ?) / (? - ?) END,
    AVG(throughput_tps), AVG(ttft_ms_p95), AVG(vram_usage_mib),
    COUNT(*), CURRENT_TIMESTAMP
FROM benchmark_results
WHERE throughput_tps IS NOT NULL
GROUP BY config_id`,
		// TTFT normalization
		maxTTFT, minTTFT, minTTFT, maxTTFT, minTTFT,
		// TPOT normalization
		maxTPOT, minTPOT, minTPOT, maxTPOT, minTPOT,
		// Throughput normalization
		maxTP, minTP, minTP, maxTP, minTP,
		// QPS normalization
		maxQPS, minQPS, minQPS, maxQPS, minQPS,
		// VRAM normalization
		maxVRAM, minVRAM, minVRAM, maxVRAM, minVRAM,
		// Power normalization
		maxPower, minPower, minPower, maxPower, minPower,
	)
	if err != nil {
		return fmt.Errorf("refresh perf vectors: %w", err)
	}
	return nil
}

func weightedEuclidean(a, b, w [6]float64) float64 {
	var sum float64
	for i := 0; i < 6; i++ {
		diff := a[i] - b[i]
		sum += w[i] * diff * diff
	}
	return math.Sqrt(sum)
}
