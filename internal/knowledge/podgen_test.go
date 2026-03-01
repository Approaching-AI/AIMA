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
		Engine:          "vllm",
		EngineImage:     "vllm/vllm-openai:latest",
		ModelPath:       "/data/models/qwen3-8b",
		ModelName:       "qwen3-8b",
		Slot:            "primary",
		Config:          map[string]any{"port": 8000},
		Provenance:      map[string]string{"port": "L0"},
		GPUResourceName: "nvidia.com/gpu",
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
		Container: &ContainerAccess{
			Env: map[string]string{
				"NVIDIA_VISIBLE_DEVICES":      "all",
				"NVIDIA_DRIVER_CAPABILITIES":  "all",
				"LD_LIBRARY_PATH":             "/lib/x86_64-linux-gnu:/usr/local/nvidia/lib:/usr/local/nvidia/lib64",
			},
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

	t.Run("env vars from hardware container access", func(t *testing.T) {
		envList, ok := c["env"].([]any)
		if !ok {
			t.Fatal("expected env in container")
		}
		envMap := make(map[string]string)
		for _, e := range envList {
			entry := e.(map[string]any)
			envMap[entry["name"].(string)] = entry["value"].(string)
		}
		if envMap["NVIDIA_VISIBLE_DEVICES"] != "all" {
			t.Errorf("NVIDIA_VISIBLE_DEVICES = %q, want %q", envMap["NVIDIA_VISIBLE_DEVICES"], "all")
		}
		if envMap["NVIDIA_DRIVER_CAPABILITIES"] != "all" {
			t.Errorf("NVIDIA_DRIVER_CAPABILITIES = %q, want %q", envMap["NVIDIA_DRIVER_CAPABILITIES"], "all")
		}
		if !strings.Contains(envMap["LD_LIBRARY_PATH"], "x86_64-linux-gnu") {
			t.Errorf("LD_LIBRARY_PATH = %q, should contain x86_64-linux-gnu", envMap["LD_LIBRARY_PATH"])
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

func TestGeneratePodAMDDevices(t *testing.T) {
	resolved := &ResolvedConfig{
		Engine:      "rocm-engine",
		EngineImage: "rocm/vllm:latest",
		ModelPath:   "/data/models/test-model",
		ModelName:   "test-model",
		Slot:        "default",
		Config:      map[string]any{"port": 8000},
		Command:     []string{"vllm", "serve", "--model", "{{.ModelPath}}"},
		Container: &ContainerAccess{
			Devices: []string{"/dev/kfd", "/dev/dri"},
			Env: map[string]string{
				"LD_PRELOAD": "/opt/rocm/lib/librocm_smi64.so",
			},
			Security: &ContainerSecurity{
				SupplementalGroups: []int{44, 110},
			},
		},
	}

	podYAML, err := GeneratePod(resolved)
	if err != nil {
		t.Fatalf("GeneratePod: %v", err)
	}

	s := string(podYAML)

	var pod map[string]any
	if err := yaml.Unmarshal(podYAML, &pod); err != nil {
		t.Fatalf("invalid YAML: %v\n%s", err, podYAML)
	}

	t.Run("device mounts", func(t *testing.T) {
		if !strings.Contains(s, "/dev/kfd") {
			t.Error("expected /dev/kfd in pod YAML")
		}
		if !strings.Contains(s, "/dev/dri") {
			t.Error("expected /dev/dri in pod YAML")
		}
	})

	t.Run("LD_PRELOAD env", func(t *testing.T) {
		if !strings.Contains(s, "LD_PRELOAD") {
			t.Error("expected LD_PRELOAD in pod YAML")
		}
		if !strings.Contains(s, "/opt/rocm/lib/librocm_smi64.so") {
			t.Error("expected rocm lib path in pod YAML")
		}
	})

	t.Run("supplemental groups", func(t *testing.T) {
		spec := pod["spec"].(map[string]any)
		sc, ok := spec["securityContext"].(map[string]any)
		if !ok {
			t.Fatal("expected securityContext in pod spec")
		}
		groups, ok := sc["supplementalGroups"].([]any)
		if !ok {
			t.Fatal("expected supplementalGroups in securityContext")
		}
		if len(groups) != 2 {
			t.Fatalf("supplementalGroups count = %d, want 2", len(groups))
		}
	})

	t.Run("no GPU resource request without resource name", func(t *testing.T) {
		spec := pod["spec"].(map[string]any)
		containers := spec["containers"].([]any)
		c := containers[0].(map[string]any)
		if c["resources"] != nil {
			t.Error("should not have resources when GPUResourceName is empty and no partition")
		}
	})
}

func TestGeneratePodEnvMerge(t *testing.T) {
	resolved := &ResolvedConfig{
		Engine:      "vllm",
		EngineImage: "vllm/vllm-openai:latest",
		ModelPath:   "/data/models/test",
		ModelName:   "test",
		Slot:        "default",
		Config:      map[string]any{"port": 8000},
		Command:     []string{"vllm", "serve"},
		Env: map[string]string{
			"HSA_OVERRIDE_GFX_VERSION": "11.0.0",
			"SHARED_VAR":              "engine-wins",
		},
		Container: &ContainerAccess{
			Env: map[string]string{
				"LD_PRELOAD": "/opt/rocm/lib/librocm_smi64.so",
				"SHARED_VAR": "hw-loses",
			},
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

	envList, ok := c["env"].([]any)
	if !ok {
		t.Fatal("expected env in container")
	}
	envMap := make(map[string]string)
	for _, e := range envList {
		entry := e.(map[string]any)
		envMap[entry["name"].(string)] = entry["value"].(string)
	}

	if envMap["HSA_OVERRIDE_GFX_VERSION"] != "11.0.0" {
		t.Errorf("HSA_OVERRIDE_GFX_VERSION = %q, want %q", envMap["HSA_OVERRIDE_GFX_VERSION"], "11.0.0")
	}
	if envMap["LD_PRELOAD"] != "/opt/rocm/lib/librocm_smi64.so" {
		t.Errorf("LD_PRELOAD = %q, want hw value", envMap["LD_PRELOAD"])
	}
	if envMap["SHARED_VAR"] != "engine-wins" {
		t.Errorf("SHARED_VAR = %q, want engine-wins (engine overrides hw)", envMap["SHARED_VAR"])
	}
}
