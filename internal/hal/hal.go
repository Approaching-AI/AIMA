package hal

import "context"

// HardwareInfo is the device capability vector (matches PRD Resource vector R).
type HardwareInfo struct {
	GPU     *GPUInfo    `json:"gpu,omitempty"`
	NPU     *NPUInfo    `json:"npu,omitempty"`
	CPU     CPUInfo     `json:"cpu"`
	RAM     RAMInfo     `json:"ram"`
	Storage StorageInfo `json:"storage"`
	OS      OSInfo      `json:"os"`
}

// GPUInfo describes a GPU or accelerator's capabilities.
type GPUInfo struct {
	Vendor             string  `json:"vendor"`
	Name               string  `json:"name"`
	Arch               string  `json:"arch"`
	VRAMMiB            int     `json:"vram_mib"`
	ComputeID          string  `json:"compute_id"`
	ComputeUnits       int     `json:"compute_units,omitempty"`
	DriverVersion      string  `json:"driver_version"`
	SDKVersion         string  `json:"sdk_version"`
	PowerDrawWatts     float64 `json:"power_draw_watts,omitempty"`
	PowerLimitWatts    float64 `json:"power_limit_watts,omitempty"`
	TemperatureCelsius float64 `json:"temperature_celsius,omitempty"`
	UnifiedMemory      bool    `json:"unified_memory"`
	Count              int     `json:"count"`
}

// NPUInfo describes a Neural Processing Unit (AI accelerator co-processor).
type NPUInfo struct {
	Vendor          string `json:"vendor"`
	Name            string `json:"name"`
	FirmwareVersion string `json:"firmware_version,omitempty"`
	Driver          string `json:"driver"`
	Count           int    `json:"count"`
}

// CPUInfo describes the host CPU.
type CPUInfo struct {
	Arch    string  `json:"arch"`
	Model   string  `json:"model"`
	Cores   int     `json:"cores"`
	Threads int     `json:"threads"`
	FreqGHz float64 `json:"freq_ghz"`
}

// RAMInfo describes system memory.
type RAMInfo struct {
	TotalMiB     int `json:"total_mib"`
	AvailableMiB int `json:"available_mib"`
	SwapTotalMiB int `json:"swap_total_mib,omitempty"`
}

// StorageInfo describes disk space for the AIMA data directory and all volumes.
type StorageInfo struct {
	DataDirPath string       `json:"data_dir_path"`
	FreeMiB     int64        `json:"free_mib"`
	TotalMiB    int64        `json:"total_mib"`
	Volumes     []VolumeInfo `json:"volumes,omitempty"`
}

// VolumeInfo describes a mounted filesystem volume.
type VolumeInfo struct {
	MountPoint string `json:"mount_point"`
	Device     string `json:"device,omitempty"`
	TotalMiB   int64  `json:"total_mib"`
	FreeMiB    int64  `json:"free_mib"`
}

// OSInfo describes the operating system.
type OSInfo struct {
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	Version         string `json:"version,omitempty"`       // OS version (e.g., "11", "22.04", "14.5")
	Kernel          string `json:"kernel,omitempty"`      // Kernel version (Linux)
	ContainerRuntime string `json:"container_runtime,omitempty"` // "k3s" | "docker" | "none"
}

// Metrics holds real-time utilization data.
type Metrics struct {
	GPU *GPUMetrics `json:"gpu,omitempty"`
	CPU CPUMetrics  `json:"cpu"`
	RAM RAMMetrics  `json:"ram"`
}

// GPUMetrics holds real-time GPU utilization.
type GPUMetrics struct {
	UtilizationPercent int     `json:"utilization_percent"`
	MemoryUsedMiB      int     `json:"memory_used_mib"`
	MemoryTotalMiB     int     `json:"memory_total_mib"`
	TemperatureCelsius float64 `json:"temperature_celsius"`
	PowerDrawWatts     float64 `json:"power_draw_watts"`
}

// CPUMetrics holds real-time CPU utilization.
type CPUMetrics struct {
	UsagePercent float64 `json:"usage_percent"`
}

// RAMMetrics holds real-time memory utilization.
type RAMMetrics struct {
	UsedMiB      int `json:"used_mib"`
	AvailableMiB int `json:"available_mib"`
	TotalMiB     int `json:"total_mib"`
}

// CommandRunner abstracts command execution for testability.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Detect performs full hardware detection. Missing tools (e.g., no nvidia-smi)
// result in partial data, never errors.
func Detect(ctx context.Context) (*HardwareInfo, error) {
	return detectWithRunner(ctx, &execRunner{})
}

// CollectMetrics gathers real-time utilization data.
func CollectMetrics(ctx context.Context) (*Metrics, error) {
	return collectMetricsWithRunner(ctx, &execRunner{})
}
