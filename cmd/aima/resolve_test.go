package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/knowledge"
)

func TestResolveWithFallbackRefreshesSyntheticModel(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.InsertModel(ctx, &state.Model{
		ID:         "model-refresh",
		Name:       "refresh-model",
		Type:       "llm",
		Path:       "/models/refresh-model",
		Format:     "safetensors",
		SizeBytes:  32 * 1024 * 1024 * 1024,
		Status:     "registered",
		ModelClass: "dense",
	}); err != nil {
		t.Fatalf("InsertModel: %v", err)
	}

	cat := &knowledge.Catalog{
		EngineAssets: []knowledge.EngineAsset{
			{
				Metadata: knowledge.EngineMetadata{
					Name: "vllm-test", Type: "vllm", Version: "1.0",
					Default: true, SupportedFormats: []string{"safetensors"},
				},
				Hardware: knowledge.EngineHardware{GPUArch: "*"},
				Startup: knowledge.EngineStartup{
					Command:     []string{"serve"},
					DefaultArgs: map[string]any{"gpu_memory_utilization": 0.9},
				},
			},
		},
	}

	firstHW := knowledge.HardwareInfo{GPUArch: "Ada"}
	if _, canonical, err := resolveWithFallback(ctx, cat, db, firstHW, "refresh-model", "", nil, ""); err != nil {
		t.Fatalf("first resolveWithFallback: %v", err)
	} else if canonical != "refresh-model" {
		t.Fatalf("canonical name = %q, want refresh-model", canonical)
	}

	secondHW := knowledge.HardwareInfo{GPUArch: "Ada", GPUVRAMMiB: 24576, GPUCount: 2}
	resolved, _, err := resolveWithFallback(ctx, cat, db, secondHW, "refresh-model", "", nil, "")
	if err != nil {
		t.Fatalf("second resolveWithFallback: %v", err)
	}
	got, ok := resolved.Config["tensor_parallel_size"].(int)
	if !ok {
		t.Fatalf("tensor_parallel_size type = %T, want int", resolved.Config["tensor_parallel_size"])
	}
	if got != 2 {
		t.Fatalf("tensor_parallel_size = %d, want 2", got)
	}

	var refreshed *knowledge.ModelAsset
	for i := range cat.ModelAssets {
		if cat.ModelAssets[i].Metadata.Name == "refresh-model" {
			refreshed = &cat.ModelAssets[i]
			break
		}
	}
	if refreshed == nil {
		t.Fatal("refresh-model not found in catalog after refresh")
	}
	foundAdaTP := false
	for _, variant := range refreshed.Variants {
		if variant.Hardware.GPUArch == "Ada" && variant.Hardware.GPUCountMin == 2 {
			foundAdaTP = true
			break
		}
	}
	if !foundAdaTP {
		t.Fatalf("expected refreshed synthetic variant with Ada GPUCountMin=2, got %+v", refreshed.Variants)
	}
}

func TestResolveWithFallbackDoesNotRebuildUnsupportedCatalogVariant(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.InsertModel(ctx, &state.Model{
		ID:         "model-unsupported",
		Name:       "unsupported-model",
		Type:       "llm",
		Path:       "/models/unsupported-model",
		Format:     "safetensors",
		SizeBytes:  16 * 1024 * 1024 * 1024,
		Status:     "registered",
		ModelClass: "dense",
	}); err != nil {
		t.Fatalf("InsertModel: %v", err)
	}

	cat := &knowledge.Catalog{
		EngineAssets: []knowledge.EngineAsset{
			{
				Metadata: knowledge.EngineMetadata{
					Name:             "vllm-nightly-blackwell",
					Type:             "vllm-nightly",
					SupportedFormats: []string{"safetensors"},
				},
				Image:    knowledge.EngineImage{Name: "vllm/vllm-openai", Tag: "qwen3_5-cu130", Platforms: []string{"linux/arm64"}},
				Hardware: knowledge.EngineHardware{GPUArch: "Blackwell"},
				Runtime:  knowledge.EngineRuntime{Default: "container"},
			},
		},
		ModelAssets: []knowledge.ModelAsset{
			{
				Metadata: knowledge.ModelMetadata{Name: "unsupported-model", Type: "llm"},
				Variants: []knowledge.ModelVariant{
					{
						Name:   "unsupported-model-blocked",
						Engine: "vllm-nightly",
						Format: "safetensors",
						Hardware: knowledge.ModelVariantHardware{
							GPUArch: "Blackwell",
						},
						Compatibility: knowledge.ModelCompatibility{
							UnsupportedReason: "runtime image is specialized and not validated for this model",
						},
					},
				},
			},
		},
	}

	_, _, err = resolveWithFallback(ctx, cat, db, knowledge.HardwareInfo{
		GPUArch:    "Blackwell",
		GPUVRAMMiB: 65536,
		Platform:   "linux/arm64",
	}, "unsupported-model", "vllm-nightly", nil, "")
	if err == nil {
		t.Fatal("expected error for unsupported catalog variant")
	}
	if !strings.Contains(err.Error(), "marked unsupported") {
		t.Fatalf("error = %v, want unsupported reason", err)
	}
	if cat.HasSyntheticModel("unsupported-model") {
		t.Fatal("unsupported catalog model should not be replaced by a synthetic fallback")
	}
}

func TestResolveWithFallbackDoesNotSynthesizeWhenCatalogModelLacksRequestedEngine(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.InsertModel(ctx, &state.Model{
		ID:         "model-catalog-only",
		Name:       "catalog-only-model",
		Type:       "llm",
		Path:       "/models/catalog-only-model",
		Format:     "safetensors",
		SizeBytes:  16 * 1024 * 1024 * 1024,
		Status:     "registered",
		ModelClass: "dense",
	}); err != nil {
		t.Fatalf("InsertModel: %v", err)
	}

	cat := &knowledge.Catalog{
		EngineAssets: []knowledge.EngineAsset{
			{
				Metadata: knowledge.EngineMetadata{
					Name:             "vllm-nightly-blackwell",
					Type:             "vllm-nightly",
					SupportedFormats: []string{"safetensors"},
				},
				Image:    knowledge.EngineImage{Name: "vllm/vllm-openai", Tag: "qwen3_5-cu130", Platforms: []string{"linux/arm64"}},
				Hardware: knowledge.EngineHardware{GPUArch: "Blackwell"},
				Runtime:  knowledge.EngineRuntime{Default: "container"},
			},
			{
				Metadata: knowledge.EngineMetadata{
					Name:             "vllm-blackwell",
					Type:             "vllm",
					SupportedFormats: []string{"safetensors"},
				},
				Image:    knowledge.EngineImage{Name: "vllm/vllm-openai", Tag: "v0.19.0-aarch64-cu130", Platforms: []string{"linux/arm64"}},
				Hardware: knowledge.EngineHardware{GPUArch: "Blackwell"},
				Runtime:  knowledge.EngineRuntime{Default: "container"},
			},
		},
		ModelAssets: []knowledge.ModelAsset{
			{
				Metadata: knowledge.ModelMetadata{Name: "catalog-only-model", Type: "llm"},
				Variants: []knowledge.ModelVariant{
					{
						Name:   "catalog-only-model-nightly",
						Engine: "vllm-nightly",
						Format: "safetensors",
						Hardware: knowledge.ModelVariantHardware{
							GPUArch: "Blackwell",
						},
					},
				},
			},
		},
	}

	_, _, err = resolveWithFallback(ctx, cat, db, knowledge.HardwareInfo{
		GPUArch:    "Blackwell",
		GPUVRAMMiB: 65536,
		Platform:   "linux/arm64",
	}, "catalog-only-model", "vllm", nil, "")
	if err == nil {
		t.Fatal("expected error for missing catalog variant")
	}
	if !strings.Contains(err.Error(), "no variant of model") {
		t.Fatalf("error = %v, want missing variant error", err)
	}
	if cat.HasSyntheticModel("catalog-only-model") {
		t.Fatal("catalog-backed model should not gain synthetic variants for a missing requested engine")
	}
}

func TestResolveWithFallbackPrefersCompatibleScannedModelPath(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	modelPath := filepath.Join(t.TempDir(), "demo-model.gguf")
	if err := os.WriteFile(modelPath, []byte("gguf"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := db.InsertModel(ctx, &state.Model{
		ID:        "model-demo-gguf",
		Name:      "demo-model",
		Type:      "llm",
		Path:      modelPath,
		Format:    "gguf",
		SizeBytes: 4 * 1024 * 1024,
		Status:    "registered",
	}); err != nil {
		t.Fatalf("InsertModel: %v", err)
	}

	cat := &knowledge.Catalog{
		EngineAssets: []knowledge.EngineAsset{{
			Metadata: knowledge.EngineMetadata{
				Name:             "llamacpp-universal",
				Type:             "llamacpp",
				SupportedFormats: []string{"gguf"},
			},
			Hardware: knowledge.EngineHardware{GPUArch: "Ada"},
			Runtime:  knowledge.EngineRuntime{Default: "native"},
			Source: &knowledge.EngineSource{
				Binary:    "llamacpp",
				Platforms: []string{"linux/amd64"},
			},
		}},
		ModelAssets: []knowledge.ModelAsset{{
			Metadata: knowledge.ModelMetadata{Name: "demo-model", Type: "llm"},
			Storage:  knowledge.ModelStorage{DefaultPathPattern: "/missing/demo-model"},
			Variants: []knowledge.ModelVariant{{
				Name:     "demo-model-llamacpp",
				Engine:   "llamacpp",
				Format:   "gguf",
				Hardware: knowledge.ModelVariantHardware{GPUArch: "Ada"},
			}},
		}},
	}

	resolved, _, err := resolveWithFallback(ctx, cat, db, knowledge.HardwareInfo{
		GPUArch:     "Ada",
		Platform:    "linux/amd64",
		RuntimeType: "native",
	}, "demo-model", "llamacpp", nil, "")
	if err != nil {
		t.Fatalf("resolveWithFallback: %v", err)
	}
	if resolved.ModelPath != modelPath {
		t.Fatalf("ModelPath = %q, want scanned path %q", resolved.ModelPath, modelPath)
	}
}

func TestEnsureResolvedEngineProbePathPrependsLocalBinary(t *testing.T) {
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "sglang-kt")
	if err := os.WriteFile(binaryPath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	resolved := &knowledge.ResolvedConfig{
		Engine: "sglang-kt",
		Source: &knowledge.EngineSource{
			Binary: "sglang-kt",
			Probe: &knowledge.EngineSourceProbe{
				Paths: []string{"/existing/path"},
			},
		},
	}

	ensureResolvedEngineProbePath(resolved, binaryPath)
	if resolved.Source == nil || resolved.Source.Probe == nil {
		t.Fatal("expected source probe to exist")
	}
	if len(resolved.Source.Probe.Paths) != 2 {
		t.Fatalf("probe paths = %v", resolved.Source.Probe.Paths)
	}
	if resolved.Source.Probe.Paths[0] != binaryPath {
		t.Fatalf("first probe path = %q, want %q", resolved.Source.Probe.Paths[0], binaryPath)
	}

	ensureResolvedEngineProbePath(resolved, binaryPath)
	if len(resolved.Source.Probe.Paths) != 2 {
		t.Fatalf("duplicate probe path appended: %v", resolved.Source.Probe.Paths)
	}
}

func TestNormalizeAutoPortOverrides(t *testing.T) {
	overrides := map[string]any{
		"port":                   "auto",
		"grpc_port":              " AUTO ",
		"grpc_port_v1beta1":      "auto",
		"device_map":             "auto",
		"gpu_memory_utilization": 0.75,
	}

	normalizeAutoPortOverrides(overrides)

	if _, ok := overrides["port"]; ok {
		t.Fatal("expected port override to be removed")
	}
	if _, ok := overrides["grpc_port"]; ok {
		t.Fatal("expected grpc_port override to be removed")
	}
	if _, ok := overrides["grpc_port_v1beta1"]; ok {
		t.Fatal("expected grpc_port_v1beta1 override to be removed")
	}
	if got := overrides["device_map"]; got != "auto" {
		t.Fatalf("device_map = %v, want preserved auto sentinel", got)
	}
	if got := overrides["gpu_memory_utilization"]; got != 0.75 {
		t.Fatalf("gpu_memory_utilization = %v, want preserved numeric override", got)
	}
}

func TestResolveCatalogWithLocalEngineOverlayUsesInstalledContainerAsset(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.InsertEngine(ctx, &state.Engine{
		ID:          "engine-local-vllm",
		Type:        "vllm",
		Image:       "local/vllm",
		Tag:         "custom",
		RuntimeType: "container",
		Available:   true,
	}); err != nil {
		t.Fatalf("InsertEngine: %v", err)
	}

	oldDocker := resolveImageExistsInDocker
	oldContainerd := resolveImageExistsInContainerd
	defer func() {
		resolveImageExistsInDocker = oldDocker
		resolveImageExistsInContainerd = oldContainerd
	}()
	resolveImageExistsInDocker = func(ctx context.Context, image string, runner engine.CommandRunner) bool {
		return image == "local/vllm:custom"
	}
	resolveImageExistsInContainerd = func(ctx context.Context, image string, runner engine.CommandRunner) bool {
		return false
	}

	cat := &knowledge.Catalog{
		EngineAssets: []knowledge.EngineAsset{{
			Metadata: knowledge.EngineMetadata{Name: "vllm-catalog", Type: "vllm", Version: "1.0", SupportedFormats: []string{"safetensors"}},
			Image:    knowledge.EngineImage{Name: "catalog/vllm", Tag: "catalog", Platforms: []string{"linux/amd64"}},
			Hardware: knowledge.EngineHardware{GPUArch: "Ada"},
			Startup:  knowledge.EngineStartup{DefaultArgs: map[string]any{"gpu_memory_utilization": 0.75}},
			Runtime:  knowledge.EngineRuntime{Default: "container"},
		}},
		ModelAssets: []knowledge.ModelAsset{{
			Metadata: knowledge.ModelMetadata{Name: "demo-model", Type: "llm"},
			Storage:  knowledge.ModelStorage{DefaultPathPattern: "/models/demo"},
			Variants: []knowledge.ModelVariant{{
				Name:     "demo-model-vllm",
				Engine:   "vllm",
				Format:   "safetensors",
				Hardware: knowledge.ModelVariantHardware{GPUArch: "Ada"},
			}},
		}},
	}

	hw := knowledge.HardwareInfo{GPUArch: "Ada", Platform: "linux/amd64"}
	merged := resolveCatalogWithLocalEngineOverlay(ctx, cat, db, hw, t.TempDir())
	if merged == nil {
		t.Fatal("merged catalog is nil")
	}
	if got := cat.EngineAssets[0].Image.Name; got != "catalog/vllm" {
		t.Fatalf("base catalog image mutated = %q", got)
	}

	resolved, _, err := resolveWithFallback(ctx, merged, db, hw, "demo-model", "vllm", map[string]any{"model_path": "/models/demo"}, "")
	if err != nil {
		t.Fatalf("resolveWithFallback: %v", err)
	}
	if got := resolved.EngineImage; got != "local/vllm:custom" {
		t.Fatalf("resolved engine image = %q, want local/vllm:custom", got)
	}
	if got := merged.EngineAssets[0].Image.Name; got != "local/vllm" {
		t.Fatalf("merged engine image name = %q, want local/vllm", got)
	}
}

func TestResolveCatalogWithLocalEngineOverlayUsesInstalledNativeBinary(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "llamacpp-native")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := db.InsertEngine(ctx, &state.Engine{
		ID:          "engine-native-llamacpp",
		Type:        "llamacpp",
		RuntimeType: "native",
		BinaryPath:  binaryPath,
		Available:   true,
	}); err != nil {
		t.Fatalf("InsertEngine: %v", err)
	}

	cat := &knowledge.Catalog{
		EngineAssets: []knowledge.EngineAsset{{
			Metadata: knowledge.EngineMetadata{Name: "llamacpp-native", Type: "llamacpp", Version: "1.0", SupportedFormats: []string{"gguf"}},
			Hardware: knowledge.EngineHardware{GPUArch: "Ada"},
			Startup:  knowledge.EngineStartup{DefaultArgs: map[string]any{"ctx-size": 2048}},
			Runtime:  knowledge.EngineRuntime{Default: "native"},
			Source: &knowledge.EngineSource{
				Binary:    "llamacpp-native",
				Platforms: []string{"linux/amd64"},
			},
		}},
		ModelAssets: []knowledge.ModelAsset{{
			Metadata: knowledge.ModelMetadata{Name: "demo-model", Type: "llm"},
			Storage:  knowledge.ModelStorage{DefaultPathPattern: "/models/demo"},
			Variants: []knowledge.ModelVariant{{
				Name:     "demo-model-llamacpp",
				Engine:   "llamacpp",
				Format:   "gguf",
				Hardware: knowledge.ModelVariantHardware{GPUArch: "Ada"},
			}},
		}},
	}

	hw := knowledge.HardwareInfo{GPUArch: "Ada", Platform: "linux/amd64", RuntimeType: "native"}
	merged := resolveCatalogWithLocalEngineOverlay(ctx, cat, db, hw, t.TempDir())
	if merged == nil {
		t.Fatal("merged catalog is nil")
	}
	if cat.EngineAssets[0].Source == nil || cat.EngineAssets[0].Source.Probe != nil {
		t.Fatalf("base catalog source mutated: %+v", cat.EngineAssets[0].Source)
	}

	resolved, _, err := resolveWithFallback(ctx, merged, db, hw, "demo-model", "llamacpp", map[string]any{"model_path": "/models/demo"}, "")
	if err != nil {
		t.Fatalf("resolveWithFallback: %v", err)
	}
	if resolved.Source == nil || resolved.Source.Probe == nil {
		t.Fatalf("resolved source probe missing: %+v", resolved.Source)
	}
	if got := resolved.Source.Probe.Paths[0]; got != binaryPath {
		t.Fatalf("probe path[0] = %q, want %q", got, binaryPath)
	}
	if got := resolved.Source.Binary; got != filepath.Base(binaryPath) {
		t.Fatalf("resolved source binary = %q, want %q", got, filepath.Base(binaryPath))
	}
	if strings.TrimSpace(resolved.EngineImage) != "" {
		t.Fatalf("resolved engine image = %q, want empty for native overlay", resolved.EngineImage)
	}
}
