//go:build linux

package hal

import (
	"os"
	"path/filepath"
	"strings"
)

const accelSysfsDir = "/sys/class/accel"

const xh2aDriver = "houmo,xh2a"

func detectNPU() *NPUInfo {
	if npu := detectAccelNPU(accelSysfsDir); npu != nil {
		return npu
	}
	return detectXH2ANPU("/sys/bus/pci/devices")
}

func detectAccelNPU(sysfsDir string) *NPUInfo {
	entries, err := os.ReadDir(sysfsDir)
	if err != nil {
		return nil
	}

	var npu *NPUInfo
	count := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "accel") {
			continue
		}
		devDir := filepath.Join(sysfsDir, entry.Name(), "device")

		uevent, err := os.ReadFile(filepath.Join(devDir, "uevent"))
		if err != nil {
			continue
		}
		driver, pciID := parseAccelUevent(string(uevent))
		if driver == "" {
			continue
		}

		count++
		if npu == nil {
			vbnv := readTrimmedFile(filepath.Join(devDir, "vbnv"))
			fwVer := readTrimmedFile(filepath.Join(devDir, "fw_version"))

			npu = &NPUInfo{
				Vendor:          npuVendorFromDriver(driver),
				Name:            npuName(vbnv, pciID, driver),
				FirmwareVersion: fwVer,
				Driver:          driver,
			}
		}
	}

	if npu != nil {
		npu.Count = count
	}
	return npu
}

// detectXH2ANPU detects Houmo XH2A IPUs. The vendor kernel driver exposes the
// accelerator as a PCI memory controller instead of the generic /sys/class/accel
// interface, so the standard NPU probe cannot see it.
func detectXH2ANPU(pciDevicesDir string) *NPUInfo {
	entries, err := os.ReadDir(pciDevicesDir)
	if err != nil {
		return nil
	}

	count := 0
	for _, entry := range entries {
		driverLink := filepath.Join(pciDevicesDir, entry.Name(), "driver")
		target, err := os.Readlink(driverLink)
		if err != nil || filepath.Base(target) != xh2aDriver {
			continue
		}
		count++
	}
	if count == 0 {
		return nil
	}

	return &NPUInfo{
		Vendor: "houmo",
		Name:   "Houmo XH2A IPU",
		Driver: xh2aDriver,
		Count:  count,
	}
}

func readTrimmedFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
