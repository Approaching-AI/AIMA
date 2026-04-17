package main

import (
	"context"
	"encoding/json"
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

	deployConfig := map[string]any{"tp_size": 2, "gmu": 0.9}
	benchmarkID, configID, _, err := saveBenchmarkResult(ctx, db,
		"nvidia-rtx4090-x86", "sglang-kt", "qwen3-4b",
		"llm",
		&benchpkg.RunResult{
			ThroughputTPS:   42.5,
			TTFTP95ms:       123.4,
			TTFTP50ms:       90,
			AvgInputTokens:  2048,
			AvgOutputTokens: 256,
			TotalRequests:   8,
		},
		deployConfig, benchmarkSystemMetrics{}, 2, "explorer validate")
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
	// Post-S2: Config JSON reflects the deploy-level engine params that are
	// shared across every matrix cell, not the per-cell benchmark profile
	// (concurrency / input_tokens / max_tokens). Accept any key ordering.
	var got map[string]any
	if uerr := json.Unmarshal([]byte(cfg.Config), &got); uerr != nil {
		t.Fatalf("Config JSON not valid JSON: %v (%s)", uerr, cfg.Config)
	}
	if !reflect.DeepEqual(got, map[string]any{"tp_size": float64(2), "gmu": 0.9}) {
		t.Fatalf("Config = %v, want deploy-level params", got)
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

func TestSaveBenchmarkResultPersistsEmbeddingModality(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, configID, saved, err := saveBenchmarkResult(ctx, db,
		"nvidia-gb10-arm64", "vllm", "bge-m3",
		"embedding",
		&benchpkg.RunResult{
			ThroughputTPS:          18.5,
			AvgInputTokens:         256,
			AvgOutputTokens:        1024,
			AvgEmbeddingDimensions: 1024,
			TotalRequests:          6,
			DurationMs:             1000,
		},
		nil, benchmarkSystemMetrics{}, 1, "embedding validate")
	if err != nil {
		t.Fatalf("saveBenchmarkResult: %v", err)
	}
	if saved == nil || saved.Modality != "embedding" {
		t.Fatalf("saved modality = %#v, want embedding", saved)
	}

	results, err := db.ListBenchmarkResults(ctx, []string{configID}, 10)
	if err != nil {
		t.Fatalf("ListBenchmarkResults: %v", err)
	}
	if len(results) != 1 || results[0].Modality != "embedding" {
		t.Fatalf("results = %#v, want one embedding benchmark", results)
	}
}

func TestSaveBenchmarkResultPersistsSuccessfulZeroThroughputReranker(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, configID, saved, err := saveBenchmarkResult(ctx, db,
		"nvidia-gb10-arm64", "sglang", "bge-reranker-v2-m3",
		"reranker",
		&benchpkg.RunResult{
			SuccessfulReqs:     4,
			TotalRequests:      4,
			DurationMs:         1200,
			QPS:                3.3,
			ReranksPerSec:      3.3,
			AvgInputTokens:     128,
			AvgOutputTokens:    4,
			RerankLatencyP50ms: 120,
		},
		nil, benchmarkSystemMetrics{}, 1, "reranker validate")
	if err != nil {
		t.Fatalf("saveBenchmarkResult: %v", err)
	}
	if saved == nil || saved.Modality != "reranker" {
		t.Fatalf("saved = %#v, want reranker benchmark row", saved)
	}

	cfg, err := db.GetConfiguration(ctx, configID)
	if err != nil {
		t.Fatalf("GetConfiguration: %v", err)
	}
	if cfg.Config == "" {
		t.Fatalf("Config should not be empty")
	}
	if saved.ThroughputTPS != 0 {
		t.Fatalf("ThroughputTPS = %v, want 0 for zero-output reranker evidence", saved.ThroughputTPS)
	}
}

func TestStorageBenchmarkModality(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: "text"},
		{input: "llm", want: "text"},
		{input: "text", want: "text"},
		{input: "vlm", want: "vlm"},
		{input: "embedding", want: "embedding"},
		{input: "reranker", want: "reranker"},
	}
	for _, tt := range tests {
		if got := storageBenchmarkModality(tt.input); got != tt.want {
			t.Fatalf("storageBenchmarkModality(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestEffectiveMatrixMaxTokenLevels(t *testing.T) {
	if got := effectiveMatrixMaxTokenLevels("embedding", []int{128, 512}); !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("embedding levels = %v, want [0]", got)
	}
	if got := effectiveMatrixMaxTokenLevels("reranker", []int{128, 512}); !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("reranker levels = %v, want [0]", got)
	}
	if got := effectiveMatrixMaxTokenLevels("llm", []int{128, 512}); !reflect.DeepEqual(got, []int{128, 512}) {
		t.Fatalf("llm levels = %v, want unchanged", got)
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
