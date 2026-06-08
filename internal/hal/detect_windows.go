//go:build windows

package hal

import (
	"context"
	"log/slog"
	"runtime"
)

// Windows hardware detection via CIM. Win32 WMIC was removed in Windows 11 24H2+,
// so every query shells out to PowerShell's Get-CimInstance and is parsed by the
// build-tag-free helpers in cim.go.

const (
	cimCPUScript      = "Get-CimInstance Win32_Processor | Select-Object Name,NumberOfCores,NumberOfLogicalProcessors,MaxClockSpeed | ConvertTo-Json -Compress"
	cimRAMScript      = "Get-CimInstance Win32_OperatingSystem | Select-Object TotalVisibleMemorySize,FreePhysicalMemory | ConvertTo-Json -Compress"
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
	return parseWindowsGPUs(string(out))
}
