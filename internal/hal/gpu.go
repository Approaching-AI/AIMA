package hal

import (
	"context"
	"log/slog"
	"math"
	"strconv"
	"strings"
)

var nvidiaSMIQueryArgs = []string{
	"--query-gpu=name,memory.total,driver_version,compute_cap,power.draw,power.limit,temperature.gpu",
	"--format=csv,noheader,nounits",
}

var nvidiaSMIMetricsArgs = []string{
	"--query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw",
	"--format=csv,noheader,nounits",
}

func detectGPU(ctx context.Context, runner CommandRunner) *GPUInfo {
	out, err := runner.Run(ctx, "nvidia-smi", nvidiaSMIQueryArgs...)
	if err != nil {
		slog.Debug("nvidia-smi not available", "error", err)
		return nil
	}
	return parseGPUInfo(string(out))
}

func parseGPUInfo(output string) *GPUInfo {
	lines := nonEmptyLines(output)
	if len(lines) == 0 {
		return nil
	}

	// Parse first GPU line for primary info
	gpu, ok := parseGPULine(lines[0])
	if !ok {
		return nil
	}
	gpu.Count = len(lines)
	return gpu
}

func parseGPULine(line string) (*GPUInfo, bool) {
	fields := splitCSV(line)
	if len(fields) < 7 {
		return nil, false
	}

	vram, err := strconv.Atoi(fields[1])
	if err != nil {
		return nil, false
	}

	powerDraw, _ := strconv.ParseFloat(fields[4], 64)
	powerLimit, _ := strconv.ParseFloat(fields[5], 64)
	temp, _ := strconv.ParseFloat(fields[6], 64)

	cc := fields[3]
	return &GPUInfo{
		Name:               fields[0],
		VRAMMiB:            vram,
		DriverVersion:      fields[2],
		ComputeCapability:  cc,
		Arch:               computeCapToArch(cc),
		PowerDrawWatts:     powerDraw,
		PowerLimitWatts:    powerLimit,
		TemperatureCelsius: temp,
		Count:              1,
	}, true
}

func computeCapToArch(cc string) string {
	if cc == "" {
		return "unknown"
	}
	major, minor := parseVersion(cc)
	if major < 0 {
		return "unknown"
	}

	switch {
	case major >= 10:
		return "Blackwell"
	case major == 9:
		return "Hopper"
	case major == 8 && minor == 9:
		return "Ada"
	case major == 8:
		return "Ampere"
	case major == 7 && minor >= 5:
		return "Turing"
	case major == 7:
		return "Volta"
	case major == 6:
		return "Pascal"
	default:
		return "unknown"
	}
}

func parseVersion(v string) (int, int) {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) < 1 {
		return -1, -1
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return -1, -1
	}
	minor := 0
	if len(parts) == 2 {
		minor, _ = strconv.Atoi(parts[1])
	}
	return major, minor
}

func collectGPUMetrics(ctx context.Context, runner CommandRunner) *GPUMetrics {
	out, err := runner.Run(ctx, "nvidia-smi", nvidiaSMIMetricsArgs...)
	if err != nil {
		return nil
	}
	return parseGPUMetrics(string(out))
}

func parseGPUMetrics(output string) *GPUMetrics {
	lines := nonEmptyLines(output)
	if len(lines) == 0 {
		return nil
	}

	fields := splitCSV(lines[0])
	if len(fields) < 5 {
		return nil
	}

	util, err := strconv.Atoi(fields[0])
	if err != nil {
		return nil
	}
	memUsed, err := strconv.Atoi(fields[1])
	if err != nil {
		return nil
	}
	memTotal, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil
	}
	temp, _ := strconv.ParseFloat(fields[3], 64)
	power, _ := strconv.ParseFloat(fields[4], 64)

	return &GPUMetrics{
		UtilizationPercent: util,
		MemoryUsedMiB:      memUsed,
		MemoryTotalMiB:     memTotal,
		TemperatureCelsius: temp,
		PowerDrawWatts:     roundTo(power, 2),
	}
}

// nonEmptyLines splits output into non-empty trimmed lines.
func nonEmptyLines(s string) []string {
	var result []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

// splitCSV splits a CSV line and trims whitespace from each field.
func splitCSV(line string) []string {
	parts := strings.Split(line, ",")
	result := make([]string, len(parts))
	for i, p := range parts {
		result[i] = strings.TrimSpace(p)
	}
	return result
}

func roundTo(val float64, places int) float64 {
	shift := math.Pow(10, float64(places))
	return math.Round(val*shift) / shift
}
