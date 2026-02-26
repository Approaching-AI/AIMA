package knowledge

import (
	"strings"
	"testing"
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
