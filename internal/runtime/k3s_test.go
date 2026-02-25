package runtime

import (
	"testing"

	"github.com/jguan/aima/internal/k3s"
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
		ModelPath: "/data/models/test",
		Port:      8080,
		Config:    map[string]any{"n_gpu_layers": 999},
		Partition: &PartitionRequest{
			GPUMemoryMiB:    4096,
			GPUCoresPercent: 50,
			CPUCores:        4,
			RAMMiB:          8192,
		},
		HealthCheck: &HealthCheckConfig{Path: "/health", TimeoutS: 60},
		Labels:      map[string]string{"aima.dev/slot": "primary"},
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
}
