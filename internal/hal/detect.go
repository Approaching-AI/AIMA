package hal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// execRunner is the real CommandRunner that executes system commands.
type execRunner struct{}

func (r *execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// detectWithRunner performs hardware detection using the given CommandRunner.
func detectWithRunner(ctx context.Context, runner CommandRunner) (*HardwareInfo, error) {
	hw := &HardwareInfo{
		OS: detectOS(),
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
	// When GPU VRAM ≈ system RAM and the card isn't a known datacenter GPU, flag as unified.
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

// collectMetricsWithRunner gathers real-time metrics using the given CommandRunner.
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
