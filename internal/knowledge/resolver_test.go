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
	// Slot override is in config
	if resolved.Config["slot"] != "secondary" {
		t.Errorf("Config[slot] = %v, want secondary", resolved.Config["slot"])
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

func TestBuildSyntheticModelAsset(t *testing.T) {
	tests := []struct {
		name          string
		format        string
		modelType     string
		wantEngine    string
		wantType      string
		wantVariants  int  // non-llamacpp engines get a llamacpp fallback variant
	}{
		{"safetensors→vllm", "safetensors", "llm", "vllm", "llm", 2},
		{"gguf→llamacpp", "gguf", "llm", "llamacpp", "llm", 1},
		{"empty type defaults to llm", "gguf", "", "llamacpp", "llm", 1},
		{"unknown format→llamacpp", "awq", "llm", "llamacpp", "llm", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ma := BuildSyntheticModelAsset("test-model", tt.modelType, "testfam", "8B", tt.format)
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
			// Non-llamacpp engines should have a llamacpp fallback variant
			if tt.wantVariants == 2 {
				fb := ma.Variants[1]
				if fb.Engine != "llamacpp" {
					t.Errorf("fallback Engine = %q, want llamacpp", fb.Engine)
				}
			}
		})
	}
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
