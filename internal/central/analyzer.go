package central

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Analyzer runs background analysis on the knowledge store.
type Analyzer struct {
	store CentralStore
	llm   LLMCompleter

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
}

// NewAnalyzer creates an Analyzer backed by the given store and LLM.
func NewAnalyzer(store CentralStore, llm LLMCompleter) *Analyzer {
	return &Analyzer{store: store, llm: llm}
}

// Start begins periodic background analysis. Safe to call multiple times.
func (a *Analyzer) Start(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return
	}
	a.running = true
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	go a.loop(ctx)
}

// Stop halts background analysis.
func (a *Analyzer) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	a.running = false
}

func (a *Analyzer) loop(ctx context.Context) {
	// Run initial analysis after a short delay
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			a.runCycle(ctx)
			timer.Reset(1 * time.Hour)
		}
	}
}

func (a *Analyzer) runCycle(ctx context.Context) {
	slog.Info("analyzer: starting analysis cycle")
	if _, err := a.RunGapScan(ctx); err != nil {
		slog.Warn("analyzer: gap scan failed", "error", err)
	}
	if _, err := a.RunPatternDiscovery(ctx); err != nil {
		slog.Warn("analyzer: pattern discovery failed", "error", err)
	}
}

// GapScanResult is the outcome of a gap scan analysis run.
type GapScanResult struct {
	AnalysisRun AnalysisRun `json:"analysis_run"`
	Advisories  []Advisory  `json:"advisories"`
}

// RunGapScan identifies gaps in the knowledge base and creates advisories.
func (a *Analyzer) RunGapScan(ctx context.Context) (*GapScanResult, error) {
	start := time.Now()
	runID := genID("run")

	run := AnalysisRun{
		ID:        runID,
		Type:      "gap_scan",
		Status:    "running",
		CreatedAt: start.UTC().Format(time.RFC3339),
	}
	_ = a.store.InsertAnalysisRun(ctx, run)

	// Build context from store
	coverage, _ := a.store.CoverageMatrix(ctx)
	stats, _ := a.store.Stats(ctx)

	userPrompt := a.buildGapScanPrompt(coverage, stats)

	raw, err := a.llm.Complete(ctx, promptGapAnalysis, userPrompt)
	if err != nil {
		// Graceful fallback: record failure, return what we have
		run.Status = "failed"
		run.Error = err.Error()
		run.DurationMs = int(time.Since(start).Milliseconds())
		_ = a.store.InsertAnalysisRun(ctx, AnalysisRun{
			ID:         genID("run"),
			Type:       "gap_scan",
			Status:     "failed",
			Error:      err.Error(),
			DurationMs: run.DurationMs,
			CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		})
		return &GapScanResult{AnalysisRun: run}, fmt.Errorf("gap scan llm: %w", err)
	}

	// Parse LLM response as array of gap items
	var gaps []struct {
		Type            string `json:"type"`
		Hardware        string `json:"hardware"`
		Model           string `json:"model"`
		Engine          string `json:"engine"`
		Priority        string `json:"priority"`
		Reasoning       string `json:"reasoning"`
		SuggestedAction string `json:"suggested_action"`
	}
	if err := json.Unmarshal([]byte(raw), &gaps); err != nil {
		run.Status = "failed"
		run.Error = "parse gap response: " + err.Error()
		run.DurationMs = int(time.Since(start).Milliseconds())
		return &GapScanResult{AnalysisRun: run}, fmt.Errorf("parse gap response: %w", err)
	}

	var advisories []Advisory
	for _, g := range gaps {
		severity := "info"
		if g.Priority == "high" {
			severity = "warning"
		}
		adv := Advisory{
			ID:         genID("adv"),
			Type:       "gap",
			Severity:   severity,
			Hardware:   g.Hardware,
			Model:      g.Model,
			Engine:     g.Engine,
			Title:      fmt.Sprintf("Gap: %s", g.Type),
			Summary:    g.Reasoning,
			Details:    g.SuggestedAction,
			Confidence: g.Priority,
			AnalysisID: runID,
			CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		}
		if err := a.store.InsertAdvisory(ctx, adv); err != nil {
			slog.Warn("analyzer: insert advisory", "error", err)
			continue
		}
		advisories = append(advisories, adv)
	}

	run.Status = "completed"
	run.Summary = fmt.Sprintf("Found %d gaps", len(advisories))
	run.AdvisoryCount = len(advisories)
	run.DurationMs = int(time.Since(start).Milliseconds())

	// Update the run status by inserting a completion record
	_ = a.store.InsertAnalysisRun(ctx, AnalysisRun{
		ID:            genID("run"),
		Type:          "gap_scan",
		Status:        "completed",
		Summary:       run.Summary,
		AdvisoryCount: run.AdvisoryCount,
		DurationMs:    run.DurationMs,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	})

	return &GapScanResult{AnalysisRun: run, Advisories: advisories}, nil
}

// PatternResult is the outcome of a pattern discovery analysis run.
type PatternResult struct {
	AnalysisRun AnalysisRun `json:"analysis_run"`
	Advisories  []Advisory  `json:"advisories"`
}

// RunPatternDiscovery analyzes benchmark data for performance patterns.
func (a *Analyzer) RunPatternDiscovery(ctx context.Context) (*PatternResult, error) {
	start := time.Now()
	runID := genID("run")

	run := AnalysisRun{
		ID:        runID,
		Type:      "pattern_discovery",
		Status:    "running",
		CreatedAt: start.UTC().Format(time.RFC3339),
	}
	_ = a.store.InsertAnalysisRun(ctx, run)

	// Gather benchmark data
	benchmarks, _ := a.store.QueryBenchmarks(ctx, BenchmarkFilter{Limit: 100})
	configs, _ := a.store.QueryConfigurations(ctx, ConfigFilter{Limit: 100})

	if len(benchmarks) == 0 {
		run.Status = "completed"
		run.Summary = "No benchmark data to analyze"
		run.DurationMs = int(time.Since(start).Milliseconds())
		return &PatternResult{AnalysisRun: run}, nil
	}

	userPrompt := a.buildPatternPrompt(benchmarks, configs)

	raw, err := a.llm.Complete(ctx, promptOptimize, userPrompt)
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		run.DurationMs = int(time.Since(start).Milliseconds())
		return &PatternResult{AnalysisRun: run}, fmt.Errorf("pattern discovery llm: %w", err)
	}

	// Parse as optimization response
	var resp OptimizeResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		run.Status = "failed"
		run.Error = "parse pattern response: " + err.Error()
		run.DurationMs = int(time.Since(start).Milliseconds())
		return &PatternResult{AnalysisRun: run}, fmt.Errorf("parse pattern response: %w", err)
	}

	var advisories []Advisory
	adv := Advisory{
		ID:         genID("adv"),
		Type:       "pattern",
		Severity:   "info",
		Title:      "Pattern Discovery Results",
		Summary:    resp.Reasoning,
		Details:    raw,
		Confidence: resp.Confidence,
		AnalysisID: runID,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	if err := a.store.InsertAdvisory(ctx, adv); err == nil {
		advisories = append(advisories, adv)
	}

	run.Status = "completed"
	run.Summary = fmt.Sprintf("Discovered patterns, %d advisory", len(advisories))
	run.AdvisoryCount = len(advisories)
	run.DurationMs = int(time.Since(start).Milliseconds())

	_ = a.store.InsertAnalysisRun(ctx, AnalysisRun{
		ID:            genID("run"),
		Type:          "pattern_discovery",
		Status:        "completed",
		Summary:       run.Summary,
		AdvisoryCount: run.AdvisoryCount,
		DurationMs:    run.DurationMs,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	})

	return &PatternResult{AnalysisRun: run, Advisories: advisories}, nil
}

// OnIngest is called after data ingestion to trigger incremental analysis.
func (a *Analyzer) OnIngest(ctx context.Context, payload IngestPayload) {
	if len(payload.Configurations) == 0 && len(payload.Benchmarks) == 0 {
		return
	}
	slog.Info("analyzer: new data ingested, scheduling analysis",
		"configs", len(payload.Configurations),
		"benchmarks", len(payload.Benchmarks))
}

func (a *Analyzer) buildGapScanPrompt(coverage []CoverageEntry, stats StoreStats) string {
	prompt := fmt.Sprintf("Knowledge base stats:\n- Devices: %d\n- Configurations: %d\n- Benchmarks: %d\n\n",
		stats.Devices, stats.Configurations, stats.Benchmarks)
	if len(coverage) > 0 {
		prompt += "Coverage matrix (hardware x engine -> model count):\n"
		for _, c := range coverage {
			prompt += fmt.Sprintf("- %s / %s: %d models tested\n", c.Hardware, c.Engine, c.Models)
		}
	} else {
		prompt += "Coverage matrix: empty (no configurations yet)\n"
	}
	prompt += "\nIdentify gaps and suggest what should be tested next."
	return prompt
}

func (a *Analyzer) buildPatternPrompt(benchmarks []BenchmarkResult, configs []Configuration) string {
	prompt := fmt.Sprintf("Analyzing %d benchmark results across %d configurations.\n\n", len(benchmarks), len(configs))
	prompt += "Benchmark data:\n"
	for i, b := range benchmarks {
		if i >= 20 {
			prompt += fmt.Sprintf("... and %d more\n", len(benchmarks)-20)
			break
		}
		prompt += fmt.Sprintf("- Config: %s, Throughput: %.1f tps, TTFT_p50: %.1f ms, VRAM: %d MiB, GPU_Util: %.0f%%\n",
			b.ConfigID, b.ThroughputTPS, b.TTFTP50ms, b.VRAMUsageMiB, b.GPUUtilPct)
	}
	prompt += "\nIdentify performance patterns and suggest optimizations."
	return prompt
}
