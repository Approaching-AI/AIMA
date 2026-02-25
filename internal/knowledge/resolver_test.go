package knowledge

import (
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
