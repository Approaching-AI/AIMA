//go:build windows

package hal

import (
	"context"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
)

func detectCPU(ctx context.Context, runner CommandRunner) CPUInfo {
	info := CPUInfo{
		Arch:    runtime.GOARCH,
		Cores:   runtime.NumCPU(),
		Threads: runtime.NumCPU(),
	}

	out, err := runner.Run(ctx, "wmic", "cpu", "get", "Name,NumberOfCores,NumberOfLogicalProcessors,MaxClockSpeed", "/format:csv")
	if err != nil {
		slog.Warn("wmic cpu detection failed, using defaults", "error", err)
		return info
	}

	parseWMICCPU(string(out), &info)
	return info
}

func parseWMICCPU(output string, info *CPUInfo) {
	// wmic csv output has a header line, then data lines.
	// Format: Node,MaxClockSpeed,Name,NumberOfCores,NumberOfLogicalProcessors
	lines := nonEmptyLines(output)
	if len(lines) < 2 {
		return
	}

	// Find column indices from header
	header := splitCSV(lines[0])
	colIdx := make(map[string]int)
	for i, h := range header {
		colIdx[strings.TrimSpace(h)] = i
	}

	fields := splitCSV(lines[1])

	if idx, ok := colIdx["Name"]; ok && idx < len(fields) {
		info.Model = fields[idx]
	}
	if idx, ok := colIdx["NumberOfCores"]; ok && idx < len(fields) {
		if n, err := strconv.Atoi(fields[idx]); err == nil {
			info.Cores = n
		}
	}
	if idx, ok := colIdx["NumberOfLogicalProcessors"]; ok && idx < len(fields) {
		if n, err := strconv.Atoi(fields[idx]); err == nil {
			info.Threads = n
		}
	}
	if idx, ok := colIdx["MaxClockSpeed"]; ok && idx < len(fields) {
		if mhz, err := strconv.ParseFloat(fields[idx], 64); err == nil {
			info.FreqGHz = mhz / 1000.0
		}
	}
}

func detectRAM(ctx context.Context, runner CommandRunner) RAMInfo {
	info := RAMInfo{}

	out, err := runner.Run(ctx, "wmic", "os", "get", "TotalVisibleMemorySize,FreePhysicalMemory", "/format:csv")
	if err != nil {
		slog.Warn("wmic RAM detection failed, using defaults", "error", err)
		return info
	}

	parseWMICRAM(string(out), &info)

	// Detect swap (pagefile) size
	if swapOut, err := runner.Run(ctx, "wmic", "pagefile", "get", "AllocatedBaseSize", "/format:csv"); err == nil {
		parseWMICSwap(string(swapOut), &info)
	}

	return info
}

func parseWMICSwap(output string, info *RAMInfo) {
	lines := nonEmptyLines(output)
	if len(lines) < 2 {
		return
	}
	header := splitCSV(lines[0])
	colIdx := make(map[string]int)
	for i, h := range header {
		colIdx[strings.TrimSpace(h)] = i
	}
	fields := splitCSV(lines[1])
	if idx, ok := colIdx["AllocatedBaseSize"]; ok && idx < len(fields) {
		if mb, err := strconv.Atoi(strings.TrimSpace(fields[idx])); err == nil {
			info.SwapTotalMiB = mb
		}
	}
}

func parseWMICRAM(output string, info *RAMInfo) {
	// Format: Node,FreePhysicalMemory,TotalVisibleMemorySize
	lines := nonEmptyLines(output)
	if len(lines) < 2 {
		return
	}

	header := splitCSV(lines[0])
	colIdx := make(map[string]int)
	for i, h := range header {
		colIdx[strings.TrimSpace(h)] = i
	}

	fields := splitCSV(lines[1])

	if idx, ok := colIdx["TotalVisibleMemorySize"]; ok && idx < len(fields) {
		if kb, err := strconv.ParseInt(fields[idx], 10, 64); err == nil {
			info.TotalMiB = int(kb / 1024)
		}
	}
	if idx, ok := colIdx["FreePhysicalMemory"]; ok && idx < len(fields) {
		if kb, err := strconv.ParseInt(fields[idx], 10, 64); err == nil {
			info.AvailableMiB = int(kb / 1024)
		}
	}
}

func collectCPUMetrics(ctx context.Context, runner CommandRunner) CPUMetrics {
	out, err := runner.Run(ctx, "wmic", "cpu", "get", "LoadPercentage", "/format:csv")
	if err != nil {
		slog.Warn("wmic CPU metrics failed, using defaults", "error", err)
		return CPUMetrics{}
	}

	lines := nonEmptyLines(string(out))
	if len(lines) < 2 {
		return CPUMetrics{}
	}

	header := splitCSV(lines[0])
	colIdx := make(map[string]int)
	for i, h := range header {
		colIdx[strings.TrimSpace(h)] = i
	}

	fields := splitCSV(lines[1])
	if idx, ok := colIdx["LoadPercentage"]; ok && idx < len(fields) {
		if pct, err := strconv.ParseFloat(fields[idx], 64); err == nil {
			return CPUMetrics{UsagePercent: pct}
		}
	}
	return CPUMetrics{}
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
