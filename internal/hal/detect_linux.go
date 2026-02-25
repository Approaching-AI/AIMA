//go:build linux

package hal

import (
	"bufio"
	"context"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
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

	// ARM fallback: /proc/cpuinfo lacks model name and frequency
	if info.Model == "" || info.FreqGHz == 0 {
		if out, err := runner.Run(ctx, "lscpu"); err == nil {
			parseLscpu(string(out), &info)
		}
	}

	return info
}

func parseLscpu(output string, info *CPUInfo) {
	for _, line := range strings.Split(output, "\n") {
		key, val, ok := cutLscpuLine(line)
		if !ok {
			continue
		}

		switch key {
		case "Model name", "型号名称":
			if info.Model == "" {
				info.Model = val
			}
		case "CPU max MHz", "CPU 最大 MHz":
			if info.FreqGHz == 0 {
				if mhz, err := strconv.ParseFloat(val, 64); err == nil {
					info.FreqGHz = mhz / 1000.0
				}
			}
		}
	}
}

func cutLscpuLine(line string) (string, string, bool) {
	// Try ASCII colon first, then fullwidth colon (Chinese locale)
	for _, sep := range []string{":", "："} {
		if idx := strings.Index(line, sep); idx >= 0 {
			return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+len(sep):]), true
		}
	}
	return "", "", false
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

type cpuSample struct {
	idle  uint64
	total uint64
}

func readCPUSample() *cpuSample {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return nil
	}
	line := scanner.Text()
	if !strings.HasPrefix(line, "cpu ") {
		return nil
	}

	fields := strings.Fields(line)
	if len(fields) < 5 {
		return nil
	}

	var total, idle uint64
	for i, field := range fields[1:] {
		v, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			continue
		}
		total += v
		if i == 3 { // user, nice, system, idle
			idle = v
		}
	}
	return &cpuSample{idle: idle, total: total}
}

func collectCPUMetrics(_ context.Context, _ CommandRunner) CPUMetrics {
	s1 := readCPUSample()
	if s1 == nil {
		return CPUMetrics{}
	}
	time.Sleep(200 * time.Millisecond)
	s2 := readCPUSample()
	if s2 == nil {
		return CPUMetrics{}
	}

	totalDelta := s2.total - s1.total
	idleDelta := s2.idle - s1.idle
	if totalDelta == 0 {
		return CPUMetrics{}
	}

	usage := float64(totalDelta-idleDelta) / float64(totalDelta) * 100
	return CPUMetrics{UsagePercent: math.Round(usage*10) / 10}
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
