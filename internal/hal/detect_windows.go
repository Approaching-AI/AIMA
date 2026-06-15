//go:build windows

package hal

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Windows hardware detection via CIM. Win32 WMIC was removed in Windows 11 24H2+,
// so every query shells out to PowerShell's Get-CimInstance and is parsed by the
// build-tag-free helpers in cim.go.

const (
	cimCPUScript      = "Get-CimInstance Win32_Processor | Select-Object Name,NumberOfCores,NumberOfLogicalProcessors,MaxClockSpeed | ConvertTo-Json -Compress"
	cimRAMScript      = "Get-CimInstance Win32_OperatingSystem | Select-Object TotalVisibleMemorySize,FreePhysicalMemory | ConvertTo-Json -Compress"
	cimPhysMemScript  = "Get-CimInstance Win32_PhysicalMemory | Measure-Object -Property Capacity -Sum | Select-Object Sum | ConvertTo-Json -Compress"
	cimPageFileScript = "Get-CimInstance Win32_PageFileUsage | Select-Object AllocatedBaseSize | ConvertTo-Json -Compress"
	cimCPULoadScript  = "Get-CimInstance Win32_Processor | Select-Object LoadPercentage | ConvertTo-Json -Compress"
	cimGPUScript      = "Get-CimInstance Win32_VideoController | Select-Object Name,DriverVersion,AdapterCompatibility,PNPDeviceID,AdapterRAM | ConvertTo-Json -Compress"
)

func runCIM(ctx context.Context, runner CommandRunner, script string) ([]byte, error) {
	return runner.Run(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
}

func detectCPU(ctx context.Context, runner CommandRunner) CPUInfo {
	info := CPUInfo{
		Arch:    runtime.GOARCH,
		Cores:   runtime.NumCPU(),
		Threads: runtime.NumCPU(),
	}

	out, err := runCIM(ctx, runner, cimCPUScript)
	if err != nil {
		slog.Warn("CIM cpu detection failed, using defaults", "error", err)
		return info
	}

	parseCIMCPU(string(out), &info)
	return info
}

func detectRAM(ctx context.Context, runner CommandRunner) RAMInfo {
	info := RAMInfo{}

	out, err := runCIM(ctx, runner, cimRAMScript)
	if err != nil {
		slog.Warn("CIM RAM detection failed, using defaults", "error", err)
		return info
	}
	parseCIMRAM(string(out), &info)

	// Report true installed memory: unified-memory APUs (Strix Halo) expose only
	// a fraction to the OS, carving the rest out for the iGPU.
	if physOut, err := runCIM(ctx, runner, cimPhysMemScript); err == nil {
		applyInstalledMemoryTotal(&info, parseCIMInstalledMemoryBytes(string(physOut)))
	}

	// Pagefile (swap) is best-effort; system-managed hosts may report nothing.
	if swapOut, err := runCIM(ctx, runner, cimPageFileScript); err == nil {
		parseCIMSwap(string(swapOut), &info)
	}

	return info
}

func collectCPUMetrics(ctx context.Context, runner CommandRunner) CPUMetrics {
	out, err := runCIM(ctx, runner, cimCPULoadScript)
	if err != nil {
		slog.Warn("CIM CPU metrics failed, using defaults", "error", err)
		return CPUMetrics{}
	}
	return CPUMetrics{UsagePercent: parseCIMCPULoad(string(out))}
}

func collectRAMMetrics(ctx context.Context, runner CommandRunner) RAMMetrics {
	ram := detectRAM(ctx, runner)
	used := ram.TotalMiB - ram.AvailableMiB
	if used < 0 {
		used = 0
	}
	return RAMMetrics{
		TotalMiB:     ram.TotalMiB,
		AvailableMiB: ram.AvailableMiB,
		UsedMiB:      used,
	}
}

// detectPlatformGPU detects GPUs via Win32_VideoController (CIM) as a Windows
// fallback for when no vendor SMI tool (nvidia-smi/rocm-smi) is on PATH — the
// common case on AMD APU hosts. See parseWindowsGPUs for the limitations.
func detectPlatformGPU(ctx context.Context, runner CommandRunner) *GPUInfo {
	out, err := runCIM(ctx, runner, cimGPUScript)
	if err != nil {
		slog.Debug("CIM GPU detection unavailable", "error", err)
		return nil
	}
	gpu := parseWindowsGPUs(string(out))
	// AMD APU VRAM: Win32 AdapterRAM saturates at 4 GiB and there is no rocm-smi,
	// so read the real GPU-addressable pool (dedicated VRAM + GTT) from the
	// ROCm-capable llama.cpp engine when available. Without it, the unified-memory
	// backfill (= installed RAM) applies downstream — which over-states the
	// usable VRAM on hosts where the OS carves the pool (e.g. Strix Halo).
	if gpu != nil && gpu.Vendor == "amd" && gpu.UnifiedMemory && gpu.VRAMMiB == 0 {
		if mib := amdAPUVRAMMiBFromEngine(ctx, runner); mib > 0 {
			gpu.VRAMMiB = mib
		}
	}
	return gpu
}

// amdAPUVRAMMiBFromEngine asks a ROCm-capable llama.cpp binary to enumerate its
// devices and returns the iGPU's total VRAM (MiB), or 0 if no engine is found.
func amdAPUVRAMMiBFromEngine(ctx context.Context, runner CommandRunner) int {
	llama := findROCmLlamaBinary()
	if llama == "" {
		return 0
	}
	// llama.cpp prints the device list to stderr; fold it into stdout (the only
	// stream execRunner captures) via PowerShell redirection.
	out, _ := runner.Run(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command",
		"& '"+llama+"' --list-devices 2>&1")
	return parseLlamaROCmVRAMMiB(string(out))
}

// findROCmLlamaBinary locates a llama.cpp CLI, preferring AIMA's configured
// engine directory (AIMA_ENGINE_DIR) and falling back to PATH.
func findROCmLlamaBinary() string {
	if dir := strings.TrimSpace(os.Getenv("AIMA_ENGINE_DIR")); dir != "" {
		for _, name := range []string{"llama-cli.exe", "llama-server.exe"} {
			p := filepath.Join(dir, name)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	for _, name := range []string{"llama-cli.exe", "llama-cli"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}
