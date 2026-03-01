package hal

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"strconv"
	"strings"
)

// isNA returns true if the string is a variant of N/A or Not Supported.
func isNA(s string) bool {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "[]")
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	return lower == "n/a" || lower == "not supported" || lower == ""
}

// --- Probe chain types ---

type gpuProbe struct {
	vendor string
	cmd    string
	args   []string
	parse  func(string) *GPUInfo
}

type gpuMetricsProbe struct {
	vendor string
	cmd    string
	args   []string
	parse  func(string) *GPUMetrics
}

// NVIDIA args
var nvidiaSMIQueryArgs = []string{
	"--query-gpu=name,memory.total,driver_version,compute_cap,power.draw,power.limit,temperature.gpu",
	"--format=csv,noheader,nounits",
}

var nvidiaSMIMetricsArgs = []string{
	"--query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw",
	"--format=csv,noheader,nounits",
}

// AMD args
var rocmSMIArgs = []string{"--json", "--showproductname", "--showmeminfo", "vram", "--showtemp", "--showpower"}
var rocmSMIMetricsArgs = []string{"--json", "--showuse", "--showmeminfo", "vram", "--showtemp", "--showpower"}

// Intel args
var xpuSMIArgs = []string{"discovery", "--json"}
var xpuSMIMetricsArgs = []string{"stats", "--json"}

// Huawei args
var npuSMIArgs = []string{"info", "-t", "common", "-j"}
var npuSMIMetricsArgs = []string{"info", "-t", "usages", "-j"}

// MThreads args
var mthreadsGMIArgs = []string{"-q", "-j"}
var mthreadsGMIMetricsArgs = []string{"--metrics", "-j"}

var gpuProbes = []gpuProbe{
	{"nvidia", "nvidia-smi", nvidiaSMIQueryArgs, parseNvidiaGPU},
	{"amd", "rocm-smi", rocmSMIArgs, parseAMDGPU},
	{"intel", "xpu-smi", xpuSMIArgs, parseIntelGPU},
	{"huawei", "npu-smi", npuSMIArgs, parseHuaweiNPU},
	{"mthreads", "mthreads-gmi", mthreadsGMIArgs, parseMThreadsGPU},
}

var gpuMetricsProbes = []gpuMetricsProbe{
	{"nvidia", "nvidia-smi", nvidiaSMIMetricsArgs, parseNvidiaGPUMetrics},
	{"amd", "rocm-smi", rocmSMIMetricsArgs, parseAMDGPUMetrics},
	{"intel", "xpu-smi", xpuSMIMetricsArgs, parseIntelGPUMetrics},
	{"huawei", "npu-smi", npuSMIMetricsArgs, parseHuaweiNPUMetrics},
	{"mthreads", "mthreads-gmi", mthreadsGMIMetricsArgs, parseMThreadsGPUMetrics},
}

func detectGPU(ctx context.Context, runner CommandRunner) *GPUInfo {
	for _, p := range gpuProbes {
		out, err := runner.Run(ctx, p.cmd, p.args...)
		if err != nil {
			slog.Debug("gpu probe not available", "vendor", p.vendor, "error", err)
			continue
		}
		if gpu := p.parse(string(out)); gpu != nil {
			gpu.Vendor = p.vendor
			enrichGPU(ctx, runner, gpu)
			return gpu
		}
	}
	return nil
}

func collectGPUMetrics(ctx context.Context, runner CommandRunner) *GPUMetrics {
	for _, p := range gpuMetricsProbes {
		out, err := runner.Run(ctx, p.cmd, p.args...)
		if err != nil {
			continue
		}
		if m := p.parse(string(out)); m != nil {
			return m
		}
	}
	return nil
}

// --- NVIDIA ---

func parseNvidiaGPU(output string) *GPUInfo {
	lines := nonEmptyLines(output)
	if len(lines) == 0 {
		return nil
	}
	gpu := parseNvidiaGPULine(lines[0])
	if gpu == nil {
		return nil
	}
	gpu.Count = len(lines)
	return gpu
}

func parseNvidiaGPULine(line string) *GPUInfo {
	fields := splitCSV(line)
	if len(fields) < 7 {
		return nil
	}

	name := fields[0]
	if isNA(name) {
		return nil
	}

	var vram int
	var memIsNA bool
	if !isNA(fields[1]) {
		vram, _ = strconv.Atoi(fields[1])
	} else {
		memIsNA = true
	}

	var driverVersion string
	if !isNA(fields[2]) {
		driverVersion = fields[2]
	}

	var cc string
	if !isNA(fields[3]) {
		cc = fields[3]
	}

	var powerDraw, powerLimit, temp float64
	if !isNA(fields[4]) {
		powerDraw, _ = strconv.ParseFloat(fields[4], 64)
	}
	if !isNA(fields[5]) {
		powerLimit, _ = strconv.ParseFloat(fields[5], 64)
	}
	if !isNA(fields[6]) {
		temp, _ = strconv.ParseFloat(fields[6], 64)
	}

	return &GPUInfo{
		Name:               name,
		VRAMMiB:            vram,
		DriverVersion:      driverVersion,
		ComputeID:          cc,
		Arch:               computeCapToArch(cc),
		PowerDrawWatts:     powerDraw,
		PowerLimitWatts:    powerLimit,
		TemperatureCelsius: temp,
		UnifiedMemory:      memIsNA,
		Count:              1,
	}
}

func parseNvidiaGPUMetrics(output string) *GPUMetrics {
	lines := nonEmptyLines(output)
	if len(lines) == 0 {
		return nil
	}

	fields := splitCSV(lines[0])
	if len(fields) < 5 {
		return nil
	}

	// If all critical fields are N/A, metrics are useless
	if isNA(fields[0]) && isNA(fields[1]) && isNA(fields[2]) {
		return nil
	}

	var util, memUsed, memTotal int
	if !isNA(fields[0]) {
		util, _ = strconv.Atoi(fields[0])
	}
	if !isNA(fields[1]) {
		memUsed, _ = strconv.Atoi(fields[1])
	}
	if !isNA(fields[2]) {
		memTotal, _ = strconv.Atoi(fields[2])
	}

	var temp, power float64
	if !isNA(fields[3]) {
		temp, _ = strconv.ParseFloat(fields[3], 64)
	}
	if !isNA(fields[4]) {
		power, _ = strconv.ParseFloat(fields[4], 64)
	}

	return &GPUMetrics{
		UtilizationPercent: util,
		MemoryUsedMiB:      memUsed,
		MemoryTotalMiB:     memTotal,
		TemperatureCelsius: temp,
		PowerDrawWatts:     roundTo(power, 2),
	}
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

var gpuEnrichers = map[string]func(context.Context, CommandRunner, *GPUInfo){
	"nvidia": enrichNvidiaGPU,
	"amd":    enrichAMDGPU,
}

// enrichGPU fills in fields that the primary probe couldn't provide.
func enrichGPU(ctx context.Context, runner CommandRunner, gpu *GPUInfo) {
	if fn, ok := gpuEnrichers[gpu.Vendor]; ok {
		fn(ctx, runner, gpu)
	}
}

// enrichNvidiaGPU supplements GPUInfo with data from standard nvidia-smi output.
// The CSV query format lacks CUDA version and may lack power limit on some platforms.
func enrichNvidiaGPU(ctx context.Context, runner CommandRunner, gpu *GPUInfo) {
	out, err := runner.Run(ctx, "nvidia-smi")
	if err != nil {
		return
	}
	s := string(out)

	if gpu.SDKVersion == "" {
		if ver := parseNvidiaCUDAVersion(s); ver != "" {
			gpu.SDKVersion = "CUDA " + ver
		}
	}
	if gpu.PowerLimitWatts == 0 {
		gpu.PowerLimitWatts = parseNvidiaPowerCap(s)
	}
}

// enrichAMDGPU supplements GPUInfo with SDK and driver version from system tools.
func enrichAMDGPU(ctx context.Context, runner CommandRunner, gpu *GPUInfo) {
	if gpu.SDKVersion == "" {
		if out, err := runner.Run(ctx, "cat", "/opt/rocm/.info/version"); err == nil {
			if ver := strings.TrimSpace(string(out)); ver != "" {
				gpu.SDKVersion = "ROCm " + ver
			}
		}
	}
	if gpu.DriverVersion == "" {
		// Try modinfo first; amdgpu built into kernel often has no version field,
		// so fall back to kernel version (uname -r) since amdgpu ships with the kernel.
		if out, err := runner.Run(ctx, "modinfo", "-F", "version", "amdgpu"); err == nil {
			if ver := strings.TrimSpace(string(out)); ver != "" {
				gpu.DriverVersion = ver
			}
		}
		if gpu.DriverVersion == "" {
			if out, err := runner.Run(ctx, "uname", "-r"); err == nil {
				if ver := strings.TrimSpace(string(out)); ver != "" {
					gpu.DriverVersion = ver
				}
			}
		}
	}
}

// parseNvidiaCUDAVersion extracts CUDA version from nvidia-smi standard output header.
func parseNvidiaCUDAVersion(output string) string {
	for _, line := range strings.Split(output, "\n") {
		idx := strings.Index(line, "CUDA Version:")
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(line[idx+len("CUDA Version:"):])
		end := 0
		for end < len(rest) && (rest[end] == '.' || (rest[end] >= '0' && rest[end] <= '9')) {
			end++
		}
		if end > 0 {
			return rest[:end]
		}
	}
	return ""
}

// parseNvidiaPowerCap extracts power cap from nvidia-smi "Pwr:Usage/Cap" column.
func parseNvidiaPowerCap(output string) float64 {
	for _, line := range strings.Split(output, "\n") {
		idx := strings.Index(line, "W /")
		if idx < 0 {
			continue
		}
		rest := line[idx+3:]
		if pipeIdx := strings.Index(rest, "|"); pipeIdx >= 0 {
			rest = rest[:pipeIdx]
		}
		rest = strings.TrimSpace(rest)
		rest = strings.TrimSuffix(rest, "W")
		rest = strings.TrimSpace(rest)
		if isNA(rest) {
			continue
		}
		cap, err := strconv.ParseFloat(rest, 64)
		if err == nil && cap > 0 {
			return cap
		}
	}
	return 0
}

// --- AMD ---

func parseAMDGPU(output string) *GPUInfo {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil
	}

	var firstCard map[string]interface{}
	count := 0
	for key, val := range raw {
		if !strings.HasPrefix(key, "card") {
			continue
		}
		count++
		if firstCard == nil {
			var data map[string]interface{}
			if json.Unmarshal(val, &data) == nil {
				firstCard = data
			}
		}
	}
	if firstCard == nil {
		return nil
	}

	name := jsonStr(firstCard, "Card Series", "Card series")
	if name == "" {
		return nil
	}

	// Determine arch: prefer name matching, fall back to GFX version.
	arch := amdGPUToArch(name)
	if arch == "unknown" {
		if gfxArch := gfxVersionToArch(jsonStr(firstCard, "GFX Version")); gfxArch != "" {
			arch = gfxArch
		}
	}

	var vram int
	if b := jsonInt(firstCard, "VRAM Total Memory (B)"); b > 0 {
		vram = int(b / (1024 * 1024))
	}

	return &GPUInfo{
		Name:               name,
		Arch:               arch,
		ComputeID:          jsonStr(firstCard, "GFX Version"),
		VRAMMiB:            vram,
		TemperatureCelsius: jsonFloat(firstCard, "Temperature (Sensor edge) (C)", "Temperature (Sensor junction) (C)"),
		PowerDrawWatts:     jsonFloat(firstCard, "Average Graphics Package Power (W)", "Current Socket Graphics Package Power (W)"),
		Count:              count,
	}
}

func parseAMDGPUMetrics(output string) *GPUMetrics {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil
	}

	for key, val := range raw {
		if !strings.HasPrefix(key, "card") {
			continue
		}
		var data map[string]interface{}
		if json.Unmarshal(val, &data) != nil {
			continue
		}

		util := int(jsonFloat(data, "GPU use (%)", "GPU Use (%)"))
		memUsed := int(jsonInt(data, "VRAM Total Used Memory (B)") / (1024 * 1024))
		memTotal := int(jsonInt(data, "VRAM Total Memory (B)") / (1024 * 1024))
		if memTotal == 0 && util == 0 {
			return nil
		}

		return &GPUMetrics{
			UtilizationPercent: util,
			MemoryUsedMiB:      memUsed,
			MemoryTotalMiB:     memTotal,
			TemperatureCelsius: jsonFloat(data, "Temperature (Sensor edge) (C)"),
			PowerDrawWatts:     roundTo(jsonFloat(data, "Average Graphics Package Power (W)", "Current Socket Graphics Package Power (W)"), 2),
		}
	}
	return nil
}

func amdGPUToArch(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "mi300"):
		return "CDNA3"
	case strings.Contains(lower, "mi250") || strings.Contains(lower, "mi210"):
		return "CDNA2"
	case strings.Contains(lower, "mi100"):
		return "CDNA"
	case strings.Contains(lower, "rx 7") || strings.Contains(lower, "pro w7"):
		return "RDNA3"
	case strings.Contains(lower, "rx 6") || strings.Contains(lower, "pro w6"):
		return "RDNA2"
	default:
		return "unknown"
	}
}

// gfxVersionToArch maps AMD GFX IP version strings to architecture names.
// Used as fallback when product name is too generic (e.g., "AMD Radeon Graphics").
func gfxVersionToArch(gfxVer string) string {
	gfxVer = strings.ToLower(strings.TrimSpace(gfxVer))
	if !strings.HasPrefix(gfxVer, "gfx") {
		return ""
	}
	suffix := gfxVer[3:]

	switch {
	case strings.HasPrefix(suffix, "12"):
		return "RDNA4"
	case strings.HasPrefix(suffix, "115"):
		return "RDNA3.5"
	case strings.HasPrefix(suffix, "11"):
		return "RDNA3"
	case strings.HasPrefix(suffix, "103"):
		return "RDNA2"
	case strings.HasPrefix(suffix, "101"):
		return "RDNA"
	case strings.HasPrefix(suffix, "94"):
		return "CDNA3"
	case suffix == "90a":
		return "CDNA2"
	case suffix == "908":
		return "CDNA"
	case strings.HasPrefix(suffix, "90"):
		return "GCN5"
	default:
		return ""
	}
}

// --- Intel ---

func parseIntelGPU(output string) *GPUInfo {
	var devices []map[string]interface{}
	if err := json.Unmarshal([]byte(output), &devices); err != nil {
		return nil
	}
	if len(devices) == 0 {
		return nil
	}

	name := jsonStr(devices[0], "device_name")
	if name == "" {
		return nil
	}

	var vram int
	if b := jsonInt(devices[0], "memory_physical_size_byte"); b > 0 {
		vram = int(b / (1024 * 1024))
	}

	return &GPUInfo{
		Name:    name,
		Arch:    intelGPUToArch(name),
		VRAMMiB: vram,
		Count:   len(devices),
	}
}

func parseIntelGPUMetrics(output string) *GPUMetrics {
	var devices []map[string]interface{}
	if err := json.Unmarshal([]byte(output), &devices); err != nil {
		return nil
	}
	if len(devices) == 0 {
		return nil
	}

	dev := devices[0]
	util := int(jsonFloat(dev, "gpu_utilization"))
	memUsed := int(jsonInt(dev, "memory_used_byte") / (1024 * 1024))
	memTotal := int(jsonInt(dev, "memory_physical_size_byte") / (1024 * 1024))
	if memTotal == 0 && util == 0 {
		return nil
	}

	return &GPUMetrics{
		UtilizationPercent: util,
		MemoryUsedMiB:      memUsed,
		MemoryTotalMiB:     memTotal,
		TemperatureCelsius: jsonFloat(dev, "gpu_temperature"),
		PowerDrawWatts:     roundTo(jsonFloat(dev, "power"), 2),
	}
}

func intelGPUToArch(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "max"):
		return "Xe HPC"
	case strings.Contains(lower, "flex") || strings.Contains(lower, "arc"):
		return "Xe HPG"
	default:
		return "unknown"
	}
}

// --- Huawei ---

func parseHuaweiNPU(output string) *GPUInfo {
	var raw struct {
		NPU []map[string]interface{} `json:"NPU"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil
	}
	if len(raw.NPU) == 0 {
		return nil
	}

	npu := raw.NPU[0]
	name := jsonStr(npu, "Name")
	if name == "" {
		return nil
	}

	return &GPUInfo{
		Name:               name,
		Arch:               huaweiNPUToArch(name),
		VRAMMiB:            int(jsonFloat(npu, "HBM Capacity(MB)")),
		TemperatureCelsius: jsonFloat(npu, "Temperature(C)"),
		Count:              len(raw.NPU),
	}
}

func parseHuaweiNPUMetrics(output string) *GPUMetrics {
	var raw struct {
		NPU []map[string]interface{} `json:"NPU"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil
	}
	if len(raw.NPU) == 0 {
		return nil
	}

	npu := raw.NPU[0]
	util := int(jsonFloat(npu, "Aicore Usage(%)"))
	memUsed := int(jsonFloat(npu, "HBM Usage(MB)"))
	memTotal := int(jsonFloat(npu, "HBM Capacity(MB)"))
	if memTotal == 0 && util == 0 {
		return nil
	}

	return &GPUMetrics{
		UtilizationPercent: util,
		MemoryUsedMiB:      memUsed,
		MemoryTotalMiB:     memTotal,
		TemperatureCelsius: jsonFloat(npu, "Temperature(C)"),
		PowerDrawWatts:     roundTo(jsonFloat(npu, "Power(W)"), 2),
	}
}

func huaweiNPUToArch(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "910b"):
		return "Ascend910B"
	case strings.Contains(lower, "910c"):
		return "Ascend910C"
	case strings.Contains(lower, "910"):
		return "Ascend910"
	case strings.Contains(lower, "310p"):
		return "Ascend310P"
	case strings.Contains(lower, "310"):
		return "Ascend310"
	default:
		return "unknown"
	}
}

// --- MThreads ---

func parseMThreadsGPU(output string) *GPUInfo {
	var raw struct {
		GPUs []map[string]interface{} `json:"gpus"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil
	}
	if len(raw.GPUs) == 0 {
		return nil
	}

	gpu := raw.GPUs[0]
	name := jsonStr(gpu, "product_name")
	if name == "" {
		return nil
	}

	return &GPUInfo{
		Name:               name,
		Arch:               mthreadsGPUToArch(name),
		VRAMMiB:            parseMiBString(jsonStr(gpu, "memory_total")),
		TemperatureCelsius: parseFloatPrefix(jsonStr(gpu, "temperature")),
		PowerDrawWatts:     parseFloatPrefix(jsonStr(gpu, "power_draw")),
		Count:              len(raw.GPUs),
	}
}

func parseMThreadsGPUMetrics(output string) *GPUMetrics {
	var raw struct {
		GPUs []map[string]interface{} `json:"gpus"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return nil
	}
	if len(raw.GPUs) == 0 {
		return nil
	}

	gpu := raw.GPUs[0]
	util := int(parseFloatPrefix(jsonStr(gpu, "gpu_utilization")))
	memUsed := parseMiBString(jsonStr(gpu, "memory_used"))
	memTotal := parseMiBString(jsonStr(gpu, "memory_total"))
	if memTotal == 0 && util == 0 {
		return nil
	}

	return &GPUMetrics{
		UtilizationPercent: util,
		MemoryUsedMiB:      memUsed,
		MemoryTotalMiB:     memTotal,
		TemperatureCelsius: parseFloatPrefix(jsonStr(gpu, "temperature")),
		PowerDrawWatts:     roundTo(parseFloatPrefix(jsonStr(gpu, "power_draw")), 2),
	}
}

func mthreadsGPUToArch(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "s4000"):
		return "MTT S4000"
	case strings.Contains(lower, "s3000"):
		return "MTT S3000"
	case strings.Contains(lower, "s80"):
		return "MTT S80"
	default:
		return "unknown"
	}
}

// --- JSON helpers ---

func jsonStr(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch s := v.(type) {
			case string:
				if s != "" {
					return s
				}
			case float64:
				return strconv.FormatFloat(s, 'f', -1, 64)
			}
		}
	}
	return ""
}

func jsonFloat(m map[string]interface{}, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch n := v.(type) {
			case float64:
				return n
			case string:
				f, _ := strconv.ParseFloat(strings.TrimSpace(n), 64)
				if f != 0 {
					return f
				}
			}
		}
	}
	return 0
}

func jsonInt(m map[string]interface{}, keys ...string) int64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch n := v.(type) {
			case float64:
				return int64(n)
			case string:
				i, _ := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
				if i != 0 {
					return i
				}
			}
		}
	}
	return 0
}

func parseMiBString(s string) int {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, " MiB")
	s = strings.TrimSuffix(s, " MB")
	v, _ := strconv.Atoi(s)
	return v
}

func parseFloatPrefix(s string) float64 {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && (s[i] == '.' || s[i] == '-' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	if i == 0 {
		return 0
	}
	f, _ := strconv.ParseFloat(s[:i], 64)
	return f
}

// --- Shared utilities ---

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
