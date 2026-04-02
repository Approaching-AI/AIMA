package knowledge

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestResolveBasic(t *testing.T) {
	cat := mustLoadCatalog(t)

	hw := HardwareInfo{
		GPUArch: "TestArch",
		CPUArch: "x86_64",
	}

	resolved, err := cat.Resolve(hw, "test-model-8b", "testengine", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if resolved.Engine != "testengine" {
		t.Errorf("Engine = %q, want %q", resolved.Engine, "testengine")
	}
	if resolved.EngineImage != "test/engine:v1" {
		t.Errorf("EngineImage = %q, want %q", resolved.EngineImage, "test/engine:v1")
	}

	// L0 engine defaults should be present
	if resolved.Config["port"] != 8000 {
		t.Errorf("Config[port] = %v, want 8000", resolved.Config["port"])
	}
	// Model variant config should override or add
	if resolved.Config["dtype"] != "float16" {
		t.Errorf("Config[dtype] = %v, want float16", resolved.Config["dtype"])
	}
	// Provenance tracking
	if resolved.Provenance["port"] != "L0" {
		t.Errorf("Provenance[port] = %q, want L0", resolved.Provenance["port"])
	}
	if resolved.Provenance["dtype"] != "L0" {
		t.Errorf("Provenance[dtype] = %q, want L0 (model variant defaults are still L0)", resolved.Provenance["dtype"])
	}
}

func TestResolveWithUserOverrides(t *testing.T) {
	cat := mustLoadCatalog(t)

	hw := HardwareInfo{
		GPUArch: "TestArch",
		CPUArch: "x86_64",
	}

	overrides := map[string]any{
		"port":           9999,
		"custom_setting": "hello",
	}

	resolved, err := cat.Resolve(hw, "test-model-8b", "testengine", overrides)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// User override should win
	if resolved.Config["port"] != 9999 {
		t.Errorf("Config[port] = %v, want 9999 (user override)", resolved.Config["port"])
	}
	if resolved.Provenance["port"] != "L1" {
		t.Errorf("Provenance[port] = %q, want L1", resolved.Provenance["port"])
	}
	if resolved.Config["custom_setting"] != "hello" {
		t.Errorf("Config[custom_setting] = %v, want hello", resolved.Config["custom_setting"])
	}
	// Non-overridden keys stay at L0
	if resolved.Config["dtype"] != "float16" {
		t.Errorf("Config[dtype] = %v, want float16", resolved.Config["dtype"])
	}
}

func TestResolveIncludesCompatibilityMetadata(t *testing.T) {
	cat := mustLoadCatalog(t)

	hw := HardwareInfo{
		GPUArch: "TestArch",
		CPUArch: "x86_64",
	}

	resolved, err := cat.Resolve(hw, "test-model-8b", "testengine", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if resolved.CompatibilityProbe != "transformers_autoconfig" {
		t.Fatalf("CompatibilityProbe = %q, want transformers_autoconfig", resolved.CompatibilityProbe)
	}
	if len(resolved.RepairInitCommands) != 1 || resolved.RepairInitCommands[0] != "python3 -m pip install --no-cache-dir transformers>=5" {
		t.Fatalf("RepairInitCommands = %v", resolved.RepairInitCommands)
	}
}

func TestResolveWildcardEngine(t *testing.T) {
	cat := mustLoadCatalog(t)

	// Use an arch that only matches the wildcard engine
	hw := HardwareInfo{
		GPUArch: "UnknownArch",
		CPUArch: "arm64",
	}

	resolved, err := cat.Resolve(hw, "test-model-8b", "universal", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if resolved.Engine != "universal" {
		t.Errorf("Engine = %q, want %q", resolved.Engine, "universal")
	}
	if resolved.EngineImage != "test/universal:v1" {
		t.Errorf("EngineImage = %q, want %q", resolved.EngineImage, "test/universal:v1")
	}
}

func TestResolvePartition(t *testing.T) {
	cat := mustLoadCatalog(t)

	hw := HardwareInfo{
		GPUArch: "TestArch",
		CPUArch: "x86_64",
	}

	resolved, err := cat.Resolve(hw, "test-model-8b", "testengine", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if resolved.Partition == nil {
		t.Fatal("expected non-nil Partition")
	}
	// Should match the wildcard single-default partition, "primary" slot
	if resolved.Partition.Name != "primary" {
		t.Errorf("Partition.Name = %q, want %q", resolved.Partition.Name, "primary")
	}
}

func TestResolveNoMatchingEngine(t *testing.T) {
	cat := mustLoadCatalog(t)

	hw := HardwareInfo{
		GPUArch: "TestArch",
		CPUArch: "x86_64",
	}

	_, err := cat.Resolve(hw, "test-model-8b", "nonexistent-engine", nil)
	if err == nil {
		t.Fatal("expected error for no matching engine")
	}
}

func TestResolveNoMatchingModel(t *testing.T) {
	cat := mustLoadCatalog(t)

	hw := HardwareInfo{
		GPUArch: "TestArch",
		CPUArch: "x86_64",
	}

	_, err := cat.Resolve(hw, "nonexistent-model", "testengine", nil)
	if err == nil {
		t.Fatal("expected error for no matching model")
	}
}

func TestResolveWithSlotOverride(t *testing.T) {
	cat := mustLoadCatalog(t)

	hw := HardwareInfo{
		GPUArch: "TestArch",
		CPUArch: "x86_64",
	}

	overrides := map[string]any{
		"slot": "secondary",
	}

	// "secondary" only exists in specific partition for test-gpu hardware.
	// The wildcard partition only has "primary" and "system_reserved".
	// Since test-gpu doesn't match hardware_profile exactly (no matching hw profile name),
	// we use the wildcard partition which has no "secondary".
	// The resolver should still work, just with default slot.
	resolved, err := cat.Resolve(hw, "test-model-8b", "testengine", overrides)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Slot override should not leak into config (handled via resolved.Slot)
	if _, ok := resolved.Config["slot"]; ok {
		t.Errorf("Config[slot] should not be set, but was %v", resolved.Config["slot"])
	}
}

func TestResolveAutoEngine(t *testing.T) {
	cat := mustLoadCatalog(t)

	t.Run("exact gpu_arch picks testengine", func(t *testing.T) {
		hw := HardwareInfo{GPUArch: "TestArch", CPUArch: "x86_64"}
		resolved, err := cat.Resolve(hw, "test-model-8b", "", nil)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolved.Engine != "testengine" {
			t.Errorf("Engine = %q, want testengine", resolved.Engine)
		}
	})

	t.Run("unknown arch falls back to universal", func(t *testing.T) {
		hw := HardwareInfo{GPUArch: "UnknownArch", CPUArch: "arm64"}
		resolved, err := cat.Resolve(hw, "test-model-8b", "", nil)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolved.Engine != "universal" {
			t.Errorf("Engine = %q, want universal", resolved.Engine)
		}
	})
}

func TestResolvePrefersExactEngineAssetVariant(t *testing.T) {
	cat := mustLoadCatalog(t)
	cat.RegisterModel(ModelAsset{
		Metadata: ModelMetadata{
			Name: "named-engine-model",
			Type: "llm",
		},
		Variants: []ModelVariant{
			{
				Name:     "named-engine-exact",
				Hardware: ModelVariantHardware{GPUArch: "TestArch"},
				Engine:   "testengine-1.0",
				Format:   "safetensors",
				DefaultConfig: map[string]any{
					"ctx_size": 8192,
				},
			},
			{
				Name:     "named-engine-generic",
				Hardware: ModelVariantHardware{GPUArch: "*"},
				Engine:   "testengine",
				Format:   "safetensors",
				DefaultConfig: map[string]any{
					"ctx_size": 4096,
				},
			},
		},
	})

	hw := HardwareInfo{
		GPUArch:    "TestArch",
		CPUArch:    "x86_64",
		GPUVRAMMiB: 8192,
	}

	resolved, err := cat.Resolve(hw, "named-engine-model", "testengine", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Config["ctx_size"] != 8192 {
		t.Fatalf("ctx_size = %v, want 8192 from exact engine asset variant", resolved.Config["ctx_size"])
	}

	engine, err := cat.InferEngineType("named-engine-model", hw)
	if err != nil {
		t.Fatalf("InferEngineType: %v", err)
	}
	if engine != "testengine-1.0" {
		t.Fatalf("InferEngineType = %q, want %q", engine, "testengine-1.0")
	}
}

func TestBuildSyntheticModelAsset(t *testing.T) {
	// Build a catalog with engines that declare supported_formats.
	// This mirrors the real catalog: llamacpp supports gguf, vllm supports safetensors.
	cat := &Catalog{
		EngineAssets: []EngineAsset{
			{
				Metadata: EngineMetadata{
					Name: "llamacpp-universal", Type: "llamacpp", Version: "1.0",
					Default: true, SupportedFormats: []string{"gguf"},
				},
				Hardware: EngineHardware{GPUArch: "*"},
			},
			{
				Metadata: EngineMetadata{
					Name: "vllm-test", Type: "vllm", Version: "1.0",
					SupportedFormats: []string{"safetensors"},
				},
				Hardware: EngineHardware{GPUArch: "TestArch"},
			},
		},
	}

	tests := []struct {
		name         string
		format       string
		modelType    string
		wantEngine   string
		wantType     string
		wantVariants int // non-default engines get a fallback variant
	}{
		{"safetensors->vllm", "safetensors", "llm", "vllm", "llm", 2},
		{"gguf->llamacpp", "gguf", "llm", "llamacpp", "llm", 1},
		{"empty type defaults to llm", "gguf", "", "llamacpp", "llm", 1},
		{"unknown format->default engine", "awq", "llm", "llamacpp", "llm", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ma := cat.BuildSyntheticModelAsset(ScanMetadata{
				Name: "test-model", Type: tt.modelType, Family: "testfam",
				ParamCount: "8B", Format: tt.format,
			}, HardwareInfo{})
			if ma.Metadata.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", ma.Metadata.Type, tt.wantType)
			}
			if len(ma.Variants) != tt.wantVariants {
				t.Fatalf("Variants count = %d, want %d", len(ma.Variants), tt.wantVariants)
			}
			v := ma.Variants[0]
			if v.Engine != tt.wantEngine {
				t.Errorf("Engine = %q, want %q", v.Engine, tt.wantEngine)
			}
			if v.Hardware.GPUArch != "*" {
				t.Errorf("GPUArch = %q, want *", v.Hardware.GPUArch)
			}
			if !strings.HasSuffix(v.Name, "-auto") {
				t.Errorf("variant Name = %q, want suffix -auto", v.Name)
			}
			// Non-default engines should have a fallback variant
			if tt.wantVariants == 2 {
				fb := ma.Variants[1]
				if fb.Engine != "llamacpp" {
					t.Errorf("fallback Engine = %q, want llamacpp", fb.Engine)
				}
			}
		})
	}
}

func TestEstimateVRAMMiB(t *testing.T) {
	tests := []struct {
		name string
		meta ScanMetadata
		want int
	}{
		{"size_bytes 8GB GGUF", ScanMetadata{SizeBytes: 8 * 1024 * 1024 * 1024}, 8192 + 2048},
		{"params 8B fp16", ScanMetadata{TotalParams: 8_000_000_000, Quantization: "fp16"}, 15258 + 3814},
		{"params 70B int4", ScanMetadata{TotalParams: 70_000_000_000, Quantization: "int4"}, 33378 + 8344},
		{"size_bytes takes priority over params", ScanMetadata{SizeBytes: 5 * 1024 * 1024 * 1024, TotalParams: 70_000_000_000}, 5120 + 1280},
		{"no data returns 0", ScanMetadata{}, 0},
		{"small model overhead floor 1GB", ScanMetadata{SizeBytes: 500 * 1024 * 1024}, 500 + 1024},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateVRAMMiB(tt.meta)
			if got != tt.want {
				t.Errorf("estimateVRAMMiB() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestInferTP(t *testing.T) {
	tests := []struct {
		name string
		vram int
		hw   HardwareInfo
		want int
	}{
		{"fits single GPU", 10000, HardwareInfo{GPUVRAMMiB: 24576, GPUCount: 1}, 1},
		{"no hw info", 10000, HardwareInfo{}, 1},
		{"needs 2 GPUs", 40000, HardwareInfo{GPUVRAMMiB: 24576, GPUCount: 2}, 2},
		{"needs 4 but only 2", 100000, HardwareInfo{GPUVRAMMiB: 24576, GPUCount: 2}, 2},
		{"needs 8 GPUs", 140000, HardwareInfo{GPUVRAMMiB: 24576, GPUCount: 8}, 8},
		{"unified memory fits", 30000, HardwareInfo{GPUVRAMMiB: 128000, GPUCount: 1, UnifiedMemory: true, RAMTotalMiB: 131072}, 1},
		{"single GPU too small but only 1", 50000, HardwareInfo{GPUVRAMMiB: 24576, GPUCount: 1}, 1},
		{"zero estimated", 0, HardwareInfo{GPUVRAMMiB: 24576, GPUCount: 2}, 1},
		{"round up to power of 2", 60000, HardwareInfo{GPUVRAMMiB: 24576, GPUCount: 8}, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferTP(tt.vram, tt.hw)
			if got != tt.want {
				t.Errorf("inferTP() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestInferGMU(t *testing.T) {
	tests := []struct {
		name string
		vram int
		hw   HardwareInfo
		want float64
	}{
		{"no hw info", 10000, HardwareInfo{}, 0},
		{"discrete small model", 5000, HardwareInfo{GPUVRAMMiB: 24576}, 0.50},
		{"discrete large model", 22000, HardwareInfo{GPUVRAMMiB: 24576}, 0.90},
		{"unified 128GB", 30000, HardwareInfo{GPUVRAMMiB: 128000, UnifiedMemory: true, RAMTotalMiB: 131072}, 0.85},
		{"unified 32GB", 10000, HardwareInfo{GPUVRAMMiB: 32000, UnifiedMemory: true, RAMTotalMiB: 32768}, 0.75},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferGMU(tt.vram, tt.hw)
			if got != tt.want {
				t.Errorf("inferGMU() = %.2f, want %.2f", got, tt.want)
			}
		})
	}
}

func TestBuildSyntheticWithHardware(t *testing.T) {
	cat := &Catalog{
		EngineAssets: []EngineAsset{
			{
				Metadata: EngineMetadata{
					Name: "vllm-test", Type: "vllm", Version: "1.0",
					SupportedFormats: []string{"safetensors"},
				},
				Hardware: EngineHardware{GPUArch: "*"},
			},
			{
				Metadata: EngineMetadata{
					Name: "llamacpp-universal", Type: "llamacpp", Version: "1.0",
					Default: true, SupportedFormats: []string{"gguf"},
				},
				Hardware: EngineHardware{GPUArch: "*"},
			},
		},
	}
	meta := ScanMetadata{
		Name: "big-model", Type: "llm", Format: "safetensors",
		SizeBytes: 16 * 1024 * 1024 * 1024, // 16GB
	}
	hw := HardwareInfo{GPUArch: "Ada", GPUVRAMMiB: 24576, GPUCount: 1}

	ma := cat.BuildSyntheticModelAsset(meta, hw)

	// Should have 3 variants: hardware-specific + wildcard + llamacpp fallback
	if len(ma.Variants) != 3 {
		t.Fatalf("Variants count = %d, want 3", len(ma.Variants))
	}

	// First variant should be hardware-specific with VRAM estimate
	v := ma.Variants[0]
	if v.Hardware.GPUArch != "Ada" {
		t.Errorf("variant[0] GPUArch = %q, want Ada", v.Hardware.GPUArch)
	}
	if v.Hardware.VRAMMinMiB == 0 {
		t.Error("variant[0] VRAMMinMiB should be > 0")
	}
	if v.DefaultConfig == nil {
		t.Fatal("variant[0] DefaultConfig should not be nil")
	}
	if _, ok := v.DefaultConfig["gpu_memory_utilization"]; !ok {
		t.Error("variant[0] should have gpu_memory_utilization")
	}
	if _, ok := v.DefaultConfig["max_model_len"]; !ok {
		t.Error("variant[0] should have max_model_len")
	}

	// Wildcard variant should also have VRAM estimate
	wc := ma.Variants[1]
	if wc.Hardware.GPUArch != "*" {
		t.Errorf("variant[1] GPUArch = %q, want *", wc.Hardware.GPUArch)
	}
	if wc.Hardware.VRAMMinMiB == 0 {
		t.Error("variant[1] VRAMMinMiB should be > 0 (wildcard with VRAM constraint)")
	}
}

func TestResolveSyntheticWithAutoTP(t *testing.T) {
	cat := &Catalog{
		EngineAssets: []EngineAsset{
			{
				Metadata: EngineMetadata{
					Name: "vllm-test", Type: "vllm", Version: "1.0",
					Default: true, SupportedFormats: []string{"safetensors"},
				},
				Hardware: EngineHardware{GPUArch: "*"},
				Startup: EngineStartup{
					Command:     []string{"serve"},
					DefaultArgs: map[string]any{"gpu_memory_utilization": 0.9},
				},
			},
		},
	}

	meta := ScanMetadata{
		Name: "big-model", Type: "llm", Format: "safetensors",
		SizeBytes: 32 * 1024 * 1024 * 1024,
	}
	hw := HardwareInfo{GPUArch: "Ada", GPUVRAMMiB: 24576, GPUCount: 2}
	totalVRAM := estimateVRAMMiB(meta)

	synth := cat.BuildSyntheticModelAsset(meta, hw)
	cat.UpsertSyntheticModel(synth)

	if len(cat.ModelAssets) != 1 {
		t.Fatalf("ModelAssets count = %d, want 1", len(cat.ModelAssets))
	}
	variant := cat.ModelAssets[0].Variants[0]
	if variant.Hardware.GPUCountMin != 2 {
		t.Fatalf("GPUCountMin = %d, want 2", variant.Hardware.GPUCountMin)
	}
	if variant.Hardware.VRAMMinMiB != ceilDiv(totalVRAM, 2) {
		t.Fatalf("VRAMMinMiB = %d, want %d", variant.Hardware.VRAMMinMiB, ceilDiv(totalVRAM, 2))
	}

	resolved, err := cat.Resolve(hw, "big-model", "", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Engine != "vllm" {
		t.Fatalf("Engine = %q, want vllm", resolved.Engine)
	}
	if got := int(toFloat64(resolved.Config["tensor_parallel_size"])); got != 2 {
		t.Fatalf("tensor_parallel_size = %d, want 2", got)
	}
	if resolved.EstimatedVRAMMiB != totalVRAM {
		t.Fatalf("EstimatedVRAMMiB = %d, want %d", resolved.EstimatedVRAMMiB, totalVRAM)
	}
}

func TestFormatToEngine(t *testing.T) {
	cat := &Catalog{
		EngineAssets: []EngineAsset{
			{
				Metadata: EngineMetadata{
					Name: "llamacpp-universal", Type: "llamacpp",
					SupportedFormats: []string{"gguf"},
				},
			},
			{
				Metadata: EngineMetadata{
					Name: "vllm-test", Type: "vllm",
					SupportedFormats: []string{"safetensors"},
				},
			},
		},
	}

	tests := []struct {
		format string
		want   string
	}{
		{"gguf", "llamacpp"},
		{"GGUF", "llamacpp"}, // case insensitive
		{"safetensors", "vllm"},
		{"Safetensors", "vllm"},
		{"awq", ""}, // unknown
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			got := cat.FormatToEngine(tt.format)
			if got != tt.want {
				t.Errorf("FormatToEngine(%q) = %q, want %q", tt.format, got, tt.want)
			}
		})
	}
}

func TestDefaultEngine(t *testing.T) {
	t.Run("explicit default", func(t *testing.T) {
		cat := &Catalog{
			EngineAssets: []EngineAsset{
				{Metadata: EngineMetadata{Name: "vllm", Type: "vllm"}, Hardware: EngineHardware{GPUArch: "Ada"}},
				{Metadata: EngineMetadata{Name: "llamacpp", Type: "llamacpp", Default: true}, Hardware: EngineHardware{GPUArch: "*"}},
			},
		}
		if got := cat.DefaultEngine(); got != "llamacpp" {
			t.Errorf("DefaultEngine() = %q, want llamacpp", got)
		}
	})

	t.Run("wildcard fallback", func(t *testing.T) {
		cat := &Catalog{
			EngineAssets: []EngineAsset{
				{Metadata: EngineMetadata{Name: "vllm", Type: "vllm"}, Hardware: EngineHardware{GPUArch: "Ada"}},
				{Metadata: EngineMetadata{Name: "uni", Type: "universal"}, Hardware: EngineHardware{GPUArch: "*"}},
			},
		}
		if got := cat.DefaultEngine(); got != "universal" {
			t.Errorf("DefaultEngine() = %q, want universal", got)
		}
	})

	t.Run("empty catalog uses FallbackEngine", func(t *testing.T) {
		cat := &Catalog{}
		if got := cat.DefaultEngine(); got != FallbackEngine {
			t.Errorf("DefaultEngine() = %q, want %q", got, FallbackEngine)
		}
	})
}

func TestRegisterModelDedup(t *testing.T) {
	cat := mustLoadCatalog(t)
	before := len(cat.ModelAssets)

	// Register a model that already exists
	cat.RegisterModel(ModelAsset{Metadata: ModelMetadata{Name: "test-model-8b"}})
	if len(cat.ModelAssets) != before {
		t.Errorf("ModelAssets count = %d after dup register, want %d", len(cat.ModelAssets), before)
	}

	// Register a new model
	cat.RegisterModel(ModelAsset{Metadata: ModelMetadata{Name: "new-model"}})
	if len(cat.ModelAssets) != before+1 {
		t.Errorf("ModelAssets count = %d after new register, want %d", len(cat.ModelAssets), before+1)
	}
}

func TestResolveSyntheticModel(t *testing.T) {
	cat := mustLoadCatalog(t)

	// Register a synthetic model using "universal" engine (available in test catalog with gpu_arch="*")
	synth := ModelAsset{
		Kind:     "model_asset",
		Metadata: ModelMetadata{Name: "synth-model-7b", Type: "llm"},
		Variants: []ModelVariant{{
			Name:     "synth-model-7b-auto",
			Hardware: ModelVariantHardware{GPUArch: "*"},
			Engine:   "universal",
			Format:   "gguf",
		}},
	}
	cat.RegisterModel(synth)

	hw := HardwareInfo{GPUArch: "TestArch", CPUArch: "x86_64"}
	resolved, err := cat.Resolve(hw, "synth-model-7b", "", nil)
	if err != nil {
		t.Fatalf("Resolve synthetic: %v", err)
	}
	if resolved.Engine != "universal" {
		t.Errorf("Engine = %q, want universal", resolved.Engine)
	}
	// Should inherit engine L0 defaults from universal engine
	if resolved.Config["port"] != 8080 {
		t.Errorf("Config[port] = %v, want 8080 (universal engine default)", resolved.Config["port"])
	}
	if resolved.Config["ctx_size"] != 4096 {
		t.Errorf("Config[ctx_size] = %v, want 4096 (universal engine default)", resolved.Config["ctx_size"])
	}
}

// --- Hardware-aware resolution tests ---

func TestResolveVRAMFiltering(t *testing.T) {
	cat := mustLoadCatalog(t)

	// test-model-8b TestArch variant requires vram_min_mib: 4096.
	// With only 2048 MiB VRAM, the TestArch variant should be filtered out,
	// falling back to the universal wildcard variant.
	hw := HardwareInfo{
		GPUArch:    "TestArch",
		CPUArch:    "x86_64",
		GPUVRAMMiB: 2048, // Less than variant's vram_min_mib: 4096
	}

	resolved, err := cat.Resolve(hw, "test-model-8b", "testengine", nil)
	if err == nil {
		t.Fatalf("expected error (TestArch testengine variant needs 4096 MiB, only 2048 available), got engine=%q", resolved.Engine)
	}

	// But auto-engine inference should fall through to universal
	resolved, err = cat.Resolve(hw, "test-model-8b", "", nil)
	if err != nil {
		t.Fatalf("Resolve with auto-engine: %v", err)
	}
	if resolved.Engine != "universal" {
		t.Errorf("Engine = %q, want universal (VRAM too low for testengine)", resolved.Engine)
	}
}

func TestResolveVRAMSufficient(t *testing.T) {
	cat := mustLoadCatalog(t)

	// With enough VRAM, the exact TestArch variant should be selected
	hw := HardwareInfo{
		GPUArch:    "TestArch",
		CPUArch:    "x86_64",
		GPUVRAMMiB: 8192, // More than variant's vram_min_mib: 4096
	}

	resolved, err := cat.Resolve(hw, "test-model-8b", "testengine", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Engine != "testengine" {
		t.Errorf("Engine = %q, want testengine", resolved.Engine)
	}
	if resolved.Config["dtype"] != "float16" {
		t.Errorf("Config[dtype] = %v, want float16 (TestArch variant)", resolved.Config["dtype"])
	}
}

func TestResolveVRAMZeroSkipsFilter(t *testing.T) {
	cat := mustLoadCatalog(t)

	// GPUVRAMMiB=0 means "unknown" — should NOT filter by VRAM (backward compat)
	hw := HardwareInfo{
		GPUArch: "TestArch",
		CPUArch: "x86_64",
		// GPUVRAMMiB: 0 (default, unknown)
	}

	resolved, err := cat.Resolve(hw, "test-model-8b", "testengine", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Engine != "testengine" {
		t.Errorf("Engine = %q, want testengine (zero VRAM = skip filter)", resolved.Engine)
	}
}

func TestRealCatalogKTLowVRAMDoesNotMatchSingleGPUVariants(t *testing.T) {
	cat, err := LoadCatalog(catalogFS())
	if err != nil {
		t.Fatalf("LoadCatalog(real FS): %v", err)
	}

	tests := []string{
		"qwen3-30b-a3b",
		"qwen3.5-35b-a3b",
	}
	hw := HardwareInfo{
		GPUArch:     "Ada",
		GPUVRAMMiB:  4096,
		GPUCount:    1,
		Platform:    "linux/amd64",
		RuntimeType: "native",
	}

	for _, modelName := range tests {
		modelName := modelName
		t.Run(modelName, func(t *testing.T) {
			if _, err := cat.Resolve(hw, modelName, "sglang-kt", nil); err == nil {
				t.Fatalf("Resolve(%s, sglang-kt) unexpectedly succeeded on 4 GiB Ada", modelName)
			}
		})
	}
}

func TestRealCatalogSGLangKTUsesAppImageExtractAndRunFallback(t *testing.T) {
	cat, err := LoadCatalog(catalogFS())
	if err != nil {
		t.Fatalf("LoadCatalog(real FS): %v", err)
	}

	engine := cat.FindEngineByName("sglang-kt-ada", HardwareInfo{GPUArch: "Ada"})
	if engine == nil {
		t.Fatal("sglang-kt-ada engine not found in real catalog")
	}
	if got := engine.Startup.Env["APPIMAGE_EXTRACT_AND_RUN"]; got != "1" {
		t.Fatalf("APPIMAGE_EXTRACT_AND_RUN = %q, want 1", got)
	}
}

func TestResolveUnifiedMemoryFilter(t *testing.T) {
	unified := true
	discrete := false

	// Build a catalog with two variants: one unified-only, one discrete-only
	fs := fstest.MapFS{
		"engines/eng.yaml": &fstest.MapFile{Data: []byte(`kind: engine_asset
metadata:
  name: eng-1
  type: eng
  version: "1.0"
image:
  name: test/eng
  tag: "v1"
  platforms: [linux/amd64]
hardware:
  gpu_arch: TestArch
startup:
  command: ["serve", "{{.ModelPath}}"]
  default_args:
    port: 8000
  health_check:
    path: /health
    timeout_s: 60
`)},
		"models/m.yaml": &fstest.MapFile{Data: []byte(`kind: model_asset
metadata:
  name: test-unified-model
  type: llm
  family: test
  parameter_count: "8B"
storage:
  formats: [safetensors]
variants:
  - name: unified-variant
    hardware:
      gpu_arch: TestArch
      vram_min_mib: 1024
      unified_memory: true
    engine: eng
    format: safetensors
    default_config:
      gpu_memory_utilization: 0.30
  - name: discrete-variant
    hardware:
      gpu_arch: TestArch
      vram_min_mib: 1024
      unified_memory: false
    engine: eng
    format: safetensors
    default_config:
      gpu_memory_utilization: 0.90
`)},
	}
	cat, err := LoadCatalog(fs)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}

	t.Run("unified memory selects unified variant", func(t *testing.T) {
		hw := HardwareInfo{GPUArch: "TestArch", GPUVRAMMiB: 8192, UnifiedMemory: true}
		resolved, err := cat.Resolve(hw, "test-unified-model", "eng", nil)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		gmu := toFloat64(resolved.Config["gpu_memory_utilization"])
		if gmu != 0.30 {
			t.Errorf("gpu_memory_utilization = %.2f, want 0.30 (unified variant)", gmu)
		}
		_ = unified
	})

	t.Run("discrete memory selects discrete variant", func(t *testing.T) {
		hw := HardwareInfo{GPUArch: "TestArch", GPUVRAMMiB: 8192, UnifiedMemory: false}
		resolved, err := cat.Resolve(hw, "test-unified-model", "eng", nil)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		gmu := toFloat64(resolved.Config["gpu_memory_utilization"])
		if gmu != 0.90 {
			t.Errorf("gpu_memory_utilization = %.2f, want 0.90 (discrete variant)", gmu)
		}
		_ = discrete
	})
}

func TestCheckFitAdjustsGMU(t *testing.T) {
	resolved := &ResolvedConfig{
		Config:     map[string]any{"gpu_memory_utilization": 0.90},
		Provenance: map[string]string{"gpu_memory_utilization": "L0"},
	}

	// GPU has 10240 MiB total but 4096 used, so 6144 free.
	// maxSafeGMU = (6144 - 512) / 10240 ≈ 0.55
	hw := HardwareInfo{
		GPUVRAMMiB:    10240,
		GPUMemUsedMiB: 4096,
		GPUMemFreeMiB: 6144,
	}

	fit := CheckFit(resolved, hw)
	if !fit.Fit {
		t.Fatalf("expected Fit=true, got Reason=%q", fit.Reason)
	}
	if len(fit.Warnings) == 0 {
		t.Fatal("expected warnings about GMU adjustment")
	}
	adj, ok := fit.Adjustments["gpu_memory_utilization"]
	if !ok {
		t.Fatal("expected gpu_memory_utilization adjustment")
	}
	adjVal := toFloat64(adj)
	if adjVal < 0.50 || adjVal > 0.56 {
		t.Errorf("adjusted gpu_memory_utilization = %.2f, want ~0.55", adjVal)
	}
}

func TestCheckFitInsufficientGPU(t *testing.T) {
	resolved := &ResolvedConfig{
		Config: map[string]any{"gpu_memory_utilization": 0.90},
	}

	// GPU almost full: only 256 MiB free (below 512 safety margin)
	hw := HardwareInfo{
		GPUVRAMMiB:    8192,
		GPUMemUsedMiB: 7936,
		GPUMemFreeMiB: 256,
	}

	fit := CheckFit(resolved, hw)
	if fit.Fit {
		t.Fatal("expected Fit=false for nearly-full GPU")
	}
	if !strings.Contains(fit.Reason, "insufficient") {
		t.Errorf("Reason = %q, want substring 'insufficient'", fit.Reason)
	}
}

func TestCheckFitGracefulDegradation(t *testing.T) {
	resolved := &ResolvedConfig{
		Config: map[string]any{"gpu_memory_utilization": 0.90},
	}

	// No dynamic metrics (zero values) — should pass without adjustments
	hw := HardwareInfo{}

	fit := CheckFit(resolved, hw)
	if !fit.Fit {
		t.Fatalf("expected Fit=true with no metrics, got Reason=%q", fit.Reason)
	}
	if len(fit.Adjustments) != 0 {
		t.Errorf("expected no adjustments with no metrics, got %v", fit.Adjustments)
	}
	if len(fit.Warnings) != 0 {
		t.Errorf("expected no warnings with no metrics, got %v", fit.Warnings)
	}
}

func TestCheckFitLowRAMWarning(t *testing.T) {
	resolved := &ResolvedConfig{
		Config: map[string]any{},
	}

	hw := HardwareInfo{RAMAvailMiB: 1024}

	fit := CheckFit(resolved, hw)
	if !fit.Fit {
		t.Fatal("expected Fit=true for low RAM (warning only)")
	}
	if len(fit.Warnings) == 0 {
		t.Fatal("expected low RAM warning")
	}
	if !strings.Contains(fit.Warnings[0], "low available RAM") {
		t.Errorf("warning = %q, want substring 'low available RAM'", fit.Warnings[0])
	}
}

func TestCheckFitUnifiedMemoryGuard(t *testing.T) {
	// GB10-like: 128GB unified memory, gmu=0.9 leaves only ~13GB for OS (<16GB reserve)
	resolved := &ResolvedConfig{
		Config: map[string]any{"gpu_memory_utilization": 0.90},
	}
	hw := HardwareInfo{
		UnifiedMemory: true,
		RAMTotalMiB:   131072, // 128 GB
	}

	fit := CheckFit(resolved, hw)
	if !fit.Fit {
		t.Fatalf("expected Fit=true with adjustment, got Reason=%q", fit.Reason)
	}
	adj, ok := fit.Adjustments["gpu_memory_utilization"]
	if !ok {
		t.Fatal("expected gpu_memory_utilization adjustment for unified memory guard")
	}
	adjVal := toFloat64(adj)
	// maxSafe = floor((131072-16384)/131072 * 100) / 100 = floor(87.5) / 100 = 0.87
	if adjVal < 0.85 || adjVal > 0.88 {
		t.Errorf("adjusted gmu = %.2f, want ~0.87", adjVal)
	}
	if len(fit.Warnings) == 0 || !strings.Contains(fit.Warnings[0], "unified memory") {
		t.Errorf("expected unified memory warning, got %v", fit.Warnings)
	}
}

func TestCheckFitUnifiedMemoryBlock(t *testing.T) {
	// Tiny unified memory system where even minimum gmu can't leave enough for OS
	resolved := &ResolvedConfig{
		Config: map[string]any{"gpu_memory_utilization": 0.99},
	}
	hw := HardwareInfo{
		UnifiedMemory: true,
		RAMTotalMiB:   8192, // 8GB — reserve 8GB means maxSafe < 0.1
	}

	fit := CheckFit(resolved, hw)
	if fit.Fit {
		t.Fatal("expected Fit=false for tiny unified memory system with high gmu")
	}
	if !strings.Contains(fit.Reason, "unified memory") {
		t.Errorf("Reason = %q, want substring 'unified memory'", fit.Reason)
	}
}

func TestCheckFitUnifiedMemorySmallSystem(t *testing.T) {
	// mac-m4-like: 16GB unified memory, gmu=0.9 leaves 1.6GB (<8GB reserve)
	resolved := &ResolvedConfig{
		Config: map[string]any{"gpu_memory_utilization": 0.90},
	}
	hw := HardwareInfo{
		UnifiedMemory: true,
		RAMTotalMiB:   16384, // 16GB
	}

	fit := CheckFit(resolved, hw)
	if !fit.Fit {
		t.Fatalf("expected Fit=true with adjustment, got Reason=%q", fit.Reason)
	}
	adj, ok := fit.Adjustments["gpu_memory_utilization"]
	if !ok {
		t.Fatal("expected gpu_memory_utilization adjustment for 16GB unified memory")
	}
	adjVal := toFloat64(adj)
	// maxSafe = floor((16384-8192)/16384 * 100) / 100 = 0.50
	if adjVal != 0.50 {
		t.Errorf("adjusted gmu = %.2f, want 0.50", adjVal)
	}
}

func TestCheckFitUnifiedMemoryDiscreteUnaffected(t *testing.T) {
	// Discrete GPU system: unified memory guard should NOT trigger
	resolved := &ResolvedConfig{
		Config: map[string]any{"gpu_memory_utilization": 0.90},
	}
	hw := HardwareInfo{
		UnifiedMemory: false,
		RAMTotalMiB:   131072, // 128GB system RAM, but discrete GPU
	}

	fit := CheckFit(resolved, hw)
	if !fit.Fit {
		t.Fatalf("expected Fit=true for discrete GPU, got Reason=%q", fit.Reason)
	}
	if _, ok := fit.Adjustments["gpu_memory_utilization"]; ok {
		t.Error("expected no gmu adjustment for discrete GPU system")
	}
}

func TestCheckFitSGLangMemFraction(t *testing.T) {
	// SGLang uses mem_fraction_static instead of gpu_memory_utilization
	resolved := &ResolvedConfig{
		Config: map[string]any{"mem_fraction_static": 0.90},
	}
	hw := HardwareInfo{
		UnifiedMemory: true,
		RAMTotalMiB:   131072, // 128GB
	}

	fit := CheckFit(resolved, hw)
	if !fit.Fit {
		t.Fatalf("expected Fit=true with adjustment, got Reason=%q", fit.Reason)
	}
	adj, ok := fit.Adjustments["mem_fraction_static"]
	if !ok {
		t.Fatal("expected mem_fraction_static adjustment for unified memory guard")
	}
	adjVal := toFloat64(adj)
	if adjVal < 0.85 || adjVal > 0.88 {
		t.Errorf("adjusted mem_fraction_static = %.2f, want ~0.87", adjVal)
	}
}

func TestCheckFitSwapWarning(t *testing.T) {
	// Unified memory system with swap enabled should produce a warning
	resolved := &ResolvedConfig{
		Config: map[string]any{"gpu_memory_utilization": 0.80},
	}
	hw := HardwareInfo{
		UnifiedMemory: true,
		RAMTotalMiB:   131072,
		SwapTotalMiB:  16384, // 16GB swap
	}

	fit := CheckFit(resolved, hw)
	if !fit.Fit {
		t.Fatalf("expected Fit=true, got Reason=%q", fit.Reason)
	}
	var found bool
	for _, w := range fit.Warnings {
		if strings.Contains(w, "swap") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected swap warning for unified memory system, got %v", fit.Warnings)
	}
}

func TestResolveVariantForPull(t *testing.T) {
	cat := mustLoadCatalog(t)

	tests := []struct {
		name        string
		modelName   string
		hw          HardwareInfo
		wantVariant string // expected variant name, "" if nil
		wantEngine  string // expected engine type, "" if none
		wantFormat  string
		wantErr     bool
	}{
		{
			name:        "exact gpu_arch match",
			modelName:   "test-model-8b",
			hw:          HardwareInfo{GPUArch: "TestArch", CPUArch: "x86_64", GPUVRAMMiB: 8192},
			wantVariant: "test-model-8b-testarch-testengine",
			wantEngine:  "testengine",
			wantFormat:  "safetensors",
		},
		{
			name:        "wildcard fallback for unknown arch",
			modelName:   "test-model-8b",
			hw:          HardwareInfo{GPUArch: "UnknownArch", CPUArch: "arm64"},
			wantVariant: "test-model-8b-universal",
			wantEngine:  "universal",
			wantFormat:  "gguf",
		},
		{
			name:        "VRAM too low falls back to universal",
			modelName:   "test-model-8b",
			hw:          HardwareInfo{GPUArch: "TestArch", CPUArch: "x86_64", GPUVRAMMiB: 2048},
			wantVariant: "test-model-8b-universal",
			wantEngine:  "universal",
			wantFormat:  "gguf",
		},
		{
			name:      "model not found",
			modelName: "nonexistent",
			hw:        HardwareInfo{GPUArch: "TestArch"},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ma, variant, engineType, err := cat.ResolveVariantForPull(tt.modelName, tt.hw)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if ma == nil {
				t.Fatal("expected non-nil ModelAsset")
			}
			if engineType != tt.wantEngine {
				t.Errorf("engineType = %q, want %q", engineType, tt.wantEngine)
			}
			if tt.wantVariant == "" {
				if variant != nil {
					t.Errorf("expected nil variant, got %q", variant.Name)
				}
			} else {
				if variant == nil {
					t.Fatalf("expected variant %q, got nil", tt.wantVariant)
				}
				if variant.Name != tt.wantVariant {
					t.Errorf("variant.Name = %q, want %q", variant.Name, tt.wantVariant)
				}
				if variant.Format != tt.wantFormat {
					t.Errorf("variant.Format = %q, want %q", variant.Format, tt.wantFormat)
				}
			}
		})
	}
}

func TestParsedExpectedPerf(t *testing.T) {
	tests := []struct {
		name   string
		perf   map[string]any
		wantS  int     // StartupTimeS
		wantCS int     // ColdStartTimeS
		wantV  int     // VRAMMiB
		wantT0 float64 // TokensPerSecond[0]
		wantT1 float64 // TokensPerSecond[1]
	}{
		{"nil map", nil, 0, 0, 0, 0, 0},
		{"empty map", map[string]any{}, 0, 0, 0, 0, 0},
		{"full fields", map[string]any{
			"startup_time_s":    30,
			"cold_start_time_s": 45,
			"vram_mib":          16000,
			"tokens_per_second": []any{10.5, 20.0},
		}, 30, 45, 16000, 10.5, 20.0},
		{"partial fields", map[string]any{
			"startup_time_s": 15,
		}, 15, 0, 0, 0, 0},
		{"float values", map[string]any{
			"startup_time_s": 12.7,
			"vram_mib":       8192.0,
		}, 12, 0, 8192, 0, 0},
		{"tokens_per_second short array", map[string]any{
			"tokens_per_second": []any{5.0},
		}, 0, 0, 0, 0, 0},
		{"non-numeric startup", map[string]any{
			"startup_time_s": "not-a-number",
		}, 0, 0, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &ModelVariant{ExpectedPerformance: tt.perf}
			p := v.ParsedExpectedPerf()
			if p.StartupTimeS != tt.wantS {
				t.Errorf("StartupTimeS = %d, want %d", p.StartupTimeS, tt.wantS)
			}
			if p.ColdStartTimeS != tt.wantCS {
				t.Errorf("ColdStartTimeS = %d, want %d", p.ColdStartTimeS, tt.wantCS)
			}
			if p.VRAMMiB != tt.wantV {
				t.Errorf("VRAMMiB = %d, want %d", p.VRAMMiB, tt.wantV)
			}
			if p.TokensPerSecond[0] != tt.wantT0 {
				t.Errorf("TokensPerSecond[0] = %f, want %f", p.TokensPerSecond[0], tt.wantT0)
			}
			if p.TokensPerSecond[1] != tt.wantT1 {
				t.Errorf("TokensPerSecond[1] = %f, want %f", p.TokensPerSecond[1], tt.wantT1)
			}
		})
	}
}

func TestResolveTimeAndPowerFields(t *testing.T) {
	cat := mustLoadCatalog(t)

	hw := HardwareInfo{GPUArch: "TestArch", CPUArch: "x86_64", GPUVRAMMiB: 8192}
	resolved, err := cat.Resolve(hw, "test-model-8b", "testengine", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// testengine has cold_start_s: [10, 30] and typical_draw_watts: [50, 100]
	if resolved.ColdStartSMin != 10 || resolved.ColdStartSMax != 30 {
		t.Errorf("ColdStartS = [%d, %d], want [10, 30]", resolved.ColdStartSMin, resolved.ColdStartSMax)
	}
	if resolved.EnginePowerWattsMin != 50 || resolved.EnginePowerWattsMax != 100 {
		t.Errorf("EnginePowerWatts = [%d, %d], want [50, 100]", resolved.EnginePowerWattsMin, resolved.EnginePowerWattsMax)
	}

	// TestArch variant has vram_min_mib: 4096, no expected_performance.vram_mib
	if resolved.EstimatedVRAMMiB != 4096 {
		t.Errorf("EstimatedVRAMMiB = %d, want 4096 (from vram_min_mib fallback)", resolved.EstimatedVRAMMiB)
	}
}

func TestResolveTimeFieldsZeroWhenMissing(t *testing.T) {
	// Engine with no time/power constraints should produce zero values
	cat := &Catalog{
		EngineAssets: []EngineAsset{{
			Metadata: EngineMetadata{Name: "bare-engine", Type: "bare", Version: "1.0"},
			Image:    EngineImage{Name: "bare/engine", Tag: "v1"},
			Hardware: EngineHardware{GPUArch: "*"},
			Startup:  EngineStartup{Command: []string{"serve"}, DefaultArgs: map[string]any{}, HealthCheck: HealthCheck{Path: "/health", TimeoutS: 60}},
		}},
		ModelAssets: []ModelAsset{{
			Kind:     "model_asset",
			Metadata: ModelMetadata{Name: "bare-model"},
			Variants: []ModelVariant{{
				Name:     "bare-v",
				Hardware: ModelVariantHardware{GPUArch: "*"},
				Engine:   "bare",
			}},
		}},
	}
	hw := HardwareInfo{GPUArch: "any"}
	resolved, err := cat.Resolve(hw, "bare-model", "bare", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.ColdStartSMin != 0 || resolved.ColdStartSMax != 0 {
		t.Errorf("ColdStartS = [%d, %d], want [0, 0]", resolved.ColdStartSMin, resolved.ColdStartSMax)
	}
	if resolved.EnginePowerWattsMin != 0 || resolved.EnginePowerWattsMax != 0 {
		t.Errorf("EnginePowerWatts = [%d, %d], want [0, 0]", resolved.EnginePowerWattsMin, resolved.EnginePowerWattsMax)
	}
	if resolved.EstimatedVRAMMiB != 0 {
		t.Errorf("EstimatedVRAMMiB = %d, want 0", resolved.EstimatedVRAMMiB)
	}
}

func TestResolveLeavesEngineImageEmptyForNativePreinstalledEngine(t *testing.T) {
	cat := &Catalog{
		EngineAssets: []EngineAsset{{
			Metadata: EngineMetadata{Name: "vllm-musa", Type: "vllm", Version: "0.9.2"},
			Hardware: EngineHardware{GPUArch: "MUSA"},
			Startup: EngineStartup{
				Command:     []string{"vllm", "serve", "{{.ModelPath}}"},
				DefaultArgs: map[string]any{},
				HealthCheck: HealthCheck{Path: "/health", TimeoutS: 60},
			},
			Source: &EngineSource{
				InstallType: "preinstalled",
				Probe: &EngineSourceProbe{
					Paths: []string{"/opt/mt-ai/llm/venv/bin/vllm"},
				},
			},
			Runtime: EngineRuntime{Default: "native"},
		}},
		ModelAssets: []ModelAsset{{
			Kind:     "model_asset",
			Metadata: ModelMetadata{Name: "qwen3-8b"},
			Variants: []ModelVariant{{
				Name:     "qwen3-8b-musa",
				Hardware: ModelVariantHardware{GPUArch: "MUSA"},
				Engine:   "vllm",
			}},
		}},
	}
	hw := HardwareInfo{GPUArch: "MUSA", Platform: "linux/arm64", RuntimeType: "native"}
	resolved, err := cat.Resolve(hw, "qwen3-8b", "vllm", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.EngineImage != "" {
		t.Fatalf("EngineImage = %q, want empty for native preinstalled engine", resolved.EngineImage)
	}
	if resolved.RuntimeRecommendation != "native" {
		t.Fatalf("RuntimeRecommendation = %q, want native", resolved.RuntimeRecommendation)
	}
}

func TestCheckFitPowerBudget(t *testing.T) {
	tests := []struct {
		name        string
		tdpWatts    int
		powerMin    int
		powerMax    int
		wantWarning string // substring, "" = no warning expected
	}{
		{"TDP=450, power [80,200] → no warning", 450, 80, 200, ""},
		{"TDP=22, power [80,200] → min exceeds", 22, 80, 200, "minimum power draw (80 W) exceeds"},
		{"TDP=150, power [80,200] → may reach", 150, 80, 200, "may reach 200 W"},
		{"TDP=0 → skip", 0, 80, 200, ""},
		{"power=0 → skip", 150, 0, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved := &ResolvedConfig{
				Config:              map[string]any{},
				EnginePowerWattsMin: tt.powerMin,
				EnginePowerWattsMax: tt.powerMax,
			}
			hw := HardwareInfo{TDPWatts: tt.tdpWatts}
			fit := CheckFit(resolved, hw)
			if !fit.Fit {
				t.Fatalf("expected Fit=true, got Reason=%q", fit.Reason)
			}
			if tt.wantWarning == "" {
				for _, w := range fit.Warnings {
					if strings.Contains(w, "power") {
						t.Errorf("unexpected power warning: %q", w)
					}
				}
			} else {
				var found bool
				for _, w := range fit.Warnings {
					if strings.Contains(w, tt.wantWarning) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected warning containing %q, got %v", tt.wantWarning, fit.Warnings)
				}
			}
		})
	}
}

func TestFindHardwareTDP(t *testing.T) {
	cat := mustLoadCatalog(t)
	// test-gpu has tdp_watts: 300
	if tdp := cat.FindHardwareTDP(HardwareInfo{GPUArch: "TestArch"}); tdp != 300 {
		t.Errorf("FindHardwareTDP(TestArch) = %d, want 300", tdp)
	}
	// Unknown arch → 0
	if tdp := cat.FindHardwareTDP(HardwareInfo{GPUArch: "Unknown"}); tdp != 0 {
		t.Errorf("FindHardwareTDP(Unknown) = %d, want 0", tdp)
	}
}

// --- P0 Feature A: L2c Golden Config injection tests ---

func TestResolveWithGoldenConfig(t *testing.T) {
	cat := mustLoadCatalog(t)
	hw := HardwareInfo{GPUArch: "TestArch", CPUArch: "x86_64", GPUVRAMMiB: 8192}

	goldenFn := func(hardware, engine, model string) map[string]any {
		// Only return golden config for the expected triple
		if hardware == "TestArch" && engine == "testengine" && model == "test-model-8b" {
			return map[string]any{
				"max_batch_size": 64,    // override L0 engine default (32) and variant default (16)
				"golden_param":   "yes", // new key only in L2c
			}
		}
		return nil
	}

	resolved, err := cat.Resolve(hw, "test-model-8b", "testengine", nil, WithGoldenConfig(goldenFn))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// L2c should override L0 defaults
	if resolved.Config["max_batch_size"] != 64 {
		t.Errorf("Config[max_batch_size] = %v, want 64 (L2c golden)", resolved.Config["max_batch_size"])
	}
	if resolved.Provenance["max_batch_size"] != "L2c" {
		t.Errorf("Provenance[max_batch_size] = %q, want L2c", resolved.Provenance["max_batch_size"])
	}

	// New L2c key should be present
	if resolved.Config["golden_param"] != "yes" {
		t.Errorf("Config[golden_param] = %v, want yes", resolved.Config["golden_param"])
	}
	if resolved.Provenance["golden_param"] != "L2c" {
		t.Errorf("Provenance[golden_param] = %q, want L2c", resolved.Provenance["golden_param"])
	}

	// L0 keys not overridden by L2c should remain at L0
	if resolved.Config["port"] != 8000 {
		t.Errorf("Config[port] = %v, want 8000 (L0 engine default)", resolved.Config["port"])
	}
	if resolved.Provenance["port"] != "L0" {
		t.Errorf("Provenance[port] = %q, want L0", resolved.Provenance["port"])
	}
}

func TestResolveGoldenConfigOverriddenByUser(t *testing.T) {
	cat := mustLoadCatalog(t)
	hw := HardwareInfo{GPUArch: "TestArch", CPUArch: "x86_64", GPUVRAMMiB: 8192}

	goldenFn := func(hardware, engine, model string) map[string]any {
		return map[string]any{"max_batch_size": 64}
	}
	userOverrides := map[string]any{"max_batch_size": 128}

	resolved, err := cat.Resolve(hw, "test-model-8b", "testengine", userOverrides, WithGoldenConfig(goldenFn))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// L1 (user) must override L2c (golden)
	if resolved.Config["max_batch_size"] != 128 {
		t.Errorf("Config[max_batch_size] = %v, want 128 (L1 user override wins over L2c)", resolved.Config["max_batch_size"])
	}
	if resolved.Provenance["max_batch_size"] != "L1" {
		t.Errorf("Provenance[max_batch_size] = %q, want L1", resolved.Provenance["max_batch_size"])
	}
}

func TestResolveGoldenConfigNil(t *testing.T) {
	cat := mustLoadCatalog(t)
	hw := HardwareInfo{GPUArch: "TestArch", CPUArch: "x86_64", GPUVRAMMiB: 8192}

	// GoldenConfigFunc returns nil — should not affect resolution
	goldenFn := func(hardware, engine, model string) map[string]any {
		return nil
	}

	resolved, err := cat.Resolve(hw, "test-model-8b", "testengine", nil, WithGoldenConfig(goldenFn))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Should still have L0 defaults
	if resolved.Config["max_batch_size"] != 16 {
		t.Errorf("Config[max_batch_size] = %v, want 16 (variant L0)", resolved.Config["max_batch_size"])
	}
	if resolved.Provenance["max_batch_size"] != "L0" {
		t.Errorf("Provenance[max_batch_size] = %q, want L0", resolved.Provenance["max_batch_size"])
	}
}

// --- P0 Feature B: Time constraint filtering tests ---

func TestInferEngineWithMaxColdStart(t *testing.T) {
	cat := mustLoadCatalog(t)
	hw := HardwareInfo{GPUArch: "TestArch", CPUArch: "x86_64", GPUVRAMMiB: 8192}

	t.Run("cold start within limit picks testengine", func(t *testing.T) {
		// testengine cold_start_s: [10, 30], limit=60 → testengine OK
		engine, err := cat.InferEngineType("test-model-8b", hw, WithMaxColdStart(60))
		if err != nil {
			t.Fatalf("InferEngineType: %v", err)
		}
		if engine != "testengine" {
			t.Errorf("engine = %q, want testengine (cold start 30 <= 60)", engine)
		}
	})

	t.Run("cold start exceeds limit falls back to universal", func(t *testing.T) {
		// testengine cold_start_s max=30, universal cold_start_s max=10
		// limit=15 → testengine filtered, universal passes
		engine, err := cat.InferEngineType("test-model-8b", hw, WithMaxColdStart(15))
		if err != nil {
			t.Fatalf("InferEngineType: %v", err)
		}
		if engine != "universal" {
			t.Errorf("engine = %q, want universal (testengine cold_start 30 > 15)", engine)
		}
	})

	t.Run("all exceed limit degrades gracefully", func(t *testing.T) {
		// limit=1 → both testengine(30) and universal(10) exceed → graceful degradation keeps all
		engine, err := cat.InferEngineType("test-model-8b", hw, WithMaxColdStart(1))
		if err != nil {
			t.Fatalf("InferEngineType: %v", err)
		}
		// Should still pick testengine (best amplifier for TestArch, graceful degradation)
		if engine != "testengine" {
			t.Errorf("engine = %q, want testengine (graceful degradation when all filtered)", engine)
		}
	})

	t.Run("zero limit means no constraint", func(t *testing.T) {
		engine, err := cat.InferEngineType("test-model-8b", hw, WithMaxColdStart(0))
		if err != nil {
			t.Fatalf("InferEngineType: %v", err)
		}
		if engine != "testengine" {
			t.Errorf("engine = %q, want testengine (0 = no constraint)", engine)
		}
	})
}

func TestFindEngineByName(t *testing.T) {
	cat := mustLoadCatalog(t)

	tests := []struct {
		name     string
		query    string
		hw       HardwareInfo
		wantName string // expected engine metadata.name, "" if nil
	}{
		{
			name:     "exact metadata.name",
			query:    "testengine-1.0",
			hw:       HardwareInfo{},
			wantName: "testengine-1.0",
		},
		{
			name:     "by type with hw match",
			query:    "testengine",
			hw:       HardwareInfo{GPUArch: "TestArch"},
			wantName: "testengine-1.0",
		},
		{
			name:     "by type no hw match returns first",
			query:    "universal",
			hw:       HardwareInfo{GPUArch: "TestArch"},
			wantName: "universal-engine",
		},
		{
			name:     "by image substring",
			query:    "test/engine",
			hw:       HardwareInfo{},
			wantName: "testengine-1.0",
		},
		{
			name:     "not found",
			query:    "nonexistent",
			hw:       HardwareInfo{},
			wantName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ea := cat.FindEngineByName(tt.query, tt.hw)
			if tt.wantName == "" {
				if ea != nil {
					t.Errorf("expected nil, got %q", ea.Metadata.Name)
				}
			} else {
				if ea == nil {
					t.Fatalf("expected %q, got nil", tt.wantName)
				}
				if ea.Metadata.Name != tt.wantName {
					t.Errorf("Name = %q, want %q", ea.Metadata.Name, tt.wantName)
				}
			}
		})
	}
}

func TestGPUCountFiltering(t *testing.T) {
	// Build a catalog with a multi-GPU variant (gpu_count_min: 2) and a single-GPU fallback.
	cat := &Catalog{
		EngineAssets: []EngineAsset{
			{
				Metadata:  EngineMetadata{Name: "eng-multi", Type: "vllm", Version: "1.0"},
				Hardware:  EngineHardware{GPUArch: "TestArch"},
				Amplifier: EngineAmplifier{PerformanceMultiplier: 2.0},
			},
			{
				Metadata:  EngineMetadata{Name: "eng-single", Type: "single", Version: "1.0"},
				Hardware:  EngineHardware{GPUArch: "TestArch"},
				Amplifier: EngineAmplifier{PerformanceMultiplier: 1.0},
			},
		},
		ModelAssets: []ModelAsset{{
			Metadata: ModelMetadata{Name: "test-multi-gpu"},
			Variants: []ModelVariant{
				{
					Name:     "multi-gpu-tp2",
					Hardware: ModelVariantHardware{GPUArch: "TestArch", VRAMMinMiB: 4096, GPUCountMin: 2},
					Engine:   "eng-multi",
					Format:   "safetensors",
					DefaultConfig: map[string]any{
						"tensor_parallel_size": 2,
						"dtype":                "bfloat16",
					},
				},
				{
					Name:     "single-gpu",
					Hardware: ModelVariantHardware{GPUArch: "TestArch", VRAMMinMiB: 4096},
					Engine:   "eng-single",
					Format:   "safetensors",
					DefaultConfig: map[string]any{
						"dtype": "float16",
					},
				},
			},
		}},
		PartitionStrategies: []PartitionStrategy{{
			Metadata: PartitionMetadata{Name: "test-multi-slot"},
			Target: PartitionTarget{
				HardwareProfile: "*",
				WorkloadPattern: "single_model",
			},
			Slots: []PartitionSlotDef{
				{Name: "primary", GPU: SlotGPU{Count: 2}},
				{Name: "secondary", GPU: SlotGPU{Count: 1}},
			},
		}},
	}

	t.Run("2 GPUs selects multi-GPU variant", func(t *testing.T) {
		hw := HardwareInfo{GPUArch: "TestArch", GPUVRAMMiB: 8192, GPUCount: 2}
		engine, err := cat.InferEngineType("test-multi-gpu", hw)
		if err != nil {
			t.Fatalf("InferEngineType: %v", err)
		}
		if engine != "eng-multi" {
			t.Errorf("engine = %q, want eng-multi", engine)
		}
		resolved, err := cat.Resolve(hw, "test-multi-gpu", engine, nil)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolved.Config["tensor_parallel_size"] != 2 {
			t.Errorf("tensor_parallel_size = %v, want 2", resolved.Config["tensor_parallel_size"])
		}
	})

	t.Run("1 GPU skips multi-GPU variant", func(t *testing.T) {
		hw := HardwareInfo{GPUArch: "TestArch", GPUVRAMMiB: 8192, GPUCount: 1}
		engine, err := cat.InferEngineType("test-multi-gpu", hw)
		if err != nil {
			t.Fatalf("InferEngineType: %v", err)
		}
		if engine != "eng-single" {
			t.Errorf("engine = %q, want eng-single (multi-GPU variant should be filtered)", engine)
		}
		resolved, err := cat.Resolve(hw, "test-multi-gpu", engine, nil)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if _, has := resolved.Config["tensor_parallel_size"]; has {
			t.Errorf("single-GPU variant should not have tensor_parallel_size")
		}
	})

	t.Run("GPUCount=0 (unknown) does not filter", func(t *testing.T) {
		hw := HardwareInfo{GPUArch: "TestArch", GPUVRAMMiB: 8192, GPUCount: 0}
		engine, err := cat.InferEngineType("test-multi-gpu", hw)
		if err != nil {
			t.Fatalf("InferEngineType: %v", err)
		}
		// eng-multi has higher multiplier; GPUCount=0 means unknown, should not filter
		if engine != "eng-multi" {
			t.Errorf("engine = %q, want eng-multi (GPUCount=0 should not filter)", engine)
		}
	})

	t.Run("slot GPU count constrains variant selection", func(t *testing.T) {
		hw := HardwareInfo{GPUArch: "TestArch", GPUVRAMMiB: 8192, GPUCount: 2}
		resolved, err := cat.Resolve(hw, "test-multi-gpu", "", map[string]any{
			"partition": "test-multi-slot",
			"slot":      "secondary",
		})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolved.Engine != "eng-single" {
			t.Errorf("engine = %q, want eng-single for 1-GPU slot", resolved.Engine)
		}
		if resolved.Partition == nil || resolved.Partition.GPUCount != 1 {
			t.Fatalf("partition GPUCount = %+v, want 1-GPU slot", resolved.Partition)
		}
		if _, has := resolved.Config["tensor_parallel_size"]; has {
			t.Errorf("single-GPU slot should not select TP=2 variant")
		}
	})
}

func TestCheckFitTPExceedsGPUCount(t *testing.T) {
	t.Run("TP=2 with 1 GPU rejects", func(t *testing.T) {
		resolved := &ResolvedConfig{
			Config: map[string]any{"tensor_parallel_size": 2},
		}
		hw := HardwareInfo{GPUCount: 1, GPUVRAMMiB: 24576}
		fit := CheckFit(resolved, hw)
		if fit.Fit {
			t.Fatal("expected Fit=false for TP=2 on 1 GPU")
		}
		if !strings.Contains(fit.Reason, "tensor_parallel_size=2") || !strings.Contains(fit.Reason, "GPU count=1") {
			t.Errorf("unexpected reason: %q", fit.Reason)
		}
	})

	t.Run("TP=2 with 2 GPUs passes", func(t *testing.T) {
		resolved := &ResolvedConfig{
			Config: map[string]any{"tensor_parallel_size": 2},
		}
		hw := HardwareInfo{GPUCount: 2, GPUVRAMMiB: 24576}
		fit := CheckFit(resolved, hw)
		if !fit.Fit {
			t.Fatalf("expected Fit=true for TP=2 on 2 GPUs, got Reason=%q", fit.Reason)
		}
	})

	t.Run("TP=2 with GPUCount=0 (unknown) passes", func(t *testing.T) {
		resolved := &ResolvedConfig{
			Config: map[string]any{"tensor_parallel_size": 2},
		}
		hw := HardwareInfo{GPUCount: 0, GPUVRAMMiB: 24576}
		fit := CheckFit(resolved, hw)
		if !fit.Fit {
			t.Fatalf("expected Fit=true when GPUCount unknown, got Reason=%q", fit.Reason)
		}
	})

	t.Run("TP=2 with 2 GPUs but 1-GPU slot rejects", func(t *testing.T) {
		resolved := &ResolvedConfig{
			Config:    map[string]any{"tensor_parallel_size": 2},
			Partition: &PartitionSlot{Name: "secondary", GPUCount: 1},
		}
		hw := HardwareInfo{GPUCount: 2, GPUVRAMMiB: 24576}
		fit := CheckFit(resolved, hw)
		if fit.Fit {
			t.Fatal("expected Fit=false for TP=2 on 1-GPU slot")
		}
		if !strings.Contains(fit.Reason, "GPU count=1") {
			t.Errorf("unexpected reason: %q", fit.Reason)
		}
	})

	t.Run("no TP config passes", func(t *testing.T) {
		resolved := &ResolvedConfig{
			Config: map[string]any{"dtype": "float16"},
		}
		hw := HardwareInfo{GPUCount: 1, GPUVRAMMiB: 8192}
		fit := CheckFit(resolved, hw)
		if !fit.Fit {
			t.Fatalf("expected Fit=true without TP config, got Reason=%q", fit.Reason)
		}
	})
}
