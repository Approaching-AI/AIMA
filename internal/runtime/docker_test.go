package runtime

import (
	"testing"

	"github.com/jguan/aima/internal/knowledge"
)

func TestBuildRunArgs_NVIDIA(t *testing.T) {
	r := &DockerRuntime{}
	req := &DeployRequest{
		Name:      "test-model",
		Engine:    "vllm",
		Image:     "vllm/vllm-openai:latest",
		Command:   []string{"vllm", "serve", "{{.ModelPath}}"},
		ModelPath: "/data/models/qwen3",
		Port:      8000,
		Labels:    map[string]string{"aima.dev/engine": "vllm", "aima.dev/model": "qwen3"},
		Env:       map[string]string{"VLLM_WORKER_MULTIPROC_METHOD": "spawn"},
		Container: &knowledge.ContainerAccess{
			Env: map[string]string{"NVIDIA_VISIBLE_DEVICES": "all", "NVIDIA_DRIVER_CAPABILITIES": "compute,utility"},
		},
	}

	args := r.buildRunArgs("test-model-vllm", req)
	argStr := joinArgs(args)

	assertContains(t, argStr, "--gpus all", "NVIDIA GPU flag")
	assertContains(t, argStr, "--ipc=host", "IPC host")
	assertContains(t, argStr, "--env NVIDIA_VISIBLE_DEVICES=all", "NVIDIA env")
	assertContains(t, argStr, "--env VLLM_WORKER_MULTIPROC_METHOD=spawn", "extra env")
	assertContains(t, argStr, "--volume /data/models/qwen3:/models:ro", "model volume")
	assertContains(t, argStr, "--publish 8000:8000", "port publish")
	assertContains(t, argStr, "--restart unless-stopped", "restart policy")
	assertContains(t, argStr, "vllm serve /models", "command with model path substitution")
}

func TestBuildRunArgs_AMD(t *testing.T) {
	r := &DockerRuntime{}
	req := &DeployRequest{
		Name:      "test-model",
		Engine:    "vllm-rocm",
		Image:     "rocm/vllm:latest",
		Command:   []string{"vllm", "serve", "{{.ModelPath}}"},
		ModelPath: "/data/models/qwen3",
		Port:      8000,
		Container: &knowledge.ContainerAccess{
			Devices: []string{"/dev/kfd", "/dev/dri"},
			Env:     map[string]string{"HSA_OVERRIDE_GFX_VERSION": "11.0.0"},
			Security: &knowledge.ContainerSecurity{
				Privileged:         true,
				SupplementalGroups: []int{110},
			},
		},
	}

	args := r.buildRunArgs("test-model-vllm-rocm", req)
	argStr := joinArgs(args)

	assertContains(t, argStr, "--device /dev/kfd", "AMD KFD device")
	assertContains(t, argStr, "--device /dev/dri", "AMD DRI device")
	assertContains(t, argStr, "--privileged", "privileged mode")
	assertContains(t, argStr, "--group-add 110", "supplemental group")
	assertNotContains(t, argStr, "--gpus", "should not have NVIDIA gpus flag")
}

func TestBuildRunArgs_InitCommands(t *testing.T) {
	r := &DockerRuntime{}
	req := &DeployRequest{
		Name:         "test-model",
		Engine:       "vllm",
		Image:        "vllm/vllm-openai:latest",
		Command:      []string{"vllm", "serve", "{{.ModelPath}}"},
		InitCommands: []string{"pip install librosa", "pip install soundfile"},
		ModelPath:    "/data/models/qwen3",
		Port:         8000,
	}

	args := r.buildRunArgs("test-model-vllm", req)
	argStr := joinArgs(args)

	assertContains(t, argStr, "sh -c", "shell wrapper")
	assertContains(t, argStr, "pip install librosa && pip install soundfile && exec vllm serve /models", "init chain + exec main")
}

func TestBuildRunArgs_ModelVolume(t *testing.T) {
	r := &DockerRuntime{}
	req := &DeployRequest{
		Name:      "test",
		Engine:    "llamacpp",
		Image:     "ghcr.io/ggerganov/llama.cpp:server",
		Command:   []string{"llama-server", "--model", "{{.ModelPath}}/model.gguf"},
		ModelPath: "/mnt/data/models/phi3",
		Port:      8080,
	}

	args := r.buildRunArgs("test-llamacpp", req)
	argStr := joinArgs(args)

	assertContains(t, argStr, "--volume /mnt/data/models/phi3:/models:ro", "model volume mount")
	assertContains(t, argStr, "/models/model.gguf", "model path replaced in command")
}

func TestBuildRunArgs_Labels(t *testing.T) {
	r := &DockerRuntime{}
	req := &DeployRequest{
		Name:   "test",
		Engine: "vllm",
		Image:  "vllm/vllm:latest",
		Labels: map[string]string{
			"aima.dev/engine": "vllm",
			"aima.dev/model":  "qwen3",
		},
	}

	args := r.buildRunArgs("test-vllm", req)
	argStr := joinArgs(args)

	assertContains(t, argStr, "--label aima.dev/engine=vllm", "engine label")
	assertContains(t, argStr, "--label aima.dev/model=qwen3", "model label")
}

func TestBuildRunArgs_ExtraVolumes(t *testing.T) {
	r := &DockerRuntime{}
	req := &DeployRequest{
		Name:   "test",
		Engine: "vllm",
		Image:  "vllm/vllm:latest",
		Container: &knowledge.ContainerAccess{
			Volumes: []knowledge.ContainerVolume{
				{HostPath: "/dev/shm", MountPath: "/dev/shm"},
			},
		},
		ExtraVolumes: []knowledge.ContainerVolume{
			{HostPath: "/opt/data", MountPath: "/data", ReadOnly: true},
		},
	}

	args := r.buildRunArgs("test-vllm", req)
	argStr := joinArgs(args)

	assertContains(t, argStr, "--volume /dev/shm:/dev/shm", "container volume")
	assertContains(t, argStr, "--volume /opt/data:/data:ro", "extra volume readonly")
}

func TestDockerInspectToStatus(t *testing.T) {
	r := &DockerRuntime{}

	tests := []struct {
		name      string
		di        dockerInspect
		wantPhase string
	}{
		{
			name: "running",
			di: dockerInspect{
				Name: "/test-vllm",
				State: struct {
					Status     string `json:"Status"`
					StartedAt  string `json:"StartedAt"`
					ExitCode   int    `json:"ExitCode"`
					Running    bool   `json:"Running"`
					Restarting bool   `json:"Restarting"`
				}{Status: "running", Running: true, StartedAt: "2026-03-03T00:00:00Z"},
			},
			wantPhase: "running",
		},
		{
			name: "exited with error",
			di: dockerInspect{
				Name: "/test-vllm",
				State: struct {
					Status     string `json:"Status"`
					StartedAt  string `json:"StartedAt"`
					ExitCode   int    `json:"ExitCode"`
					Running    bool   `json:"Running"`
					Restarting bool   `json:"Restarting"`
				}{Status: "exited", ExitCode: 1},
			},
			wantPhase: "failed",
		},
		{
			name: "exited cleanly",
			di: dockerInspect{
				Name: "/test-vllm",
				State: struct {
					Status     string `json:"Status"`
					StartedAt  string `json:"StartedAt"`
					ExitCode   int    `json:"ExitCode"`
					Running    bool   `json:"Running"`
					Restarting bool   `json:"Restarting"`
				}{Status: "exited", ExitCode: 0},
			},
			wantPhase: "stopped",
		},
		{
			name: "created",
			di: dockerInspect{
				Name: "/test-vllm",
				State: struct {
					Status     string `json:"Status"`
					StartedAt  string `json:"StartedAt"`
					ExitCode   int    `json:"ExitCode"`
					Running    bool   `json:"Running"`
					Restarting bool   `json:"Restarting"`
				}{Status: "created"},
			},
			wantPhase: "starting",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ds := r.inspectToStatus(tt.di)
			if ds.Phase != tt.wantPhase {
				t.Errorf("phase = %q, want %q", ds.Phase, tt.wantPhase)
			}
			if ds.Runtime != "docker" {
				t.Errorf("runtime = %q, want %q", ds.Runtime, "docker")
			}
		})
	}
}

func TestParseLabelString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  map[string]string
	}{
		{
			name:  "multiple labels",
			input: "aima.dev/engine=vllm,aima.dev/model=qwen3,aima.dev/port=8000",
			want:  map[string]string{"aima.dev/engine": "vllm", "aima.dev/model": "qwen3", "aima.dev/port": "8000"},
		},
		{
			name:  "empty string",
			input: "",
			want:  map[string]string{},
		},
		{
			name:  "single label",
			input: "aima.dev/engine=vllm",
			want:  map[string]string{"aima.dev/engine": "vllm"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLabelString(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("len = %d, want %d", len(got), len(tt.want))
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestDockerStatusToPhase(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"Up 2 hours", "running"},
		{"Up About a minute", "running"},
		{"Exited (1) 5 minutes ago", "failed"},
		{"Exited (0) 5 minutes ago", "failed"}, // docker ps doesn't distinguish exit codes
		{"Created", "starting"},
		{"Restarting (1) 5 seconds ago", "starting"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := dockerStatusToPhase(tt.status)
			if got != tt.want {
				t.Errorf("dockerStatusToPhase(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

// --- helpers ---

func joinArgs(args []string) string {
	return " " + join(args, " ") + " "
}

func join(ss []string, sep string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}

func assertContains(t *testing.T, haystack, needle, msg string) {
	t.Helper()
	if !containsSubstring(haystack, needle) {
		t.Errorf("%s: args should contain %q, got: %s", msg, needle, haystack)
	}
}

func assertNotContains(t *testing.T, haystack, needle, msg string) {
	t.Helper()
	if containsSubstring(haystack, needle) {
		t.Errorf("%s: args should NOT contain %q, got: %s", msg, needle, haystack)
	}
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
