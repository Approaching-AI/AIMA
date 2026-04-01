package support

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func buildSelfRegisterRequest(ctx context.Context) (map[string]any, error) {
	profile, fingerprint, hardwareID, candidates, err := collectOSProfile(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"fingerprint": fingerprint,
		"os_profile":  profile,
	}
	if hardwareID != "" {
		body["hardware_id"] = hardwareID
	}
	if len(candidates) > 0 {
		body["hardware_id_candidates"] = candidates
	}
	return body, nil
}

func collectOSProfile(ctx context.Context) (profile map[string]any, fingerprint, hardwareID string, candidates []string, err error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("resolve hostname: %w", err)
	}
	hostname = strings.TrimSpace(hostname)
	machineID := strings.TrimSpace(readMachineID(ctx))
	if machineID == "" {
		machineID = hostname
	}
	hardwareID = hashString(machineID)

	// Collect multiple hardware ID candidates for robust dedup across reinstalls.
	seen := map[string]bool{hardwareID: true}
	for _, raw := range collectHardwareIDCandidates(ctx) {
		h := hashString(raw)
		if !seen[h] {
			candidates = append(candidates, h)
			seen[h] = true
		}
	}

	profile = map[string]any{
		"os_type":          runtime.GOOS,
		"os_version":       detectOSVersion(ctx),
		"arch":             runtime.GOARCH,
		"hostname":         hostname,
		"machine_id":       machineID,
		"package_managers": detectPackageManagers(),
		"shell":            detectShell(),
		"shell_env": map[string]any{
			"proxy": map[string]any{
				"http_configured":     envConfigured("http_proxy", "HTTP_PROXY"),
				"https_configured":    envConfigured("https_proxy", "HTTPS_PROXY"),
				"no_proxy_configured": envConfigured("no_proxy", "NO_PROXY"),
			},
		},
	}
	fingerprint = fmt.Sprintf("%s|%s|%s", runtime.GOOS, runtime.GOARCH, hostname)
	return profile, fingerprint, hardwareID, candidates, nil
}

// collectHardwareIDCandidates gathers additional hardware identifiers
// (board serial, disk serial, MAC addresses) for server-side dedup.
func collectHardwareIDCandidates(ctx context.Context) []string {
	var ids []string
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.CommandContext(ctx, "ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.TrimSpace(line)
				if strings.Contains(line, "IOPlatformSerialNumber") {
					parts := strings.Split(line, "\"")
					if len(parts) >= 4 && strings.TrimSpace(parts[3]) != "" {
						ids = append(ids, "serial:"+strings.TrimSpace(parts[3]))
					}
				}
			}
		}
	case "linux":
		for _, path := range []string{"/sys/class/dmi/id/board_serial", "/sys/class/dmi/id/product_serial"} {
			if data, err := os.ReadFile(path); err == nil {
				v := strings.TrimSpace(string(data))
				if v != "" && v != "None" && v != "Default string" {
					ids = append(ids, "serial:"+v)
				}
			}
		}
	}
	// Primary MAC address as fallback candidate.
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagLoopback != 0 || len(iface.HardwareAddr) == 0 {
				continue
			}
			ids = append(ids, "mac:"+iface.HardwareAddr.String())
			break
		}
	}
	return ids
}

func readMachineID(ctx context.Context) string {
	switch runtime.GOOS {
	case "linux":
		for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
			if data, err := os.ReadFile(path); err == nil {
				return strings.TrimSpace(string(data))
			}
		}
	case "darwin":
		if out, err := exec.CommandContext(ctx, "ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.TrimSpace(line)
				if !strings.Contains(line, "IOPlatformUUID") {
					continue
				}
				parts := strings.Split(line, "\"")
				if len(parts) >= 4 {
					return strings.TrimSpace(parts[3])
				}
			}
		}
	}
	return ""
}

func detectOSVersion(ctx context.Context) string {
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.CommandContext(ctx, "sw_vers", "-productVersion").Output(); err == nil {
			version := strings.TrimSpace(string(out))
			if version != "" {
				return "macOS " + version
			}
		}
	case "linux":
		if data, err := os.ReadFile("/etc/os-release"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if !strings.HasPrefix(line, "PRETTY_NAME=") {
					continue
				}
				value := strings.TrimPrefix(line, "PRETTY_NAME=")
				value = strings.Trim(value, `"`)
				if value != "" {
					return value
				}
			}
		}
	}
	return runtime.GOOS
}

func detectPackageManagers() []string {
	candidates := []struct {
		Name string
		Bin  string
	}{
		{Name: "apt", Bin: "apt-get"},
		{Name: "brew", Bin: "brew"},
		{Name: "dnf", Bin: "dnf"},
		{Name: "yum", Bin: "yum"},
		{Name: "snap", Bin: "snap"},
		{Name: "pip", Bin: "pip3"},
	}
	found := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate.Bin); err == nil {
			found = append(found, candidate.Name)
		}
	}
	return found
}

func detectShell() string {
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return shell
	}
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "/bin/sh"
}

func envConfigured(keys ...string) bool {
	for _, key := range keys {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
