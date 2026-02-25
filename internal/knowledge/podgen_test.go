package knowledge

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGeneratePod(t *testing.T) {
	cat := mustLoadCatalog(t)

	hw := HardwareInfo{
		GPUArch: "TestArch",
		CPUArch: "x86_64",
	}

	resolved, err := cat.Resolve(hw, "test-model-8b", "testengine", map[string]any{
		"model_path": "/data/models/test-model-8b",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	podYAML, err := GeneratePod(resolved)
	if err != nil {
		t.Fatalf("GeneratePod: %v", err)
	}

	if len(podYAML) == 0 {
		t.Fatal("generated YAML is empty")
	}

	// Parse the generated YAML to validate structure
	var pod map[string]any
	if err := yaml.Unmarshal(podYAML, &pod); err != nil {
		t.Fatalf("generated YAML is not valid: %v\n%s", err, podYAML)
	}

	t.Run("apiVersion and kind", func(t *testing.T) {
		if pod["apiVersion"] != "v1" {
			t.Errorf("apiVersion = %v, want v1", pod["apiVersion"])
		}
		if pod["kind"] != "Pod" {
			t.Errorf("kind = %v, want Pod", pod["kind"])
		}
	})

	t.Run("metadata labels", func(t *testing.T) {
		meta, ok := pod["metadata"].(map[string]any)
		if !ok {
			t.Fatal("metadata is not a map")
		}
		labels, ok := meta["labels"].(map[string]any)
		if !ok {
			t.Fatal("labels is not a map")
		}
		if labels["aima.dev/engine"] != "testengine" {
			t.Errorf("engine label = %v, want testengine", labels["aima.dev/engine"])
		}
		if labels["aima.dev/model"] != "test-model-8b" {
			t.Errorf("model label = %v, want test-model-8b", labels["aima.dev/model"])
		}
	})

	t.Run("container spec", func(t *testing.T) {
		spec := pod["spec"].(map[string]any)
		containers := spec["containers"].([]any)
		if len(containers) != 1 {
			t.Fatalf("containers count = %d, want 1", len(containers))
		}
		c := containers[0].(map[string]any)
		if c["name"] != "inference" {
			t.Errorf("container name = %v, want inference", c["name"])
		}
		image, ok := c["image"].(string)
		if !ok || image != "test/engine:v1" {
			t.Errorf("image = %v, want test/engine:v1", c["image"])
		}
	})

	t.Run("volume mounts present", func(t *testing.T) {
		spec := pod["spec"].(map[string]any)
		volumes := spec["volumes"]
		if volumes == nil {
			t.Fatal("expected volumes in pod spec")
		}
	})

	t.Run("yaml is valid string", func(t *testing.T) {
		s := string(podYAML)
		if !strings.Contains(s, "apiVersion") {
			t.Error("YAML should contain apiVersion")
		}
		if !strings.Contains(s, "aima.dev/engine") {
			t.Error("YAML should contain aima.dev/engine label")
		}
	})
}

func TestGeneratePodWithPartition(t *testing.T) {
	resolved := &ResolvedConfig{
		Engine:      "vllm",
		EngineImage: "vllm/vllm-openai:latest",
		ModelPath:   "/data/models/qwen3-8b",
		ModelName:   "qwen3-8b",
		Slot:        "primary",
		Config:      map[string]any{"port": 8000},
		Provenance:  map[string]string{"port": "L0"},
		Partition: &PartitionSlot{
			Name:            "primary",
			GPUMemoryMiB:    10240,
			GPUCoresPercent: 60,
			CPUCores:        8,
			RAMMiB:          65536,
		},
		Command: []string{"vllm", "serve", "--model", "{{.ModelPath}}"},
		HealthCheck: &HealthCheck{
			Path:     "/health",
			TimeoutS: 300,
		},
	}

	podYAML, err := GeneratePod(resolved)
	if err != nil {
		t.Fatalf("GeneratePod: %v", err)
	}

	var pod map[string]any
	if err := yaml.Unmarshal(podYAML, &pod); err != nil {
		t.Fatalf("invalid YAML: %v\n%s", err, podYAML)
	}

	spec := pod["spec"].(map[string]any)
	containers := spec["containers"].([]any)
	c := containers[0].(map[string]any)

	t.Run("resource limits", func(t *testing.T) {
		resources, ok := c["resources"].(map[string]any)
		if !ok {
			t.Fatal("expected resources in container")
		}
		limits, ok := resources["limits"].(map[string]any)
		if !ok {
			t.Fatal("expected limits in resources")
		}
		// HAMi GPU resources
		if limits["nvidia.com/gpu"] == nil {
			t.Error("expected nvidia.com/gpu in limits")
		}
	})

	t.Run("liveness probe", func(t *testing.T) {
		probe := c["livenessProbe"]
		if probe == nil {
			t.Error("expected livenessProbe")
		}
	})

	t.Run("readiness probe", func(t *testing.T) {
		probe := c["readinessProbe"]
		if probe == nil {
			t.Error("expected readinessProbe")
		}
	})

	t.Run("HAMi annotations", func(t *testing.T) {
		meta := pod["metadata"].(map[string]any)
		annotations, ok := meta["annotations"].(map[string]any)
		if !ok {
			t.Fatal("expected annotations")
		}
		if annotations["nvidia.com/gpumem"] == nil {
			t.Error("expected nvidia.com/gpumem annotation")
		}
		if annotations["nvidia.com/gpucores"] == nil {
			t.Error("expected nvidia.com/gpucores annotation")
		}
	})
}

func TestGeneratePodNilResolved(t *testing.T) {
	_, err := GeneratePod(nil)
	if err == nil {
		t.Fatal("expected error for nil resolved config")
	}
}
