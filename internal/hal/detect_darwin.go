//go:build darwin

package hal

import (
	"context"
	"math"
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

	if out, err := runner.Run(ctx, "sysctl", "-n", "machdep.cpu.brand_string"); err == nil {
		info.Model = strings.TrimSpace(string(out))
	}

	if out, err := runner.Run(ctx, "sysctl", "-n", "hw.physicalcpu"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
			info.Cores = n
		}
	}

	if out, err := runner.Run(ctx, "sysctl", "-n", "hw.logicalcpu"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
			info.Threads = n
		}
	}

	if out, err := runner.Run(ctx, "sysctl", "-n", "hw.cpufrequency"); err == nil {
		if hz, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil {
			info.FreqGHz = hz / 1e9
		}
	}

	return info
}

func detectRAM(ctx context.Context, runner CommandRunner) RAMInfo {
	info := RAMInfo{}

	if out, err := runner.Run(ctx, "sysctl", "-n", "hw.memsize"); err == nil {
		if bytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
			info.TotalMiB = int(bytes / (1024 * 1024))
		}
	}

	// macOS doesn't have MemAvailable like Linux. Use vm_stat to estimate.
	if out, err := runner.Run(ctx, "vm_stat"); err == nil {
		info.AvailableMiB = parseVMStatAvailable(string(out))
	}

	// Parse swap from "sysctl vm.swapusage" — e.g. "total = 2048.00M  used = ..."
	if out, err := runner.Run(ctx, "sysctl", "vm.swapusage"); err == nil {
		info.SwapTotalMiB = parseSwapUsage(string(out))
	}

	return info
}

// parseSwapUsage extracts total swap from sysctl vm.swapusage output.
// Example: "vm.swapusage: total = 2048.00M  used = 1024.00M  free = 1024.00M ..."
func parseSwapUsage(output string) int {
	idx := strings.Index(output, "total = ")
	if idx < 0 {
		return 0
	}
	rest := output[idx+len("total = "):]
	// Find the number before 'M'
	mIdx := strings.Index(rest, "M")
	if mIdx < 0 {
		return 0
	}
	valStr := strings.TrimSpace(rest[:mIdx])
	if mb, err := strconv.ParseFloat(valStr, 64); err == nil {
		return int(mb)
	}
	return 0
}

func parseVMStatAvailable(output string) int {
	// vm_stat reports page counts. Page size is typically 16384 on Apple Silicon, 4096 on Intel.
	var pageSize int64 = 4096
	var freePages, inactivePages int64

	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "Mach Virtual Memory Statistics") {
			// Extract page size from header: "... (page size of XXXX bytes)"
			if idx := strings.Index(line, "page size of "); idx >= 0 {
				rest := line[idx+len("page size of "):]
				rest = strings.TrimSuffix(rest, " bytes)")
				if ps, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64); err == nil {
					pageSize = ps
				}
			}
			continue
		}
		key, val := parseVMStatLine(line)
		switch key {
		case "Pages free":
			freePages = val
		case "Pages inactive":
			inactivePages = val
		}
	}

	availableBytes := (freePages + inactivePages) * pageSize
	return int(availableBytes / (1024 * 1024))
}

func parseVMStatLine(line string) (string, int64) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", 0
	}
	key := strings.TrimSpace(line[:idx])
	valStr := strings.TrimSpace(line[idx+1:])
	valStr = strings.TrimSuffix(valStr, ".")
	val, _ := strconv.ParseInt(valStr, 10, 64)
	return key, val
}

func collectCPUMetrics(ctx context.Context, runner CommandRunner) CPUMetrics {
	out, err := runner.Run(ctx, "ps", "-A", "-o", "%cpu")
	if err != nil {
		return CPUMetrics{}
	}

	var total float64
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if v, err := strconv.ParseFloat(line, 64); err == nil {
			total += v
		}
	}

	// ps reports per-core percentages; normalize to 0-100 range
	cpus := runtime.NumCPU()
	if cpus > 0 {
		total = total / float64(cpus)
	}
	if total > 100 {
		total = 100
	}

	return CPUMetrics{UsagePercent: math.Round(total*10) / 10}
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
