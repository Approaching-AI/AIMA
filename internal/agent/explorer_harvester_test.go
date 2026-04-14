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
			EngineVersion:   "1.2.3",
			EngineImage:     "example/engine:1.2.3",
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
	if !strings.Contains(note, "example/engine:1.2.3") {
		t.Error("note missing engine image")
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

func TestHarvester_ValidateUsesTemplate_TuneUsesLLM(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "LLM analysis: good throughput", TotalTokens: 50}},
	}

	h := NewHarvester(2, WithHarvesterLLM(llm))
	ctx := context.Background()

	// Test 1: validate task at tier 2 with LLM available → template, LLM NOT called
	note, insightPending := h.generateNote(ctx, HarvestInput{
		Task:   PlanTask{Kind: "validate", Model: "qwen3-8b", Engine: "vllm"},
		Result: HarvestResult{Success: true, Throughput: 42, Config: map[string]any{"gmu": 0.9}},
	})
	if llm.calls != 0 {
		t.Fatalf("validate: LLM was called (%d times), expected 0", llm.calls)
	}
	if !strings.Contains(note, "42") {
		t.Error("validate: template note missing throughput")
	}
	if insightPending {
		t.Error("validate: insightPending should be false for template-by-design")
	}

	// Test 2: tune task at tier 2 with LLM available → LLM called
	note, insightPending = h.generateNote(ctx, HarvestInput{
		Task:   PlanTask{Kind: "tune", Model: "qwen3-8b", Engine: "vllm"},
		Result: HarvestResult{Success: true, Throughput: 55, Config: map[string]any{"gmu": 0.85}},
	})
	if llm.calls != 1 {
		t.Fatalf("tune: LLM call count = %d, expected 1", llm.calls)
	}
	if note != "LLM analysis: good throughput" {
		t.Errorf("tune: note = %q, want LLM response", note)
	}
	if insightPending {
		t.Error("tune: insightPending should be false when LLM succeeds")
	}
}

func TestHarvester_MatrixNote(t *testing.T) {
	h := NewHarvester(1)
	input := HarvestInput{
		Task: PlanTask{Model: "test-model", Engine: "sglang-kt"},
		Result: HarvestResult{
			Success:       true,
			BenchmarkID:   "bench-009",
			ConfigID:      "cfg-009",
			EngineVersion: "1.0.0",
			EngineImage:   "example/engine:1.0.0",
			VRAMMiB:       32768,
			RAMMiB:        8192,
			CPUUsagePct:   61.5,
			GPUUtilPct:    88.0,
			PowerWatts:    245.0,
			MatrixCells:   4,
			SuccessCells:  3,
			MatrixJSON: `{"matrix_profiles":[{"label":"latency","cells":[` +
				`{"concurrency":1,"input_tokens":128,"max_tokens":256,"result":{"throughput_tps":170.5,"ttft_p95_ms":45}},` +
				`{"concurrency":1,"input_tokens":1024,"max_tokens":256,"result":{"throughput_tps":155.0,"ttft_p95_ms":120}}` +
				`]},{"label":"throughput","cells":[` +
				`{"concurrency":4,"input_tokens":512,"max_tokens":1024,"result":{"throughput_tps":520.0,"ttft_p95_ms":200}},` +
				`{"concurrency":4,"input_tokens":2048,"max_tokens":1024,"error":"timeout"}` +
				`]}]}`,
			Config: map[string]any{"gpu_memory_utilization": 0.85},
		},
	}
	note := h.generateTemplateNote(input)
	if !strings.Contains(note, "4 cells, 3 ok") {
		t.Errorf("note missing cell count: %s", note)
	}
	if !strings.Contains(note, "170tok/s") || !strings.Contains(note, "171tok/s") {
		// Allow rounding: 170.5 → "170" or "171"
		if !strings.Contains(note, "17") {
			t.Errorf("note missing throughput: %s", note)
		}
	}
	if !strings.Contains(note, "Latency") || !strings.Contains(note, "Throughput") {
		t.Errorf("note missing profile labels: %s", note)
	}
	if !strings.Contains(note, "benchmark_id=bench-009") || !strings.Contains(note, "config_id=cfg-009") {
		t.Errorf("note missing artifact ids: %s", note)
	}
	if !strings.Contains(note, "Resources: VRAM 32768 MiB") || !strings.Contains(note, "Power 245.0 W") {
		t.Errorf("note missing resource summary: %s", note)
	}
	// Timeout cell should be excluded
	if strings.Contains(note, "2048") {
		t.Errorf("note should exclude error cells: %s", note)
	}
}

func TestHarvester_SinglePointNote(t *testing.T) {
	h := NewHarvester(1)
	input := HarvestInput{
		Task: PlanTask{Model: "test-model", Engine: "vllm"},
		Result: HarvestResult{
			Success:    true,
			Throughput: 100.5,
			TTFTP95:    45.0,
			Config:     map[string]any{"tp_size": 2},
		},
	}
	note := h.generateTemplateNote(input)
	if !strings.Contains(note, "100.5 tok/s") {
		t.Errorf("note missing throughput: %s", note)
	}
	if !strings.Contains(note, "TTFT P95 45ms") {
		t.Errorf("note missing TTFT: %s", note)
	}
}

func TestHarvester_SkipsAllFailureNotes(t *testing.T) {
	var savedTitle string
	h := NewHarvester(1, WithSaveNote(func(ctx context.Context, title, content, hardware, model, engine string) error {
		savedTitle = title
		return nil
	}))

	// Structural failure: should NOT be persisted (no failure notes at all)
	h.Harvest(context.Background(), HarvestInput{
		Task: PlanTask{Hardware: "nvidia-rtx4090-x86", Model: "qwen3-tts-0.6b", Engine: "vllm"},
		Result: HarvestResult{
			Success: false,
			Error:   "Transformers does not recognize this architecture qwen3_tts",
		},
	})
	if savedTitle != "" {
		t.Fatalf("failure notes should not be persisted, got title=%q", savedTitle)
	}

	// Transient failure: also should NOT be persisted
	h.Harvest(context.Background(), HarvestInput{
		Task: PlanTask{Hardware: "nvidia-rtx4090-x86", Model: "qwen3-8b", Engine: "vllm"},
		Result: HarvestResult{
			Success: false,
			Error:   "CUDA out of memory",
		},
	})
	if savedTitle != "" {
		t.Fatalf("failure notes should not be persisted, got title=%q", savedTitle)
	}
}
