package runtime

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/knowledge"
)

// Runtime abstracts deployment execution. K3S uses Pod YAML via kubectl;
// Docker uses docker CLI; Native uses direct process exec.
// MCP tools and CLI are unaware of which.
type Runtime interface {
	Deploy(ctx context.Context, req *DeployRequest) error
	Delete(ctx context.Context, name string) error
	Status(ctx context.Context, name string) (*DeploymentStatus, error)
	List(ctx context.Context) ([]*DeploymentStatus, error)
	Logs(ctx context.Context, name string, tailLines int) (string, error)
	Name() string // "k3s", "docker", or "native"
}

// DeployRequest describes what to deploy, independent of how.
type DeployRequest struct {
	Name         string
	Engine       string
	Image        string            // container image (K3S, Docker)
	Command      []string          // startup command with {{.ModelPath}} placeholder
	InitCommands []string          // pre-commands to run before main server (K3S, Docker)
	ModelPath    string            // host path to model files
	Port         int
	Config       map[string]any
	Partition        *PartitionRequest // resource limits (K3S+HAMi); native ignores
	RuntimeClassName string            // K8s runtimeClassName, e.g. "nvidia" (K3S only; from hardware profile)
	HealthCheck      *HealthCheckConfig
	Labels       map[string]string
	BinarySource *engine.BinarySource // native: where to download the engine binary if missing
	Warmup       *WarmupConfig  // post-healthcheck warmup (send dummy inference request)
	CPUArch          string                     // "arm64", "amd64" -- for platform-specific paths in Pod spec
	Env              map[string]string          // extra env vars (engine YAML + hardware YAML merged)
	WorkDir          string                     // working directory for native process (from engine YAML)
	Container        *knowledge.ContainerAccess // vendor-specific container access (K3S, Docker)
	GPUResourceName  string                     // K8s GPU resource name, e.g. "nvidia.com/gpu", "amd.com/gpu"
	ExtraVolumes     []knowledge.ContainerVolume // additional host volumes to mount (K3S, Docker)
}

// DeploymentStatus is the unified status across runtimes.
type DeploymentStatus struct {
	Name      string            `json:"name"`
	Phase     string            `json:"phase"` // running / starting / stopped / failed
	Ready     bool              `json:"ready"`
	Address   string            `json:"address"` // host:port
	Labels    map[string]string `json:"labels"`
	StartTime string            `json:"start_time"`
	Message   string            `json:"message,omitempty"`
	Runtime   string            `json:"runtime"` // "k3s", "docker", or "native"
	Restarts  int               `json:"restarts,omitempty"`
	ExitCode  *int              `json:"exit_code,omitempty"`

	StartupPhase    string `json:"startup_phase,omitempty"`    // scheduling/pulling_image/initializing/loading_weights/cuda_graphs/ready
	StartupProgress int    `json:"startup_progress,omitempty"` // 0-100
	StartupMessage  string `json:"startup_message,omitempty"`  // human-readable
	EstimatedTotalS int    `json:"estimated_total_s,omitempty"`
	ErrorLines      string `json:"error_lines,omitempty"`      // last few log lines on failure
}

// PartitionRequest holds GPU/CPU/RAM resource limits.
type PartitionRequest struct {
	GPUMemoryMiB    int
	GPUCoresPercent int
	CPUCores        int
	RAMMiB          int
}

// HealthCheckConfig defines how to check if a deployment is ready.
type HealthCheckConfig struct {
	Path     string
	TimeoutS int
}

// WarmupConfig defines how to warm up an engine after health check passes.
type WarmupConfig struct {
	Prompt    string
	MaxTokens int
	TimeoutS  int
}

// configToFlags converts a Config map into CLI flags.
// Keys are underscore-separated (e.g. "mem_fraction_static") → "--mem-fraction-static".
// "port" is excluded (handled separately by each runtime).
// Bool true → flag only (no value); bool false → omitted.
func configToFlags(config map[string]any) []string {
	if len(config) == 0 {
		return nil
	}
	keys := make([]string, 0, len(config))
	for k := range config {
		if k != "port" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var flags []string
	for _, k := range keys {
		flag := "--" + strings.ReplaceAll(k, "_", "-")
		switch v := config[k].(type) {
		case bool:
			if v {
				flags = append(flags, flag)
			}
		default:
			flags = append(flags, flag, fmt.Sprintf("%v", v))
		}
	}
	return flags
}
