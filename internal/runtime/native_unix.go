//go:build !windows

package runtime

import (
	"fmt"
	"os"
)

// launchViaSchtasks is a no-op on non-Windows platforms.
// Windows Task Scheduler is only needed for GPU engines requiring interactive desktop session.
func (r *NativeRuntime) launchViaSchtasks(name string, command []string, logPath string, envVars map[string]string, workDir string) (int, error) {
	return 0, fmt.Errorf("schtasks launch is only supported on Windows")
}

// findProcessPIDByPort is not implemented on non-Windows platforms.
// Port-based PID discovery uses Windows netstat; on Unix, the caller uses /proc or ps instead.
func findProcessPIDByPort(port int) int {
	return 0
}

// findProcessPIDByName is not implemented on non-Windows platforms.
// Process name lookup uses Windows tasklist; on Unix, the caller uses /proc or ps instead.
func findProcessPIDByName(imageName string) int {
	return 0
}

// pidAlive checks if a process with the given PID exists.
// On non-Windows platforms, conservatively returns true
// (schtasks-based launching is Windows-only, so this path is not exercised).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return true
}

// killProcessByPID kills a process by PID using os.Process.Kill.
func killProcessByPID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
