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

	llmHugeAsset := &knowledge.ModelAsset{}
	llmHugeAsset.Metadata.Name = "qwen3-coder-next"
	llmHugeAsset.Metadata.Type = "llm"
	llmHugeAsset.Metadata.ParameterCount = "80B"

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
		policy         FirstRunPolicy
		wantMin        int
		wantMax        int
	}{
		{
			// D1=30(LLM) + D2a=10(65%util) + D2b=6(bw=0→neutral) + D2c=5(Ada)
			// + D3=20(model+engine+golden) + D4a=8(30/30) + D4b=0(no date)
			// + D5=10(single) = 89
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
			wantMin:        82,
			wantMax:        95,
		},
		{
			// D1=30(LLM) + D2a=5(33%util) + D2b=6(bw=0) + D2c=5(Ada)
			// + D3=0 + D4a=2(8/30) + D4b=0 + D5=10(single) = 58
			name: "LLM no local assets = medium score",
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
			wantMin:       50,
			wantMax:       65,
		},
		{
			// D1=30(LLM) + D2a=12(81%util) + D2b=6(bw=0) + D2c=5(Ada)
			// + D3=0 + D4a=2(8/30) + D4b=0 + D5=5(dual) = 60
			name: "multi-GPU: better VRAM util offset by D5 penalty",
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
			wantMin:       52,
			wantMax:       66,
		},
		{
			// D1=5(ASR) + D2a=5(49%util) + D2b=6(bw=0) + D2c=5(CUDA)
			// + D3=0 + D4a=0(1.7/30) + D4b=0 + D5=10(single) = 31
			name: "ASR scores far below LLM",
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
			wantMin:       25,
			wantMax:       40,
		},
		{
			// llamacpp on 16GB M4 — 8B Q4 fits well in RAM
			// D1=30(LLM) + D2a=5(ram 6144/16384=37%util) + D2b=6(bw=0) + D2c=0(arch="*")
			// + D3=0 + D4a=0(int(8/80*8)=0) + D4b=0 + D5=10(single) = 51
			name: "llamacpp 8B on M4: RAM util good fit",
			ma:   llmAsset,
			hw: knowledge.HardwareInfo{
				RAMTotalMiB: 16384,
				GPUCount:    0,
			},
			variant: &knowledge.ModelVariant{
				Hardware: knowledge.ModelVariantHardware{
					GPUArch:    "*",
					VRAMMinMiB: 0,
					RAMMinMiB:  6144,
				},
			},
			fit: &knowledge.FitReport{
				Fit:         true,
				Adjustments: make(map[string]any),
			},
			maxFitBillion: 80,
			wantMin:       45,
			wantMax:       58,
		},
		{
			// llamacpp on 16GB M4 — 80B Q4 overflows RAM
			// D1=30(LLM) + D2a=0(ram 53248/16384=325%!) + D2b=6(bw=0) + D2c=0(arch="*")
			// + D3=0 + D4a=8(80/80) + D4b=0 + D5=10(single) = 54
			// - first-run risk penalty=45 => 9
			name: "llamacpp 80B on M4: RAM overflow penalized",
			ma:   llmHugeAsset,
			hw: knowledge.HardwareInfo{
				RAMTotalMiB: 16384,
				GPUCount:    0,
			},
			variant: &knowledge.ModelVariant{
				Hardware: knowledge.ModelVariantHardware{
					GPUArch:    "*",
					VRAMMinMiB: 0,
					RAMMinMiB:  53248,
				},
			},
			fit: &knowledge.FitReport{
				Fit:         true,
				Adjustments: make(map[string]any),
			},
			maxFitBillion: 80,
			policy:        testNativeFirstRunPolicy(),
			wantMin:       0,
			wantMax:       18,
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
			policy := tt.policy
			if len(policy.ModalityScores) == 0 {
				policy = testNativeFirstRunPolicy()
			}
			score := computeFitScore(tt.ma, tt.hw, tt.variant, tt.fit, tt.engineStatus, tt.modelAvailable, tt.goldenExists, tt.maxFitBillion, policy)
			if score < tt.wantMin || score > tt.wantMax {
				t.Errorf("computeFitScore() = %d, want [%d, %d]", score, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestNativeFirstRunRiskPenaltyKeepsSmallModelAboveOversized(t *testing.T) {
	small := &knowledge.ModelAsset{}
	small.Metadata.Name = "qwen3-8b"
	small.Metadata.Type = "llm"
	small.Metadata.ParameterCount = "8B"

	huge := &knowledge.ModelAsset{}
	huge.Metadata.Name = "qwen3-coder-next"
	huge.Metadata.Type = "llm"
	huge.Metadata.ParameterCount = "80B"

	hw := knowledge.HardwareInfo{
		RAMTotalMiB: 16384,
		GPUCount:    0,
	}
	fit := &knowledge.FitReport{
		Fit:         true,
		Adjustments: make(map[string]any),
	}
	smallVariant := &knowledge.ModelVariant{
		Hardware: knowledge.ModelVariantHardware{
			GPUArch:   "*",
			RAMMinMiB: 6144,
		},
	}
	hugeVariant := &knowledge.ModelVariant{
		Hardware: knowledge.ModelVariantHardware{
			GPUArch:   "*",
			RAMMinMiB: 53248,
		},
	}

	policy := testNativeFirstRunPolicy()

	smallScore := computeFitScore(small, hw, smallVariant, fit, RecommendedEngineStatus{}, false, false, 80, policy)
	hugeScore := computeFitScore(huge, hw, hugeVariant, fit, RecommendedEngineStatus{}, false, false, 80, policy)

	if smallScore <= hugeScore {
		t.Fatalf("small native model score = %d, huge native model score = %d; want small higher", smallScore, hugeScore)
	}
}

func TestNativeFirstRunRiskPenaltyCanBeDisabledByPolicy(t *testing.T) {
	small := &knowledge.ModelAsset{}
	small.Metadata.Name = "qwen3-8b"
	small.Metadata.Type = "llm"
	small.Metadata.ParameterCount = "8B"

	huge := &knowledge.ModelAsset{}
	huge.Metadata.Name = "qwen3-coder-next"
	huge.Metadata.Type = "llm"
	huge.Metadata.ParameterCount = "80B"

	hw := knowledge.HardwareInfo{
		RAMTotalMiB: 16384,
		GPUCount:    0,
	}
	fit := &knowledge.FitReport{
		Fit:         true,
		Adjustments: make(map[string]any),
	}
	smallVariant := &knowledge.ModelVariant{
		Hardware: knowledge.ModelVariantHardware{
			GPUArch:   "*",
			RAMMinMiB: 6144,
		},
	}
	hugeVariant := &knowledge.ModelVariant{
		Hardware: knowledge.ModelVariantHardware{
			GPUArch:   "*",
			RAMMinMiB: 53248,
		},
	}
	policy := FirstRunPolicy{NativeGuardrail: NativeFirstRunGuardrail{Disabled: true}}

	smallScore := computeFitScore(small, hw, smallVariant, fit, RecommendedEngineStatus{}, false, false, 80, policy)
	hugeScore := computeFitScore(huge, hw, hugeVariant, fit, RecommendedEngineStatus{}, false, false, 80, policy)

	if hugeScore <= smallScore {
		t.Fatalf("small native model score = %d, huge native model score = %d; disabled guardrail should restore raw largest-model preference", smallScore, hugeScore)
	}
}

func testNativeFirstRunPolicy() FirstRunPolicy {
	skipDiscreteAccelerators := true
	return FirstRunPolicy{
		DefaultModalityScore: 2,
		ModalityScores: map[string]int{
			"llm":       30,
			"vlm":       25,
			"embedding": 8,
			"rerank":    8,
			"asr":       5,
			"tts":       5,
			"image_gen": 3,
			"video_gen": 3,
		},
		NativeGuardrail: NativeFirstRunGuardrail{
			WildcardGPUArch:          "*",
			SkipDiscreteAccelerators: &skipDiscreteAccelerators,
			RAMUtilizationPenalties: []UtilizationPenalty{
				{Above: 0.55, Penalty: 40},
			},
			ParameterCountPenalties: []ParameterPenalty{
				{AboveBillion: 14, Penalty: 10},
			},
			MaxPenalty: 45,
		},
	}
}

func TestBandwidthAffinity(t *testing.T) {
	moeAsset := &knowledge.ModelAsset{}
	moeAsset.Metadata.Name = "qwen3-30b-a3b"
	moeAsset.Metadata.Type = "llm"
	moeAsset.Metadata.ParameterCount = "30B-A3B"

	denseAsset := &knowledge.ModelAsset{}
	denseAsset.Metadata.Name = "qwen3-8b"
	denseAsset.Metadata.Type = "llm"
	denseAsset.Metadata.ParameterCount = "8B"

	tests := []struct {
		name    string
		hw      knowledge.HardwareInfo
		ma      *knowledge.ModelAsset
		wantMin int
		wantMax int
	}{
		{
			// GB10: 128GB unified / 273 GB/s → ratio=0.469 → VRAM-rich
			// MoE on VRAM-rich → 8
			name: "VRAM-rich + MoE = 8",
			hw: knowledge.HardwareInfo{
				GPUBandwidthGbps: 273,
				GPUVRAMMiB:       15360,
				RAMTotalMiB:      131072,
				UnifiedMemory:    true,
				GPUCount:         1,
			},
			ma:      moeAsset,
			wantMin: 8,
			wantMax: 8,
		},
		{
			// GB10: same device, Dense → 2
			name: "VRAM-rich + Dense = 2",
			hw: knowledge.HardwareInfo{
				GPUBandwidthGbps: 273,
				GPUVRAMMiB:       15360,
				RAMTotalMiB:      131072,
				UnifiedMemory:    true,
				GPUCount:         1,
			},
			ma:      denseAsset,
			wantMin: 2,
			wantMax: 2,
		},
		{
			// RTX 4090: 24GB / 1008 GB/s → ratio=0.024 → BW-rich
			// Dense on BW-rich → 8
			name: "BW-rich + Dense = 8",
			hw: knowledge.HardwareInfo{
				GPUBandwidthGbps: 1008,
				GPUVRAMMiB:       24576,
				UnifiedMemory:    false,
				GPUCount:         1,
			},
			ma:      denseAsset,
			wantMin: 8,
			wantMax: 8,
		},
		{
			// RTX 4090: BW-rich + MoE → 5
			name: "BW-rich + MoE = 5",
			hw: knowledge.HardwareInfo{
				GPUBandwidthGbps: 1008,
				GPUVRAMMiB:       24576,
				UnifiedMemory:    false,
				GPUCount:         1,
			},
			ma:      moeAsset,
			wantMin: 5,
			wantMax: 5,
		},
		{
			// Apple M4: 16GB / 120 GB/s → ratio=0.133 → Neutral → 6
			name: "Neutral device = 6 for both",
			hw: knowledge.HardwareInfo{
				GPUBandwidthGbps: 120,
				GPUVRAMMiB:       0,
				RAMTotalMiB:      16384,
				UnifiedMemory:    true,
				GPUCount:         1,
			},
			ma:      denseAsset,
			wantMin: 6,
			wantMax: 6,
		},
		{
			// Unknown bandwidth → 6 (neutral fallback, matches the neutral ratio band)
			name: "unknown bandwidth = 6",
			hw: knowledge.HardwareInfo{
				GPUBandwidthGbps: 0,
				GPUVRAMMiB:       24576,
				GPUCount:         1,
			},
			ma:      denseAsset,
			wantMin: 6,
			wantMax: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := bandwidthAffinityScore(tt.hw, tt.ma)
			if score < tt.wantMin || score > tt.wantMax {
				t.Errorf("bandwidthAffinityScore() = %d, want [%d, %d]", score, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestEffectiveVRAMMiB(t *testing.T) {
	tests := []struct {
		name string
		hw   knowledge.HardwareInfo
		want int
	}{
		{
			name: "GB10 unified uses RAMTotal",
			hw:   knowledge.HardwareInfo{UnifiedMemory: true, GPUVRAMMiB: 15360, RAMTotalMiB: 131072, GPUCount: 1},
			want: 131072,
		},
		{
			name: "Apple M4 unified GPU=0",
			hw:   knowledge.HardwareInfo{UnifiedMemory: true, GPUVRAMMiB: 0, RAMTotalMiB: 16384, GPUCount: 1},
			want: 16384,
		},
		{
			name: "RTX 4090 x2 discrete",
			hw:   knowledge.HardwareInfo{UnifiedMemory: false, GPUVRAMMiB: 24576, GPUCount: 2},
			want: 49152,
		},
		{
			name: "single discrete GPU default count",
			hw:   knowledge.HardwareInfo{UnifiedMemory: false, GPUVRAMMiB: 8192, GPUCount: 0},
			want: 8192,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveVRAMMiB(tt.hw)
			if got != tt.want {
				t.Errorf("effectiveVRAMMiB() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestModalityScore(t *testing.T) {
	policy := testNativeFirstRunPolicy()
	tests := []struct {
		modelType string
		want      int
	}{
		{"llm", 30},
		{"LLM", 30},
		{"vlm", 25},
		{"embedding", 8},
		{"rerank", 8},
		{"asr", 5},
		{"tts", 5},
		{"image_gen", 3},
		{"video_gen", 3},
		{"unknown", 2},
	}
	for _, tt := range tests {
		t.Run(tt.modelType, func(t *testing.T) {
			if got := policy.modalityScore(tt.modelType); got != tt.want {
				t.Errorf("modalityScore(%q) = %d, want %d", tt.modelType, got, tt.want)
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
