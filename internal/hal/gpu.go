package hal

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
)

// bytesToMiB converts bytes to mebibytes.
func bytesToMiB(b int64) int {
	return int(b / (1024 * 1024))
}

// parseUsedTotal splits "used / total" strings and returns both values.
func parseUsedTotal(s string) (used, total int) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	used, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
	total, _ = strconv.Atoi(strings.TrimSpace(parts[1]))
	return
}

// huaweiNPUJSON is the JSON structure returned by npu-smi with -j flag.
type huaweiNPUJSON struct {
	NPU []map[string]interface{} `json:"NPU"`
}

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

// Huawei args — plain "info" (no -j): universally supported across npu-smi versions
var npuSMIArgs = []string{"info"}
var npuSMIMetricsArgs = []string{"info"}

// MThreads args
var mthreadsGMIArgs = []string{"-q", "-j"}
var mthreadsGMIMetricsArgs = []string{"--metrics", "-j"}

// MetaX args — no args for standard table output (has name, memory, temp, power, utilization)
var metaxSMIArgs = []string{}
var metaxSMIMetricsArgs = []string{}

var gpuProbes = []gpuProbe{
	{"nvidia", "nvidia-smi", nvidiaSMIQueryArgs, parseNvidiaGPU},
	{"amd", "rocm-smi", rocmSMIArgs, parseAMDGPU},
	{"intel", "xpu-smi", xpuSMIArgs, parseIntelGPU},
	{"huawei", "npu-smi", npuSMIArgs, parseHuaweiNPU},
	{"mthreads", "mthreads-gmi", mthreadsGMIArgs, parseMThreadsGPU},
	{"metax", "mx-smi", metaxSMIArgs, parseMetaXGPU},
}

var gpuMetricsProbes = []gpuMetricsProbe{
	{"nvidia", "nvidia-smi", nvidiaSMIMetricsArgs, parseNvidiaGPUMetrics},
	{"amd", "rocm-smi", rocmSMIMetricsArgs, parseAMDGPUMetrics},
	{"intel", "xpu-smi", xpuSMIMetricsArgs, parseIntelGPUMetrics},
	{"huawei", "npu-smi", npuSMIMetricsArgs, parseHuaweiNPUMetrics},
	{"mthreads", "mthreads-gmi", mthreadsGMIMetricsArgs, parseMThreadsGPUMetrics},
	{"metax", "mx-smi", metaxSMIMetricsArgs, parseMetaXGPUMetrics},
}

func detectGPU(ctx context.Context, runner CommandRunner) *GPUInfo {
	// Hygon DCU: sysfs-based detection, must run before AMD probe (DCU also has /dev/kfd).
	if gpu := detectHygonDCU(ctx, runner); gpu != nil {
		return gpu
	}

	// Moore Threads MUSA: sysfs-based detection for edge devices (M1000) without mthreads-gmi.
	if gpu := detectMThreadsMUSA(ctx, runner); gpu != nil {
		return gpu
	}

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
	// Hygon DCU: sysfs-based metrics, must run before AMD probe.
	if m := collectHygonDCUMetrics(ctx, runner); m != nil {
		return m
	}

	// Moore Threads MUSA: text-based metrics from mthreads-smi.
	if m := collectMThreadsMUSAMetrics(ctx, runner); m != nil {
		return m
	}

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
	"nvidia":   enrichNvidiaGPU,
	"amd":      enrichAMDGPU,
	"huawei":   enrichHuaweiNPU,
	"mthreads": enrichMThreadsGPU,
	"metax":    enrichMetaXGPU,
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
		// Fallback: parse version from dpkg when /opt/rocm is not installed (minimal rocm-smi)
		if gpu.SDKVersion == "" {
			if out, err := runner.Run(ctx, "dpkg-query", "-W", "-f=${Version}", "rocm-smi"); err == nil {
				if ver := strings.TrimSpace(string(out)); ver != "" {
					gpu.SDKVersion = "ROCm " + ver
				}
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

// enrichHuaweiNPU supplements GPUInfo with driver and CANN SDK version from system files.
func enrichHuaweiNPU(ctx context.Context, runner CommandRunner, gpu *GPUInfo) {
	if gpu.DriverVersion == "" {
		if out, err := runner.Run(ctx, "cat", "/usr/local/Ascend/driver/version.info"); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "Version=") {
					gpu.DriverVersion = strings.TrimPrefix(line, "Version=")
					break
				}
			}
		}
	}
	if gpu.SDKVersion == "" {
		if out, err := runner.Run(ctx, "cat", "/usr/local/Ascend/ascend-toolkit/latest/version.cfg"); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "version=") {
					gpu.SDKVersion = "CANN " + strings.TrimPrefix(line, "version=")
					break
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
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		val := raw[key]
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
		vram = bytesToMiB(b)
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

	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		val := raw[key]
		if !strings.HasPrefix(key, "card") {
			continue
		}
		var data map[string]interface{}
		if json.Unmarshal(val, &data) != nil {
			continue
		}

		util := int(jsonFloat(data, "GPU use (%)", "GPU Use (%)"))
		memUsed := bytesToMiB(jsonInt(data, "VRAM Total Used Memory (B)"))
		memTotal := bytesToMiB(jsonInt(data, "VRAM Total Memory (B)"))
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
		vram = bytesToMiB(b)
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
	memUsed := bytesToMiB(jsonInt(dev, "memory_used_byte"))
	memTotal := bytesToMiB(jsonInt(dev, "memory_physical_size_byte"))
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
	// Try JSON first (forward-compatible with npu-smi versions that support -j)
	var raw huaweiNPUJSON
	if err := json.Unmarshal([]byte(output), &raw); err == nil && len(raw.NPU) > 0 {
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

	// Fallback: parse npu-smi info text table output.
	// Table has 2 rows per NPU separated by +---+ lines:
	//   Row 1: | <id> <name>              | <health>      | <power>  <temp>  ...        |
	//   Row 2: | <id>                     | <bus_id>      | <aicore> ...  <hbm>/<total>  |
	return parseHuaweiNPUTable(output)
}

// parseHuaweiNPUTable parses the text table output of `npu-smi info`.
// Depends on table structure: cell 0 = "ID  ChipName", cell 2 = data fields.
// Row 1 (chip name present): Power(W), Temp(C); Row 2 (following): AICore%, HBM.
func parseHuaweiNPUTable(output string) *GPUInfo {
	lines := strings.Split(output, "\n")

	var name string
	var vram int
	var temp, power float64
	var count int

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "+") {
			continue
		}

		cells := splitTableCells(line)
		if len(cells) < 3 {
			continue
		}

		cell0 := strings.TrimSpace(cells[0])
		if containsNPUName(cell0) {
			count++
			if name == "" {
				// Cell 0 is "<id> <name>", e.g. "0     910B1" — skip numeric device ID
				fields := strings.Fields(cell0)
				if len(fields) >= 2 {
					if _, err := strconv.Atoi(fields[0]); err == nil {
						name = strings.Join(fields[1:], " ")
					} else {
						name = cell0
					}
				} else {
					name = cell0
				}
				cell2 := strings.TrimSpace(cells[2])
				temp, power = parseNPUTempPower(cell2)
			}
		} else if count > 0 && vram == 0 {
			// Row 2: extract HBM total from last "used / total" fraction
			cell2 := strings.TrimSpace(cells[2])
			parts := strings.Split(cell2, "/")
			if len(parts) >= 2 {
				lastRight := strings.TrimSpace(parts[len(parts)-1])
				if f := strings.Fields(lastRight); len(f) > 0 {
					vram, _ = strconv.Atoi(f[0])
				}
			}
		}
	}

	if count == 0 || name == "" {
		return nil
	}

	return &GPUInfo{
		Name:               name,
		Arch:               huaweiNPUToArch(name),
		VRAMMiB:            vram,
		TemperatureCelsius: temp,
		PowerDrawWatts:     power,
		Count:              count,
	}
}

// splitTableCells splits a "|"-delimited table row into cells (excluding outer pipes).
func splitTableCells(line string) []string {
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	return strings.Split(line, "|")
}

// containsNPUName checks if cell 0 contains an NPU chip name pattern (e.g. "910B1", "310P").
// Relies on table structure where cell 0 is always "<device_id> <chip_name>" for data rows;
// header rows ("NPU  Name", "Chip  Device") and non-data rows won't match these patterns.
func containsNPUName(cell string) bool {
	lower := strings.ToLower(cell)
	return strings.Contains(lower, "910") || strings.Contains(lower, "310") ||
		strings.Contains(lower, "ascend")
}

// parseNPUTempPower extracts temperature and power from a row-1 cell.
// Header order: "Power(W) Temp(C) Hugepages-Usage(page)"
// Example: "99.3  50  0 / 0" → power=99.3, temp=50
func parseNPUTempPower(cell string) (temp, power float64) {
	fields := strings.Fields(cell)
	nums := make([]float64, 0, 4)
	for _, f := range fields {
		if f == "/" {
			break
		}
		v, err := strconv.ParseFloat(f, 64)
		if err == nil {
			nums = append(nums, v)
		}
	}
	if len(nums) >= 1 {
		power = nums[0]
	}
	if len(nums) >= 2 {
		temp = nums[1]
	}
	return
}

func parseHuaweiNPUMetrics(output string) *GPUMetrics {
	// Try JSON first
	var raw huaweiNPUJSON
	if err := json.Unmarshal([]byte(output), &raw); err == nil && len(raw.NPU) > 0 {
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

	// Fallback: parse text table
	return parseHuaweiNPUMetricsTable(output)
}

// parseHuaweiNPUMetricsTable extracts metrics from npu-smi info text table.
// Row 1: Power(W), Temp(C). Row 2: AICore%, Memory-Usage (HBM used/total).
func parseHuaweiNPUMetricsTable(output string) *GPUMetrics {
	lines := strings.Split(output, "\n")

	var util, memUsed, memTotal int
	var temp, power float64
	foundNPU := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "+") {
			continue
		}
		cells := splitTableCells(line)
		if len(cells) < 3 {
			continue
		}

		cell0 := strings.TrimSpace(cells[0])
		if containsNPUName(cell0) {
			foundNPU = true
			cell2 := strings.TrimSpace(cells[2])
			temp, power = parseNPUTempPower(cell2)
		} else if foundNPU {
			// Row 2 cell format: "<aicore%> <...> / <...> <hbm_used> / <hbm_total>"
			cell2 := strings.TrimSpace(cells[2])
			fields := strings.Fields(cell2)
			if len(fields) > 0 {
				util, _ = strconv.Atoi(fields[0])
			}
			parts := strings.Split(cell2, "/")
			if len(parts) >= 2 {
				if f := strings.Fields(strings.TrimSpace(parts[len(parts)-1])); len(f) > 0 {
					memTotal, _ = strconv.Atoi(f[0])
				}
				if f := strings.Fields(strings.TrimSpace(parts[len(parts)-2])); len(f) > 0 {
					memUsed, _ = strconv.Atoi(f[len(f)-1])
				}
			}
			break // only first NPU
		}
	}

	if !foundNPU || (memTotal == 0 && util == 0) {
		return nil
	}

	return &GPUMetrics{
		UtilizationPercent: util,
		MemoryUsedMiB:      memUsed,
		MemoryTotalMiB:     memTotal,
		TemperatureCelsius: temp,
		PowerDrawWatts:     roundTo(power, 2),
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

func mthreadsGPUToArch(_ string) string {
	return "MUSA"
}

// enrichMThreadsGPU fills in driver/SDK version gaps for Moore Threads GPUs.
// parseMThreadsGPU already populates most fields from mthreads-gmi -q -j,
// so this only acts when fields were left empty.
func enrichMThreadsGPU(ctx context.Context, runner CommandRunner, gpu *GPUInfo) {
	if gpu.DriverVersion != "" && gpu.SDKVersion != "" {
		return
	}
	out, err := runner.Run(ctx, "mthreads-gmi", "-q", "-j")
	if err != nil {
		return
	}
	var raw struct {
		GPUs []map[string]interface{} `json:"gpus"`
	}
	if json.Unmarshal(out, &raw) != nil || len(raw.GPUs) == 0 {
		return
	}
	g := raw.GPUs[0]
	if gpu.DriverVersion == "" {
		if v := jsonStr(g, "driver_version"); v != "" {
			gpu.DriverVersion = v
		}
	}
	if gpu.SDKVersion == "" {
		if v := jsonStr(g, "musa_version"); v != "" {
			gpu.SDKVersion = "MUSA " + v
		}
	}
}

// detectMThreadsMUSA detects Moore Threads MUSA GPUs via Linux sysfs.
// Sentinel: /dev/mtgpu.0 must exist. Then checks /sys/class/drm/card*/device/uevent
// for DRIVER=mt-igpu entries. Reads GPU info from mthreads-smi text output.
func detectMThreadsMUSA(ctx context.Context, runner CommandRunner) *GPUInfo {
	if _, err := runner.Run(ctx, "stat", "/dev/mtgpu.0"); err != nil {
		slog.Debug("mthreads MUSA sentinel not found", "path", "/dev/mtgpu.0")
		return nil
	}

	// Try mthreads-smi for product name and GPU info
	name := "M1000"
	var tempC float64
	if out, err := runner.Run(ctx, "mthreads-smi"); err == nil {
		if parsed := parseMThreadsSMI(string(out)); parsed != nil {
			if parsed.Name != "" {
				name = parsed.Name
			}
			tempC = parsed.TemperatureCelsius
		}
	}

	// Get system memory (unified memory — GPU shares system RAM)
	var vramMiB int
	if out, err := runner.Run(ctx, "cat", "/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if kb, err := strconv.Atoi(fields[1]); err == nil {
						vramMiB = kb / 1024
					}
				}
				break
			}
		}
	}

	slog.Info("detected Moore Threads MUSA GPU via sysfs", "name", name, "vram_mib", vramMiB)
	return &GPUInfo{
		Vendor:             "mthreads",
		Name:               name,
		Arch:               mthreadsGPUToArch(name),
		VRAMMiB:            vramMiB,
		TemperatureCelsius: tempC,
		UnifiedMemory:      true,
		Count:              1,
	}
}

// parseMThreadsSMI parses the text output of mthreads-smi (non-JSON, edge devices).
func parseMThreadsSMI(output string) *GPUInfo {
	info := &GPUInfo{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "cpu name:") {
			info.Name = strings.TrimSpace(strings.TrimPrefix(line, "cpu name:"))
		} else if strings.HasPrefix(line, "gpu temperature:") {
			info.TemperatureCelsius = parseFloatPrefix(strings.TrimSpace(strings.TrimPrefix(line, "gpu temperature:")))
		}
	}
	return info
}

// collectMThreadsMUSAMetrics reads GPU metrics from mthreads-smi text output.
func collectMThreadsMUSAMetrics(ctx context.Context, runner CommandRunner) *GPUMetrics {
	if _, err := runner.Run(ctx, "stat", "/dev/mtgpu.0"); err != nil {
		return nil
	}

	out, err := runner.Run(ctx, "mthreads-smi")
	if err != nil {
		return nil
	}

	var util int
	var memUsed, memTotal int
	var tempC float64

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "gpu utilization:"):
			util = int(parseFloatPrefix(strings.TrimSpace(strings.TrimPrefix(line, "gpu utilization:"))))
		case strings.HasPrefix(line, "gpu temperature:"):
			tempC = parseFloatPrefix(strings.TrimSpace(strings.TrimPrefix(line, "gpu temperature:")))
		case strings.HasPrefix(line, "used:"):
			// system memory used in KB
			val := strings.TrimSpace(strings.TrimPrefix(line, "used:"))
			if kb, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(val), " KB")); err == nil {
				memUsed = kb / 1024
			}
		case strings.HasPrefix(line, "capacity:"):
			val := strings.TrimSpace(strings.TrimPrefix(line, "capacity:"))
			if kb, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(val), " KB")); err == nil {
				memTotal = kb / 1024
			}
		}
	}

	if memTotal == 0 {
		return nil
	}

	return &GPUMetrics{
		UtilizationPercent: util,
		MemoryUsedMiB:      memUsed,
		MemoryTotalMiB:     memTotal,
		TemperatureCelsius: tempC,
	}
}

// --- Hygon DCU (sysfs-based, no host CLI tool) ---

// detectHygonDCU detects Hygon DCU cards via Linux sysfs.
// Sentinel: /opt/hyhal must exist. Then scans /sys/class/drm/card*/device/uevent
// for DRIVER=hycu entries. Reads VRAM from mem_info_vram_total sysfs file.
func detectHygonDCU(ctx context.Context, runner CommandRunner) *GPUInfo {
	if _, err := runner.Run(ctx, "stat", "/opt/hyhal"); err != nil {
		return nil
	}

	out, err := runner.Run(ctx, "ls", "/sys/class/drm/")
	if err != nil {
		return nil
	}

	var count int
	var vramMiB int
	var name, computeID string

	for _, entry := range strings.Fields(string(out)) {
		if !strings.HasPrefix(entry, "card") || strings.Contains(entry, "-") {
			continue
		}

		ueventOut, err := runner.Run(ctx, "cat", "/sys/class/drm/"+entry+"/device/uevent")
		if err != nil {
			continue
		}
		uevent := string(ueventOut)
		if !strings.Contains(uevent, "DRIVER=hycu") {
			continue
		}

		count++

		if name == "" {
			for _, line := range strings.Split(uevent, "\n") {
				if strings.HasPrefix(line, "PCI_ID=") {
					pciID := strings.TrimPrefix(line, "PCI_ID=")
					name, computeID = hygonPCIToName(strings.TrimSpace(pciID))
					break
				}
			}
		}

		if vramMiB == 0 {
			if vramOut, err := runner.Run(ctx, "cat", "/sys/class/drm/"+entry+"/device/mem_info_vram_total"); err == nil {
				if bytes, err := strconv.ParseInt(strings.TrimSpace(string(vramOut)), 10, 64); err == nil && bytes > 0 {
					vramMiB = bytesToMiB(bytes)
				}
			}
		}
	}

	if count == 0 {
		return nil
	}
	if name == "" {
		name = "Hygon DCU"
	}

	slog.Info("detected Hygon DCU via sysfs", "count", count, "name", name, "vram_mib", vramMiB)
	return &GPUInfo{
		Vendor:    "hygon",
		Name:      name,
		Arch:      "DCU",
		VRAMMiB:   vramMiB,
		ComputeID: computeID,
		Count:     count,
	}
}

func hygonPCIToName(pciID string) (name, computeID string) {
	upper := strings.ToUpper(pciID)
	switch {
	case strings.HasSuffix(upper, ":6320"):
		return "BW150", "DCU-C3000"
	default:
		return "Hygon DCU", "DCU"
	}
}

// collectHygonDCUMetrics reads per-card VRAM usage from sysfs.
// Returns first DCU card's metrics. Utilization and temperature unavailable via sysfs.
func collectHygonDCUMetrics(ctx context.Context, runner CommandRunner) *GPUMetrics {
	if _, err := runner.Run(ctx, "stat", "/opt/hyhal"); err != nil {
		return nil
	}

	out, err := runner.Run(ctx, "ls", "/sys/class/drm/")
	if err != nil {
		return nil
	}

	for _, entry := range strings.Fields(string(out)) {
		if !strings.HasPrefix(entry, "card") || strings.Contains(entry, "-") {
			continue
		}

		ueventOut, err := runner.Run(ctx, "cat", "/sys/class/drm/"+entry+"/device/uevent")
		if err != nil || !strings.Contains(string(ueventOut), "DRIVER=hycu") {
			continue
		}

		var memUsed, memTotal int
		basePath := "/sys/class/drm/" + entry + "/device/"

		if totalOut, err := runner.Run(ctx, "cat", basePath+"mem_info_vram_total"); err == nil {
			if bytes, err := strconv.ParseInt(strings.TrimSpace(string(totalOut)), 10, 64); err == nil {
				memTotal = bytesToMiB(bytes)
			}
		}
		if usedOut, err := runner.Run(ctx, "cat", basePath+"mem_info_vram_used"); err == nil {
			if bytes, err := strconv.ParseInt(strings.TrimSpace(string(usedOut)), 10, 64); err == nil {
				memUsed = bytesToMiB(bytes)
			}
		}

		if memTotal == 0 {
			continue
		}

		return &GPUMetrics{
			MemoryUsedMiB:  memUsed,
			MemoryTotalMiB: memTotal,
		}
	}
	return nil
}

// --- MetaX (mx-smi text table parser) ---

// parseMetaXGPU parses the standard text output of mx-smi (no flags).
// Table format per GPU (2 rows):
//
//	| 0       MetaX N260             Off | 0000:01:00.0        | 0%            Native |
//	| 37C     29W / 225W              P0 | 666/65536 MiB       | Available            |
func parseMetaXGPU(output string) *GPUInfo {
	lines := strings.Split(output, "\n")

	var name, driverVersion, sdkVersion string
	var vram int
	var temp, powerDraw, powerLimit float64
	var count int

	// Extract driver and MACA version from header lines
	for _, line := range lines {
		if idx := strings.Index(line, "Kernel Mode Driver Version:"); idx >= 0 {
			rest := strings.TrimSpace(line[idx+len("Kernel Mode Driver Version:"):])
			rest = strings.TrimRight(rest, "| ")
			driverVersion = rest
		}
		if idx := strings.Index(line, "MACA Version:"); idx >= 0 {
			rest := line[idx+len("MACA Version:"):]
			// MACA version ends at next whitespace or field
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				sdkVersion = "MACA " + fields[0]
			}
		}
	}

	// Parse GPU data rows. Each GPU is 2 consecutive data rows between separator lines.
	// Data rows start with "| " and contain actual GPU info (not headers or separators).
	inGPUTable := false
	expectRow2 := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect start of GPU table (after ====+====+==== separator)
		if strings.Contains(trimmed, "====") && strings.Contains(trimmed, "+") {
			inGPUTable = true
			continue
		}
		if !inGPUTable {
			continue
		}
		// Skip separator lines
		if strings.HasPrefix(trimmed, "+--") {
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}

		cells := splitTableCells(trimmed)
		if len(cells) < 3 {
			continue
		}

		if !expectRow2 {
			// Row 1: | <id> <name> <persist-mode> | <bus-id> | <util>% <sgpu-mode> |
			cell0 := strings.TrimSpace(cells[0])
			fields := strings.Fields(cell0)
			if len(fields) < 2 {
				continue
			}
			// First field should be numeric device ID
			if _, err := strconv.Atoi(fields[0]); err != nil {
				continue
			}
			count++

			if name == "" {
				// Extract GPU name: fields between device_id and persistence mode (On/Off)
				nameEnd := len(fields)
				for i, f := range fields {
					if f == "On" || f == "Off" {
						nameEnd = i
						break
					}
				}
				if nameEnd > 1 {
					name = strings.Join(fields[1:nameEnd], " ")
				}
			}
			expectRow2 = true
		} else {
			// Row 2: | <temp>C <power>W / <cap>W <perf> | <mem_used>/<mem_total> MiB | <state> |
			if count == 1 {
				cell0 := strings.TrimSpace(cells[0])
				cell1 := strings.TrimSpace(cells[1])

				// Parse temp and power from cell 0
				cFields := strings.Fields(cell0)
				for i, f := range cFields {
					if strings.HasSuffix(f, "C") && !strings.Contains(f, "P") {
						t, err := strconv.ParseFloat(strings.TrimSuffix(f, "C"), 64)
						if err == nil {
							temp = t
						}
					}
					if strings.HasSuffix(f, "W") && !strings.HasSuffix(f, "PW") {
						w, err := strconv.ParseFloat(strings.TrimSuffix(f, "W"), 64)
						if err == nil {
							if i+1 < len(cFields) && cFields[i+1] == "/" {
								powerDraw = w
							} else if i > 0 && cFields[i-1] == "/" {
								powerLimit = w
							} else {
								powerDraw = w
							}
						}
					}
				}

				// Parse memory from cell 1: "666/65536 MiB"
				memStr := strings.TrimSuffix(cell1, "MiB")
				memStr = strings.TrimSpace(memStr)
				_, vram = parseUsedTotal(memStr)
			}
			expectRow2 = false
		}
	}

	if count == 0 || name == "" {
		return nil
	}

	return &GPUInfo{
		Name:               name,
		Arch:               metaxGPUToArch(name),
		VRAMMiB:            vram,
		DriverVersion:      driverVersion,
		SDKVersion:         sdkVersion,
		TemperatureCelsius: temp,
		PowerDrawWatts:     powerDraw,
		PowerLimitWatts:    powerLimit,
		Count:              count,
	}
}

// parseMetaXGPUMetrics extracts real-time metrics from mx-smi text output.
func parseMetaXGPUMetrics(output string) *GPUMetrics {
	lines := strings.Split(output, "\n")

	var util, memUsed, memTotal int
	var temp, power float64

	inGPUTable := false
	expectRow2 := false
	found := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.Contains(trimmed, "====") && strings.Contains(trimmed, "+") {
			inGPUTable = true
			continue
		}
		if !inGPUTable {
			continue
		}
		if strings.HasPrefix(trimmed, "+--") || !strings.HasPrefix(trimmed, "|") {
			continue
		}

		cells := splitTableCells(trimmed)
		if len(cells) < 3 {
			continue
		}

		if !expectRow2 {
			cell0 := strings.TrimSpace(cells[0])
			fields := strings.Fields(cell0)
			if len(fields) < 2 {
				continue
			}
			if _, err := strconv.Atoi(fields[0]); err != nil {
				continue
			}

			if !found {
				// Extract utilization from cell 2 of first GPU
				cell2 := strings.TrimSpace(cells[2])
				for _, f := range strings.Fields(cell2) {
					if strings.HasSuffix(f, "%") {
						util, _ = strconv.Atoi(strings.TrimSuffix(f, "%"))
						break
					}
				}
			}
			expectRow2 = true
		} else {
			if !found {
				cell0 := strings.TrimSpace(cells[0])
				cell1 := strings.TrimSpace(cells[1])

				// Parse temp and power
				for _, f := range strings.Fields(cell0) {
					if strings.HasSuffix(f, "C") && !strings.Contains(f, "P") {
						t, err := strconv.ParseFloat(strings.TrimSuffix(f, "C"), 64)
						if err == nil {
							temp = t
						}
					}
					if strings.HasSuffix(f, "W") && !strings.HasSuffix(f, "PW") {
						w, err := strconv.ParseFloat(strings.TrimSuffix(f, "W"), 64)
						if err == nil {
							power = w
							break // take first W value (usage, not cap)
						}
					}
				}

				// Parse memory: "666/65536 MiB"
				memStr := strings.TrimSuffix(cell1, "MiB")
				memStr = strings.TrimSpace(memStr)
				memUsed, memTotal = parseUsedTotal(memStr)
				found = true
			}
			expectRow2 = false
		}
	}

	if !found || (memTotal == 0 && util == 0) {
		return nil
	}

	return &GPUMetrics{
		UtilizationPercent: util,
		MemoryUsedMiB:      memUsed,
		MemoryTotalMiB:     memTotal,
		TemperatureCelsius: temp,
		PowerDrawWatts:     roundTo(power, 2),
	}
}

// enrichMetaXGPU supplements GPUInfo with driver and MACA SDK version from mx-smi JSON output.
func enrichMetaXGPU(ctx context.Context, runner CommandRunner, gpu *GPUInfo) {
	out, err := runner.Run(ctx, "mx-smi", "-j")
	if err != nil {
		return
	}
	var raw map[string]interface{}
	if json.Unmarshal(out, &raw) != nil {
		return
	}
	if gpu.DriverVersion == "" {
		if v := jsonStr(raw, "driver_version"); v != "" {
			gpu.DriverVersion = v
		}
	}
	if gpu.SDKVersion == "" {
		if v := jsonStr(raw, "maca_version"); v != "" {
			gpu.SDKVersion = "MACA " + v
		}
	}
}

// metaxGPUToArch returns the architecture family for a MetaX GPU.
// All current MetaX GPUs (N260, N100, C500, C280) use the MACA architecture.
func metaxGPUToArch(_ string) string {
	return "MACA"
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
