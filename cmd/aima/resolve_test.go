package main

import (
	"context"
	"testing"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/knowledge"
)

func TestResolveWithFallbackRefreshesSyntheticModel(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.InsertModel(ctx, &state.Model{
		ID:         "model-refresh",
		Name:       "refresh-model",
		Type:       "llm",
		Path:       "/models/refresh-model",
		Format:     "safetensors",
		SizeBytes:  32 * 1024 * 1024 * 1024,
		Status:     "registered",
		ModelClass: "dense",
	}); err != nil {
		t.Fatalf("InsertModel: %v", err)
	}

	cat := &knowledge.Catalog{
		EngineAssets: []knowledge.EngineAsset{
			{
				Metadata: knowledge.EngineMetadata{
					Name: "vllm-test", Type: "vllm", Version: "1.0",
					Default: true, SupportedFormats: []string{"safetensors"},
				},
				Hardware: knowledge.EngineHardware{GPUArch: "*"},
				Startup: knowledge.EngineStartup{
					Command:     []string{"serve"},
					DefaultArgs: map[string]any{"gpu_memory_utilization": 0.9},
				},
			},
		},
	}

	firstHW := knowledge.HardwareInfo{GPUArch: "Ada"}
	if _, canonical, err := resolveWithFallback(ctx, cat, db, firstHW, "refresh-model", "", nil, ""); err != nil {
		t.Fatalf("first resolveWithFallback: %v", err)
	} else if canonical != "refresh-model" {
		t.Fatalf("canonical name = %q, want refresh-model", canonical)
	}

	secondHW := knowledge.HardwareInfo{GPUArch: "Ada", GPUVRAMMiB: 24576, GPUCount: 2}
	resolved, _, err := resolveWithFallback(ctx, cat, db, secondHW, "refresh-model", "", nil, "")
	if err != nil {
		t.Fatalf("second resolveWithFallback: %v", err)
	}
	got, ok := resolved.Config["tensor_parallel_size"].(int)
	if !ok {
		t.Fatalf("tensor_parallel_size type = %T, want int", resolved.Config["tensor_parallel_size"])
	}
	if got != 2 {
		t.Fatalf("tensor_parallel_size = %d, want 2", got)
	}

	var refreshed *knowledge.ModelAsset
	for i := range cat.ModelAssets {
		if cat.ModelAssets[i].Metadata.Name == "refresh-model" {
			refreshed = &cat.ModelAssets[i]
			break
		}
	}
	if refreshed == nil {
		t.Fatal("refresh-model not found in catalog after refresh")
	}
	foundAdaTP := false
	for _, variant := range refreshed.Variants {
		if variant.Hardware.GPUArch == "Ada" && variant.Hardware.GPUCountMin == 2 {
			foundAdaTP = true
			break
		}
	}
	if !foundAdaTP {
		t.Fatalf("expected refreshed synthetic variant with Ada GPUCountMin=2, got %+v", refreshed.Variants)
	}
}
