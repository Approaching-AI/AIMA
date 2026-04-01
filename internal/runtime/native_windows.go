//go:build windows

package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// launchViaSchtasks launches a process via Windows Task Scheduler to ensure it
// runs in the interactive desktop session (Session 1). This is required for GPU
// engines (Vulkan/DirectX) that need display session access, which is unavailable
// from SSH sessions (Session 0).
func (r *NativeRuntime) launchViaSchtasks(name string, command []string, logPath string, envVars map[string]string, workDir string) (int, error) {
	// Write a batch wrapper that sets env vars and redirects output.
	batPath := filepath.Join(r.logDir, name+"-launcher.bat")
	var bat strings.Builder
	bat.WriteString("@echo off\r\n")
	for k, v := range envVars {
		bat.WriteString(fmt.Sprintf("set \"%s=%s\"\r\n", k, v))
	}
	if workDir != "" {
		bat.WriteString(fmt.Sprintf("cd /d \"%s\"\r\n", workDir))
	}
	var cmdLine strings.Builder
	for i, arg := range command {
		if i > 0 {
			cmdLine.WriteString(" ")
		}
		if strings.ContainsAny(arg, " \t") {
			cmdLine.WriteString(fmt.Sprintf("\"%s\"", arg))
		} else {
			cmdLine.WriteString(arg)
		}
	}
	bat.WriteString(fmt.Sprintf("%s > \"%s\" 2>&1\r\n", cmdLine.String(), logPath))

	if err := os.WriteFile(batPath, []byte(bat.String()), 0o644); err != nil {
		return 0, fmt.Errorf("write launcher script: %w", err)
	}

	// Create a one-time scheduled task with /it (interactive token).
	taskName := "AIMA-deploy-" + name
	createOut, err := exec.Command("schtasks", "/create", "/tn", taskName,
		"/tr", batPath, "/sc", "once", "/st", "00:00", "/it", "/f").CombinedOutput()
	if err != nil {
		os.Remove(batPath)
		return 0, fmt.Errorf("schtasks create: %w (output: %s)", err, string(createOut))
	}

	// Run the task.
	runOut, err := exec.Command("schtasks", "/run", "/tn", taskName).CombinedOutput()
	if err != nil {
		exec.Command("schtasks", "/delete", "/tn", taskName, "/f").Run()
		os.Remove(batPath)
		return 0, fmt.Errorf("schtasks run: %w (output: %s)", err, string(runOut))
	}

	// Wait for the engine process to appear and discover its PID.
	// Prefer finding the process by port (more precise than image name when
	// multiple instances exist). Fall back to image name if port discovery fails.
	port := 0
	for i, arg := range command {
		if arg == "--port" && i+1 < len(command) {
			if p, err := strconv.Atoi(command[i+1]); err == nil {
				port = p
				break
			}
		}
	}

	binaryName := filepath.Base(command[0])
	var pid int
	for i := 0; i < 30; i++ {
		time.Sleep(1 * time.Second)
		if port > 0 {
			pid = findProcessPIDByPort(port)
		}
		if pid == 0 {
			pid = findProcessPIDByName(binaryName)
		}
		if pid > 0 {
			break
		}
	}

	// Clean up scheduled task definition (engine process continues running).
	exec.Command("schtasks", "/delete", "/tn", taskName, "/f").Run()
	os.Remove(batPath)

	if pid == 0 {
		return 0, fmt.Errorf("discover PID after schtasks launch: binary=%s port=%d", binaryName, port)
	}

	return pid, nil
}

// findProcessPIDByPort returns the PID of a process listening on the given port.
// Uses Windows netstat command. Returns 0 if not found.
func findProcessPIDByPort(port int) int {
	out, err := exec.Command("netstat", "-aon").Output()
	if err != nil {
		return 0
	}
	target := fmt.Sprintf(":%d ", port)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "LISTENING") {
			continue
		}
		if !strings.Contains(line, target) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 5 {
			if pid, err := strconv.Atoi(fields[len(fields)-1]); err == nil {
				return pid
			}
		}
	}
	return 0
}

// findProcessPIDByName returns the PID of a running process by its image name.
// Uses Windows tasklist command. Returns 0 if not found.
func findProcessPIDByName(imageName string) int {
	out, err := exec.Command("tasklist", "/fi",
		fmt.Sprintf("IMAGENAME eq %s", imageName), "/fo", "csv", "/nh").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "INFO:") {
			continue
		}
		// CSV: "imagename","pid","session","session#","mem"
		fields := strings.Split(line, "\",\"")
		if len(fields) >= 2 {
			pidStr := strings.Trim(fields[1], "\" \r")
			if pid, err := strconv.Atoi(pidStr); err == nil {
				return pid
			}
		}
	}
	return 0
}

// pidAlive checks if a process with the given PID exists using tasklist.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist", "/fi",
		fmt.Sprintf("PID eq %d", pid), "/nh").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}

// killProcessByPID kills a process by PID using taskkill with /F /T flags.
func killProcessByPID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	out, err := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).CombinedOutput()
	if err != nil {
		if !pidAlive(pid) {
			return nil
		}
		return fmt.Errorf("taskkill pid %d: %w (output: %s)", pid, err, strings.TrimSpace(string(out)))
	}
	return nil
}
