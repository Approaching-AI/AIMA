package runtime

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"
)

var nativePortReleaseTimeout = 5 * time.Second

var nativePortReleasePollInterval = 100 * time.Millisecond

type processMetaState uint8

const (
	processMetaMatching processMetaState = iota
	processMetaStarting
	processMetaStale
	processMetaExited
)

func classifyWindowsProcessMetaState(recordedPID, listenerPID int, recordedAlive bool) processMetaState {
	switch {
	case listenerPID > 0 && listenerPID == recordedPID:
		return processMetaMatching
	case listenerPID > 0:
		return processMetaStale
	case recordedAlive:
		return processMetaStarting
	default:
		return processMetaExited
	}
}

func selectNewProcessPID(before, current []int) int {
	seen := make(map[int]struct{}, len(before))
	for _, pid := range before {
		seen[pid] = struct{}{}
	}
	candidates := make(map[int]struct{})
	for _, pid := range current {
		if pid <= 0 {
			continue
		}
		if _, exists := seen[pid]; exists {
			continue
		}
		candidates[pid] = struct{}{}
	}
	if len(candidates) != 1 {
		return 0
	}
	for pid := range candidates {
		return pid
	}
	return 0
}

func metaPhaseForProcessState(state processMetaState, portBound bool, startedAt time.Time, timeoutS int) string {
	if state == processMetaStale || state == processMetaExited {
		return "failed"
	}
	if portBound {
		return "running"
	}
	if timeoutS <= 0 {
		timeoutS = 60
	}
	if time.Since(startedAt) < time.Duration(timeoutS)*time.Second {
		return "starting"
	}
	return "failed"
}

// processMatchesMeta validates that the process at the given PID still matches the
// deployment metadata. This guards against PID reuse — if the OS recycled the PID
// for a different process, we must not kill it.
func processMatchesMeta(meta *deploymentMeta) bool {
	state := processStateForMeta(meta)
	return state == processMetaMatching || state == processMetaStarting
}

func processStateForMeta(meta *deploymentMeta) processMetaState {
	if meta.PID <= 0 || len(meta.Command) == 0 {
		return processMetaExited
	}
	// On Linux, read /proc/<pid>/cmdline to verify the process identity.
	if goruntime.GOOS == "linux" {
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", meta.PID))
		if err != nil {
			return processMetaExited
		}
		raw := strings.TrimRight(string(cmdline), "\x00")
		if raw == "" {
			return processMetaExited
		}
		procArgs := strings.Split(raw, "\x00")
		if commandPrefixMatches(procArgs, meta.Command) {
			return processMetaMatching
		}
		return processMetaStale
	}
	if goruntime.GOOS != "windows" {
		out, err := exec.Command("ps", "-ww", "-p", strconv.Itoa(meta.PID), "-o", "command=").Output()
		if err != nil {
			return processMetaExited
		}
		if commandLineMatches(strings.TrimSpace(string(out)), meta.Command) {
			return processMetaMatching
		}
		return processMetaStale
	}
	// On Windows, Task Scheduler launches detached engine processes. Verify the
	// listener PID discovered from the port instead of treating any live port as
	// this deployment; otherwise stale metadata can masquerade as a ready model.
	if meta.Port > 0 {
		return classifyWindowsProcessMetaState(meta.PID, findProcessPIDByPort(meta.Port), pidAlive(meta.PID))
	}
	if pidAlive(meta.PID) {
		return processMetaMatching
	}
	return processMetaExited
}

func commandPrefixMatches(actual, expected []string) bool {
	if len(actual) < len(expected) || len(expected) == 0 {
		return false
	}
	offset, ok := commandStartOffset(actual, expected[0], len(expected))
	if !ok {
		return false
	}
	for i := 1; i < len(expected); i++ {
		if actual[offset+i] != expected[i] {
			return false
		}
	}
	return true
}

func sameCommandElement(actual, expected string) bool {
	return actual == expected || filepath.Base(actual) == filepath.Base(expected)
}

func commandStartOffset(actual []string, expected0 string, expectedLen int) (int, bool) {
	if len(actual) < expectedLen || expectedLen == 0 {
		return 0, false
	}
	maxOffset := len(actual) - expectedLen
	if maxOffset > 2 {
		maxOffset = 2
	}
	for offset := 0; offset <= maxOffset; offset++ {
		if offset > 0 && !safeLauncherPrefix(actual[:offset]) {
			continue
		}
		if sameCommandElement(actual[offset], expected0) {
			return offset, true
		}
	}
	return 0, false
}

func safeLauncherPrefix(prefix []string) bool {
	for _, arg := range prefix {
		if arg == "" {
			return false
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		base := strings.ToLower(filepath.Base(arg))
		switch {
		case base == "env", base == "bash", base == "sh", base == "zsh":
			continue
		case strings.HasPrefix(base, "python"):
			continue
		default:
			return false
		}
	}
	return len(prefix) > 0
}

func commandLineMatches(actualLine string, expected []string) bool {
	if actualLine == "" || len(expected) == 0 {
		return false
	}
	fields := strings.Fields(actualLine)
	if _, ok := commandStartOffset(fields, expected[0], len(expected)); !ok {
		return false
	}
	for _, arg := range expected[1:] {
		if !strings.Contains(actualLine, arg) {
			return false
		}
	}
	return true
}

// portAlive checks if a TCP port is responding on localhost.
func portAlive(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func waitForPortRelease(port int, timeout time.Duration) bool {
	if port <= 0 {
		return true
	}
	deadline := time.Now().Add(timeout)
	for {
		if !portAlive(port) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(nativePortReleasePollInterval)
	}
}

func (r *NativeRuntime) portConflict(port int, selfName string) string {
	if port <= 0 || !portAlive(port) {
		return ""
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	for name, proc := range r.processes {
		if name == selfName || proc == nil {
			continue
		}
		if proc.port == port {
			return fmt.Sprintf("deployment %q", name)
		}
	}

	for _, meta := range r.loadAllMeta() {
		if meta == nil || meta.Name == selfName {
			continue
		}
		if meta.Port == port {
			return fmt.Sprintf("deployment %q", meta.Name)
		}
	}

	return "another process"
}

func waitForProcessExit(proc *nativeProcess, timeout time.Duration) bool {
	// proc.done is always initialized in Deploy(); this function must not be
	// called on a process without a done channel.
	select {
	case <-proc.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func effectiveHealthTimeout(hc *HealthCheckConfig) time.Duration {
	if hc == nil || hc.TimeoutS <= 0 {
		return 60 * time.Second
	}
	return time.Duration(hc.TimeoutS) * time.Second
}

func externalProcessAlive(proc *nativeProcess) bool {
	if proc.port > 0 {
		return portAlive(proc.port)
	}
	if proc.pid > 0 {
		return pidAlive(proc.pid)
	}
	return false
}
