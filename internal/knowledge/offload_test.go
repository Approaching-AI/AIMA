package knowledge

import "testing"

// baseOffloadInputs builds a resolved/engine/variant/hw set representing a
// llamacpp GGUF variant that declares "fits any GPU" (vram_min_mib=0) with a
// full-GPU-offload default (n_gpu_layers=999) and a real RAM footprint.
func baseOffloadInputs() (*ResolvedConfig, *EngineAsset, *ModelVariant) {
	resolved := &ResolvedConfig{
		Config:     map[string]any{"n_gpu_layers": 999},
		Provenance: map[string]string{"n_gpu_layers": "L0"},
	}
	engine := &EngineAsset{Amplifier: EngineAmplifier{OffloadConfigKey: "n_gpu_layers"}}
	variant := &ModelVariant{Hardware: ModelVariantHardware{RAMMinMiB: 7168}}
	return resolved, engine, variant
}

func TestCapGPUOffloadDowngradesOnSmallDiscreteGPU(t *testing.T) {
	resolved, engine, variant := baseOffloadInputs()
	hw := HardwareInfo{GPUVRAMMiB: 4096, UnifiedMemory: false}

	capGPUOffloadForVRAM(resolved, engine, variant, hw)

	if got := toFloat64(resolved.Config["n_gpu_layers"]); got != 0 {
		t.Errorf("n_gpu_layers = %v, want 0 (CPU) when model (7168 MiB) exceeds GPU VRAM (4096 MiB)", got)
	}
	if !resolved.OffloadPath {
		t.Error("OffloadPath = false, want true after CPU downgrade")
	}
	if len(resolved.Warnings) != 1 {
		t.Errorf("Warnings = %d, want 1 explaining the CPU fallback", len(resolved.Warnings))
	}
}

func TestCapGPUOffloadLeavesUnifiedMemoryAlone(t *testing.T) {
	resolved, engine, variant := baseOffloadInputs()
	// Unified-memory APU/GB10/Apple: the GPU shares system RAM, so full offload
	// is correct even when the footprint exceeds nominal VRAM.
	hw := HardwareInfo{GPUVRAMMiB: 4096, UnifiedMemory: true}

	capGPUOffloadForVRAM(resolved, engine, variant, hw)

	if got := toFloat64(resolved.Config["n_gpu_layers"]); got != 999 {
		t.Errorf("n_gpu_layers = %v, want 999 (untouched) on unified memory", got)
	}
	if len(resolved.Warnings) != 0 {
		t.Errorf("Warnings = %d, want 0 on unified memory", len(resolved.Warnings))
	}
}

func TestCapGPUOffloadLeavesFittingGPUAlone(t *testing.T) {
	resolved, engine, variant := baseOffloadInputs()
	hw := HardwareInfo{GPUVRAMMiB: 49152, UnifiedMemory: false} // 48 GB card fits 7 GB model

	capGPUOffloadForVRAM(resolved, engine, variant, hw)

	if got := toFloat64(resolved.Config["n_gpu_layers"]); got != 999 {
		t.Errorf("n_gpu_layers = %v, want 999 (untouched) when model fits VRAM", got)
	}
}

func TestCapGPUOffloadRespectsUserOverride(t *testing.T) {
	resolved, engine, variant := baseOffloadInputs()
	resolved.Config["n_gpu_layers"] = 50
	resolved.Provenance["n_gpu_layers"] = "L1" // user set it explicitly
	hw := HardwareInfo{GPUVRAMMiB: 4096, UnifiedMemory: false}

	capGPUOffloadForVRAM(resolved, engine, variant, hw)

	if got := toFloat64(resolved.Config["n_gpu_layers"]); got != 50 {
		t.Errorf("n_gpu_layers = %v, want 50 (user override respected)", got)
	}
}

func TestCapGPUOffloadNoopWithoutOffloadKey(t *testing.T) {
	resolved, engine, variant := baseOffloadInputs()
	engine.Amplifier.OffloadConfigKey = "" // engine declares no offload knob
	hw := HardwareInfo{GPUVRAMMiB: 4096, UnifiedMemory: false}

	capGPUOffloadForVRAM(resolved, engine, variant, hw)

	if got := toFloat64(resolved.Config["n_gpu_layers"]); got != 999 {
		t.Errorf("n_gpu_layers = %v, want 999 (untouched) when no offload_config_key", got)
	}
}

func TestCheckFitSurfacesResolvedWarnings(t *testing.T) {
	resolved := &ResolvedConfig{
		Config:   map[string]any{},
		Warnings: []string{"model needs ~7168 MiB but this GPU has 4096 MiB VRAM"},
	}

	fit := CheckFit(resolved, HardwareInfo{})

	found := false
	for _, w := range fit.Warnings {
		if w == resolved.Warnings[0] {
			found = true
		}
	}
	if !found {
		t.Errorf("CheckFit did not surface resolve-time warning; got %v", fit.Warnings)
	}
}
