package hal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	hw.CPU = detectCPU(ctx, runner)
	hw.RAM = detectRAM(ctx, runner)
	hw.Storage = detectStorage()

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
	m.CPU = collectCPUMetrics()
	m.RAM = collectRAMMetrics(ctx, runner)

	return m, nil
}
