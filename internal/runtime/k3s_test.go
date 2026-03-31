package runtime

import (
	"testing"

	"github.com/jguan/aima/internal/k3s"
	"github.com/jguan/aima/internal/knowledge"
)

func TestPodToStatus(t *testing.T) {
	tests := []struct {
		name      string
		pod       *k3s.PodStatus
		wantPhase string
		wantReady bool
	}{
		{
			name:      "running and ready",
			pod:       &k3s.PodStatus{Name: "test", Phase: "Running", Ready: true, IP: "10.0.0.1"},
			wantPhase: "running",
			wantReady: true,
		},
		{
			name:      "pending",
			pod:       &k3s.PodStatus{Name: "test", Phase: "Pending"},
			wantPhase: "starting",
			wantReady: false,
		},
		{
			name:      "failed",
			pod:       &k3s.PodStatus{Name: "test", Phase: "Failed", Message: "OOMKilled"},
			wantPhase: "failed",
			wantReady: false,
		},
		{
			name:      "succeeded",
			pod:       &k3s.PodStatus{Name: "test", Phase: "Succeeded"},
			wantPhase: "stopped",
			wantReady: false,
		},
		{
			name:      "terminating pod is not reusable",
			pod:       &k3s.PodStatus{Name: "test", Phase: "Running", Ready: true, DeletionTimestamp: "2026-03-31T08:30:00Z"},
			wantPhase: "stopped",
			wantReady: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := podToStatus(tt.pod)
			if s.Phase != tt.wantPhase {
				t.Errorf("phase = %q, want %q", s.Phase, tt.wantPhase)
			}
			if s.Ready != tt.wantReady {
				t.Errorf("ready = %v, want %v", s.Ready, tt.wantReady)
			}
			if s.Runtime != "k3s" {
				t.Errorf("runtime = %q, want %q", s.Runtime, "k3s")
			}
		})
	}
}

func TestToResolvedConfig(t *testing.T) {
	req := &DeployRequest{
		Name:      "test-model",
		Engine:    "llamacpp",
		Image:     "ghcr.io/ggerganov/llama.cpp:server",
		Command:   []string{"llama-server", "--model", "{{.ModelPath}}"},
		PortSpecs: []knowledge.StartupPort{{Name: "http", Flag: "--port", ConfigKey: "port", Primary: true}},
		ModelPath: "/data/models/test",
		Config:    map[string]any{"n_gpu_layers": 999, "port": 8080},
		Partition: &PartitionRequest{
			GPUMemoryMiB:    4096,
			GPUCoresPercent: 50,
			CPUCores:        4,
			RAMMiB:          8192,
		},
		HealthCheck:     &HealthCheckConfig{Path: "/health", TimeoutS: 60},
		Labels:          map[string]string{"aima.dev/slot": "primary"},
		Env:             map[string]string{"HSA_OVERRIDE_GFX_VERSION": "11.0.0"},
		GPUResourceName: "nvidia.com/gpu",
		CPUArch:         "x86_64",
		Container: &knowledge.ContainerAccess{
			Devices: []string{"/dev/kfd"},
			Env:     map[string]string{"LD_PRELOAD": "/opt/rocm/lib/librocm_smi64.so"},
		},
	}

	rc := toResolvedConfig(req)

	if rc.Engine != "llamacpp" {
		t.Errorf("engine = %q, want %q", rc.Engine, "llamacpp")
	}
	if rc.EngineImage != "ghcr.io/ggerganov/llama.cpp:server" {
		t.Errorf("image = %q", rc.EngineImage)
	}
	if rc.ModelName != "test-model" {
		t.Errorf("model = %q", rc.ModelName)
	}
	if rc.Slot != "primary" {
		t.Errorf("slot = %q, want %q", rc.Slot, "primary")
	}
	if rc.Partition == nil {
		t.Fatal("partition is nil")
	}
	if rc.Partition.GPUMemoryMiB != 4096 {
		t.Errorf("gpu_memory = %d, want 4096", rc.Partition.GPUMemoryMiB)
	}
	if rc.HealthCheck == nil || rc.HealthCheck.Path != "/health" {
		t.Error("health check not set correctly")
	}
	bindings := knowledge.ResolvePortBindingsFromSpecs(rc.PortSpecs, rc.Config)
	if len(bindings) != 1 || bindings[0].Port != 8080 || !bindings[0].Primary {
		t.Errorf("bindings = %+v, want one primary port 8080", bindings)
	}
	if rc.Env == nil || rc.Env["HSA_OVERRIDE_GFX_VERSION"] != "11.0.0" {
		t.Error("env not mapped correctly")
	}
	if rc.GPUResourceName != "nvidia.com/gpu" {
		t.Errorf("GPUResourceName = %q, want %q", rc.GPUResourceName, "nvidia.com/gpu")
	}
	if rc.CPUArch != "x86_64" {
		t.Errorf("CPUArch = %q, want %q", rc.CPUArch, "x86_64")
	}
	if rc.Container == nil || len(rc.Container.Devices) != 1 || rc.Container.Devices[0] != "/dev/kfd" {
		t.Error("container access not mapped correctly")
	}
}
