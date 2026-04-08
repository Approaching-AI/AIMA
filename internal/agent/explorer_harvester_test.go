package agent

import (
	"context"
	"strings"
	"testing"
)

func TestHarvester_TemplateNote(t *testing.T) {
	h := &Harvester{tier: 1}
	note, _ := h.generateNote(context.Background(), HarvestInput{
		Task: PlanTask{Model: "qwen3-8b", Engine: "vllm"},
		Result: HarvestResult{
			Success:         true,
			BenchmarkID:     "bench-001",
			ConfigID:        "cfg-001",
			ExecutionPath:   "gpu+cpu",
			Throughput:      45.2,
			QPS:             0.18,
			TTFTP95:         280.0,
			TPOTP95:         21.7,
			Concurrency:     2,
			InputTokens:     512,
			MaxTokens:       256,
			AvgInputTokens:  530,
			AvgOutputTokens: 240,
			VRAMMiB:         32768,
			RAMMiB:          8192,
			CPUUsagePct:     64.5,
			GPUUtilPct:      88.0,
			Config:          map[string]any{"gpu_memory_utilization": 0.85},
		},
	})
	if note == "" {
		t.Fatal("expected non-empty note")
	}
	// Template note should contain model and throughput
	if !strings.Contains(note, "qwen3-8b") {
		t.Error("note missing model name")
	}
	if !strings.Contains(note, "45.2") {
		t.Error("note missing throughput")
	}
	if !strings.Contains(note, "conc=2") {
		t.Error("note missing concurrency")
	}
	if !strings.Contains(note, "TPOT P95") {
		t.Error("note missing TPOT")
	}
	if !strings.Contains(note, "CPU 64.5%") {
		t.Error("note missing CPU usage")
	}
	if !strings.Contains(note, "benchmark_id=bench-001") || !strings.Contains(note, "config_id=cfg-001") {
		t.Error("note missing artifact ids")
	}
	if !strings.Contains(note, "path gpu+cpu") {
		t.Error("note missing execution path")
	}
}

func TestHarvester_ShouldPromote(t *testing.T) {
	h := &Harvester{tier: 1}
	tests := []struct {
		name        string
		result      HarvestResult
		wantPromote bool
	}{
		{"success", HarvestResult{Success: true, Promoted: true}, true},
		{"failed", HarvestResult{Success: false}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actions := h.Harvest(context.Background(), HarvestInput{
				Task:   PlanTask{Model: "qwen3-8b", Engine: "vllm"},
				Result: tt.result,
			})
			hasPromote := false
			for _, a := range actions {
				if a.Type == "promote" {
					hasPromote = true
				}
			}
			if hasPromote != tt.wantPromote {
				t.Errorf("promote = %v, want %v", hasPromote, tt.wantPromote)
			}
		})
	}
}

func TestHarvester_SaveNoteIncludesHardware(t *testing.T) {
	var gotHardware string
	h := NewHarvester(1, WithSaveNote(func(ctx context.Context, title, content, hardware, model, engine string) error {
		gotHardware = hardware
		return nil
	}))

	h.Harvest(context.Background(), HarvestInput{
		Task: PlanTask{
			Hardware: "nvidia-gb10-arm64",
			Model:    "qwen3-8b",
			Engine:   "vllm",
		},
		Result: HarvestResult{
			Success:     true,
			Throughput:  42,
			TTFTP95:     200,
			CPUUsagePct: 55,
			RAMMiB:      4096,
			Config:      map[string]any{"kt_cpuinfer": 40},
		},
	})

	if gotHardware != "nvidia-gb10-arm64" {
		t.Fatalf("hardware = %q, want nvidia-gb10-arm64", gotHardware)
	}
}
