package main

import (
	"testing"

	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/proxy"
	"github.com/jguan/aima/internal/runtime"
)

func TestResolvedServedModelNameExpandsModelTemplate(t *testing.T) {
	got := resolvedServedModelName("GLM-4.1V-9B-Thinking-FP4", map[string]any{
		"served_model_name": "{{.ModelName}}",
	})
	if got != "GLM-4.1V-9B-Thinking-FP4" {
		t.Fatalf("resolvedServedModelName = %q, want model name", got)
	}
}

func TestDeploymentUpstreamModelIgnoresUnresolvedTemplateLabel(t *testing.T) {
	got := deploymentUpstreamModel(&runtime.DeploymentStatus{
		Labels: map[string]string{
			proxy.LabelServedModel: "{{.ModelName}}",
			"aima.dev/model":       "GLM-4.1V-9B-Thinking-FP4",
		},
	}, "")
	if got != "GLM-4.1V-9B-Thinking-FP4" {
		t.Fatalf("deploymentUpstreamModel = %q, want model label fallback", got)
	}
}

func TestDeploymentOverviewIncludesCatalogModelType(t *testing.T) {
	cat := &knowledge.Catalog{
		ModelAssets: []knowledge.ModelAsset{{
			Metadata: knowledge.ModelMetadata{Name: "qwen3-tts-0.6b", Type: "tts"},
		}},
	}
	overview := deploymentOverviewFromStatus(&runtime.DeploymentStatus{
		Name:            "qwen3-tts-0.6b-qwen-tts-fastapi",
		Model:           "qwen3-tts-0.6b",
		Image:           "docker.1ms.run/example/qwen-tts:latest",
		Phase:           "running",
		Ready:           true,
		GPUMemoryMiB:    1536,
		GPUMemorySource: "nvidia-smi",
	}, cat)
	if overview.ModelType != "tts" {
		t.Fatalf("ModelType = %q, want tts", overview.ModelType)
	}
	if overview.Image != "docker.1ms.run/example/qwen-tts:latest" {
		t.Fatalf("Image = %q, want docker.1ms.run/example/qwen-tts:latest", overview.Image)
	}
	if overview.GPUMemoryMiB != 1536 || overview.GPUMemorySource != "nvidia-smi" {
		t.Fatalf("GPU memory = %d/%q, want 1536/nvidia-smi", overview.GPUMemoryMiB, overview.GPUMemorySource)
	}
}
