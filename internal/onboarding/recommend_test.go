package onboarding

import (
	"context"
	"strings"
	"testing"

	"github.com/jguan/aima/internal/knowledge"
)

func TestComputeFitScore(t *testing.T) {
	llmAsset := &knowledge.ModelAsset{}
	llmAsset.Metadata.Name = "qwen3-8b"
	llmAsset.Metadata.Type = "llm"
	llmAsset.Metadata.ParameterCount = "8B"

	llmBigAsset := &knowledge.ModelAsset{}
	llmBigAsset.Metadata.Name = "qwen3-30b-a3b"
	llmBigAsset.Metadata.Type = "llm"
	llmBigAsset.Metadata.ParameterCount = "30B-A3B"

	asrAsset := &knowledge.ModelAsset{}
	asrAsset.Metadata.Name = "qwen3-asr-1.7b"
	asrAsset.Metadata.Type = "asr"
	asrAsset.Metadata.ParameterCount = "1.7B"

	tests := []struct {
		name           string
		ma             *knowledge.ModelAsset
		hw             knowledge.HardwareInfo
		variant        *knowledge.ModelVariant
		fit            *knowledge.FitReport
		engineStatus   RecommendedEngineStatus
		modelAvailable bool
		goldenExists   bool
		maxFitBillion  float64
		wantMin        int
		wantMax        int
	}{
		{
			name: "exact arch + golden + local engine = high score",
			ma:   llmBigAsset,
			hw: knowledge.HardwareInfo{
				GPUArch:    "Ada",
				GPUVRAMMiB: 24576,
				GPUCount:   1,
			},
			variant: &knowledge.ModelVariant{
				Hardware: knowledge.ModelVariantHardware{
					GPUArch:     "Ada",
					VRAMMinMiB:  16000,
					GPUCountMin: 1,
				},
			},
			fit: &knowledge.FitReport{
				Fit:         true,
				Adjustments: make(map[string]any),
			},
			engineStatus:   RecommendedEngineStatus{Installed: true},
			modelAvailable: true,
			goldenExists:   true,
			maxFitBillion:  30,
			wantMin:        700,
			wantMax:        1100,
		},
		{
			name: "LLM ranks above ASR with same hardware fit",
			ma:   llmAsset,
			hw: knowledge.HardwareInfo{
				GPUArch:    "Ada",
				GPUVRAMMiB: 24576,
				GPUCount:   1,
			},
			variant: &knowledge.ModelVariant{
				Hardware: knowledge.ModelVariantHardware{
					GPUArch:     "Ada",
					VRAMMinMiB:  8000,
					GPUCountMin: 1,
				},
			},
			fit: &knowledge.FitReport{
				Fit:         true,
				Adjustments: make(map[string]any),
			},
			maxFitBillion: 30,
			wantMin:       300,
			wantMax:       500,
		},
		{
			name: "multi-GPU requirement lowers score",
			ma:   llmAsset,
			hw: knowledge.HardwareInfo{
				GPUArch:    "Ada",
				GPUVRAMMiB: 24576,
				GPUCount:   2,
			},
			variant: &knowledge.ModelVariant{
				Hardware: knowledge.ModelVariantHardware{
					GPUArch:     "Ada",
					VRAMMinMiB:  40000,
					GPUCountMin: 2,
				},
			},
			fit: &knowledge.FitReport{
				Fit:         true,
				Adjustments: make(map[string]any),
			},
			maxFitBillion: 30,
			wantMin:       250,
			wantMax:       450,
		},
		{
			name: "no performance data still scores non-zero",
			ma:   asrAsset,
			hw: knowledge.HardwareInfo{
				GPUArch:    "CUDA",
				GPUVRAMMiB: 8192,
				GPUCount:   1,
			},
			variant: &knowledge.ModelVariant{
				Hardware: knowledge.ModelVariantHardware{
					GPUArch:     "CUDA",
					VRAMMinMiB:  4000,
					GPUCountMin: 1,
				},
			},
			fit: &knowledge.FitReport{
				Fit:         true,
				Adjustments: make(map[string]any),
			},
			maxFitBillion: 30,
			wantMin:       150,
			wantMax:       350,
		},
		{
			name: "fit=false returns zero",
			ma:   llmAsset,
			hw: knowledge.HardwareInfo{
				GPUArch:    "Ada",
				GPUVRAMMiB: 4096,
				GPUCount:   1,
			},
			variant: &knowledge.ModelVariant{
				Hardware: knowledge.ModelVariantHardware{
					GPUArch:     "Ada",
					VRAMMinMiB:  24000,
					GPUCountMin: 1,
				},
			},
			fit: &knowledge.FitReport{
				Fit:         false,
				Reason:      "insufficient VRAM",
				Adjustments: make(map[string]any),
			},
			engineStatus:   RecommendedEngineStatus{Installed: false},
			modelAvailable: false,
			goldenExists:   false,
			maxFitBillion:  30,
			wantMin:        0,
			wantMax:        0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := computeFitScore(tt.ma, tt.hw, tt.variant, tt.fit, tt.engineStatus, tt.modelAvailable, tt.goldenExists, tt.maxFitBillion)
			if score < tt.wantMin || score > tt.wantMax {
				t.Errorf("computeFitScore() = %d, want [%d, %d]", score, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestBuildRecommendationReason_Localization(t *testing.T) {
	ma := &knowledge.ModelAsset{}
	ma.Metadata.Name = "qwen3-8b"
	variant := &knowledge.ModelVariant{
		Hardware: knowledge.ModelVariantHardware{
			GPUArch:     "Ada",
			VRAMMinMiB:  8000,
			GPUCountMin: 1,
		},
	}
	fit := &knowledge.FitReport{Fit: true}
	perf := knowledge.ExpectedPerf{TokensPerSecond: [2]float64{40, 60}}
	hw := knowledge.HardwareInfo{
		GPUArch:    "Ada",
		GPUVRAMMiB: 24576,
	}

	en := buildRecommendationReason(ma, variant, "vllm", fit, perf, hw, "en")
	zh := buildRecommendationReason(ma, variant, "vllm", fit, perf, hw, "zh")
	fr := buildRecommendationReason(ma, variant, "vllm", fit, perf, hw, "fr")

	if en == "" || zh == "" || fr == "" {
		t.Fatalf("unexpected empty reason: en=%q zh=%q fr=%q", en, zh, fr)
	}
	if en == zh {
		t.Errorf("expected en and zh to differ, both = %q", en)
	}
	if fr != en {
		t.Errorf("expected unknown locale to fall back to English, got fr=%q en=%q", fr, en)
	}
	if !strings.Contains(en, "fits in single GPU") {
		t.Errorf("expected English reason to contain 'fits in single GPU', got %q", en)
	}
	if !strings.Contains(zh, "单卡") {
		t.Errorf("expected Chinese reason to contain '单卡', got %q", zh)
	}
}

func TestTr_FallbackChain(t *testing.T) {
	if got := tr("zh", "single_gpu"); got != "单卡即可运行" {
		t.Errorf("tr(zh, single_gpu) = %q, want 单卡即可运行", got)
	}
	if got := tr("unknown", "single_gpu"); got != "fits in single GPU" {
		t.Errorf("tr(unknown, single_gpu) = %q, want English fallback", got)
	}
	if got := tr("en", "no_such_key"); got != "no_such_key" {
		t.Errorf("tr(en, no_such_key) = %q, want key as last-resort", got)
	}
}

func TestRecommend_EmptyCatalog(t *testing.T) {
	cat := &knowledge.Catalog{
		ModelAssets: []knowledge.ModelAsset{},
	}

	deps := &Deps{
		Cat: cat,
		BuildHardwareInfo: func(ctx context.Context) knowledge.HardwareInfo {
			return knowledge.HardwareInfo{}
		},
	}

	result, err := Recommend(context.Background(), deps, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.TotalModels != 0 {
		t.Errorf("total_models_evaluated = %d, want 0", result.TotalModels)
	}
	if len(result.Recommendations) != 0 {
		t.Errorf("recommendations length = %d, want 0", len(result.Recommendations))
	}
}
