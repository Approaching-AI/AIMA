package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	benchpkg "github.com/jguan/aima/internal/benchmark"
	"github.com/jguan/aima/internal/mcp"

	state "github.com/jguan/aima/internal"
)

// buildBenchmarkDeps wires benchmark.record, benchmark.run, benchmark.matrix,
// benchmark.list, and knowledge.promote tools.
func buildBenchmarkDeps(ac *appContext, deps *mcp.ToolDeps, resolveEndpoint func(explicit, model string) string) {
	db := ac.db
	kStore := ac.kStore

	deps.RecordBenchmark = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Hardware        string         `json:"hardware"`
			Engine          string         `json:"engine"`
			Model           string         `json:"model"`
			DeviceID        string         `json:"device_id"`
			Config          map[string]any `json:"config"`
			Concurrency     int            `json:"concurrency"`
			InputLenBucket  string         `json:"input_len_bucket"`
			OutputLenBucket string         `json:"output_len_bucket"`
			TTFTP50ms       float64        `json:"ttft_ms_p50"`
			TTFTP95ms       float64        `json:"ttft_ms_p95"`
			TPOTP50ms       float64        `json:"tpot_ms_p50"`
			TPOTP95ms       float64        `json:"tpot_ms_p95"`
			ThroughputTPS   float64        `json:"throughput_tps"`
			QPS             float64        `json:"qps"`
			VRAMUsageMiB    int            `json:"vram_usage_mib"`
			SampleCount     int            `json:"sample_count"`
			Stability       string         `json:"stability"`
			Notes           string         `json:"notes"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse benchmark params: %w", err)
		}
		if p.Concurrency <= 0 {
			p.Concurrency = 1
		}

		// Find or create configuration
		configJSON, err := json.Marshal(p.Config)
		if err != nil {
			return nil, fmt.Errorf("marshal benchmark config: %w", err)
		}
		configHash := fmt.Sprintf("%x", sha256.Sum256(
			[]byte(p.Hardware+"|"+p.Engine+"|"+p.Model+"|"+string(configJSON))))

		cfg, err := db.FindConfigByHash(ctx, configHash)
		if err != nil {
			return nil, err
		}
		if cfg == nil {
			cfg = &state.Configuration{
				ID:         configHash[:16],
				HardwareID: p.Hardware,
				EngineID:   p.Engine,
				ModelID:    p.Model,
				Config:     string(configJSON),
				ConfigHash: configHash,
				Status:     "experiment",
				Source:     "benchmark",
				DeviceID:   p.DeviceID,
			}
			if err := db.InsertConfiguration(ctx, cfg); err != nil {
				return nil, fmt.Errorf("create configuration: %w", err)
			}
		}

		// Insert benchmark result
		benchID := fmt.Sprintf("%x", sha256.Sum256(
			[]byte(cfg.ID+"|"+fmt.Sprintf("%d", time.Now().UnixNano()))))[:16]
		br := &state.BenchmarkResult{
			ID:              benchID,
			ConfigID:        cfg.ID,
			Concurrency:     p.Concurrency,
			InputLenBucket:  p.InputLenBucket,
			OutputLenBucket: p.OutputLenBucket,
			Modality:        "text",
			TTFTP50ms:       p.TTFTP50ms,
			TTFTP95ms:       p.TTFTP95ms,
			TPOTP50ms:       p.TPOTP50ms,
			TPOTP95ms:       p.TPOTP95ms,
			ThroughputTPS:   p.ThroughputTPS,
			QPS:             p.QPS,
			VRAMUsageMiB:    p.VRAMUsageMiB,
			SampleCount:     p.SampleCount,
			Stability:       p.Stability,
			TestedAt:        time.Now(),
			AgentModel:      "claude-opus-4.6",
			Notes:           p.Notes,
		}
		if err := db.InsertBenchmarkResult(ctx, br); err != nil {
			return nil, fmt.Errorf("insert benchmark: %w", err)
		}
		postProcessBenchmarkSave(ctx, db, kStore, benchID, cfg.ID, p.Hardware, p.Engine, p.Model, p.ThroughputTPS)

		return json.Marshal(map[string]any{
			"benchmark_id": benchID,
			"config_id":    cfg.ID,
			"status":       "recorded",
			"hardware":     p.Hardware,
			"engine":       p.Engine,
			"model":        p.Model,
		})
	}

	deps.PromoteConfig = func(ctx context.Context, configID, status string) (json.RawMessage, error) {
		validStatuses := map[string]bool{"golden": true, "experiment": true, "archived": true}
		if !validStatuses[status] {
			return nil, fmt.Errorf("invalid status %q: must be golden, experiment, or archived", status)
		}
		// Fetch current config to return old status
		cfg, err := db.GetConfiguration(ctx, configID)
		if err != nil {
			return nil, fmt.Errorf("get configuration: %w", err)
		}
		oldStatus := cfg.Status
		if err := db.UpdateConfigStatus(ctx, configID, status); err != nil {
			return nil, fmt.Errorf("promote config: %w", err)
		}
		return json.Marshal(map[string]any{
			"config_id":  configID,
			"old_status": oldStatus,
			"new_status": status,
			"message":    fmt.Sprintf("Configuration %s promoted from %s to %s", configID, oldStatus, status),
		})
	}

	deps.RunBenchmark = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Model          string  `json:"model"`
			Endpoint       string  `json:"endpoint"`
			Concurrency    int     `json:"concurrency"`
			NumRequests    int     `json:"num_requests"`
			MaxTokens      int     `json:"max_tokens"`
			InputTokens    int     `json:"input_tokens"`
			Warmup         *int    `json:"warmup"`
			Rounds         int     `json:"rounds"`
			MinOutputRatio float64 `json:"min_output_ratio"`
			MaxRetries     int     `json:"max_retries"`
			Save           *bool   `json:"save"`
			Hardware       string  `json:"hardware"`
			Engine         string  `json:"engine"`
			Notes          string  `json:"notes"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse benchmark params: %w", err)
		}

		endpoint := resolveEndpoint(p.Endpoint, p.Model)

		warmup := 2
		if p.Warmup != nil {
			warmup = *p.Warmup
		}

		cfg := benchpkg.RunConfig{
			Endpoint:       endpoint,
			Model:          p.Model,
			Concurrency:    p.Concurrency,
			NumRequests:    p.NumRequests,
			MaxTokens:      p.MaxTokens,
			InputTokens:    p.InputTokens,
			WarmupCount:    warmup,
			Rounds:         p.Rounds,
			MinOutputRatio: p.MinOutputRatio,
			MaxRetries:     p.MaxRetries,
		}

		result, err := benchpkg.Run(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("benchmark run: %w", err)
		}

		// Save to DB unless explicitly disabled
		save := p.Save == nil || *p.Save
		var benchmarkID, configID string
		if save && p.Hardware != "" && p.Engine != "" {
			var err error
			benchmarkID, configID, err = saveBenchmarkResult(ctx, db,
				p.Hardware, p.Engine, p.Model, result,
				cfg.Concurrency, cfg.InputTokens, cfg.MaxTokens, p.Notes)
			if err != nil {
				return nil, err
			}
			postProcessBenchmarkSave(ctx, db, kStore, benchmarkID, configID, p.Hardware, p.Engine, p.Model, result.ThroughputTPS)
		}

		resp := map[string]any{
			"result": result,
			"saved":  save && benchmarkID != "",
		}
		if benchmarkID != "" {
			resp["benchmark_id"] = benchmarkID
			resp["config_id"] = configID

			// L2c auto-promote: if new benchmark beats current golden by >5%
			if promoted, oldID := maybeAutoPromote(ctx, db, configID, result.ThroughputTPS, p.Hardware, p.Engine, p.Model); promoted {
				resp["auto_promoted"] = true
				if oldID != "" {
					resp["old_golden_id"] = oldID
				}
			}

			// K5: Update runtime overlay with actual performance data
			if p.Model != "" {
				go updatePerfOverlay(ac.dataDir, p.Model, p.Hardware, p.Engine, result)
			}
		}
		return json.Marshal(resp)
	}

	deps.RunBenchmarkMatrix = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Model             string  `json:"model"`
			Endpoint          string  `json:"endpoint"`
			ConcurrencyLevels []int   `json:"concurrency_levels"`
			InputTokenLevels  []int   `json:"input_token_levels"`
			MaxTokenLevels    []int   `json:"max_token_levels"`
			RequestsPerCombo  int     `json:"requests_per_combo"`
			Rounds            int     `json:"rounds"`
			MinOutputRatio    float64 `json:"min_output_ratio"`
			MaxRetries        int     `json:"max_retries"`
			Save              *bool   `json:"save"`
			Hardware          string  `json:"hardware"`
			Engine            string  `json:"engine"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse matrix params: %w", err)
		}
		if len(p.ConcurrencyLevels) == 0 {
			p.ConcurrencyLevels = []int{1, 4}
		}
		if len(p.InputTokenLevels) == 0 {
			p.InputTokenLevels = []int{128, 1024}
		}
		if len(p.MaxTokenLevels) == 0 {
			p.MaxTokenLevels = []int{128, 512}
		}
		if p.RequestsPerCombo <= 0 {
			p.RequestsPerCombo = 5
		}

		endpoint := resolveEndpoint(p.Endpoint, p.Model)

		type matrixCell struct {
			Concurrency int                 `json:"concurrency"`
			InputTokens int                 `json:"input_tokens"`
			MaxTokens   int                 `json:"max_tokens"`
			Result      *benchpkg.RunResult `json:"result"`
			Error       string              `json:"error,omitempty"`
		}

		var cells []matrixCell
		refreshVectors := false
		for _, conc := range p.ConcurrencyLevels {
			for _, inTok := range p.InputTokenLevels {
				for _, maxTok := range p.MaxTokenLevels {
					cfg := benchpkg.RunConfig{
						Endpoint:       endpoint,
						Model:          p.Model,
						Concurrency:    conc,
						NumRequests:    p.RequestsPerCombo,
						MaxTokens:      maxTok,
						InputTokens:    inTok,
						WarmupCount:    1,
						Rounds:         p.Rounds,
						MinOutputRatio: p.MinOutputRatio,
						MaxRetries:     p.MaxRetries,
					}
					result, err := benchpkg.Run(ctx, cfg)
					cell := matrixCell{
						Concurrency: conc,
						InputTokens: inTok,
						MaxTokens:   maxTok,
					}
					if err != nil {
						cell.Error = err.Error()
					} else {
						cell.Result = result
						// Save each cell if requested
						save := p.Save == nil || *p.Save
						if save && p.Hardware != "" && p.Engine != "" {
							notes := fmt.Sprintf("matrix: conc=%d in=%d out=%d", conc, inTok, maxTok)
							benchmarkID, configID, saveErr := saveBenchmarkResult(ctx, db, p.Hardware, p.Engine, p.Model, result, conc, inTok, maxTok, notes)
							if saveErr != nil {
								slog.Warn("benchmark matrix: save failed", "error", saveErr, "concurrency", conc, "input_tokens", inTok, "max_tokens", maxTok)
							} else {
								refreshVectors = true
								if err := writeBenchmarkValidation(ctx, db, benchmarkID, configID, p.Hardware, p.Engine, p.Model, result.ThroughputTPS); err != nil {
									slog.Warn("benchmark validation: write failed", "error", err, "benchmark_id", benchmarkID)
								}
							}
						}
					}
					cells = append(cells, cell)
				}
			}
		}
		if refreshVectors {
			refreshPerfVectors(ctx, kStore)
		}

		return json.Marshal(map[string]any{
			"model": p.Model,
			"cells": cells,
			"total": len(cells),
		})
	}

	deps.ListBenchmarks = func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			ConfigID string `json:"config_id"`
			Hardware string `json:"hardware"`
			Model    string `json:"model"`
			Engine   string `json:"engine"`
			Limit    int    `json:"limit"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse list params: %w", err)
		}
		if p.Limit <= 0 {
			p.Limit = 20
		}

		var configIDs []string
		if p.ConfigID != "" {
			configIDs = []string{p.ConfigID}
		} else if p.Hardware != "" || p.Model != "" || p.Engine != "" {
			configs, err := db.ListConfigurations(ctx, p.Hardware, p.Model, p.Engine)
			if err != nil {
				return nil, fmt.Errorf("list configurations: %w", err)
			}
			for _, c := range configs {
				configIDs = append(configIDs, c.ID)
			}
			if len(configIDs) == 0 {
				return json.Marshal(map[string]any{
					"results": []any{},
					"total":   0,
				})
			}
		}

		results, err := db.ListBenchmarkResults(ctx, configIDs, p.Limit)
		if err != nil {
			return nil, fmt.Errorf("list benchmarks: %w", err)
		}

		return json.Marshal(map[string]any{
			"results": results,
			"total":   len(results),
		})
	}
}

// suppress "imported and not used" for packages only used in type literals
var _ = strings.ToLower
