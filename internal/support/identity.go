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
	hardwareID, candidates = deriveHardwareIDs(
		runtime.GOOS,
		runtime.GOARCH,
		hostname,
		machineID,
		collectHardwareIDCandidates(ctx),
	)

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

func deriveHardwareIDs(goos, goarch, hostname, machineID string, rawCandidates []string) (string, []string) {
	primarySignal := primaryHardwareSignal(goos, goarch, hostname, machineID, rawCandidates)
	if primarySignal == "" {
		return "", nil
	}
	hardwareID := hashString(primarySignal)

	// Keep the v1 machine-id-only hash as a candidate so existing devices can
	// be recovered and upgraded to the v2 primary identity with a recovery code.
	seen := map[string]bool{hardwareID: true}
	var candidates []string
	addCandidate := func(raw string) {
		raw = normalizeHardwareIDCandidate(raw)
		if raw == "" {
			return
		}
		h := hashString(raw)
		if seen[h] {
			return
		}
		candidates = append(candidates, h)
		seen[h] = true
	}
	if strings.TrimSpace(machineID) != "" {
		addCandidate(machineID)
	} else {
		addCandidate(hostname)
	}
	for _, raw := range rawCandidates {
		addCandidate(raw)
	}
	return hardwareID, candidates
}

func primaryHardwareSignal(goos, goarch, hostname, machineID string, rawCandidates []string) string {
	goos = strings.TrimSpace(goos)
	goarch = strings.TrimSpace(goarch)
	hostname = strings.TrimSpace(hostname)
	machineID = strings.TrimSpace(machineID)
	hardwareComponent := strongestHardwareComponent(rawCandidates)
	if hostname == "" && machineID == "" && hardwareComponent == "" {
		return ""
	}
	hostComponent := hostname
	if hardwareComponent != "" {
		hostComponent = hardwareComponent
	}
	return strings.Join([]string{
		"aima:device-hardware:v2",
		goos,
		goarch,
		machineID,
		hostComponent,
	}, "\x00")
}

func strongestHardwareComponent(rawCandidates []string) string {
	for _, raw := range rawCandidates {
		raw = normalizeHardwareIDCandidate(raw)
		if strings.HasPrefix(raw, "serial:") {
			return raw
		}
	}
	for _, raw := range rawCandidates {
		raw = normalizeHardwareIDCandidate(raw)
		if strings.HasPrefix(raw, "mac:") {
			return raw
		}
	}
	for _, raw := range rawCandidates {
		if raw = normalizeHardwareIDCandidate(raw); raw != "" {
			return raw
		}
	}
	return ""
}

func normalizeHardwareIDCandidate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "serial:"):
		value := strings.TrimSpace(raw[len("serial:"):])
		if !usableHardwareValue(value) {
			return ""
		}
		return "serial:" + value
	case strings.HasPrefix(lower, "mac:"):
		value := strings.TrimSpace(raw[len("mac:"):])
		mac, err := net.ParseMAC(value)
		if err != nil || !usableMAC(mac) {
			return ""
		}
		return "mac:" + mac.String()
	default:
		if !usableHardwareValue(raw) {
			return ""
		}
		return raw
	}
}

func usableHardwareValue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	normalized := strings.ToLower(value)
	normalized = strings.Trim(normalized, " \t\r\n\"'")
	switch normalized {
	case "none", "default string", "to be filled by o.e.m.", "to be filled by oem",
		"unknown", "system serial number", "serial number", "not specified",
		"not applicable", "n/a", "na", "null", "undefined", "0":
		return false
	}
	stripped := strings.NewReplacer("-", "", "_", "", " ", "", ":", "").Replace(normalized)
	if stripped == "" {
		return false
	}
	allZero := true
	for _, ch := range stripped {
		if ch != '0' {
			allZero = false
			break
		}
	}
	return !allZero
}

func usableMAC(mac net.HardwareAddr) bool {
	if len(mac) == 0 {
		return false
	}
	allZero := true
	allFF := true
	for _, b := range mac {
		if b != 0 {
			allZero = false
		}
		if b != 0xff {
			allFF = false
		}
	}
	if allZero || allFF {
		return false
	}
	return mac[0]&1 == 0
}

func virtualNetworkInterfaceName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, prefix := range []string{
		"br-", "cni", "docker", "flannel", "kube", "lo", "podman",
		"tap", "tailscale", "tun", "utun", "veth", "virbr", "wg",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
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
				if candidate := normalizeHardwareIDCandidate("serial:" + v); candidate != "" {
					ids = append(ids, candidate)
				}
			}
		}
	}
	// Primary MAC address as fallback candidate.
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagLoopback != 0 || len(iface.HardwareAddr) == 0 || virtualNetworkInterfaceName(iface.Name) {
				continue
			}
			if candidate := normalizeHardwareIDCandidate("mac:" + iface.HardwareAddr.String()); candidate != "" {
				ids = append(ids, candidate)
				break
			}
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
