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
    compute_capability: "8.0"
    cuda_cores: 4096
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
