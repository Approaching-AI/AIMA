package hal

import "strings"

// parseAccelUevent extracts driver and PCI ID from a Linux accel uevent file.
func parseAccelUevent(content string) (driver, pciID string) {
	for _, line := range strings.Split(content, "\n") {
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "DRIVER":
			driver = val
		case "PCI_ID":
			pciID = val
		}
	}
	return
}

// npuVendorFromDriver maps a kernel driver name to a vendor string.
func npuVendorFromDriver(driver string) string {
	switch {
	case strings.HasPrefix(driver, "amdxdna"):
		return "amd"
	case strings.HasPrefix(driver, "intel"):
		return "intel"
	case strings.HasPrefix(driver, "qcom"):
		return "qualcomm"
	default:
		return driver
	}
}

// npuName returns the best available name, preferring vbnv over PCI ID over driver.
func npuName(vbnv, pciID, driver string) string {
	if vbnv != "" {
		return vbnv
	}
	if pciID != "" {
		return pciID
	}
	return driver
}
