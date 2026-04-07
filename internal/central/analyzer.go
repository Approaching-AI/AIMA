package central

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const promptScenarioHealth = `You are an AI inference deployment health analyzer.
Given centrally managed scenarios and the benchmark evidence available for them,
identify scenarios that should be optimized or revalidated.

Respond with a JSON array:
[
  {
    "scenario": "scenario name",
    "hardware": "hardware profile",
    "priority": "high|medium|low",
    "reasoning": "why this scenario needs attention",
    "suggested_action": "what should be changed or validated next"
  }
]`

type AnalyzerConfig struct {
	InitialDelay           time.Duration
	GapScanInterval        time.Duration
	PatternInterval        time.Duration
	ScenarioHealthInterval time.Duration
	PostIngestDelay        time.Duration
	AdvisoryTTL            time.Duration
}

type AnalyzerOption func(*AnalyzerConfig)

func defaultAnalyzerConfig() AnalyzerConfig {
	return AnalyzerConfig{
		InitialDelay:           30 * time.Second,
		GapScanInterval:        24 * time.Hour,
		PatternInterval:        7 * 24 * time.Hour,
		ScenarioHealthInterval: 7 * 24 * time.Hour,
		PostIngestDelay:        5 * time.Minute,
		AdvisoryTTL:            30 * 24 * time.Hour,
	}
}

func WithAnalyzerConfig(cfg AnalyzerConfig) AnalyzerOption {
	return func(dst *AnalyzerConfig) {
		*dst = cfg
	}
}

// Analyzer runs background analysis on the knowledge store.
type Analyzer struct {
	store CentralStore
	llm   LLMCompleter
	cfg   AnalyzerConfig

	mu          sync.Mutex
	running     bool
	cancel      context.CancelFunc
	triggerCh   chan struct{}
	ingestTimer *time.Timer
}

// NewAnalyzer creates an Analyzer backed by the given store and LLM.
func NewAnalyzer(store CentralStore, llm LLMCompleter, opts ...AnalyzerOption) *Analyzer {
	cfg := defaultAnalyzerConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return &Analyzer{
		store:     store,
		llm:       llm,
		cfg:       cfg,
		triggerCh: make(chan struct{}, 1),
	}
}

// Start begins periodic background analysis. Safe to call multiple times.
func (a *Analyzer) Start(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.running = true
	go a.loop(ctx)
}

// Stop halts background analysis.
func (a *Analyzer) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ingestTimer != nil {
		a.ingestTimer.Stop()
		a.ingestTimer = nil
	}
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	a.running = false
}

func (a *Analyzer) loop(ctx context.Context) {
	var initial <-chan time.Time
	if a.cfg.InitialDelay <= 0 {
		a.enqueueRun()
	} else {
		timer := time.NewTimer(a.cfg.InitialDelay)
		defer timer.Stop()
		initial = timer.C
	}

	gapTicker := newTicker(a.cfg.GapScanInterval)
	patternTicker := newTicker(a.cfg.PatternInterval)
	scenarioTicker := newTicker(a.cfg.ScenarioHealthInterval)
	if gapTicker != nil {
		defer gapTicker.Stop()
	}
	if patternTicker != nil {
		defer patternTicker.Stop()
	}
	if scenarioTicker != nil {
		defer scenarioTicker.Stop()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-initial:
			initial = nil
			a.runFullCycle(ctx)
		case <-a.triggerCh:
			a.runFullCycle(ctx)
		case <-tickerChan(gapTicker):
			a.runGapScanCycle(ctx)
		case <-tickerChan(patternTicker):
			a.runPatternCycle(ctx)
		case <-tickerChan(scenarioTicker):
			a.runScenarioHealthCycle(ctx)
		}
	}
}

func (a *Analyzer) runFullCycle(ctx context.Context) {
	slog.Info("analyzer: starting full analysis cycle")
	a.expireStaleAdvisories(ctx)
	if _, err := a.RunGapScan(ctx); err != nil {
		slog.Warn("analyzer: gap scan failed", "error", err)
	}
	if _, err := a.RunPatternDiscovery(ctx); err != nil {
		slog.Warn("analyzer: pattern discovery failed", "error", err)
	}
	if _, err := a.RunScenarioHealth(ctx); err != nil {
		slog.Warn("analyzer: scenario health failed", "error", err)
	}
}

func (a *Analyzer) runGapScanCycle(ctx context.Context) {
	a.expireStaleAdvisories(ctx)
	if _, err := a.RunGapScan(ctx); err != nil {
		slog.Warn("analyzer: gap scan failed", "error", err)
	}
}

func (a *Analyzer) runPatternCycle(ctx context.Context) {
	a.expireStaleAdvisories(ctx)
	if _, err := a.RunPatternDiscovery(ctx); err != nil {
		slog.Warn("analyzer: pattern discovery failed", "error", err)
	}
}

func (a *Analyzer) runScenarioHealthCycle(ctx context.Context) {
	a.expireStaleAdvisories(ctx)
	if _, err := a.RunScenarioHealth(ctx); err != nil {
		slog.Warn("analyzer: scenario health failed", "error", err)
	}
}

func (a *Analyzer) expireStaleAdvisories(ctx context.Context) {
	if a.cfg.AdvisoryTTL <= 0 {
		return
	}
	expired, err := a.store.ExpireAdvisories(ctx, time.Now().UTC().Add(-a.cfg.AdvisoryTTL))
	if err != nil {
		slog.Warn("analyzer: expire advisories failed", "error", err)
		return
	}
	if expired > 0 {
		slog.Info("analyzer: expired stale advisories", "count", expired)
	}
}

// GapScanResult is the outcome of a gap scan analysis run.
type GapScanResult struct {
	AnalysisRun AnalysisRun `json:"analysis_run"`
	Advisories  []Advisory  `json:"advisories"`
}

// PatternResult is the outcome of a pattern discovery analysis run.
type PatternResult struct {
	AnalysisRun AnalysisRun `json:"analysis_run"`
	Advisories  []Advisory  `json:"advisories"`
}

// ScenarioHealthResult is the outcome of a scenario health analysis run.
type ScenarioHealthResult struct {
	AnalysisRun AnalysisRun `json:"analysis_run"`
	Advisories  []Advisory  `json:"advisories"`
}

// RunGapScan identifies gaps in the knowledge base and creates advisories.
func (a *Analyzer) RunGapScan(ctx context.Context) (*GapScanResult, error) {
	start := time.Now()
	coverage, _ := a.store.CoverageMatrix(ctx)
	stats, _ := a.store.Stats(ctx)
	input := map[string]any{
		"stats":    stats,
		"coverage": coverage,
	}

	run, err := a.startRun(ctx, AnalysisTypeGapScan, input)
	if err != nil {
		return nil, err
	}

	userPrompt := a.buildGapScanPrompt(coverage, stats)
	raw, err := a.llm.Complete(ctx, promptGapAnalysis, userPrompt)
	if err != nil {
		run.Status = AnalysisStatusFailed
		run.Error = err.Error()
		run.DurationMs = int(time.Since(start).Milliseconds())
		a.finishRun(ctx, &run, map[string]any{"error": err.Error()}, nil)
		return &GapScanResult{AnalysisRun: run}, fmt.Errorf("gap scan llm: %w", err)
	}

	var gaps []struct {
		Type            string `json:"type"`
		Hardware        string `json:"hardware"`
		Model           string `json:"model"`
		Engine          string `json:"engine"`
		Priority        string `json:"priority"`
		Reasoning       string `json:"reasoning"`
		SuggestedAction string `json:"suggested_action"`
	}
	if err := json.Unmarshal([]byte(stripCodeFence(raw)), &gaps); err != nil {
		run.Status = AnalysisStatusFailed
		run.Error = "parse gap response: " + err.Error()
		run.DurationMs = int(time.Since(start).Milliseconds())
		a.finishRun(ctx, &run, map[string]any{"raw": raw}, nil)
		return &GapScanResult{AnalysisRun: run}, fmt.Errorf("parse gap response: %w", err)
	}

	var advisories []Advisory
	var advisoryIDs []string
	for _, g := range gaps {
		severity := "info"
		if g.Priority == "high" {
			severity = "warning"
		}
		contentJSON, _ := json.Marshal(map[string]any{
			"gap_type":         g.Type,
			"suggested_action": g.SuggestedAction,
			"priority":         g.Priority,
		})
		basedOnJSON, _ := json.Marshal(map[string]any{
			"analysis_type": AnalysisTypeGapScan,
			"coverage_rows": len(coverage),
		})
		adv := Advisory{
			ID:             genID("adv"),
			Type:           AdvisoryTypeGapAlert,
			Status:         AdvisoryStatusPending,
			Severity:       severity,
			TargetHardware: g.Hardware,
			TargetModel:    g.Model,
			TargetEngine:   g.Engine,
			ContentJSON:    contentJSON,
			Reasoning:      g.Reasoning,
			Confidence:     g.Priority,
			BasedOnJSON:    basedOnJSON,
			AnalysisID:     run.ID,
			Title:          fmt.Sprintf("Gap Alert: %s", g.Type),
			Summary:        g.Reasoning,
			Hardware:       g.Hardware,
			Model:          g.Model,
			Engine:         g.Engine,
			Details:        string(contentJSON),
			CreatedAt:      nowRFC3339(),
		}
		if err := a.store.InsertAdvisory(ctx, adv); err != nil {
			slog.Warn("analyzer: insert advisory", "error", err)
			continue
		}
		advisories = append(advisories, normalizeAdvisory(adv))
		advisoryIDs = append(advisoryIDs, adv.ID)
	}

	run.Status = AnalysisStatusCompleted
	run.Summary = fmt.Sprintf("Found %d gaps", len(advisories))
	run.AdvisoryCount = len(advisories)
	run.DurationMs = int(time.Since(start).Milliseconds())
	a.finishRun(ctx, &run, map[string]any{"gaps": gaps}, advisoryIDs)

	return &GapScanResult{AnalysisRun: run, Advisories: advisories}, nil
}

// RunPatternDiscovery analyzes benchmark data for performance patterns.
func (a *Analyzer) RunPatternDiscovery(ctx context.Context) (*PatternResult, error) {
	start := time.Now()
	benchmarks, _ := a.store.QueryBenchmarks(ctx, BenchmarkFilter{Limit: 100})
	configs, _ := a.store.QueryConfigurations(ctx, ConfigFilter{Limit: 100})
	input := map[string]any{
		"benchmark_count":     len(benchmarks),
		"configuration_count": len(configs),
	}
	run, err := a.startRun(ctx, AnalysisTypePatternDiscovery, input)
	if err != nil {
		return nil, err
	}

	if len(benchmarks) == 0 {
		run.Status = AnalysisStatusCompleted
		run.Summary = "No benchmark data to analyze"
		run.DurationMs = int(time.Since(start).Milliseconds())
		a.finishRun(ctx, &run, map[string]any{"message": run.Summary}, nil)
		return &PatternResult{AnalysisRun: run}, nil
	}

	userPrompt := a.buildPatternPrompt(benchmarks, configs)
	raw, err := a.llm.Complete(ctx, promptOptimize, userPrompt)
	if err != nil {
		run.Status = AnalysisStatusFailed
		run.Error = err.Error()
		run.DurationMs = int(time.Since(start).Milliseconds())
		a.finishRun(ctx, &run, map[string]any{"error": err.Error()}, nil)
		return &PatternResult{AnalysisRun: run}, fmt.Errorf("pattern discovery llm: %w", err)
	}

	var resp OptimizeResponse
	if err := json.Unmarshal([]byte(stripCodeFence(raw)), &resp); err != nil {
		run.Status = AnalysisStatusFailed
		run.Error = "parse pattern response: " + err.Error()
		run.DurationMs = int(time.Since(start).Milliseconds())
		a.finishRun(ctx, &run, map[string]any{"raw": raw}, nil)
		return &PatternResult{AnalysisRun: run}, fmt.Errorf("parse pattern response: %w", err)
	}

	basedOnJSON, _ := json.Marshal(map[string]any{
		"benchmark_count":     len(benchmarks),
		"configuration_count": len(configs),
	})
	adv := Advisory{
		ID:          genID("adv"),
		Type:        AdvisoryTypeConfigRecommend,
		Status:      AdvisoryStatusPending,
		Severity:    "info",
		ContentJSON: json.RawMessage(raw),
		Reasoning:   resp.Reasoning,
		Confidence:  resp.Confidence,
		BasedOnJSON: basedOnJSON,
		AnalysisID:  run.ID,
		Title:       "Pattern Discovery Results",
		Summary:     resp.Reasoning,
		Details:     raw,
		CreatedAt:   nowRFC3339(),
	}

	var advisories []Advisory
	var advisoryIDs []string
	if err := a.store.InsertAdvisory(ctx, adv); err == nil {
		advisories = append(advisories, normalizeAdvisory(adv))
		advisoryIDs = append(advisoryIDs, adv.ID)
	}

	run.Status = AnalysisStatusCompleted
	run.Summary = fmt.Sprintf("Discovered patterns, %d advisory", len(advisories))
	run.AdvisoryCount = len(advisories)
	run.DurationMs = int(time.Since(start).Milliseconds())
	a.finishRun(ctx, &run, resp, advisoryIDs)

	return &PatternResult{AnalysisRun: run, Advisories: advisories}, nil
}

// RunScenarioHealth analyzes centrally managed scenarios and creates optimization advisories.
func (a *Analyzer) RunScenarioHealth(ctx context.Context) (*ScenarioHealthResult, error) {
	start := time.Now()
	scenarios, _ := a.store.ListScenarios(ctx, ScenarioFilter{Limit: 100})
	benchmarks, _ := a.store.QueryBenchmarks(ctx, BenchmarkFilter{Limit: 100})
	input := map[string]any{
		"scenario_count":  len(scenarios),
		"benchmark_count": len(benchmarks),
	}
	run, err := a.startRun(ctx, AnalysisTypeScenarioHealth, input)
	if err != nil {
		return nil, err
	}

	if len(scenarios) == 0 {
		run.Status = AnalysisStatusCompleted
		run.Summary = "No scenarios to analyze"
		run.DurationMs = int(time.Since(start).Milliseconds())
		a.finishRun(ctx, &run, map[string]any{"message": run.Summary}, nil)
		return &ScenarioHealthResult{AnalysisRun: run}, nil
	}

	userPrompt := a.buildScenarioHealthPrompt(scenarios, benchmarks)
	raw, err := a.llm.Complete(ctx, promptScenarioHealth, userPrompt)
	if err != nil {
		run.Status = AnalysisStatusFailed
		run.Error = err.Error()
		run.DurationMs = int(time.Since(start).Milliseconds())
		a.finishRun(ctx, &run, map[string]any{"error": err.Error()}, nil)
		return &ScenarioHealthResult{AnalysisRun: run}, fmt.Errorf("scenario health llm: %w", err)
	}

	var items []struct {
		Scenario        string `json:"scenario"`
		Hardware        string `json:"hardware"`
		Priority        string `json:"priority"`
		Reasoning       string `json:"reasoning"`
		SuggestedAction string `json:"suggested_action"`
	}
	if err := json.Unmarshal([]byte(stripCodeFence(raw)), &items); err != nil {
		run.Status = AnalysisStatusFailed
		run.Error = "parse scenario health response: " + err.Error()
		run.DurationMs = int(time.Since(start).Milliseconds())
		a.finishRun(ctx, &run, map[string]any{"raw": raw}, nil)
		return &ScenarioHealthResult{AnalysisRun: run}, fmt.Errorf("parse scenario health response: %w", err)
	}

	var advisories []Advisory
	var advisoryIDs []string
	for _, item := range items {
		severity := "info"
		if item.Priority == "high" {
			severity = "warning"
		}
		contentJSON, _ := json.Marshal(map[string]any{
			"scenario":         item.Scenario,
			"suggested_action": item.SuggestedAction,
			"priority":         item.Priority,
		})
		basedOnJSON, _ := json.Marshal(map[string]any{
			"analysis_type":  AnalysisTypeScenarioHealth,
			"scenario_count": len(scenarios),
		})
		adv := Advisory{
			ID:             genID("adv"),
			Type:           AdvisoryTypeScenarioOptimization,
			Status:         AdvisoryStatusPending,
			Severity:       severity,
			TargetHardware: item.Hardware,
			ContentJSON:    contentJSON,
			Reasoning:      item.Reasoning,
			Confidence:     item.Priority,
			BasedOnJSON:    basedOnJSON,
			AnalysisID:     run.ID,
			Title:          fmt.Sprintf("Scenario Health: %s", item.Scenario),
			Summary:        item.Reasoning,
			Hardware:       item.Hardware,
			Details:        string(contentJSON),
			CreatedAt:      nowRFC3339(),
		}
		if err := a.store.InsertAdvisory(ctx, adv); err != nil {
			slog.Warn("analyzer: insert scenario advisory", "error", err)
			continue
		}
		advisories = append(advisories, normalizeAdvisory(adv))
		advisoryIDs = append(advisoryIDs, adv.ID)
	}

	run.Status = AnalysisStatusCompleted
	run.Summary = fmt.Sprintf("Checked %d scenarios, produced %d advisories", len(scenarios), len(advisories))
	run.AdvisoryCount = len(advisories)
	run.DurationMs = int(time.Since(start).Milliseconds())
	a.finishRun(ctx, &run, map[string]any{"items": items}, advisoryIDs)

	return &ScenarioHealthResult{AnalysisRun: run, Advisories: advisories}, nil
}

// OnIngest is called after data ingestion to trigger incremental analysis.
func (a *Analyzer) OnIngest(_ context.Context, payload IngestPayload) {
	if len(payload.Configurations) == 0 && len(payload.Benchmarks) == 0 && len(payload.KnowledgeNotes) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running {
		return
	}
	if a.ingestTimer != nil {
		a.ingestTimer.Stop()
	}
	delay := a.cfg.PostIngestDelay
	if delay < 0 {
		delay = 0
	}
	a.ingestTimer = time.AfterFunc(delay, func() {
		a.enqueueRun()
	})
	slog.Info("analyzer: new data ingested, scheduled delayed analysis",
		"configs", len(payload.Configurations),
		"benchmarks", len(payload.Benchmarks),
		"notes", len(payload.KnowledgeNotes),
		"delay", delay.String())
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

func (a *Analyzer) buildScenarioHealthPrompt(scenarios []Scenario, benchmarks []BenchmarkResult) string {
	prompt := fmt.Sprintf("Analyzing %d centrally managed scenarios with %d benchmark results.\n\n", len(scenarios), len(benchmarks))
	prompt += "Scenarios:\n"
	for i, sc := range scenarios {
		if i >= 20 {
			prompt += fmt.Sprintf("... and %d more scenarios\n", len(scenarios)-20)
			break
		}
		prompt += fmt.Sprintf("- %s on %s (source=%s, version=%d)\n", sc.Name, sc.HardwareProfile, sc.Source, sc.Version)
	}
	prompt += "\nRecent benchmark highlights:\n"
	for i, b := range benchmarks {
		if i >= 20 {
			prompt += fmt.Sprintf("... and %d more benchmark rows\n", len(benchmarks)-20)
			break
		}
		prompt += fmt.Sprintf("- Config %s: Throughput %.1f tps, TTFT %.1f ms, GPU %.0f%%\n",
			b.ConfigID, b.ThroughputTPS, b.TTFTP50ms, b.GPUUtilPct)
	}
	prompt += "\nIdentify scenarios that need optimization or revalidation."
	return prompt
}

func (a *Analyzer) startRun(ctx context.Context, runType string, input any) (AnalysisRun, error) {
	var inputJSON json.RawMessage
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return AnalysisRun{}, fmt.Errorf("marshal analysis input: %w", err)
		}
		inputJSON = encoded
	}
	run := normalizeAnalysisRun(AnalysisRun{
		ID:        genID("run"),
		Type:      runType,
		Status:    AnalysisStatusRunning,
		InputJSON: inputJSON,
		StartedAt: nowRFC3339(),
	})
	if err := a.store.InsertAnalysisRun(ctx, run); err != nil {
		return AnalysisRun{}, fmt.Errorf("insert analysis run: %w", err)
	}
	return run, nil
}

func (a *Analyzer) finishRun(ctx context.Context, run *AnalysisRun, output any, advisoryIDs []string) {
	if run == nil {
		return
	}
	var outputJSON json.RawMessage
	if output != nil {
		if encoded, err := json.Marshal(output); err == nil {
			outputJSON = encoded
		}
	}
	var advisoriesJSON json.RawMessage
	if advisoryIDs != nil {
		if encoded, err := json.Marshal(advisoryIDs); err == nil {
			advisoriesJSON = encoded
		}
	}
	run.CompletedAt = nowRFC3339()
	run.UpdatedAt = run.CompletedAt
	run.OutputJSON = outputJSON
	run.Advisories = advisoriesJSON
	if err := a.store.UpdateAnalysisRun(ctx, run.ID, AnalysisRunUpdate{
		Status:        run.Status,
		Summary:       run.Summary,
		OutputJSON:    outputJSON,
		Advisories:    advisoriesJSON,
		AdvisoryCount: run.AdvisoryCount,
		DurationMs:    run.DurationMs,
		Error:         run.Error,
		CompletedAt:   run.CompletedAt,
	}); err != nil {
		slog.Warn("analyzer: update analysis run failed", "id", run.ID, "error", err)
	}
}

func (a *Analyzer) enqueueRun() {
	select {
	case a.triggerCh <- struct{}{}:
	default:
	}
}

func newTicker(interval time.Duration) *time.Ticker {
	if interval <= 0 {
		return nil
	}
	return time.NewTicker(interval)
}

func tickerChan(t *time.Ticker) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}
