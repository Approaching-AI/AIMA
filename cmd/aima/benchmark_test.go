package main

import (
	"context"
	"reflect"
	"testing"

	state "github.com/jguan/aima/internal"
	benchpkg "github.com/jguan/aima/internal/benchmark"
	"github.com/jguan/aima/internal/runtime"
)

func TestSaveBenchmarkResultPersistsDeployConfig(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	benchmarkID, configID, _, err := saveBenchmarkResult(ctx, db,
		"nvidia-rtx4090-x86", "sglang-kt", "qwen3-4b",
		&benchpkg.RunResult{
			ThroughputTPS:   42.5,
			TTFTP95ms:       123.4,
			TTFTP50ms:       90,
			AvgInputTokens:  2048,
			AvgOutputTokens: 256,
			TotalRequests:   8,
		},
		nil, benchmarkSystemMetrics{}, 2, 2048, 256, "explorer validate")
	if err != nil {
		t.Fatalf("saveBenchmarkResult: %v", err)
	}
	if benchmarkID == "" || configID == "" {
		t.Fatalf("ids = (%q, %q), want non-empty", benchmarkID, configID)
	}

	cfg, err := db.GetConfiguration(ctx, configID)
	if err != nil {
		t.Fatalf("GetConfiguration: %v", err)
	}
	if cfg.Config != `{"concurrency":2,"input_tokens":2048,"max_tokens":256}` &&
		cfg.Config != `{"concurrency":2,"max_tokens":256,"input_tokens":2048}` &&
		cfg.Config != `{"input_tokens":2048,"concurrency":2,"max_tokens":256}` &&
		cfg.Config != `{"input_tokens":2048,"max_tokens":256,"concurrency":2}` &&
		cfg.Config != `{"max_tokens":256,"concurrency":2,"input_tokens":2048}` &&
		cfg.Config != `{"max_tokens":256,"input_tokens":2048,"concurrency":2}` {
		t.Fatalf("Config JSON = %s, want benchmark config {concurrency,input_tokens,max_tokens}", cfg.Config)
	}

	results, err := db.ListBenchmarkResults(ctx, []string{configID}, 10)
	if err != nil {
		t.Fatalf("ListBenchmarkResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("benchmark results = %d, want 1", len(results))
	}
	if results[0].ThroughputTPS != 42.5 {
		t.Fatalf("ThroughputTPS = %v, want 42.5", results[0].ThroughputTPS)
	}
}

func TestSelectReadyDeployConfigPrefersExplicitAndMatchingReadyDeployment(t *testing.T) {
	explicit := map[string]any{"concurrency": 8, "max_tokens": 512}
	matches := []matchedDeployment{
		{
			Status: &runtime.DeploymentStatus{
				Ready: true,
				Config: map[string]any{
					"concurrency": 4,
					"max_tokens":  256,
				},
				Labels: map[string]string{"aima.dev/engine": "sglang"},
			},
		},
	}

	got := selectReadyDeployConfig("sglang", explicit, matches)
	if !reflect.DeepEqual(got, explicit) {
		t.Fatalf("explicit deploy config = %#v, want %#v", got, explicit)
	}

	got = selectReadyDeployConfig("sglang", nil, matches)
	want := map[string]any{"concurrency": 4, "max_tokens": 256}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ready deploy config = %#v, want %#v", got, want)
	}

	got = selectReadyDeployConfig("llama.cpp", nil, matches)
	if got != nil {
		t.Fatalf("mismatched engine config = %#v, want nil", got)
	}
}
