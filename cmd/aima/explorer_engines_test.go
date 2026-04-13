package main

import (
	"testing"

	"github.com/jguan/aima/internal/knowledge"
)

func TestExplorerEngineAssetDeployable_NativeRequiresNativeInstall(t *testing.T) {
	if explorerEngineAssetDeployable("native", "native", &fakeRuntime{name: "native"}, &fakeRuntime{name: "native"}, nil, nil, false, true, false, false) {
		t.Fatal("expected native-preferred engine to be unavailable without native install")
	}
	if !explorerEngineAssetDeployable("native", "native", &fakeRuntime{name: "native"}, &fakeRuntime{name: "native"}, nil, nil, true, false, false, false) {
		t.Fatal("expected native-preferred engine to be available with native install")
	}
}

func TestExplorerContainerRuntimeAvailable_RequiresExactImageForDocker(t *testing.T) {
	if explorerContainerRuntimeAvailable("docker", &fakeRuntime{name: "docker"}, &fakeRuntime{name: "native"}, &fakeRuntime{name: "docker"}, nil, false, false, false) {
		t.Fatal("expected docker container runtime to reject missing exact image")
	}
	if !explorerContainerRuntimeAvailable("docker", &fakeRuntime{name: "docker"}, &fakeRuntime{name: "native"}, &fakeRuntime{name: "docker"}, nil, true, false, false) {
		t.Fatal("expected docker container runtime to accept exact image in Docker")
	}
}

func TestExplorerContainerRuntimeAvailable_FallsBackToDockerFromRootlessK3S(t *testing.T) {
	if !explorerContainerRuntimeAvailable("container", &fakeRuntime{name: "k3s"}, &fakeRuntime{name: "native"}, &fakeRuntime{name: "docker"}, &fakeRuntime{name: "k3s"}, true, false, false) {
		t.Fatal("expected rootless K3S to accept Docker fallback when exact image exists in Docker")
	}
}

func TestCatalogModelMaxContextLen(t *testing.T) {
	cat := &knowledge.Catalog{
		ModelAssets: []knowledge.ModelAsset{
			{
				Metadata: knowledge.ModelMetadata{Name: "qwen3-4b"},
				Variants: []knowledge.ModelVariant{
					{DefaultConfig: map[string]any{"max_model_len": float64(8192)}},
					{DefaultConfig: map[string]any{"max_model_len": float64(4096)}},
				},
			},
			{
				Metadata: knowledge.ModelMetadata{Name: "qwen3.5-27b"},
				Variants: []knowledge.ModelVariant{
					{DefaultConfig: map[string]any{"context_length": float64(65536)}},
				},
			},
			{
				Metadata: knowledge.ModelMetadata{Name: "gemma-4"},
				Variants: []knowledge.ModelVariant{
					{DefaultConfig: map[string]any{"max_context_tokens": float64(32768)}},
				},
			},
			{
				Metadata: knowledge.ModelMetadata{Name: "no-context-model"},
				Variants: []knowledge.ModelVariant{
					{DefaultConfig: map[string]any{"some_other_key": float64(99)}},
				},
			},
		},
	}

	tests := []struct {
		name      string
		modelName string
		want      int
	}{
		{"picks largest variant", "qwen3-4b", 8192},
		{"uses context_length key", "qwen3.5-27b", 65536},
		{"uses max_context_tokens key", "gemma-4", 32768},
		{"unknown model returns 0", "nonexistent", 0},
		{"model without context keys returns 0", "no-context-model", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cat.ModelMaxContextLen(tt.modelName)
			if got != tt.want {
				t.Errorf("ModelMaxContextLen(%q) = %d, want %d", tt.modelName, got, tt.want)
			}
		})
	}
}
