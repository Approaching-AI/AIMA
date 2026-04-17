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
	"sync"
	"time"

	benchpkg "github.com/jguan/aima/internal/benchmark"
	"github.com/jguan/aima/internal/hal"
	"github.com/jguan/aima/internal/knowledge"

	state "github.com/jguan/aima/internal"
)

type benchmarkSystemMetrics struct {
	VRAMUsageMiB      int
	RAMUsageMiB       int
	PowerDrawWatts    float64
	GPUUtilizationPct float64
	CPUUsagePct       float64
}

type benchmarkMetricsWindow struct {
	mu           sync.Mutex
	peakVRAMMiB  int
	peakRAMMiB   int
	cpuTotalPct  float64
	cpuSamples   int
	gpuTotalPct  float64
	gpuSamples   int
	powerTotalW  float64
	powerSamples int
}

var benchmarkMetricsSampleInterval = time.Second

// unifiedMemoryRatioLow/High define the VRAM-to-RAM ratio band that identifies
// unified memory systems (e.g., NVIDIA GB10, Apple Silicon) where VRAM and RAM
// report nearly identical values from the same physical memory pool.
const (
	unifiedMemoryRatioLow  = 0.90
	unifiedMemoryRatioHigh = 1.10
)

func isUnifiedMemory(vramMiB, ramMiB int) bool {
	if vramMiB <= 0 || ramMiB <= 0 {
		return false
	}
	ratio := float64(vramMiB) / float64(ramMiB)
	return ratio > unifiedMemoryRatioLow && ratio < unifiedMemoryRatioHigh
}

var executeBenchmarkRun = benchpkg.Run

// defaultChatRequester creates a ChatRequester from RunConfig for backward compatibility.
func defaultChatRequester(cfg benchpkg.RunConfig) *benchpkg.ChatRequester {
	return &benchpkg.ChatRequester{
		Model:          cfg.Model,
		MaxTokens:      cfg.MaxTokens,
		InputTokens:    cfg.InputTokens,
		Temperature:    cfg.Temperature,
		APIKey:         cfg.APIKey,
		Timeout:        cfg.Timeout,
		MinOutputRatio: cfg.MinOutputRatio,
		MaxRetries:     cfg.MaxRetries,
		RetryDelay:     cfg.RetryDelay,
	}
}

func storageBenchmarkModality(modality string) string {
	switch strings.ToLower(strings.TrimSpace(modality)) {
	case "", "llm", "text":
		return "text"
	default:
		return strings.ToLower(strings.TrimSpace(modality))
	}
}

var collectBenchmarkSystemMetrics = func(ctx context.Context) benchmarkSystemMetrics {
	var metrics benchmarkSystemMetrics

	if current, err := hal.CollectMetrics(ctx); err == nil {
		metrics.RAMUsageMiB = current.RAM.UsedMiB
		metrics.CPUUsagePct = current.CPU.UsagePercent
		if current.GPU != nil {
			metrics.VRAMUsageMiB = current.GPU.MemoryUsedMiB
			metrics.PowerDrawWatts = current.GPU.PowerDrawWatts
			metrics.GPUUtilizationPct = float64(current.GPU.UtilizationPercent)
		}
	}

	if metrics.RAMUsageMiB == 0 {
		if hw, err := hal.Detect(ctx); err == nil {
			if hw.RAM.TotalMiB > 0 && hw.RAM.AvailableMiB >= 0 {
				metrics.RAMUsageMiB = max(hw.RAM.TotalMiB-hw.RAM.AvailableMiB, 0)
			}
		}
	}
	return metrics
}

func (w *benchmarkMetricsWindow) observe(metrics benchmarkSystemMetrics) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if metrics.VRAMUsageMiB > w.peakVRAMMiB {
		w.peakVRAMMiB = metrics.VRAMUsageMiB
	}
	if metrics.RAMUsageMiB > w.peakRAMMiB {
		w.peakRAMMiB = metrics.RAMUsageMiB
	}

	w.cpuTotalPct += metrics.CPUUsagePct
	w.cpuSamples++

	if metrics.VRAMUsageMiB > 0 || metrics.GPUUtilizationPct > 0 || metrics.PowerDrawWatts > 0 {
		w.gpuTotalPct += metrics.GPUUtilizationPct
		w.gpuSamples++
		w.powerTotalW += metrics.PowerDrawWatts
		w.powerSamples++
	}
}

func (w *benchmarkMetricsWindow) snapshot() benchmarkSystemMetrics {
	w.mu.Lock()
	defer w.mu.Unlock()

	metrics := benchmarkSystemMetrics{
		VRAMUsageMiB: w.peakVRAMMiB,
		RAMUsageMiB:  w.peakRAMMiB,
	}
	if w.cpuSamples > 0 {
		metrics.CPUUsagePct = w.cpuTotalPct / float64(w.cpuSamples)
	}
	if w.gpuSamples > 0 {
		metrics.GPUUtilizationPct = w.gpuTotalPct / float64(w.gpuSamples)
	}
	if w.powerSamples > 0 {
		metrics.PowerDrawWatts = w.powerTotalW / float64(w.powerSamples)
	}
	return metrics
}

func runBenchmarkWithMetrics(ctx context.Context, cfg benchpkg.RunConfig) (*benchpkg.RunResult, benchmarkSystemMetrics, error) {
	return runBenchmarkWithMetricsAndRequester(ctx, cfg, defaultChatRequester(cfg))
}

func runBenchmarkWithMetricsAndRequester(ctx context.Context, cfg benchpkg.RunConfig, req benchpkg.Requester) (*benchpkg.RunResult, benchmarkSystemMetrics, error) {
	// Capture baseline metrics before benchmark for delta calculation
	baseline := collectBenchmarkSystemMetrics(ctx)

	window := &benchmarkMetricsWindow{}
	sampleCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		window.observe(collectBenchmarkSystemMetrics(sampleCtx))
		ticker := time.NewTicker(benchmarkMetricsSampleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-sampleCtx.Done():
				return
			case <-ticker.C:
				window.observe(collectBenchmarkSystemMetrics(sampleCtx))
			}
		}
	}()

	result, err := executeBenchmarkRun(ctx, cfg, req)
	window.observe(collectBenchmarkSystemMetrics(ctx))
	cancel()
	wg.Wait()

	metrics := window.snapshot()
	if metrics == (benchmarkSystemMetrics{}) {
		metrics = collectBenchmarkSystemMetrics(ctx)
	}

	// Apply M2 honesty rule: only report memory numbers we can actually
	// attribute to this benchmark run. The host-level probe (nvidia-smi,
	// mthreads-smi, /proc) sees every tenant on the device; for containerised
	// or remote engines the raw peak can reflect unrelated workloads, so we
	// always report delta-over-baseline. If the peak never rose above the
	// baseline, the probe is unreliable for this run and we emit 0 (NULL in
	// the DB via COALESCE), rather than a misleading absolute value.
	if metrics.VRAMUsageMiB > 0 || baseline.VRAMUsageMiB > 0 {
		if metrics.VRAMUsageMiB > baseline.VRAMUsageMiB {
			metrics.VRAMUsageMiB -= baseline.VRAMUsageMiB
		} else {
			slog.Debug("benchmark metrics: VRAM probe did not observe engine delta, dropping",
				"baseline_mib", baseline.VRAMUsageMiB, "peak_mib", metrics.VRAMUsageMiB)
			metrics.VRAMUsageMiB = 0
		}
	}
	if metrics.RAMUsageMiB > 0 || baseline.RAMUsageMiB > 0 {
		if metrics.RAMUsageMiB > baseline.RAMUsageMiB {
			metrics.RAMUsageMiB -= baseline.RAMUsageMiB
		} else {
			slog.Debug("benchmark metrics: RAM probe did not observe engine delta, dropping",
				"baseline_mib", baseline.RAMUsageMiB, "peak_mib", metrics.RAMUsageMiB)
			metrics.RAMUsageMiB = 0
		}
	}

	return result, metrics, err
}

func resourceUsageMap(metrics benchmarkSystemMetrics) map[string]any {
	resourceUsage := map[string]any{
		"vram_usage_mib":      metrics.VRAMUsageMiB,
		"ram_usage_mib":       metrics.RAMUsageMiB,
		"cpu_usage_pct":       metrics.CPUUsagePct,
		"gpu_utilization_pct": metrics.GPUUtilizationPct,
		"power_draw_watts":    metrics.PowerDrawWatts,
	}
	for key, value := range resourceUsage {
		switch v := value.(type) {
		case int:
			if v <= 0 {
				delete(resourceUsage, key)
			}
		case float64:
			if v <= 0 {
				delete(resourceUsage, key)
			}
		}
	}

	// Flag unified memory systems for downstream consumers.
	if isUnifiedMemory(metrics.VRAMUsageMiB, metrics.RAMUsageMiB) {
		resourceUsage["unified_memory"] = true
	}

	return resourceUsage
}

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
// Returns (benchmarkID, configID, saved benchmark row) or error.
func saveBenchmarkResult(ctx context.Context, db *state.DB, hardware, engineID, model string,
	modality string, result *benchpkg.RunResult, deployConfig map[string]any, metrics benchmarkSystemMetrics, concurrency int, notes string) (string, string, *state.BenchmarkResult, error) {
	if result == nil {
		return "", "", nil, fmt.Errorf("benchmark result is nil")
	}
	// Bug-7: reject phantom rows that have no runtime evidence at all — this
	// happens when tune's best-config redeploy path copies throughput/TTFT from
	// the best scoring cell into a new row but never ran any request against it.
	// A genuine benchmark always has TotalRequests > 0 OR SuccessfulReqs > 0
	// (e.g., reranker zero-output evidence sets SuccessfulReqs but TotalRequests=0
	// is impossible in practice); be permissive about which counter is set, but
	// require at least one.
	if result.TotalRequests <= 0 && result.SuccessfulReqs <= 0 {
		return "", "", nil, fmt.Errorf("benchmark has no request counters — not saved")
	}
	if result.ThroughputTPS <= 0 && result.QPS <= 0 && result.ReranksPerSec <= 0 {
		return "", "", nil, fmt.Errorf("benchmark has no throughput/QPS signal — not saved")
	}
	// v0.4 §10.1: configurations are deploy-level. Never mix cell-level
	// benchmark params (concurrency/input_tokens/max_tokens) into the hash —
	// doing so multiplies one deploy into one config row per matrix cell and
	// corrupts frontier/dedup. Empty deploy config is acceptable (all cells
	// share a single anchor configuration for this model×engine×hardware).
	config := deployConfig
	if config == nil {
		config = map[string]any{}
	}
	configJSON, _ := json.Marshal(config)
	configHash := fmt.Sprintf("%x", sha256.Sum256(
		[]byte(hardware+"|"+engineID+"|"+model+"|"+string(configJSON))))

	existingCfg, err := db.FindConfigByHash(ctx, configHash)
	if err != nil {
		return "", "", nil, fmt.Errorf("find config: %w", err)
	}
	var newCfg *state.Configuration
	configID := ""
	if existingCfg == nil {
		newCfg = &state.Configuration{
			ID: configHash[:16], HardwareID: hardware,
			EngineID: engineID, ModelID: model,
			Config: string(configJSON), ConfigHash: configHash,
			Status: "experiment", Source: "benchmark",
		}
		configID = newCfg.ID
	} else {
		configID = existingCfg.ID
	}

	benchmarkID := fmt.Sprintf("%x", sha256.Sum256(
		[]byte(configID+"|"+fmt.Sprintf("%d", time.Now().UnixNano()))))[:16]

	if metrics == (benchmarkSystemMetrics{}) {
		metrics = collectBenchmarkSystemMetrics(ctx)
	}
	br := &state.BenchmarkResult{
		ID: benchmarkID, ConfigID: configID, Concurrency: concurrency,
		InputLenBucket:  tokenBucket(result.AvgInputTokens),
		OutputLenBucket: tokenBucket(result.AvgOutputTokens),
		Modality:        storageBenchmarkModality(modality),
		TTFTP50ms:       result.TTFTP50ms, TTFTP95ms: result.TTFTP95ms, TTFTP99ms: result.TTFTP99ms,
		TPOTP50ms: result.TPOTP50ms, TPOTP95ms: result.TPOTP95ms,
		ThroughputTPS: result.ThroughputTPS, QPS: result.QPS,
		VRAMUsageMiB:   metrics.VRAMUsageMiB,
		RAMUsageMiB:    metrics.RAMUsageMiB,
		PowerDrawWatts: metrics.PowerDrawWatts,
		GPUUtilPct:     metrics.GPUUtilizationPct,
		CPUUsagePct:    metrics.CPUUsagePct,
		ErrorRate:      result.ErrorRate,
		SampleCount:    result.TotalRequests,
		DurationS:      int(result.DurationMs / 1000),
		TestedAt:       time.Now(),
		Stability:      deriveStability(result.TTFTCVPct, result.ErrorRate),
		Notes:          notes,
	}
	// v0.4 §10.1 invariant: config + benchmark must land together or neither.
	if err := db.InsertConfigurationAndBenchmarkResult(ctx, existingCfg, newCfg, br); err != nil {
		return "", "", nil, fmt.Errorf("save benchmark result: %w", err)
	}
	return benchmarkID, configID, br, nil
}

// maybeAutoPromote promotes a config to golden if its benchmark throughput beats
// the current golden by >5%. Returns (promoted, oldGoldenID).
// Golden status is configuration-level today, not modality-level, so automatic
// promotion is intentionally limited to text benchmarks until the schema grows
// explicit modality-scoped golden state.
func maybeAutoPromote(ctx context.Context, db *state.DB, newConfigID string, newThroughput float64, hardware, engine, model, modality string) (bool, string) {
	modality = storageBenchmarkModality(modality)
	if modality != "text" {
		return false, ""
	}
	goldenCfg, goldenBench, err := db.FindGoldenBenchmark(ctx, hardware, engine, model, modality)
	if err != nil {
		slog.Warn("auto-promote: failed to query golden", "error", err)
		return false, ""
	}

	// B7: Never promote configs with zero throughput — they represent
	// inconclusive benchmarks (no inference service running).
	if newThroughput <= 0 {
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
func updatePerfOverlay(dataDir, model, hardware, engine string, result *benchpkg.RunResult, saved *state.BenchmarkResult, engineVersion, engineImage string, heterogeneousObservation any) {
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
		"ttft_p99_ms":    result.TTFTP99ms,
		"tpot_p50_ms":    result.TPOTP50ms,
		"tpot_p95_ms":    result.TPOTP95ms,
		"qps":            result.QPS,
		"benchmark_profile": map[string]any{
			"concurrency":       result.Config.Concurrency,
			"num_requests":      result.Config.NumRequests,
			"warmup_count":      result.Config.WarmupCount,
			"rounds":            result.Config.Rounds,
			"input_tokens":      result.Config.InputTokens,
			"max_tokens":        result.Config.MaxTokens,
			"avg_input_tokens":  result.AvgInputTokens,
			"avg_output_tokens": result.AvgOutputTokens,
		},
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	}
	if engineVersion != "" {
		observation["engine_version"] = engineVersion
	}
	if engineImage != "" {
		observation["engine_image"] = engineImage
	}
	if saved != nil {
		observation["resource_usage"] = resourceUsageMap(benchmarkSystemMetrics{
			VRAMUsageMiB:      saved.VRAMUsageMiB,
			RAMUsageMiB:       saved.RAMUsageMiB,
			CPUUsagePct:       saved.CPUUsagePct,
			GPUUtilizationPct: saved.GPUUtilPct,
			PowerDrawWatts:    saved.PowerDrawWatts,
		})
	}
	if hetero, ok := heterogeneousObservation.(map[string]any); ok && len(hetero) > 0 {
		observation["heterogeneous_observation"] = hetero
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

// deriveStability derives a stability label from TTFT coefficient of variation
// and error rate. High error rates short-circuit to "unstable" regardless of
// latency variance — a cell that fails more than half of its requests cannot
// be called stable even if the few successes clustered tightly.
func deriveStability(cvPct, errorRate float64) string {
	if errorRate >= 0.5 {
		return "unstable"
	}
	switch {
	case cvPct <= 15:
		return "stable"
	case cvPct <= 30:
		return "fluctuating"
	default:
		return "unstable"
	}
}
