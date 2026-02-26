package runtime

import "context"

// Runtime abstracts deployment execution. K3S uses Pod YAML via kubectl;
// Native uses direct process exec. MCP tools and CLI are unaware of which.
type Runtime interface {
	Deploy(ctx context.Context, req *DeployRequest) error
	Delete(ctx context.Context, name string) error
	Status(ctx context.Context, name string) (*DeploymentStatus, error)
	List(ctx context.Context) ([]*DeploymentStatus, error)
	Logs(ctx context.Context, name string, tailLines int) (string, error)
	Name() string // "k3s" or "native"
}

// DeployRequest describes what to deploy, independent of how.
type DeployRequest struct {
	Name         string
	Engine       string
	Image        string            // container image (K3S only)
	Command      []string          // startup command with {{.ModelPath}} placeholder
	ModelPath    string            // host path to model files
	Port         int
	Config       map[string]any
	Partition    *PartitionRequest // resource limits (K3S+HAMi); native ignores
	HealthCheck  *HealthCheckConfig
	Labels       map[string]string
	BinarySource *BinarySource  // native: where to download the engine binary if missing
	Warmup       *WarmupConfig  // post-healthcheck warmup (send dummy inference request)
}

// BinarySource describes where to download a native engine binary.
type BinarySource struct {
	Binary    string            // e.g. "llama-server"
	Platforms []string          // e.g. ["linux/amd64", "darwin/arm64"]
	Download  map[string]string // platform -> URL
	Mirror    map[string]string // platform -> mirror URL
}

// DeploymentStatus is the unified status across runtimes.
type DeploymentStatus struct {
	Name      string            `json:"name"`
	Phase     string            `json:"phase"`   // running / stopped / failed
	Ready     bool              `json:"ready"`
	Address   string            `json:"address"` // host:port
	Labels    map[string]string `json:"labels"`
	StartTime string            `json:"start_time"`
	Message   string            `json:"message,omitempty"`
	Runtime   string            `json:"runtime"` // "k3s" or "native"
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
