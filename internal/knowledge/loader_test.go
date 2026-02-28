package knowledge

import (
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"hardware/gpu-test.yaml": &fstest.MapFile{Data: []byte(`kind: hardware_profile
metadata:
  name: test-gpu
  description: "Test GPU"
hardware:
  gpu:
    arch: TestArch
    vram_mib: 8192
    compute_id: "8.0"
    compute_units: 4096
  cpu:
    arch: x86_64
    cores: 8
    freq_ghz: 3.0
  ram:
    total_mib: 32768
    bandwidth_gbps: 50
  unified_memory: false
constraints:
  tdp_watts: 300
  power_modes: [300]
  cooling: active
partition:
  gpu_tools: [hami]
  cpu_tools: [k3s_cgroups]
`)},
		"engines/testengine.yaml": &fstest.MapFile{Data: []byte(`kind: engine_asset
metadata:
  name: testengine-1.0
  type: testengine
  version: "1.0"
image:
  name: test/engine
  tag: "v1"
  size_approx_mb: 1000
  platforms: [linux/amd64]
  registries:
    - docker.io/test/engine
hardware:
  gpu_arch: TestArch
  vram_min_mib: 2048
startup:
  command: ["serve", "--model", "{{.ModelPath}}"]
  default_args:
    port: 8000
    max_batch_size: 32
  health_check:
    path: /health
    timeout_s: 120
api:
  protocol: openai
  base_path: /v1
amplifier:
  features: [flash_attention]
  performance_gain: "2x"
  resource_expansion:
    cpu_offload: false
    ssd_offload: false
    npu_offload: false
partition_hints:
  min_gpu_memory_mib: 2048
  recommended_gpu_cores_percent: 50
time_constraints:
  cold_start_s: [10, 30]
  model_switch_s: [10, 30]
power_constraints:
  typical_draw_watts: [50, 100]
`)},
		"engines/universal.yaml": &fstest.MapFile{Data: []byte(`kind: engine_asset
metadata:
  name: universal-engine
  type: universal
  version: "1.0"
image:
  name: test/universal
  tag: "v1"
  size_approx_mb: 500
  platforms: [linux/amd64, linux/arm64]
  registries:
    - docker.io/test/universal
hardware:
  gpu_arch: "*"
  vram_min_mib: 0
startup:
  command: ["uni-serve", "--model", "{{.ModelPath}}"]
  default_args:
    port: 8080
    ctx_size: 4096
  health_check:
    path: /health
    timeout_s: 60
api:
  protocol: openai
  base_path: /v1
amplifier:
  features: []
  performance_gain: "baseline"
  resource_expansion:
    cpu_offload: true
    ssd_offload: false
    npu_offload: false
partition_hints:
  min_gpu_memory_mib: 0
  recommended_gpu_cores_percent: 30
time_constraints:
  cold_start_s: [3, 10]
  model_switch_s: [3, 10]
power_constraints:
  typical_draw_watts: [20, 60]
`)},
		"models/test-model.yaml": &fstest.MapFile{Data: []byte(`kind: model_asset
metadata:
  name: test-model-8b
  type: llm
  family: testfam
  parameter_count: "8B"
storage:
  formats: [safetensors, gguf]
  default_path_pattern: "{{.DataDir}}/models/{{.Name}}"
  sources:
    - type: huggingface
      repo: test/test-model-8b
variants:
  - name: test-model-8b-testarch-testengine
    hardware:
      gpu_arch: TestArch
      vram_min_mib: 4096
    engine: testengine
    format: safetensors
    default_config:
      max_batch_size: 16
      dtype: float16
    expected_performance:
      tokens_per_second: [10, 20]
      latency_first_token_ms: [30, 80]
  - name: test-model-8b-universal
    hardware:
      gpu_arch: "*"
      vram_min_mib: 0
    engine: universal
    format: gguf
    default_config:
      ctx_size: 2048
    expected_performance:
      tokens_per_second: [5, 10]
      latency_first_token_ms: [50, 200]
`)},
		"partitions/default.yaml": &fstest.MapFile{Data: []byte(`kind: partition_strategy
metadata:
  name: single-default
  description: "Single model default"
target:
  hardware_profile: "*"
  workload_pattern: single_model
slots:
  - name: primary
    gpu:
      memory_mib: 0
      cores_percent: 90
    cpu:
      cores: 0
    ram:
      mib: 0
  - name: system_reserved
    gpu:
      memory_mib: 0
      cores_percent: 10
    cpu:
      cores: 2
    ram:
      mib: 4096
`)},
		"partitions/specific.yaml": &fstest.MapFile{Data: []byte(`kind: partition_strategy
metadata:
  name: test-gpu-dual
  description: "Test GPU dual model"
target:
  hardware_profile: test-gpu
  workload_pattern: dual_model
slots:
  - name: primary
    gpu:
      memory_mib: 5120
      cores_percent: 60
    cpu:
      cores: 4
    ram:
      mib: 16384
  - name: secondary
    gpu:
      memory_mib: 2048
      cores_percent: 30
    cpu:
      cores: 2
    ram:
      mib: 8192
  - name: system_reserved
    gpu:
      memory_mib: 1024
      cores_percent: 10
    cpu:
      cores: 2
    ram:
      mib: 8192
`)},
	}
}

func mustLoadCatalog(t *testing.T) *Catalog {
	t.Helper()
	cat, err := LoadCatalog(testFS())
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	return cat
}

func TestLoadCatalog(t *testing.T) {
	cat := mustLoadCatalog(t)

	t.Run("hardware profiles loaded", func(t *testing.T) {
		if len(cat.HardwareProfiles) != 1 {
			t.Fatalf("HardwareProfiles count = %d, want 1", len(cat.HardwareProfiles))
		}
		hp := cat.HardwareProfiles[0]
		if hp.Metadata.Name != "test-gpu" {
			t.Errorf("name = %q, want %q", hp.Metadata.Name, "test-gpu")
		}
		if hp.Hardware.GPU.Arch != "TestArch" {
			t.Errorf("gpu arch = %q, want %q", hp.Hardware.GPU.Arch, "TestArch")
		}
		if hp.Hardware.GPU.VRAMMiB != 8192 {
			t.Errorf("vram = %d, want 8192", hp.Hardware.GPU.VRAMMiB)
		}
		if hp.Hardware.CPU.Arch != "x86_64" {
			t.Errorf("cpu arch = %q, want %q", hp.Hardware.CPU.Arch, "x86_64")
		}
	})

	t.Run("engine assets loaded", func(t *testing.T) {
		if len(cat.EngineAssets) != 2 {
			t.Fatalf("EngineAssets count = %d, want 2", len(cat.EngineAssets))
		}
	})

	t.Run("model assets loaded", func(t *testing.T) {
		if len(cat.ModelAssets) != 1 {
			t.Fatalf("ModelAssets count = %d, want 1", len(cat.ModelAssets))
		}
		ma := cat.ModelAssets[0]
		if len(ma.Variants) != 2 {
			t.Fatalf("Variants count = %d, want 2", len(ma.Variants))
		}
	})

	t.Run("partition strategies loaded", func(t *testing.T) {
		if len(cat.PartitionStrategies) != 2 {
			t.Fatalf("PartitionStrategies count = %d, want 2", len(cat.PartitionStrategies))
		}
	})
}

func TestLoadCatalogFromEmbedFS(t *testing.T) {
	// Test with the real embedded catalog
	cat, err := LoadCatalog(catalogFS())
	if err != nil {
		t.Fatalf("LoadCatalog(real FS): %v", err)
	}
	if len(cat.HardwareProfiles) == 0 {
		t.Error("expected at least one hardware profile from real catalog")
	}
	if len(cat.EngineAssets) == 0 {
		t.Error("expected at least one engine asset from real catalog")
	}
	if len(cat.ModelAssets) == 0 {
		t.Error("expected at least one model asset from real catalog")
	}
	if len(cat.PartitionStrategies) == 0 {
		t.Error("expected at least one partition strategy from real catalog")
	}
}

func TestLoadCatalogInvalidYAML(t *testing.T) {
	fs := fstest.MapFS{
		"hardware/bad.yaml": &fstest.MapFile{Data: []byte("not: valid: yaml: [")},
	}
	_, err := LoadCatalog(fs)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadCatalogUnknownKind(t *testing.T) {
	fs := fstest.MapFS{
		"hardware/unknown.yaml": &fstest.MapFile{Data: []byte(`kind: unknown_thing
metadata:
  name: test
`)},
	}
	// Unknown kinds should be silently skipped, not error
	cat, err := LoadCatalog(fs)
	if err != nil {
		t.Fatalf("LoadCatalog with unknown kind: %v", err)
	}
	if len(cat.HardwareProfiles) != 0 {
		t.Error("expected 0 hardware profiles for unknown kind")
	}
}

func TestMergeCatalogOverride(t *testing.T) {
	base := mustLoadCatalog(t)
	if base.HardwareProfiles[0].Hardware.GPU.VRAMMiB != 8192 {
		t.Fatal("precondition: base VRAM should be 8192")
	}

	// Overlay with same name but different VRAM
	overlay := &Catalog{
		HardwareProfiles: []HardwareProfile{{
			Kind:     "hardware_profile",
			Metadata: HardwareMetadata{Name: "test-gpu"},
			Hardware: HardwareSpec{GPU: GPUSpec{Arch: "TestArch", VRAMMiB: 16384}},
		}},
	}

	merged := MergeCatalog(base, overlay)
	if len(merged.HardwareProfiles) != 1 {
		t.Fatalf("expected 1 hardware profile, got %d", len(merged.HardwareProfiles))
	}
	if merged.HardwareProfiles[0].Hardware.GPU.VRAMMiB != 16384 {
		t.Errorf("expected overlay VRAM 16384, got %d", merged.HardwareProfiles[0].Hardware.GPU.VRAMMiB)
	}
}

func TestMergeCatalogAppend(t *testing.T) {
	base := mustLoadCatalog(t)
	baseEngineCount := len(base.EngineAssets)

	overlay := &Catalog{
		EngineAssets: []EngineAsset{{
			Metadata: EngineMetadata{Name: "new-engine-1.0", Type: "new", Version: "1.0"},
		}},
	}

	merged := MergeCatalog(base, overlay)
	if len(merged.EngineAssets) != baseEngineCount+1 {
		t.Fatalf("expected %d engine assets, got %d", baseEngineCount+1, len(merged.EngineAssets))
	}
	last := merged.EngineAssets[len(merged.EngineAssets)-1]
	if last.Metadata.Name != "new-engine-1.0" {
		t.Errorf("expected appended engine name %q, got %q", "new-engine-1.0", last.Metadata.Name)
	}
}

func TestMergeCatalogEmpty(t *testing.T) {
	base := mustLoadCatalog(t)
	origHP := len(base.HardwareProfiles)
	origEA := len(base.EngineAssets)
	origMA := len(base.ModelAssets)

	merged := MergeCatalog(base, &Catalog{})
	if len(merged.HardwareProfiles) != origHP {
		t.Errorf("HardwareProfiles changed: %d → %d", origHP, len(merged.HardwareProfiles))
	}
	if len(merged.EngineAssets) != origEA {
		t.Errorf("EngineAssets changed: %d → %d", origEA, len(merged.EngineAssets))
	}
	if len(merged.ModelAssets) != origMA {
		t.Errorf("ModelAssets changed: %d → %d", origMA, len(merged.ModelAssets))
	}
}

func TestLoadCatalogLenient(t *testing.T) {
	fs := fstest.MapFS{
		"hardware/good.yaml": &fstest.MapFile{Data: []byte(`kind: hardware_profile
metadata:
  name: good-hw
  description: "Good"
hardware:
  gpu:
    arch: Test
    vram_mib: 1024
  cpu:
    arch: x86_64
    cores: 4
    freq_ghz: 3.0
  ram:
    total_mib: 8192
    bandwidth_gbps: 50
constraints:
  tdp_watts: 100
  power_modes: [100]
  cooling: active
partition:
  gpu_tools: []
  cpu_tools: []
`)},
		"hardware/bad.yaml": &fstest.MapFile{Data: []byte("not: valid: yaml: [")},
	}
	cat, warnings := LoadCatalogLenient(fs)
	if len(cat.HardwareProfiles) != 1 {
		t.Fatalf("expected 1 good profile, got %d", len(cat.HardwareProfiles))
	}
	if cat.HardwareProfiles[0].Metadata.Name != "good-hw" {
		t.Errorf("expected good-hw, got %q", cat.HardwareProfiles[0].Metadata.Name)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning for bad.yaml, got %d", len(warnings))
	}
}

func TestComputeDigests(t *testing.T) {
	fs := testFS()
	digests := ComputeDigests(fs)

	if _, ok := digests["test-gpu"]; !ok {
		t.Error("expected digest for test-gpu")
	}
	if _, ok := digests["testengine-1.0"]; !ok {
		t.Error("expected digest for testengine-1.0")
	}
	if _, ok := digests["test-model-8b"]; !ok {
		t.Error("expected digest for test-model-8b")
	}
	// Digest should be sha256: prefixed
	for name, d := range digests {
		if len(d) < 10 || d[:7] != "sha256:" {
			t.Errorf("digest for %s doesn't have sha256: prefix: %s", name, d)
		}
	}
}

func TestStalenessDetection(t *testing.T) {
	base := mustLoadCatalog(t)
	factoryDigests := ComputeDigests(testFS())

	t.Run("matching digest = no warning", func(t *testing.T) {
		overlayFS := fstest.MapFS{
			"hardware/test.yaml": &fstest.MapFile{Data: []byte(`_base_digest: ` + factoryDigests["test-gpu"] + `
kind: hardware_profile
metadata:
  name: test-gpu
  description: "Overlay"
hardware:
  gpu:
    arch: TestArch
    vram_mib: 32768
  cpu:
    arch: x86_64
    cores: 16
    freq_ghz: 4.0
  ram:
    total_mib: 65536
    bandwidth_gbps: 100
constraints:
  tdp_watts: 200
  power_modes: [200]
  cooling: active
partition:
  gpu_tools: []
  cpu_tools: []
`)},
		}
		overlayCat, _ := LoadCatalogLenient(overlayFS)
		baseCopy := mustLoadCatalog(t)
		_, warnings := MergeCatalogWithDigests(baseCopy, overlayCat, factoryDigests, overlayFS)
		if len(warnings) != 0 {
			t.Errorf("expected 0 warnings for matching digest, got %d: %v", len(warnings), warnings)
		}
	})

	t.Run("mismatched digest = warning", func(t *testing.T) {
		overlayFS := fstest.MapFS{
			"hardware/test.yaml": &fstest.MapFile{Data: []byte(`_base_digest: sha256:0000000000000000000000000000000000000000000000000000000000000000
kind: hardware_profile
metadata:
  name: test-gpu
  description: "Stale overlay"
hardware:
  gpu:
    arch: TestArch
    vram_mib: 32768
  cpu:
    arch: x86_64
    cores: 16
    freq_ghz: 4.0
  ram:
    total_mib: 65536
    bandwidth_gbps: 100
constraints:
  tdp_watts: 200
  power_modes: [200]
  cooling: active
partition:
  gpu_tools: []
  cpu_tools: []
`)},
		}
		overlayCat, _ := LoadCatalogLenient(overlayFS)
		baseCopy := mustLoadCatalog(t)
		_, warnings := MergeCatalogWithDigests(baseCopy, overlayCat, factoryDigests, overlayFS)
		if len(warnings) != 1 {
			t.Fatalf("expected 1 staleness warning, got %d: %v", len(warnings), warnings)
		}
	})

	t.Run("no base_digest = no warning", func(t *testing.T) {
		overlayFS := fstest.MapFS{
			"hardware/test.yaml": &fstest.MapFile{Data: []byte(`kind: hardware_profile
metadata:
  name: test-gpu
  description: "No digest"
hardware:
  gpu:
    arch: TestArch
    vram_mib: 32768
  cpu:
    arch: x86_64
    cores: 16
    freq_ghz: 4.0
  ram:
    total_mib: 65536
    bandwidth_gbps: 100
constraints:
  tdp_watts: 200
  power_modes: [200]
  cooling: active
partition:
  gpu_tools: []
  cpu_tools: []
`)},
		}
		overlayCat, _ := LoadCatalogLenient(overlayFS)
		baseCopy := mustLoadCatalog(t)
		_, warnings := MergeCatalogWithDigests(baseCopy, overlayCat, factoryDigests, overlayFS)
		if len(warnings) != 0 {
			t.Errorf("expected 0 warnings for no digest, got %d: %v", len(warnings), warnings)
		}
	})

	_ = base // used for reference
}

func TestKindToDir(t *testing.T) {
	tests := []struct{ kind, dir string }{
		{"engine_asset", "engines"},
		{"model_asset", "models"},
		{"hardware_profile", "hardware"},
		{"partition_strategy", "partitions"},
		{"stack_component", "stack"},
		{"unknown", ""},
	}
	for _, tt := range tests {
		if got := KindToDir(tt.kind); got != tt.dir {
			t.Errorf("KindToDir(%q) = %q, want %q", tt.kind, got, tt.dir)
		}
	}
}
