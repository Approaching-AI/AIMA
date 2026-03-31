package runtime

import (
	"strings"
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

	if !strings.Contains(argStr, "--gpus all") && !strings.Contains(argStr, "--device nvidia.com/gpu=all") {
		t.Fatalf("NVIDIA GPU flag missing, got: %s", argStr)
	}
	assertContains(t, argStr, "--ipc=host", "IPC host")
	assertContains(t, argStr, "--env NVIDIA_VISIBLE_DEVICES=all", "NVIDIA env")
	assertContains(t, argStr, "--env VLLM_WORKER_MULTIPROC_METHOD=spawn", "extra env")
	assertContains(t, argStr, "--volume /data/models/qwen3:/models:ro", "model volume")
	assertContains(t, argStr, "--publish 8000:8000", "port publish")
	assertContains(t, argStr, "--restart unless-stopped", "restart policy")
	assertContains(t, argStr, "--entrypoint vllm", "entrypoint override")
	assertContains(t, argStr, "serve /models", "command with model path substitution")
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

	assertContains(t, argStr, "--entrypoint bash", "shell wrapper entrypoint")
	assertContains(t, argStr, "-c", "shell wrapper -c flag")
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

func TestBuildRunArgs_ModelFileVolume(t *testing.T) {
	r := &DockerRuntime{}
	req := &DeployRequest{
		Name:      "test",
		Engine:    "llamacpp",
		Image:     "ghcr.io/ggerganov/llama.cpp:server",
		Command:   []string{"llama-server", "--model", "{{.ModelPath}}"},
		ModelPath: "/mnt/data/models/phi3/Qwen3-4B-Q4_K_M.gguf",
		Port:      8080,
	}

	args := r.buildRunArgs("test-llamacpp", req)
	argStr := joinArgs(args)

	assertContains(t, argStr, "--volume /mnt/data/models/phi3:/models:ro", "model file parent volume mount")
	assertContains(t, argStr, "/models/Qwen3-4B-Q4_K_M.gguf", "container command should point to mounted file")
}

func TestBuildRunArgs_ConfigFlags(t *testing.T) {
	r := &DockerRuntime{}
	req := &DeployRequest{
		Name:      "test",
		Engine:    "llamacpp",
		Image:     "ghcr.io/ggml-org/llama.cpp:server",
		Command:   []string{"llama-server", "--model", "{{.ModelPath}}/model.gguf"},
		ModelPath: "/mnt/data/models/phi3",
		Config: map[string]any{
			"ctx_size":    16384,
			"flash_attn":  true,
			"ubatch_size": 256,
		},
	}

	args := r.buildRunArgs("test-llamacpp", req)
	argStr := joinArgs(args)

	assertContains(t, argStr, "--ctx-size 16384", "ctx_size config flag")
	assertContains(t, argStr, "--ubatch-size 256", "ubatch_size config flag")
	assertContains(t, argStr, "--flash-attn", "bool config flag")
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

func TestBuildRunArgs_ExpandsEnvTemplates(t *testing.T) {
	r := &DockerRuntime{}
	req := &DeployRequest{
		Name:      "z-image",
		Engine:    "z-image-diffusers",
		Image:     "qujing-z-image:latest",
		Command:   []string{"python3", "server.py"},
		ModelPath: "/data/models/z-image",
		Env: map[string]string{
			"MODEL_PATH": "{{.ModelPath}}",
			"MODEL_NAME": "{{.ModelName}}",
		},
	}

	args := r.buildRunArgs("z-image-z-image-diffusers", req)
	argStr := joinArgs(args)

	assertContains(t, argStr, "--env MODEL_PATH=/models", "model path env should expand to mounted path")
	assertContains(t, argStr, "--env MODEL_NAME=z-image", "model name env should expand")
}

func TestBuildRunArgs_CustomPortFlags(t *testing.T) {
	r := &DockerRuntime{}
	req := &DeployRequest{
		Name:      "tts-model",
		Engine:    "litetts",
		Image:     "litetts:latest",
		Command:   []string{"./start_server.sh", "--target_voices", "AIBC006_lite"},
		ModelPath: "/data/models/litetts",
		PortSpecs: []knowledge.StartupPort{
			{Name: "grpc-v1beta1", Flag: "--grpc_port_v1beta1", ConfigKey: "grpc_port_v1beta1"},
			{Name: "grpc", Flag: "--grpc_port", ConfigKey: "grpc_port"},
			{Name: "http", Flag: "--http_port", ConfigKey: "port", Primary: true},
		},
		Config: map[string]any{
			"grpc_port_v1beta1": 32108,
			"grpc_port":         32109,
			"port":              32110,
		},
	}

	args := r.buildRunArgs("tts-model-litetts", req)
	argStr := joinArgs(args)

	assertContains(t, argStr, "--grpc_port_v1beta1 32108", "custom gRPC v1beta1 port flag")
	assertContains(t, argStr, "--grpc_port 32109", "custom gRPC port flag")
	assertContains(t, argStr, "--http_port 32110", "custom HTTP port flag")
	assertContains(t, argStr, "--publish 32110:32110", "only primary HTTP port is published")
	assertNotContains(t, argStr, "--publish 32108:32108", "extra ports should stay container-local on bridge network")
	assertNotContains(t, argStr, "--publish 32109:32109", "extra ports should stay container-local on bridge network")
}

func TestBuildRunArgs_Ascend(t *testing.T) {
	r := &DockerRuntime{}
	req := &DeployRequest{
		Name:      "test-model",
		Engine:    "sglang-ascend",
		Image:     "docker.1ms.run/lmsysorg/sglang:main-cann8.5.0-910b",
		Command:   []string{"python3", "-m", "sglang.launch_server", "--model-path", "{{.ModelPath}}", "--host", "0.0.0.0"},
		ModelPath: "/data/models/qwen3",
		Port:      30000,
		Labels:    map[string]string{"aima.dev/engine": "sglang-ascend"},
		Container: &knowledge.ContainerAccess{
			DockerRuntime: "ascend",
			NetworkMode:   "host",
			ShmSize:       "500g",
			Init:          true,
			Devices:       []string{"/dev/davinci0", "/dev/davinci_manager", "/dev/devmm_svm", "/dev/hisi_hdc"},
			Env:           map[string]string{"PYTORCH_NPU_ALLOC_CONF": "expandable_segments:True"},
			Security:      &knowledge.ContainerSecurity{Privileged: true},
		},
		InitCommands: []string{"source /usr/local/Ascend/ascend-toolkit/set_env.sh"},
	}

	args := r.buildRunArgs("test-model-sglang-ascend", req)
	argStr := joinArgs(args)

	assertContains(t, argStr, "--runtime ascend", "Ascend runtime")
	assertContains(t, argStr, "--init", "init flag")
	assertContains(t, argStr, "--network host", "host network")
	assertContains(t, argStr, "--shm-size 500g", "shared memory size")
	assertContains(t, argStr, "--privileged", "privileged mode")
	assertContains(t, argStr, "--device /dev/davinci0", "davinci device")
	assertContains(t, argStr, "--device /dev/davinci_manager", "davinci manager device")
	assertContains(t, argStr, "--env PYTORCH_NPU_ALLOC_CONF=expandable_segments:True", "NPU env")
	assertNotContains(t, argStr, "--publish", "should not have port publish with host network")
	assertContains(t, argStr, "--entrypoint bash", "init command shell wrapper entrypoint")
	assertContains(t, argStr, "-c", "init command shell wrapper -c flag")
}

func TestBuildRunArgs_ExistingUnchanged(t *testing.T) {
	// Regression: verify existing non-Ascend deployments still produce correct args
	r := &DockerRuntime{}
	req := &DeployRequest{
		Name:      "test",
		Engine:    "vllm",
		Image:     "vllm/vllm-openai:latest",
		Command:   []string{"vllm", "serve", "{{.ModelPath}}"},
		ModelPath: "/data/models/qwen3",
		Port:      8000,
		Container: &knowledge.ContainerAccess{
			Env: map[string]string{"NVIDIA_VISIBLE_DEVICES": "all"},
		},
	}

	args := r.buildRunArgs("test-vllm", req)
	argStr := joinArgs(args)

	assertContains(t, argStr, "--publish 8000:8000", "port publish without host network")
	assertNotContains(t, argStr, "--runtime", "no runtime flag for NVIDIA")
	assertNotContains(t, argStr, "--init", "no init flag")
	assertNotContains(t, argStr, "--network", "no network flag")
	assertNotContains(t, argStr, "--shm-size", "no shm-size flag")
}

func TestDockerArgsWithLegacyNVIDIAGPU(t *testing.T) {
	args := []string{"run", "--device", "nvidia.com/gpu=all", "--env", "NVIDIA_VISIBLE_DEVICES=all", "image", "serve"}
	rewritten := dockerArgsWithLegacyNVIDIAGPU(args)
	argStr := joinArgs(rewritten)

	assertContains(t, argStr, "--gpus all", "legacy NVIDIA fallback")
	assertNotContains(t, argStr, "--device nvidia.com/gpu=all", "CDI device should be removed")
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
		{"Exited (0) 5 minutes ago", "stopped"},
		{"Exited (137) 2 seconds ago", "failed"},
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
	return " " + strings.Join(args, " ") + " "
}

func assertContains(t *testing.T, haystack, needle, msg string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("%s: args should contain %q, got: %s", msg, needle, haystack)
	}
}

func assertNotContains(t *testing.T, haystack, needle, msg string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("%s: args should NOT contain %q, got: %s", msg, needle, haystack)
	}
}
