package knowledge

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestContainerPublishHostRejectsNonPrivateOrNonLiteralAddresses(t *testing.T) {
	for _, host := range []string{"model.internal", "8.8.8.8"} {
		t.Run(host, func(t *testing.T) {
			cat := &Catalog{}
			err := cat.parseAsset([]byte(fmt.Sprintf(`kind: hardware_profile
metadata:
  name: invalid-publish-host
container:
  publish_host: %q
`, host)), "hardware/invalid.yaml")
			if err == nil || !strings.Contains(err.Error(), "publish_host") {
				t.Fatalf("parseAsset() error = %v, want publish_host validation", err)
			}
		})
	}
}

func TestContainerPublishHostAcceptsExplicitLoopbackPrivateAndUnspecifiedIPs(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "192.168.121.157", "0.0.0.0", "::1"} {
		t.Run(host, func(t *testing.T) {
			cat := &Catalog{}
			err := cat.parseAsset([]byte(fmt.Sprintf(`kind: hardware_profile
metadata:
  name: valid-publish-host
container:
  publish_host: %q
`, host)), "hardware/valid.yaml")
			if err != nil {
				t.Fatalf("parseAsset() error = %v", err)
			}
		})
	}
}

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
  compatibility_probe: transformers_autoconfig
  default_args:
    port: 8000
    max_batch_size: 32
  health_check:
    path: /health
    timeout_s: 120
  warmup:
    enabled: true
    timeout_s: 30
    request_body:
      messages:
        - role: user
          content: Return JSON
      max_tokens: 64
      response_format:
        type: json_object
      chat_template_kwargs:
        enable_thinking: false
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
    compatibility:
      repair_init_commands:
        - python3 -m pip install --no-cache-dir transformers>=5
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
		var engine *EngineAsset
		for i := range cat.EngineAssets {
			if cat.EngineAssets[i].Metadata.Name == "testengine-1.0" {
				engine = &cat.EngineAssets[i]
				break
			}
		}
		if engine == nil {
			t.Fatal("test engine not loaded")
		}
		responseFormat, ok := engine.Startup.Warmup.RequestBody["response_format"].(map[string]any)
		if !ok || responseFormat["type"] != "json_object" {
			t.Fatalf("structured warmup response_format = %#v", engine.Startup.Warmup.RequestBody["response_format"])
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

func TestLoadCatalogExactContextRequestAdapter(t *testing.T) {
	cat, err := LoadCatalog(fstest.MapFS{
		"engines/profiles/native.yaml": &fstest.MapFile{Data: []byte(`kind: engine_profile
metadata:
  name: native
api:
  protocol: openai
  base_path: /v1
  request_adapter:
    kind: exact_context
    path: /v1/chat/completions
    context_config_key: context_tokens
    probe_subcommand: chat-template-probe
    disable_thinking: true
    padding_role: system
    padding_prefix: Ignore transport padding.
    padding_unit: " ·"
    upstream_model: native-model
`)},
		"engines/native.yaml": &fstest.MapFile{Data: []byte(`kind: engine_asset
_profile: native
metadata:
  name: native-test
  type: native-test
  version: v1
`)},
	})
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if len(cat.EngineAssets) != 1 {
		t.Fatalf("EngineAssets = %d, want 1", len(cat.EngineAssets))
	}
	adapter := cat.EngineAssets[0].API.RequestAdapter
	if adapter == nil {
		t.Fatal("request adapter was not inherited from profile")
	}
	if adapter.Kind != "exact_context" || adapter.ContextConfigKey != "context_tokens" {
		t.Fatalf("request adapter = %#v", adapter)
	}
	if adapter.MaxAttempts != 8 {
		t.Fatalf("MaxAttempts = %d, want default 8", adapter.MaxAttempts)
	}
	if adapter.PaddingUnit != " ·" || adapter.UpstreamModel != "native-model" {
		t.Fatalf("request adapter strings = %#v", adapter)
	}

	adapter.PaddingPrefix = "mutated"
	if got := cat.EngineProfiles["native"].API.RequestAdapter.PaddingPrefix; got == "mutated" {
		t.Fatal("engine asset shares request adapter pointer with profile")
	}
}

func TestLoadCatalogRejectsInvalidRequestAdapter(t *testing.T) {
	tests := []struct {
		name    string
		adapter string
		want    string
	}{
		{
			name: "unknown kind",
			adapter: `kind: magic_padding
    path: /v1/chat/completions`,
			want: "unknown request adapter kind",
		},
		{
			name: "missing context key",
			adapter: `kind: exact_context
    path: /v1/chat/completions
    probe_subcommand: chat-template-probe
    padding_role: system
    padding_prefix: Ignore padding.
    padding_unit: " ·"
    upstream_model: native-model`,
			want: "context_config_key",
		},
		{
			name: "invalid attempts",
			adapter: `kind: exact_context
    path: /v1/chat/completions
    context_config_key: context_tokens
    probe_subcommand: chat-template-probe
    padding_role: system
    padding_prefix: Ignore padding.
    padding_unit: " ·"
    upstream_model: native-model
    max_attempts: 33`,
			want: "max_attempts",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := "kind: engine_asset\nmetadata:\n  name: invalid-adapter\n  type: test\n  version: v1\napi:\n  request_adapter:\n    " + test.adapter + "\n"
			_, err := LoadCatalog(fstest.MapFS{
				"engines/invalid.yaml": &fstest.MapFile{Data: []byte(data)},
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadCatalog error = %v, want %q", err, test.want)
			}
		})
	}
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
	if len(cat.DeploymentScenarios) == 0 {
		t.Error("expected at least one deployment scenario from real catalog")
	}
}

func TestQwen36StructuredGB10ProfileRequiresGrammarWarmupWithoutSpeculation(t *testing.T) {
	cat, err := LoadCatalog(catalogFS())
	if err != nil {
		t.Fatalf("LoadCatalog(real FS): %v", err)
	}
	var engine *EngineAsset
	for i := range cat.EngineAssets {
		if cat.EngineAssets[i].Metadata.Name == "qwen36-gb10-structured-bf16" {
			engine = &cat.EngineAssets[i]
			break
		}
	}
	if engine == nil {
		t.Fatal("qwen36 structured GB10 engine is missing")
	}
	for _, arg := range engine.Startup.Command {
		if strings.HasPrefix(arg, "--speculative-") {
			t.Fatalf("structured engine contains speculative argument %q", arg)
		}
	}
	var toolParser string
	for i, arg := range engine.Startup.Command {
		if arg == "--tool-call-parser" && i+1 < len(engine.Startup.Command) {
			toolParser = engine.Startup.Command[i+1]
		}
	}
	if toolParser != "qwen3_coder" {
		t.Fatalf("structured engine tool call parser = %q, want qwen3_coder", toolParser)
	}
	responseFormat, ok := engine.Startup.Warmup.RequestBody["response_format"].(map[string]any)
	if !ok || responseFormat["type"] != "json_object" {
		t.Fatalf("structured engine warmup response_format = %#v", engine.Startup.Warmup.RequestBody["response_format"])
	}

	var foundAlias, foundVariant bool
	for _, model := range cat.ModelAssets {
		if model.Metadata.Name != "qwen3.6-35b-a3b" {
			continue
		}
		for _, alias := range model.Metadata.Aliases {
			if alias == "qwen3.6-35b-a3b-bf16" {
				foundAlias = true
			}
		}
		for _, variant := range model.Variants {
			if variant.Engine == "qwen36-gb10-structured-bf16" {
				foundVariant = true
				if got := variant.DefaultConfig["served_model_name"]; got != "qwen3.6-35b-a3b-bf16" {
					t.Fatalf("structured served_model_name = %#v", got)
				}
			}
		}
	}
	if !foundAlias || !foundVariant {
		t.Fatalf("qwen3.6 structured mapping incomplete: alias=%v variant=%v", foundAlias, foundVariant)
	}
}

func TestWindowsHIPLlamaAvoidsUnicodeCachePruningCrash(t *testing.T) {
	cat, err := LoadCatalog(catalogFS())
	if err != nil {
		t.Fatalf("LoadCatalog(real FS): %v", err)
	}
	var engine *EngineAsset
	for i := range cat.EngineAssets {
		if cat.EngineAssets[i].Metadata.Name == "llamacpp-hip-windows" {
			engine = &cat.EngineAssets[i]
			break
		}
	}
	if engine == nil {
		t.Fatal("llamacpp-hip-windows engine not found in catalog")
	}
	const want = "prune_interval=1h:prune_after=168h:cache_size=0%:cache_size_bytes=0:cache_size_files=100000"
	if got := engine.Startup.Env["AMD_COMGR_CACHE_POLICY"]; got != want {
		t.Fatalf("AMD_COMGR_CACHE_POLICY = %q, want %q", got, want)
	}
}

func TestAMD395LinuxHIPLlamaUsesROCmB9330(t *testing.T) {
	cat, err := LoadCatalog(catalogFS())
	if err != nil {
		t.Fatalf("LoadCatalog(real FS): %v", err)
	}

	var engine *EngineAsset
	for i := range cat.EngineAssets {
		if cat.EngineAssets[i].Metadata.Name == "llamacpp-hip-linux" {
			engine = &cat.EngineAssets[i]
			break
		}
	}
	if engine == nil {
		t.Fatal("llamacpp-hip-linux engine not found in catalog")
	}
	if engine.Metadata.Version != "b9330" {
		t.Fatalf("version = %q, want b9330", engine.Metadata.Version)
	}
	if engine.Source == nil || !engine.Source.Supports("linux/amd64") {
		t.Fatal("llamacpp-hip-linux does not support linux/amd64")
	}
	if engine.Source.Supports("windows/amd64") {
		t.Fatal("llamacpp-hip-linux unexpectedly supports windows/amd64")
	}
	wantURL := "https://github.com/ggml-org/llama.cpp/releases/download/b9330/llama-b9330-bin-ubuntu-rocm-7.2-x64.tar.gz"
	if got := engine.Source.Download["linux/amd64"]; got != wantURL {
		t.Fatalf("download URL = %q, want %q", got, wantURL)
	}

	resolved, err := cat.Resolve(HardwareInfo{
		GPUArch:         "RDNA3.5",
		GPUVRAMMiB:      65536,
		GPUCount:        1,
		UnifiedMemory:   true,
		CPUArch:         "amd64",
		CPUCores:        16,
		RAMTotalMiB:     65536,
		HardwareProfile: "amd-radeon-8060s-x86",
		Platform:        "linux/amd64",
		RuntimeType:     "native",
	}, "qwen3.6-35b-a3b", "llamacpp", map[string]any{})
	if err != nil {
		t.Fatalf("resolve AMD395 Linux config: %v", err)
	}
	if resolved.EngineAssetName != "llamacpp-hip-linux" {
		t.Fatalf("engine asset = %q, want llamacpp-hip-linux", resolved.EngineAssetName)
	}
}

func TestAMD395Qwen36NativeEngineCatalog(t *testing.T) {
	cat, err := LoadCatalog(catalogFS())
	if err != nil {
		t.Fatalf("LoadCatalog(real FS): %v", err)
	}
	engine := cat.FindEngineByName("aima-amd395-qwen36-native", HardwareInfo{
		GPUArch:  "RDNA3.5",
		Platform: "linux/amd64",
	})
	if engine == nil {
		t.Fatal("aima-amd395-qwen36-native engine not found")
	}
	if engine.Metadata.Version != "v1.4.0" || engine.Runtime.Default != "native" {
		t.Fatalf("engine version/runtime = %q/%q", engine.Metadata.Version, engine.Runtime.Default)
	}
	if engine.Source == nil || engine.Source.Binary != "bin/aima-engine" {
		t.Fatalf("engine source = %#v", engine.Source)
	}
	const wantArchive = "aima-engine-native-portable-a15b2774e3ab.tar.zst"
	if got := engine.Source.Download["linux/amd64"]; !strings.HasSuffix(got, "/"+wantArchive) {
		t.Fatalf("download URL = %q, want %s", got, wantArchive)
	}
	const wantSHA = "749a2acb8b8d49b3979e1dbb9785ce3a305bb24129175747fef6330579d2f0f2"
	if got := engine.Source.SHA256["linux/amd64"]; got != wantSHA {
		t.Fatalf("sha256 = %q, want %q", got, wantSHA)
	}
	if len(engine.Source.Mirror["linux/amd64"]) == 0 {
		t.Fatal("expected expanded mirror URLs")
	}
	if engine.Startup.HealthCheck.Path != "/health" || engine.Startup.Warmup.Enabled {
		t.Fatalf("health/warmup = %#v/%#v", engine.Startup.HealthCheck, engine.Startup.Warmup)
	}
	if engine.API.RequestAdapter == nil || engine.API.RequestAdapter.Kind != "exact_context" || engine.API.RequestAdapter.UpstreamModel != "aima-amd395-qwen36-35b" {
		t.Fatalf("request adapter = %#v", engine.API.RequestAdapter)
	}
}

func TestScenarioNewFields(t *testing.T) {
	cat, err := LoadCatalog(catalogFS())
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}

	// Find aibook-coldstart which has memory_budget, startup_order, alternative_configs
	var aibook *DeploymentScenario
	for i := range cat.DeploymentScenarios {
		if cat.DeploymentScenarios[i].Metadata.Name == "aibook-coldstart" {
			aibook = &cat.DeploymentScenarios[i]
			break
		}
	}
	if aibook == nil {
		t.Fatal("aibook-coldstart scenario not found in catalog")
	}

	// memory_budget should be populated (map[string]any from YAML)
	if aibook.MemoryBudget == nil {
		t.Error("expected memory_budget to be parsed, got nil")
	} else {
		if _, ok := aibook.MemoryBudget["total_unified_mib"]; !ok {
			t.Error("memory_budget missing total_unified_mib key")
		}
	}

	// startup_order should have 4 steps
	if len(aibook.StartupOrder) != 4 {
		t.Errorf("expected 4 startup_order steps, got %d", len(aibook.StartupOrder))
	} else {
		if aibook.StartupOrder[0].Model != "qwen3-8b" {
			t.Errorf("expected first startup step model to be qwen3-8b, got %s", aibook.StartupOrder[0].Model)
		}
		if aibook.StartupOrder[0].WaitFor != "health_check" {
			t.Errorf("expected first startup step wait_for=health_check, got %s", aibook.StartupOrder[0].WaitFor)
		}
		if aibook.StartupOrder[0].TimeoutS != 180 {
			t.Errorf("expected first startup step timeout_s=180, got %d", aibook.StartupOrder[0].TimeoutS)
		}
	}

	// alternative_configs should have 2 entries
	if len(aibook.AlternativeConfigs) != 2 {
		t.Errorf("expected 2 alternative_configs, got %d", len(aibook.AlternativeConfigs))
	} else {
		if aibook.AlternativeConfigs[0].Name != "aibook-30b-solo" {
			t.Errorf("expected first alternative name=aibook-30b-solo, got %s", aibook.AlternativeConfigs[0].Name)
		}
		if len(aibook.AlternativeConfigs[0].Replace) == 0 {
			t.Error("expected aibook-30b-solo to have at least one replacement deployment")
		}
	}

	// openclaw-multi should expose startup_order but still omit optional memory/alternatives
	var openclaw *DeploymentScenario
	for i := range cat.DeploymentScenarios {
		if cat.DeploymentScenarios[i].Metadata.Name == "openclaw-multi" {
			openclaw = &cat.DeploymentScenarios[i]
			break
		}
	}
	if openclaw == nil {
		t.Fatal("openclaw-multi scenario not found")
	}
	if openclaw.MemoryBudget != nil {
		t.Error("openclaw-multi should not have memory_budget")
	}
	if len(openclaw.StartupOrder) != 4 {
		t.Errorf("expected openclaw-multi to have 4 startup_order steps, got %d", len(openclaw.StartupOrder))
	} else {
		if openclaw.StartupOrder[0].Model != "qwen3.5-9b" {
			t.Errorf("expected first openclaw startup step model to be qwen3.5-9b, got %s", openclaw.StartupOrder[0].Model)
		}
		if openclaw.StartupOrder[0].WaitFor != "health_check" {
			t.Errorf("expected first openclaw startup step wait_for=health_check, got %s", openclaw.StartupOrder[0].WaitFor)
		}
	}
	if len(openclaw.AlternativeConfigs) != 0 {
		t.Error("openclaw-multi should not have alternative_configs")
	}

	// openclaw-multi-aibook should expose memory/startup and keep alternatives empty
	var openclawAIBook *DeploymentScenario
	for i := range cat.DeploymentScenarios {
		if cat.DeploymentScenarios[i].Metadata.Name == "openclaw-multi-aibook" {
			openclawAIBook = &cat.DeploymentScenarios[i]
			break
		}
	}
	if openclawAIBook == nil {
		t.Fatal("openclaw-multi-aibook scenario not found")
	}
	if openclawAIBook.MemoryBudget == nil {
		t.Error("openclaw-multi-aibook should have memory_budget")
	}
	if len(openclawAIBook.StartupOrder) != 2 {
		t.Errorf("expected openclaw-multi-aibook to have 2 startup_order steps, got %d", len(openclawAIBook.StartupOrder))
	} else {
		if openclawAIBook.StartupOrder[0].Model != "mooer-asr-1.5b" {
			t.Errorf("expected first openclaw-multi-aibook step model to be mooer-asr-1.5b, got %s", openclawAIBook.StartupOrder[0].Model)
		}
		if openclawAIBook.StartupOrder[1].Model != "litetts-mnn" {
			t.Errorf("expected second openclaw-multi-aibook step model to be litetts-mnn, got %s", openclawAIBook.StartupOrder[1].Model)
		}
	}
	if len(openclawAIBook.AlternativeConfigs) != 0 {
		t.Error("openclaw-multi-aibook should not have alternative_configs")
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

func TestParseAssetPublicEngineProfile(t *testing.T) {
	cat := &Catalog{}
	err := cat.ParseAssetPublic([]byte(`kind: engine_profile
metadata:
  name: vllm
startup:
  health_check:
    path: /health
    timeout_s: 300
`), "input")
	if err != nil {
		t.Fatalf("ParseAssetPublic: %v", err)
	}
	if got := cat.ParsedKind(); got != "engine_profile" {
		t.Fatalf("ParsedKind() = %q, want engine_profile", got)
	}
	if _, ok := cat.EngineProfiles["vllm"]; !ok {
		t.Fatal("expected engine profile vllm to be loaded")
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

	merged, _ := MergeCatalog(base, overlay)
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

	merged, _ := MergeCatalog(base, overlay)
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

	merged, _ := MergeCatalog(base, &Catalog{})
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

func TestLoadCatalogEngineProfilePartialOverride(t *testing.T) {
	fs := fstest.MapFS{
		"engines/profiles/vllm.yaml": &fstest.MapFile{Data: []byte(`kind: engine_profile
metadata:
  name: vllm
  version_default: "0.8.5"
  supported_formats: [safetensors]
startup:
  health_check:
    path: /health
    timeout_s: 300
  warmup:
    enabled: true
    prompt: "Hello"
    max_tokens: 1
    timeout_s: 60
api:
  protocol: openai
  base_path: /v1
`)},
		"engines/vllm-musa.yaml": &fstest.MapFile{Data: []byte(`kind: engine_asset
_profile: vllm
metadata:
  name: vllm-musa
  type: vllm
hardware:
  gpu_arch: MUSA
  vram_min_mib: 4096
startup:
  health_check:
    timeout_s: 600
  warmup:
    timeout_s: 120
`)},
	}

	cat, err := LoadCatalog(fs)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if len(cat.EngineAssets) != 1 {
		t.Fatalf("EngineAssets count = %d, want 1", len(cat.EngineAssets))
	}
	engine := cat.EngineAssets[0]
	if got := engine.Startup.HealthCheck.Path; got != "/health" {
		t.Errorf("health_check.path = %q, want /health", got)
	}
	if got := engine.Startup.HealthCheck.TimeoutS; got != 600 {
		t.Errorf("health_check.timeout_s = %d, want 600", got)
	}
	if !engine.Startup.Warmup.Enabled {
		t.Error("warmup.enabled = false, want true")
	}
	if got := engine.Startup.Warmup.Prompt; got != "Hello" {
		t.Errorf("warmup.prompt = %q, want Hello", got)
	}
	if got := engine.Startup.Warmup.MaxTokens; got != 1 {
		t.Errorf("warmup.max_tokens = %d, want 1", got)
	}
	if got := engine.Startup.Warmup.TimeoutS; got != 120 {
		t.Errorf("warmup.timeout_s = %d, want 120", got)
	}
}

func TestEngineStartupRecoveryParsesAndMerges(t *testing.T) {
	fs := fstest.MapFS{
		"engines/profiles/recovery.yaml": &fstest.MapFile{Data: []byte(`kind: engine_profile
metadata:
  name: recovery-profile
startup:
  recovery:
    enabled: true
    check_interval_s: 7
    consecutive_failures: 4
    max_attempts: 3
    window_s: 900
    backoff_s: [2, 10, 30]
    stable_reset_s: 1200
`)},
		"engines/recovery-engine.yaml": &fstest.MapFile{Data: []byte(`kind: engine_asset
_profile: recovery-profile
metadata:
  name: recovery-engine
  type: generic
hardware:
  gpu_arch: "*"
  vram_min_mib: 0
startup:
  recovery:
    max_attempts: 5
`)},
	}

	cat, err := LoadCatalog(fs)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if len(cat.EngineAssets) != 1 {
		t.Fatalf("EngineAssets count = %d, want 1", len(cat.EngineAssets))
	}
	recovery := cat.EngineAssets[0].Startup.Recovery
	if recovery.Enabled == nil || !*recovery.Enabled {
		t.Fatalf("recovery.enabled = %v, want true", recovery.Enabled)
	}
	if recovery.CheckIntervalS == nil || *recovery.CheckIntervalS != 7 {
		t.Fatalf("recovery.check_interval_s = %v, want 7", recovery.CheckIntervalS)
	}
	if recovery.ConsecutiveFailures == nil || *recovery.ConsecutiveFailures != 4 {
		t.Fatalf("recovery.consecutive_failures = %v, want 4", recovery.ConsecutiveFailures)
	}
	if recovery.MaxAttempts == nil || *recovery.MaxAttempts != 5 {
		t.Fatalf("recovery.max_attempts = %v, want 5", recovery.MaxAttempts)
	}
	if recovery.WindowS == nil || *recovery.WindowS != 900 {
		t.Fatalf("recovery.window_s = %v, want 900", recovery.WindowS)
	}
	if got := recovery.BackoffS; len(got) != 3 || got[0] != 2 || got[1] != 10 || got[2] != 30 {
		t.Fatalf("recovery.backoff_s = %v, want [2 10 30]", got)
	}
	if recovery.StableResetS == nil || *recovery.StableResetS != 1200 {
		t.Fatalf("recovery.stable_reset_s = %v, want 1200", recovery.StableResetS)
	}
}

func TestCloneEngineAssetCopiesRecoveryBackoff(t *testing.T) {
	src := EngineAsset{Startup: EngineStartup{Recovery: RecoveryPolicy{BackoffS: []int{2, 10, 30}}}}
	clone := cloneEngineAsset(src)
	clone.Startup.Recovery.BackoffS[0] = 99
	if src.Startup.Recovery.BackoffS[0] != 2 {
		t.Fatalf("recovery backoff aliases source: %v", src.Startup.Recovery.BackoffS)
	}
}

func TestEngineCompatibleVersionsParseMergeAndClone(t *testing.T) {
	fs := fstest.MapFS{
		"engines/profiles/engine-a.yaml": &fstest.MapFile{Data: []byte(`kind: engine_profile
metadata:
  name: engine-a
  version_default: "1.2.3"
  compatible_versions: ["1.2.2", "1.2.x"]
`)},
		"engines/inherited.yaml": &fstest.MapFile{Data: []byte(`kind: engine_asset
_profile: engine-a
metadata:
  name: engine-a-inherited
  type: engine-a
hardware:
  gpu_arch: "*"
`)},
		"engines/override.yaml": &fstest.MapFile{Data: []byte(`kind: engine_asset
_profile: engine-a
metadata:
  name: engine-a-override
  type: engine-a
  compatible_versions: ["1.1.9"]
hardware:
  gpu_arch: "*"
`)},
	}

	cat, err := LoadCatalog(fs)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	byName := make(map[string]*EngineAsset)
	for i := range cat.EngineAssets {
		byName[cat.EngineAssets[i].Metadata.Name] = &cat.EngineAssets[i]
	}
	if got := byName["engine-a-inherited"].Metadata.CompatibleVersions; len(got) != 2 || got[0] != "1.2.2" || got[1] != "1.2.x" {
		t.Fatalf("inherited compatible_versions = %v", got)
	}
	if got := byName["engine-a-override"].Metadata.CompatibleVersions; len(got) != 1 || got[0] != "1.1.9" {
		t.Fatalf("override compatible_versions = %v", got)
	}

	byName["engine-a-inherited"].Metadata.CompatibleVersions[0] = "mutated"
	if got := cat.EngineProfiles["engine-a"].Metadata.CompatibleVersions[0]; got != "1.2.2" {
		t.Fatalf("finalized asset aliases profile compatible_versions: %q", got)
	}
	for _, raw := range cat.RawEngineAssets {
		if raw.Metadata.Name == "engine-a-inherited" && len(raw.Metadata.CompatibleVersions) != 0 {
			t.Fatalf("finalized asset mutated raw compatible_versions: %v", raw.Metadata.CompatibleVersions)
		}
	}
}

func TestMergeCatalogOverlayProfileRebuildsEngineAssets(t *testing.T) {
	baseFS := fstest.MapFS{
		"engines/profiles/vllm.yaml": &fstest.MapFile{Data: []byte(`kind: engine_profile
metadata:
  name: vllm
startup:
  health_check:
    path: /health
    timeout_s: 300
`)},
		"engines/vllm-ada.yaml": &fstest.MapFile{Data: []byte(`kind: engine_asset
_profile: vllm
metadata:
  name: vllm-ada
  type: vllm
hardware:
  gpu_arch: Ada
  vram_min_mib: 8192
startup: {}
`)},
	}
	base, err := LoadCatalog(baseFS)
	if err != nil {
		t.Fatalf("LoadCatalog(base): %v", err)
	}
	if got := base.EngineAssets[0].Startup.HealthCheck.TimeoutS; got != 300 {
		t.Fatalf("precondition: base timeout_s = %d, want 300", got)
	}

	overlayFS := fstest.MapFS{
		"engines/profiles/vllm.yaml": &fstest.MapFile{Data: []byte(`kind: engine_profile
metadata:
  name: vllm
startup:
  health_check:
    path: /healthz
    timeout_s: 999
`)},
	}
	overlay, err := LoadCatalog(overlayFS)
	if err != nil {
		t.Fatalf("LoadCatalog(overlay): %v", err)
	}

	merged, _ := MergeCatalog(base, overlay)
	engine := merged.FindEngineByName("vllm-ada", HardwareInfo{})
	if engine == nil {
		t.Fatal("vllm-ada not found after merge")
	}
	if got := engine.Startup.HealthCheck.Path; got != "/healthz" {
		t.Errorf("health_check.path = %q, want /healthz", got)
	}
	if got := engine.Startup.HealthCheck.TimeoutS; got != 999 {
		t.Errorf("health_check.timeout_s = %d, want 999", got)
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

// TestLoadCatalogLenient_OverlayWithoutProfileIsQuiet confirms that overlay
// engine assets referencing a profile (e.g. `_profile: vllm`) without the
// accompanying engines/profiles/*.yaml do NOT emit a spurious "unknown
// profile" warning during the overlay load. The profile is expected to
// resolve against the factory catalog during MergeCatalog.
func TestLoadCatalogLenient_OverlayWithoutProfileIsQuiet(t *testing.T) {
	overlayFS := fstest.MapFS{
		"engines/vllm-nightly-blackwell.yaml": &fstest.MapFile{Data: []byte(`kind: engine_asset
_profile: vllm
metadata:
  name: vllm-nightly-blackwell
  type: vllm-nightly
  version: nightly
hardware:
  gpu_arch: Blackwell
  vram_min_mib: 4096
image:
  name: vllm/vllm-openai
  tag: qwen3_5-cu130
`)},
	}
	_, warnings := LoadCatalogLenient(overlayFS)
	for _, w := range warnings {
		if strings.Contains(w, "unknown profile") {
			t.Fatalf("overlay load should swallow 'unknown profile' warnings, got: %q", w)
		}
	}
}

// TestMergeCatalogWithDigests_SurfacesPostMergeProfileGap confirms that when
// an engine asset's profile reference cannot be resolved even after merge
// (neither overlay nor base defines it), MergeCatalogWithDigests surfaces the
// warning — previously MergeCatalog silently dropped post-merge finalize
// warnings with `_ = finalizeEngineAssets(base)`.
func TestMergeCatalogWithDigests_SurfacesPostMergeProfileGap(t *testing.T) {
	base := &Catalog{
		EngineProfiles: make(map[string]*EngineProfile),
	}
	overlayFS := fstest.MapFS{
		"engines/ghost-engine.yaml": &fstest.MapFile{Data: []byte(`kind: engine_asset
_profile: totally-missing-profile
metadata:
  name: ghost-engine
  type: ghost
hardware:
  gpu_arch: Test
`)},
	}
	overlay, _ := LoadCatalogLenient(overlayFS)
	_, warnings := MergeCatalogWithDigests(base, overlay, nil, overlayFS)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "unknown profile") && strings.Contains(w, "totally-missing-profile") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected post-merge warning about unknown profile, got warnings=%v", warnings)
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

	profileFS := fstest.MapFS{
		"engines/profiles/vllm.yaml": &fstest.MapFile{Data: []byte(`kind: engine_profile
metadata:
  name: vllm
startup:
  health_check:
    path: /health
    timeout_s: 300
`)},
	}
	profileDigests := ComputeDigests(profileFS)
	if _, ok := profileDigests["vllm"]; !ok {
		t.Error("expected digest for engine profile vllm")
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

	t.Run("engine profile stale warning", func(t *testing.T) {
		factoryFS := fstest.MapFS{
			"engines/profiles/vllm.yaml": &fstest.MapFile{Data: []byte(`kind: engine_profile
metadata:
  name: vllm
startup:
  health_check:
    path: /health
    timeout_s: 300
`)},
		}
		factoryCat, err := LoadCatalog(factoryFS)
		if err != nil {
			t.Fatalf("LoadCatalog(factoryFS): %v", err)
		}
		factoryDigests := ComputeDigests(factoryFS)
		overlayFS := fstest.MapFS{
			"engines/profiles/vllm.yaml": &fstest.MapFile{Data: []byte(`_base_digest: sha256:0000000000000000000000000000000000000000000000000000000000000000
kind: engine_profile
metadata:
  name: vllm
startup:
  health_check:
    path: /healthz
    timeout_s: 999
`)},
		}
		overlayCat, warnings := LoadCatalogLenient(overlayFS)
		if len(warnings) != 0 {
			t.Fatalf("LoadCatalogLenient(profile overlay) warnings = %v, want none", warnings)
		}
		baseCopy, err := LoadCatalog(factoryFS)
		if err != nil {
			t.Fatalf("LoadCatalog(factoryFS copy): %v", err)
		}
		merged, staleWarnings := MergeCatalogWithDigests(baseCopy, overlayCat, factoryDigests, overlayFS)
		if len(staleWarnings) != 1 {
			t.Fatalf("expected 1 staleness warning for engine profile, got %d: %v", len(staleWarnings), staleWarnings)
		}
		if len(factoryCat.EngineProfiles) != 1 {
			t.Fatalf("factoryCat.EngineProfiles = %d, want 1", len(factoryCat.EngineProfiles))
		}
		if got := merged.EngineProfiles["vllm"].Startup.HealthCheck.TimeoutS; got != 999 {
			t.Fatalf("merged engine profile timeout_s = %d, want 999", got)
		}
	})

	_ = base // used for reference
}

func TestKindToDir(t *testing.T) {
	tests := []struct{ kind, dir string }{
		{"engine_profile", "engines/profiles"},
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

func TestCatalogAssetYAMLAndValidateCatalogPatch(t *testing.T) {
	base, err := LoadCatalog(fstest.MapFS{
		"models/demo.yaml": &fstest.MapFile{Data: []byte(`kind: model_asset
metadata:
  name: demo-model
  type: llm
  family: demo
  parameter_count: 1b
storage:
  formats: [safetensors]
  default_path_pattern: models/demo-model
  sources: []
variants:
  - name: default
    engine: vllm
    format: safetensors
    hardware:
      gpu_arch: Any
      vram_min_mib: 1024
    default_config:
      max_model_len: 2048
      gpu_memory_utilization: 0.8
    expected_performance: {}
`)},
	})
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}

	patch := []byte(`kind: model_asset_patch
metadata:
  name: demo-model
variants:
  - name: default
    default_config:
      gpu_memory_utilization: 0.9
`)

	effective, err := ValidateCatalogPatch(base, patch, "models/demo-model.patch.yaml")
	if err != nil {
		t.Fatalf("ValidateCatalogPatch: %v", err)
	}
	if !strings.Contains(string(effective), "gpu_memory_utilization: 0.9") {
		t.Fatalf("effective patch YAML missing override:\n%s", string(effective))
	}
	if !strings.Contains(string(effective), "max_model_len: 2048") {
		t.Fatalf("effective patch YAML lost base config:\n%s", string(effective))
	}

	overlay := &Catalog{ModelAssets: []ModelAsset{}}
	if err := overlay.parseAsset(effective, "effective.yaml"); err != nil {
		t.Fatalf("parse effective patch: %v", err)
	}
	merged, _ := MergeCatalog(base, overlay)
	yamlData, found, err := CatalogAssetYAML(merged, "model_asset", "demo-model")
	if err != nil {
		t.Fatalf("CatalogAssetYAML: %v", err)
	}
	if !found {
		t.Fatal("CatalogAssetYAML did not find demo-model")
	}
	if !strings.Contains(string(yamlData), "gpu_memory_utilization: 0.9") {
		t.Fatalf("catalog asset YAML missing effective value:\n%s", string(yamlData))
	}

	badPatch := []byte(`kind: model_asset_patch
metadata:
  name: demo-model
variants: wrong-type
`)
	if _, err := ValidateCatalogPatch(base, badPatch, "models/demo-model.bad.patch.yaml"); err == nil {
		t.Fatal("ValidateCatalogPatch accepted invalid asset schema")
	}
}

func TestValidateCatalogPatchRejectsInvalidRequestAdapter(t *testing.T) {
	base := &Catalog{EngineAssets: []EngineAsset{{
		Kind:     "engine_asset",
		Metadata: EngineMetadata{Name: "native-demo"},
	}}}
	patch := []byte(`kind: engine_asset_patch
metadata:
  name: native-demo
api:
  request_adapter:
    kind: unknown_adapter
`)

	if _, err := ValidateCatalogPatch(base, patch, "engines/native-demo.patch.yaml"); err == nil || !strings.Contains(err.Error(), "unknown request adapter kind") {
		t.Fatalf("ValidateCatalogPatch error = %v, want unknown adapter rejection", err)
	}
}

func TestLoadCatalogPatchesLenientSkipsInvalidRequestAdapter(t *testing.T) {
	base := &Catalog{EngineAssets: []EngineAsset{{
		Kind:     "engine_asset",
		Metadata: EngineMetadata{Name: "native-demo"},
	}}}
	patchFS := fstest.MapFS{
		"engines/native-demo.patch.yaml": &fstest.MapFile{Data: []byte(`kind: engine_asset_patch
metadata:
  name: native-demo
api:
  request_adapter:
    kind: unknown_adapter
`)},
	}

	overlay, warnings := LoadCatalogPatchesLenient(patchFS, base)
	if len(overlay.EngineAssets) != 0 {
		t.Fatalf("invalid adapter patch was loaded: %#v", overlay.EngineAssets)
	}
	if !slices.ContainsFunc(warnings, func(warning string) bool {
		return strings.Contains(warning, "unknown request adapter kind")
	}) {
		t.Fatalf("warnings = %#v, want adapter validation warning", warnings)
	}
}

func TestLoadCatalogPatchesLenientSkipsInvalidProfileRequestAdapter(t *testing.T) {
	base := &Catalog{
		EngineProfiles: map[string]*EngineProfile{
			"native": {Kind: "engine_profile", Metadata: ProfileMeta{Name: "native"}},
		},
		RawEngineAssets: []EngineAsset{{
			Kind: "engine_asset", Profile: "native", Metadata: EngineMetadata{Name: "native-demo"},
		}},
	}
	patchFS := fstest.MapFS{
		"engines/profiles/native.patch.yaml": &fstest.MapFile{Data: []byte(`kind: engine_profile_patch
metadata:
  name: native
api:
  request_adapter:
    kind: unknown_adapter
`)},
	}

	overlay, warnings := LoadCatalogPatchesLenient(patchFS, base)
	if len(overlay.EngineProfiles) != 0 {
		t.Fatalf("invalid profile adapter patch was loaded: %#v", overlay.EngineProfiles)
	}
	if !slices.ContainsFunc(warnings, func(warning string) bool {
		return strings.Contains(warning, "unknown request adapter kind")
	}) {
		t.Fatalf("warnings = %#v, want profile adapter validation warning", warnings)
	}
}

func TestMergeCatalogRejectsInvalidEffectiveRequestAdapterWithoutMutation(t *testing.T) {
	base := &Catalog{RawEngineAssets: []EngineAsset{{
		Kind: "engine_asset", Metadata: EngineMetadata{Name: "native-demo"},
	}}}
	overlay := &Catalog{RawEngineAssets: []EngineAsset{{
		Kind:     "engine_asset",
		Metadata: EngineMetadata{Name: "native-demo"},
		API:      EngineAPI{RequestAdapter: &EngineRequestAdapter{Kind: "unknown_adapter"}},
	}}}

	merged, warnings := MergeCatalog(base, overlay)
	if merged != base {
		t.Fatal("MergeCatalog returned a different base pointer")
	}
	if got := rawEngineAssets(base)[0].API.RequestAdapter; got != nil {
		t.Fatalf("invalid overlay mutated effective catalog: %#v", got)
	}
	if !slices.ContainsFunc(warnings, func(warning string) bool {
		return strings.Contains(warning, "unknown request adapter kind")
	}) {
		t.Fatalf("warnings = %#v, want effective adapter validation warning", warnings)
	}
}

func TestBenchmarkProfileTiers(t *testing.T) {
	fs := fstest.MapFS{
		"benchmarks/profiles.yaml": &fstest.MapFile{Data: []byte(`kind: benchmark_profiles
tiers:
  - name: high
    min_vram_mib: 40000
    profiles:
      - label: latency
        concurrency_levels: [1]
        input_token_levels: [128, 512, 1024]
        max_token_levels: [256]
        requests_per_combo: 5
        rounds: 1
      - label: throughput
        concurrency_levels: [1, 2, 4]
        input_token_levels: [512]
        max_token_levels: [1024]
        requests_per_combo: 5
        rounds: 1
  - name: low
    min_vram_mib: 0
    profiles:
      - label: latency
        concurrency_levels: [1]
        input_token_levels: [128, 512]
        max_token_levels: [256]
        requests_per_combo: 3
        rounds: 1
`)},
	}
	cat, err := LoadCatalog(fs)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if len(cat.BenchmarkProfileTiers) != 2 {
		t.Fatalf("tiers = %d, want 2", len(cat.BenchmarkProfileTiers))
	}

	// High VRAM → high tier (2 profiles)
	profiles := cat.BenchmarkProfilesForVRAM(50000)
	if len(profiles) != 2 {
		t.Fatalf("50000 MiB: profiles = %d, want 2", len(profiles))
	}
	if profiles[0].Label != "latency" || profiles[1].Label != "throughput" {
		t.Errorf("labels = [%s, %s], want [latency, throughput]", profiles[0].Label, profiles[1].Label)
	}
	if profiles[0].RequestsPerCombo != 5 {
		t.Errorf("requests_per_combo = %d, want 5", profiles[0].RequestsPerCombo)
	}

	// Low VRAM → low tier (1 profile)
	profiles = cat.BenchmarkProfilesForVRAM(8000)
	if len(profiles) != 1 {
		t.Fatalf("8000 MiB: profiles = %d, want 1", len(profiles))
	}
	if profiles[0].Label != "latency" {
		t.Errorf("label = %s, want latency", profiles[0].Label)
	}
	if profiles[0].RequestsPerCombo != 3 {
		t.Errorf("requests_per_combo = %d, want 3", profiles[0].RequestsPerCombo)
	}
}
