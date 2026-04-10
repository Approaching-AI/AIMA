package central

import (
	"context"
	"testing"
	"time"
)

func TestAnalyzerGapScan(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Seed some data
	_ = store.UpsertDevice(ctx, Device{ID: "dev-1", GPUArch: "Ada"})
	_ = store.InsertConfiguration(ctx, Configuration{
		ID: "cfg-1", Hardware: "nvidia-rtx4090", EngineType: "vllm",
		Model: "qwen3-8b", Config: "{}", ConfigHash: "h1",
		Status: "golden", CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
	})

	llm := &mockLLM{response: `[
		{
			"type": "missing_benchmark",
			"hardware": "nvidia-rtx4090",
			"model": "llama3-8b",
			"engine": "vllm",
			"priority": "high",
			"reasoning": "No benchmarks for this popular model",
			"suggested_action": "Run benchmark suite"
		}
	]`}

	analyzer := NewAnalyzer(store, llm)
	result, err := analyzer.RunGapScan(ctx)
	if err != nil {
		t.Fatalf("RunGapScan: %v", err)
	}
	if result.AnalysisRun.Status != "completed" {
		t.Fatalf("status = %q, want completed", result.AnalysisRun.Status)
	}
	if len(result.Advisories) != 1 {
		t.Fatalf("advisories = %d, want 1", len(result.Advisories))
	}
	if result.Advisories[0].Type != AdvisoryTypeGapAlert {
		t.Fatalf("advisory type = %q, want %s", result.Advisories[0].Type, AdvisoryTypeGapAlert)
	}
	if result.Advisories[0].Severity != "warning" {
		t.Fatalf("severity = %q, want warning (high priority)", result.Advisories[0].Severity)
	}
}

func TestAnalyzerGapScanLLMFailure(t *testing.T) {
	store := newTestStore(t)
	llm := &mockLLM{err: context.DeadlineExceeded}

	analyzer := NewAnalyzer(store, llm)
	result, err := analyzer.RunGapScan(context.Background())
	if err == nil {
		t.Fatal("expected error from LLM failure")
	}
	if result == nil {
		t.Fatal("result should not be nil on LLM failure")
	}
	if result.AnalysisRun.Status != AnalysisStatusFailed {
		t.Fatalf("status = %q, want failed", result.AnalysisRun.Status)
	}
}

func TestAnalyzerPatternDiscoveryNoBenchmarks(t *testing.T) {
	store := newTestStore(t)
	llm := &mockLLM{response: `{}`} // should not be called

	analyzer := NewAnalyzer(store, llm)
	result, err := analyzer.RunPatternDiscovery(context.Background())
	if err != nil {
		t.Fatalf("RunPatternDiscovery: %v", err)
	}
	if result.AnalysisRun.Status != AnalysisStatusCompleted {
		t.Fatalf("status = %q, want completed", result.AnalysisRun.Status)
	}
	if result.AnalysisRun.Summary != "No benchmark data to analyze" {
		t.Fatalf("summary = %q, want 'No benchmark data to analyze'", result.AnalysisRun.Summary)
	}
}

func TestAnalyzerPatternDiscoveryWithData(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_ = store.InsertConfiguration(ctx, Configuration{
		ID: "cfg-p1", Hardware: "nvidia-rtx4090", EngineType: "vllm",
		Model: "qwen3-8b", Config: "{}", ConfigHash: "hp1",
		Status: "golden", CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
	})
	_ = store.InsertBenchmark(ctx, BenchmarkResult{
		ID: "bench-p1", ConfigID: "cfg-p1", ThroughputTPS: 50.0,
		TTFTP50ms: 100.0, VRAMUsageMiB: 8192, TestedAt: "2026-01-01T00:00:00Z",
	})

	llm := &mockLLM{response: `{
		"optimizations": [{"type": "parameter", "change": "increase batch"}],
		"reasoning": "GPU underutilized",
		"confidence": "medium"
	}`}

	analyzer := NewAnalyzer(store, llm)
	result, err := analyzer.RunPatternDiscovery(ctx)
	if err != nil {
		t.Fatalf("RunPatternDiscovery: %v", err)
	}
	if result.AnalysisRun.Status != "completed" {
		t.Fatalf("status = %q, want completed", result.AnalysisRun.Status)
	}
	if len(result.Advisories) != 1 {
		t.Fatalf("advisories = %d, want 1", len(result.Advisories))
	}
	runs, err := store.ListAnalysisRuns(ctx, 10)
	if err != nil {
		t.Fatalf("ListAnalysisRuns: %v", err)
	}
	if len(runs) == 0 || runs[0].CompletedAt == "" {
		t.Fatalf("expected completed analysis run, got %+v", runs)
	}
}

func TestAnalyzerStartStop(t *testing.T) {
	store := newTestStore(t)
	llm := &mockLLM{response: `[]`}
	analyzer := NewAnalyzer(store, llm)

	ctx := context.Background()
	analyzer.Start(ctx)
	analyzer.Start(ctx) // idempotent
	analyzer.Stop()
	analyzer.Stop() // idempotent
}

func TestAnalyzerOnIngest(t *testing.T) {
	store := newTestStore(t)
	llm := &mockLLM{response: `[]`}
	analyzer := NewAnalyzer(store, llm, WithAnalyzerConfig(AnalyzerConfig{
		InitialDelay:           0,
		GapScanInterval:        0,
		PatternInterval:        0,
		ScenarioHealthInterval: 0,
		PostIngestDelay:        10 * time.Millisecond,
		AdvisoryTTL:            0,
	}))
	ctx := context.Background()
	analyzer.Start(ctx)
	defer analyzer.Stop()

	// Should not panic with empty payload
	analyzer.OnIngest(ctx, IngestPayload{})
	// Should schedule with data
	analyzer.OnIngest(ctx, IngestPayload{
		Configurations: []IngestConfig{{ID: "cfg-1"}},
	})
	time.Sleep(50 * time.Millisecond)
	runs, err := store.ListAnalysisRuns(ctx, 10)
	if err != nil {
		t.Fatalf("ListAnalysisRuns: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("expected delayed analysis run after ingest")
	}
}

func TestAnalyzerScenarioHealth(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_ = store.InsertScenario(ctx, Scenario{
		ID:              "scn-1",
		Name:            "dual-model",
		HardwareProfile: "nvidia-rtx4090",
		ScenarioYAML:    "deployments:\n  - model: qwen3-8b",
		Source:          "generated",
		Version:         1,
		CreatedAt:       "2026-01-01T00:00:00Z",
		UpdatedAt:       "2026-01-01T00:00:00Z",
	})

	llm := &mockLLM{response: `[
		{
			"scenario": "dual-model",
			"hardware": "nvidia-rtx4090",
			"priority": "medium",
			"reasoning": "This scenario should be revalidated with newer configs",
			"suggested_action": "Benchmark the scenario again"
		}
	]`}

	analyzer := NewAnalyzer(store, llm)
	result, err := analyzer.RunScenarioHealth(ctx)
	if err != nil {
		t.Fatalf("RunScenarioHealth: %v", err)
	}
	if result.AnalysisRun.Status != AnalysisStatusCompleted {
		t.Fatalf("status = %q, want completed", result.AnalysisRun.Status)
	}
	if len(result.Advisories) != 1 {
		t.Fatalf("advisories = %d, want 1", len(result.Advisories))
	}
	if result.Advisories[0].Type != AdvisoryTypeScenarioOptimization {
		t.Fatalf("type = %q, want %s", result.Advisories[0].Type, AdvisoryTypeScenarioOptimization)
	}
}
