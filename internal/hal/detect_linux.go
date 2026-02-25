//go:build linux

package hal

import (
	"bufio"
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
)

func detectCPU(ctx context.Context, runner CommandRunner) CPUInfo {
	info := CPUInfo{
		Arch: runtime.GOARCH,
	}

	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		info.Cores = runtime.NumCPU()
		info.Threads = runtime.NumCPU()
		return info
	}
	defer f.Close()

	var cores, threads int
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		key, val, ok := parseProcLine(line)
		if !ok {
			continue
		}
		switch key {
		case "model name":
			if info.Model == "" {
				info.Model = val
			}
		case "cpu cores":
			if n, err := strconv.Atoi(val); err == nil {
				cores = n
			}
		case "siblings":
			if n, err := strconv.Atoi(val); err == nil {
				threads = n
			}
		case "cpu MHz":
			if info.FreqGHz == 0 {
				if mhz, err := strconv.ParseFloat(val, 64); err == nil {
					info.FreqGHz = mhz / 1000.0
				}
			}
		}
	}

	if cores > 0 {
		info.Cores = cores
	} else {
		info.Cores = runtime.NumCPU()
	}
	if threads > 0 {
		info.Threads = threads
	} else {
		info.Threads = runtime.NumCPU()
	}

	return info
}

func parseProcLine(line string) (string, string, bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:]), true
}

func detectRAM(ctx context.Context, runner CommandRunner) RAMInfo {
	info := RAMInfo{}

	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return info
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		key, val, ok := parseProcLine(line)
		if !ok {
			continue
		}
		// Values in /proc/meminfo are in kB
		kbStr := strings.TrimSuffix(val, " kB")
		kb, err := strconv.ParseInt(strings.TrimSpace(kbStr), 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			info.TotalMiB = int(kb / 1024)
		case "MemAvailable":
			info.AvailableMiB = int(kb / 1024)
		}
	}

	return info
}

func collectCPUMetrics() CPUMetrics {
	// Reading /proc/stat requires two samples with a delay to compute usage.
	// For simplicity we return 0; a future enhancement can sample /proc/stat.
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
