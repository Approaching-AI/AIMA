package hal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// execRunner is a real CommandRunner that executes system commands.
type execRunner struct{}

func (r *execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// detectWithRunner performs hardware detection using a given CommandRunner.
func detectWithRunner(ctx context.Context, runner CommandRunner) (*HardwareInfo, error) {
	hw := &HardwareInfo{
		OS: detectOSWithRunner(ctx, runner),
	}

	hw.GPU = detectGPU(ctx, runner)
	hw.NPU = detectNPU()
	hw.CPU = detectCPU(ctx, runner)
	hw.RAM = detectRAM(ctx, runner)
	hw.Storage = detectStorage()

	// Unified memory GPUs share system RAM; use RAM total as available VRAM.
	// NVIDIA: detected by memIsNA in parseNvidiaGPULine (VRAMMiB == 0).
	if hw.GPU != nil && hw.GPU.UnifiedMemory && hw.GPU.VRAMMiB == 0 {
		hw.GPU.VRAMMiB = hw.RAM.TotalMiB
	}

	// AMD APUs (e.g., Ryzen AI MAX): rocm-smi reports full physical memory as VRAM.
	// When GPU VRAM ≈ system RAM and card isn't a known datacenter GPU, flag as unified.
	if hw.GPU != nil && !hw.GPU.UnifiedMemory && hw.GPU.Vendor == "amd" &&
		hw.GPU.VRAMMiB > 0 && hw.RAM.TotalMiB > 0 &&
		!strings.HasPrefix(hw.GPU.Arch, "CDNA") {
		ratio := float64(hw.GPU.VRAMMiB) / float64(hw.RAM.TotalMiB)
		if ratio >= 0.9 && ratio <= 1.1 {
			hw.GPU.UnifiedMemory = true
		}
	}

	return hw, nil
}

// detectOSWithRunner detects OS information with version and container runtime.
func detectOSWithRunner(ctx context.Context, runner CommandRunner) OSInfo {
	info := OSInfo{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}

	// Detect OS version for Linux
	if runtime.GOOS == "linux" {
		if version := detectLinuxVersion(ctx, runner); version != "" {
			info.Version = version
		}
	}

	// Detect container runtime
	containerRuntime := detectContainerRuntime(ctx, runner)
	if containerRuntime != "none" {
		info.ContainerRuntime = containerRuntime
	}

	return info
}

// detectLinuxVersion detects Linux distribution version.
func detectLinuxVersion(ctx context.Context, runner CommandRunner) string {
	// Try /etc/os-release first
	if out, err := runner.Run(ctx, "cat", "/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "VERSION_ID=") {
				return strings.TrimPrefix(line, "VERSION_ID=")
			}
		}
	}

	// Try uname -r as fallback
	if out, err := runner.Run(ctx, "uname", "-r"); err == nil {
		// Parse kernel version from uname output
		// e.g., "6.8.0-48-generic"
		parts := strings.Fields(string(out))
		if len(parts) >= 1 {
			return parts[0] // Kernel version
		}
	}

	return ""
}

// detectContainerRuntime detects if K3S or Docker is installed and available.
func detectContainerRuntime(ctx context.Context, runner CommandRunner) string {
	// Check for K3S
	if _, err := runner.Run(ctx, "k3s", "version"); err == nil {
		return "k3s"
	}

	// Check for Docker daemon
	if _, err := runner.Run(ctx, "docker", "version"); err == nil {
		return "docker"
	}

	return "none"
}

func detectOS() OSInfo {
	return OSInfo{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}
}

func detectStorage() StorageInfo {
	dataDir := os.Getenv("AIMA_DATA_DIR")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		dataDir = filepath.Join(home, ".aima")
	}

	freeMiB := diskFreeMiB(dataDir)

	return StorageInfo{
		DataDirPath: dataDir,
		FreeMiB:     freeMiB,
	}
}

// collectMetricsWithRunner gathers real-time metrics using given CommandRunner.
func collectMetricsWithRunner(ctx context.Context, runner CommandRunner) (*Metrics, error) {
	m := &Metrics{}

	m.GPU = collectGPUMetrics(ctx, runner)
	m.CPU = collectCPUMetrics(ctx, runner)
	m.RAM = collectRAMMetrics(ctx, runner)

	// Unified memory GPUs: nvidia-smi can't report GPU memory separately.
	// Since GPU and CPU share the same pool, use RAM metrics.
	if m.GPU != nil && m.GPU.MemoryTotalMiB == 0 && m.RAM.TotalMiB > 0 {
		m.GPU.MemoryTotalMiB = m.RAM.TotalMiB
		m.GPU.MemoryUsedMiB = m.RAM.UsedMiB
	}

	return m, nil
}
