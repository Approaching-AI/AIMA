package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	benchpkg "github.com/jguan/aima/internal/benchmark"
	"github.com/jguan/aima/internal/knowledge"

	state "github.com/jguan/aima/internal"
)

func postProcessBenchmarkSave(ctx context.Context, db *state.DB, kStore *knowledge.Store, benchmarkID, configID, hardware, engine, model string, throughputTPS float64) {
	if err := writeBenchmarkValidation(ctx, db, benchmarkID, configID, hardware, engine, model, throughputTPS); err != nil {
		slog.Warn("benchmark validation: write failed", "error", err, "benchmark_id", benchmarkID)
	}
	refreshPerfVectors(ctx, kStore)
}

func writeBenchmarkValidation(ctx context.Context, db *state.DB, benchmarkID, configID, hardware, engine, model string, actualThroughput float64) error {
	if db == nil || benchmarkID == "" || configID == "" || actualThroughput <= 0 || hardware == "" || engine == "" || model == "" {
		return nil
	}

	predicted, err := lookupPredictedThroughput(ctx, db.RawDB(), hardware, engine, model)
	if err != nil {
		return err
	}
	if predicted <= 0 {
		return nil
	}

	deviation := ((actualThroughput - predicted) / predicted) * 100
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(benchmarkID+"|throughput_tps")))[:16]
	return db.InsertValidation(ctx, id, configID, hardware, engine, model, "throughput_tps", predicted, actualThroughput, deviation)
}

func lookupPredictedThroughput(ctx context.Context, db *sql.DB, hardware, engine, model string) (float64, error) {
	if db == nil {
		return 0, nil
	}

	var throughput sql.NullFloat64
	err := db.QueryRowContext(ctx, `
SELECT b.throughput_tps
FROM configurations c
JOIN benchmark_results b ON b.config_id = c.id
WHERE c.status = 'golden'
  AND c.hardware_id = ? AND c.engine_id = ? AND c.model_id = ?
ORDER BY b.throughput_tps DESC
LIMIT 1`, hardware, engine, model).Scan(&throughput)
	switch {
	case err == nil && throughput.Valid && throughput.Float64 > 0:
		return throughput.Float64, nil
	case err != nil && err != sql.ErrNoRows:
		return 0, fmt.Errorf("query golden throughput: %w", err)
	}

	var expectedPerf string
	err = db.QueryRowContext(ctx, `
SELECT expected_perf
FROM model_variants
WHERE model_id = ? AND engine_type = ?
  AND (
    hardware_id = ?
    OR hardware_id IN (SELECT id FROM hardware_profiles WHERE gpu_arch = ?)
  )
ORDER BY CASE WHEN hardware_id = ? THEN 0 ELSE 1 END
LIMIT 1`, model, engine, hardware, hardware, hardware).Scan(&expectedPerf)
	switch {
	case err == sql.ErrNoRows:
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("query expected throughput: %w", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(expectedPerf), &payload); err != nil {
		return 0, fmt.Errorf("parse expected throughput: %w", err)
	}

	rawTPS, ok := payload["tokens_per_second"]
	if !ok {
		return 0, nil
	}
	switch v := rawTPS.(type) {
	case float64:
		return v, nil
	case []any:
		if len(v) == 0 {
			return 0, nil
		}
		min := toFloat64(v[0])
		if len(v) == 1 {
			return min, nil
		}
		max := toFloat64(v[1])
		if max == 0 {
			return min, nil
		}
		return (min + max) / 2, nil
	default:
		return 0, nil
	}
}

func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}

func refreshPerfVectors(ctx context.Context, kStore *knowledge.Store) {
	if kStore == nil {
		return
	}
	if err := kStore.RefreshPerfVectors(ctx); err != nil {
		slog.Warn("perf vectors: refresh failed", "error", err)
	}
}

// saveBenchmarkResult saves a benchmark result and its configuration to the DB.
// Returns (benchmarkID, configID) or error.
func saveBenchmarkResult(ctx context.Context, db *state.DB, hardware, engineID, model string,
	result *benchpkg.RunResult, concurrency, inputTokens, maxTokens int, notes string) (string, string, error) {

	configJSON, _ := json.Marshal(map[string]any{
		"concurrency":  concurrency,
		"max_tokens":   maxTokens,
		"input_tokens": inputTokens,
	})
	configHash := fmt.Sprintf("%x", sha256.Sum256(
		[]byte(hardware+"|"+engineID+"|"+model+"|"+string(configJSON))))

	existingCfg, err := db.FindConfigByHash(ctx, configHash)
	if err != nil {
		return "", "", fmt.Errorf("find config: %w", err)
	}
	if existingCfg == nil {
		existingCfg = &state.Configuration{
			ID: configHash[:16], HardwareID: hardware,
			EngineID: engineID, ModelID: model,
			Config: string(configJSON), ConfigHash: configHash,
			Status: "experiment", Source: "benchmark",
		}
		if err := db.InsertConfiguration(ctx, existingCfg); err != nil {
			return "", "", fmt.Errorf("create configuration: %w", err)
		}
	}

	benchmarkID := fmt.Sprintf("%x", sha256.Sum256(
		[]byte(existingCfg.ID+"|"+fmt.Sprintf("%d", time.Now().UnixNano()))))[:16]

	br := &state.BenchmarkResult{
		ID: benchmarkID, ConfigID: existingCfg.ID, Concurrency: concurrency,
		InputLenBucket:  tokenBucket(result.AvgInputTokens),
		OutputLenBucket: tokenBucket(result.AvgOutputTokens),
		Modality:        "text",
		TTFTP50ms:       result.TTFTP50ms, TTFTP95ms: result.TTFTP95ms, TTFTP99ms: result.TTFTP99ms,
		TPOTP50ms: result.TPOTP50ms, TPOTP95ms: result.TPOTP95ms,
		ThroughputTPS: result.ThroughputTPS, QPS: result.QPS,
		ErrorRate: result.ErrorRate, SampleCount: result.TotalRequests,
		DurationS: int(result.DurationMs / 1000), TestedAt: time.Now(),
		Stability: stabilityFromCV(result.TTFTCVPct),
		Notes:     notes,
	}
	if err := db.InsertBenchmarkResult(ctx, br); err != nil {
		return "", "", fmt.Errorf("save benchmark result: %w", err)
	}
	return benchmarkID, existingCfg.ID, nil
}

// maybeAutoPromote promotes a config to golden if its benchmark throughput beats
// the current golden by >5%. Returns (promoted, oldGoldenID).
func maybeAutoPromote(ctx context.Context, db *state.DB, newConfigID string, newThroughput float64, hardware, engine, model string) (bool, string) {
	goldenCfg, goldenBench, err := db.FindGoldenBenchmark(ctx, hardware, engine, model)
	if err != nil {
		slog.Warn("auto-promote: failed to query golden", "error", err)
		return false, ""
	}

	// No golden exists -> promote this one directly
	if goldenCfg == nil {
		if err := db.UpdateConfigStatus(ctx, newConfigID, "golden"); err == nil {
			slog.Info("auto-promote: first golden config", "config_id", newConfigID)
			return true, ""
		}
		return false, ""
	}

	// Same config -> skip
	if goldenCfg.ID == newConfigID {
		return false, ""
	}

	// Compare: new must beat golden by >5% to avoid noisy promotion
	if goldenBench != nil && newThroughput > goldenBench.ThroughputTPS*1.05 {
		if err := db.UpdateConfigStatus(ctx, goldenCfg.ID, "experiment"); err != nil {
			slog.Warn("auto-promote: failed to demote old golden", "config_id", goldenCfg.ID, "error", err)
			return false, ""
		}
		if err := db.UpdateConfigStatus(ctx, newConfigID, "golden"); err != nil {
			slog.Warn("auto-promote: failed to promote new golden", "config_id", newConfigID, "error", err)
			// Restore old golden status
			_ = db.UpdateConfigStatus(ctx, goldenCfg.ID, "golden")
			return false, ""
		}
		slog.Info("auto-promote: new golden config",
			"old_golden", goldenCfg.ID, "new_golden", newConfigID,
			"old_tps", goldenBench.ThroughputTPS, "new_tps", newThroughput)
		return true, goldenCfg.ID
	}
	return false, ""
}

// updatePerfOverlay writes benchmark observations outside the catalog merge path.
// Runtime overlays must not masquerade as model assets because same-name assets
// replace the embedded catalog on restart.
func updatePerfOverlay(dataDir, model, hardware, engine string, result *benchpkg.RunResult) {
	observationsDir := filepath.Join(dataDir, "observations", "models")
	if err := os.MkdirAll(observationsDir, 0o755); err != nil {
		slog.Warn("perf observations: mkdir failed", "error", err)
		return
	}

	// Sanitize model name for filename
	safeName := strings.ReplaceAll(model, "/", "_")
	safeName = strings.ReplaceAll(safeName, ":", "_")
	observationPath := filepath.Join(observationsDir, safeName+"-perf.json")

	observation := map[string]any{
		"model":          model,
		"hardware":       hardware,
		"engine":         engine,
		"throughput_tps": result.ThroughputTPS,
		"ttft_p50_ms":    result.TTFTP50ms,
		"ttft_p95_ms":    result.TTFTP95ms,
		"tpot_p50_ms":    result.TPOTP50ms,
		"qps":            result.QPS,
		"updated_at":     time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(observation, "", "  ")
	if err != nil {
		slog.Warn("perf observations: marshal failed", "error", err)
		return
	}
	if err := os.WriteFile(observationPath, data, 0o644); err != nil {
		slog.Warn("perf observations: write failed", "path", observationPath, "error", err)
		return
	}
	slog.Info("perf observation updated", "model", model, "path", observationPath, "throughput_tps", result.ThroughputTPS)
}

// tokenBucket converts a token count to a human-readable bucket string.
func tokenBucket(tokens int) string {
	switch {
	case tokens >= 128000:
		return "128K"
	case tokens >= 32000:
		return "32K"
	case tokens >= 8000:
		return "8K"
	case tokens >= 1000:
		return fmt.Sprintf("%dK", tokens/1000)
	default:
		return fmt.Sprintf("%d", tokens)
	}
}

// stabilityFromCV derives a stability label from coefficient of variation (percentage).
func stabilityFromCV(cvPct float64) string {
	switch {
	case cvPct <= 15:
		return "stable"
	case cvPct <= 30:
		return "fluctuating"
	default:
		return "unstable"
	}
}
