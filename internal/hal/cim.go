package hal

import (
	"encoding/json"
	"strings"
)

// CIM (Common Information Model) parsing for Windows hardware detection.
//
// Modern Windows (11 24H2+) removes the legacy `wmic` CLI, so detection shells
// out to `powershell Get-CimInstance ... | ConvertTo-Json` instead. These
// parsers are kept free of build tags and OS calls so they unit-test on any
// platform; only the command execution lives in detect_windows.go.

// decodeCIMObjects normalizes `ConvertTo-Json -Compress` output. PowerShell
// renders a single CIM instance as a JSON object and multiple instances as an
// array, so both shapes collapse to a slice of maps here.
func decodeCIMObjects(output string) []map[string]interface{} {
	start := strings.IndexAny(output, "{[")
	if start < 0 {
		return nil
	}
	trimmed := strings.TrimSpace(output[start:])
	if trimmed == "" {
		return nil
	}
	if trimmed[0] == '[' {
		var arr []map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
			return nil
		}
		return arr
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return nil
	}
	return []map[string]interface{}{obj}
}

// parseCIMCPU fills CPUInfo from Win32_Processor JSON. Cores and threads sum
// across sockets; name and clock come from the first processor.
func parseCIMCPU(output string, info *CPUInfo) {
	objs := decodeCIMObjects(output)
	if len(objs) == 0 {
		return
	}
	var cores, threads int
	for _, o := range objs {
		cores += int(jsonInt(o, "NumberOfCores"))
		threads += int(jsonInt(o, "NumberOfLogicalProcessors"))
	}
	if name := strings.TrimSpace(jsonStr(objs[0], "Name")); name != "" {
		info.Model = name
	}
	if cores > 0 {
		info.Cores = cores
	}
	if threads > 0 {
		info.Threads = threads
	}
	if mhz := jsonFloat(objs[0], "MaxClockSpeed"); mhz > 0 {
		info.FreqGHz = mhz / 1000.0
	}
}

// parseCIMRAM fills RAMInfo from Win32_OperatingSystem JSON (values in KiB).
func parseCIMRAM(output string, info *RAMInfo) {
	objs := decodeCIMObjects(output)
	if len(objs) == 0 {
		return
	}
	o := objs[0]
	if kb := jsonInt(o, "TotalVisibleMemorySize"); kb > 0 {
		info.TotalMiB = int(kb / 1024)
	}
	if kb := jsonInt(o, "FreePhysicalMemory"); kb > 0 {
		info.AvailableMiB = int(kb / 1024)
	}
}

// parseCIMInstalledMemoryBytes reads the summed DIMM capacity (bytes) from
// `Win32_PhysicalMemory | Measure-Object Capacity -Sum` JSON ({"Sum":N}).
func parseCIMInstalledMemoryBytes(output string) int64 {
	objs := decodeCIMObjects(output)
	if len(objs) == 0 {
		return 0
	}
	return jsonInt(objs[0], "Sum")
}

// applyInstalledMemoryTotal overrides RAMInfo.TotalMiB with the true installed
// memory (sum of DIMM capacity) when it exceeds the OS-visible total. On
// unified-memory APUs (e.g. Strix Halo) the OS only sees a fraction with the
// rest carved out for the iGPU, so the installed total is the meaningful figure.
// AvailableMiB is recomputed as total minus OS-used so it stays correct on both
// unified and conventional hosts.
func applyInstalledMemoryTotal(info *RAMInfo, installedBytes int64) {
	if installedBytes <= 0 {
		return
	}
	total := int(installedBytes / (1024 * 1024))
	if total <= info.TotalMiB {
		return
	}
	osUsed := info.TotalMiB - info.AvailableMiB
	if osUsed < 0 {
		osUsed = 0
	}
	info.TotalMiB = total
	if avail := total - osUsed; avail >= 0 {
		info.AvailableMiB = avail
	}
}

// parseCIMSwap sums AllocatedBaseSize (MiB) across all Win32_PageFileUsage rows.
func parseCIMSwap(output string, info *RAMInfo) {
	total := 0
	for _, o := range decodeCIMObjects(output) {
		total += int(jsonInt(o, "AllocatedBaseSize"))
	}
	if total > 0 {
		info.SwapTotalMiB = total
	}
}

// parseCIMCPULoad averages LoadPercentage across all Win32_Processor rows.
func parseCIMCPULoad(output string) float64 {
	objs := decodeCIMObjects(output)
	if len(objs) == 0 {
		return 0
	}
	var sum float64
	for _, o := range objs {
		sum += jsonFloat(o, "LoadPercentage")
	}
	return sum / float64(len(objs))
}

// parseWindowsGPUs builds a GPUInfo from Win32_VideoController JSON. It is the
// Windows fallback used when no vendor SMI tool (nvidia-smi/rocm-smi) is on
// PATH — common on AMD APU hosts. CIM cannot report true VRAM (Win32 AdapterRAM
// is a uint32 that saturates at 4 GiB) or utilization, so only static identity
// fields are populated; VRAM is left to the unified-memory backfill in
// detectWithRunner. AMD identity (name/gfx/unified) is resolved from the PCI
// device ID via amdPCIToInfo, shared with the Linux sysfs path.
func parseWindowsGPUs(output string) *GPUInfo {
	objs := decodeCIMObjects(output)
	if len(objs) == 0 {
		return nil
	}

	var chosen map[string]interface{}
	var vendor string
	count := 0
	for _, o := range objs {
		v := windowsGPUVendor(jsonStr(o, "PNPDeviceID"), jsonStr(o, "AdapterCompatibility"))
		if v == "" {
			continue // skip Microsoft Basic Display / virtual adapters
		}
		if chosen == nil {
			chosen = o
			vendor = v
		}
		if v == vendor {
			count++
		}
	}
	if chosen == nil {
		return nil
	}

	gpu := &GPUInfo{
		Vendor:        vendor,
		Name:          strings.TrimSpace(jsonStr(chosen, "Name")),
		DriverVersion: strings.TrimSpace(jsonStr(chosen, "DriverVersion")),
		Count:         count,
	}

	switch vendor {
	case "amd":
		info := amdPCIToInfo(windowsPCIDeviceID(jsonStr(chosen, "PNPDeviceID")))
		if info.name != "" {
			gpu.Name = info.name
		}
		gpu.ComputeID = info.computeID
		gpu.UnifiedMemory = info.unified
		gpu.Arch = firstNonEmptyString(gfxVersionToArch(gpu.ComputeID), amdGPUToArch(gpu.Name), "unknown")
	case "intel":
		gpu.Arch = intelGPUToArch(gpu.Name)
	default:
		gpu.Arch = "unknown"
	}
	return gpu
}

// windowsGPUVendor maps a video controller to a vendor key, preferring the PCI
// vendor ID embedded in PNPDeviceID and falling back to AdapterCompatibility.
// Returns "" for non-hardware adapters (e.g. Microsoft Basic Display).
func windowsGPUVendor(pnpDeviceID, adapterCompatibility string) string {
	switch up := strings.ToUpper(pnpDeviceID); {
	case strings.Contains(up, "VEN_10DE"):
		return "nvidia"
	case strings.Contains(up, "VEN_1002"):
		return "amd"
	case strings.Contains(up, "VEN_8086"):
		return "intel"
	}
	switch c := strings.ToLower(adapterCompatibility); {
	case strings.Contains(c, "nvidia"):
		return "nvidia"
	case strings.Contains(c, "advanced micro devices"), strings.Contains(c, "amd"):
		return "amd"
	case strings.Contains(c, "intel"):
		return "intel"
	}
	return ""
}

// windowsPCIDeviceID extracts the 4-hex-digit PCI device ID from a PNPDeviceID
// such as `PCI\VEN_1002&DEV_1586&SUBSYS_...` → "1586".
func windowsPCIDeviceID(pnpDeviceID string) string {
	up := strings.ToUpper(pnpDeviceID)
	idx := strings.Index(up, "DEV_")
	if idx < 0 {
		return ""
	}
	rest := up[idx+len("DEV_"):]
	end := 0
	for end < len(rest) && isHexByte(rest[end]) {
		end++
	}
	return rest[:end]
}

func isHexByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'A' && b <= 'F')
}
