//go:build linux

package hal

import (
	"os"
	"path/filepath"
	"strings"
)

const accelSysfsDir = "/sys/class/accel"

func detectNPU() *NPUInfo {
	entries, err := os.ReadDir(accelSysfsDir)
	if err != nil {
		return nil
	}

	var npu *NPUInfo
	count := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "accel") {
			continue
		}
		devDir := filepath.Join(accelSysfsDir, entry.Name(), "device")

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

func readTrimmedFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
