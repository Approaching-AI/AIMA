package runtime

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// nativeProcess tracks a running inference engine process.
type nativeProcess struct {
	name      string
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	logFile   *os.File
	logPath   string
	port      int
	labels    map[string]string
	startTime time.Time
	ready     bool
	exited    bool
	mu        sync.Mutex
}

// NativeRuntime manages inference engines as direct OS processes.
type NativeRuntime struct {
	logDir    string
	processes map[string]*nativeProcess
	mu        sync.RWMutex
}

func NewNativeRuntime(logDir string) *NativeRuntime {
	return &NativeRuntime{
		logDir:    logDir,
		processes: make(map[string]*nativeProcess),
	}
}

func (r *NativeRuntime) Name() string { return "native" }

func (r *NativeRuntime) Deploy(ctx context.Context, req *DeployRequest) error {
	r.mu.Lock()
	if _, exists := r.processes[req.Name]; exists {
		r.mu.Unlock()
		return fmt.Errorf("deployment %q already exists", req.Name)
	}
	r.mu.Unlock()

	if len(req.Command) == 0 {
		return fmt.Errorf("deploy %s: empty command", req.Name)
	}

	// Replace {{.ModelPath}} with actual host path (not /models like K3S)
	command := make([]string, len(req.Command))
	for i, c := range req.Command {
		command[i] = strings.ReplaceAll(c, "{{.ModelPath}}", req.ModelPath)
	}

	// Append --port if not already present
	hasPort := false
	for _, c := range command {
		if strings.Contains(c, "--port") {
			hasPort = true
			break
		}
	}
	if !hasPort && req.Port > 0 {
		command = append(command, "--port", strconv.Itoa(req.Port))
	}

	// Set up log file
	if err := os.MkdirAll(r.logDir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	logPath := filepath.Join(r.logDir, req.Name+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}

	// Create cancellable context for this process
	procCtx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(procCtx, command[0], command[1:]...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	slog.Info("native deploy", "name", req.Name, "command", strings.Join(command, " "))

	if err := cmd.Start(); err != nil {
		cancel()
		logFile.Close()
		return fmt.Errorf("start %s: %w", req.Name, err)
	}

	proc := &nativeProcess{
		name:      req.Name,
		cmd:       cmd,
		cancel:    cancel,
		logFile:   logFile,
		logPath:   logPath,
		port:      req.Port,
		labels:    req.Labels,
		startTime: time.Now(),
	}

	r.mu.Lock()
	r.processes[req.Name] = proc
	r.mu.Unlock()

	// Background: wait for process exit and run health checks
	go r.watchProcess(proc)
	if req.HealthCheck != nil && req.HealthCheck.Path != "" {
		go r.healthCheck(proc, req.HealthCheck)
	} else {
		// No health check configured — mark ready after process starts
		proc.mu.Lock()
		proc.ready = true
		proc.mu.Unlock()
	}

	return nil
}

func (r *NativeRuntime) Delete(_ context.Context, name string) error {
	r.mu.Lock()
	proc, ok := r.processes[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("deployment %q not found", name)
	}
	delete(r.processes, name)
	r.mu.Unlock()

	proc.cancel()
	// Wait briefly for process to exit
	done := make(chan struct{})
	go func() {
		proc.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		slog.Warn("process did not exit within timeout", "name", name)
	}

	// Close log file after process exits
	if proc.logFile != nil {
		proc.logFile.Close()
	}

	return nil
}

func (r *NativeRuntime) Status(_ context.Context, name string) (*DeploymentStatus, error) {
	r.mu.RLock()
	proc, ok := r.processes[name]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("deployment %q not found", name)
	}

	return r.procToStatus(proc), nil
}

func (r *NativeRuntime) List(_ context.Context) ([]*DeploymentStatus, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	statuses := make([]*DeploymentStatus, 0, len(r.processes))
	for _, proc := range r.processes {
		statuses = append(statuses, r.procToStatus(proc))
	}
	return statuses, nil
}

func (r *NativeRuntime) Logs(_ context.Context, name string, tailLines int) (string, error) {
	r.mu.RLock()
	proc, ok := r.processes[name]
	r.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("deployment %q not found", name)
	}

	return readTail(proc.logPath, tailLines)
}

func (r *NativeRuntime) watchProcess(proc *nativeProcess) {
	err := proc.cmd.Wait()
	proc.mu.Lock()
	proc.exited = true
	proc.mu.Unlock()
	if err != nil {
		slog.Warn("process exited with error", "name", proc.name, "error", err)
	} else {
		slog.Info("process exited", "name", proc.name)
	}
}

func (r *NativeRuntime) healthCheck(proc *nativeProcess, hc *HealthCheckConfig) {
	timeout := time.Duration(hc.TimeoutS) * time.Second
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:%d%s", proc.port, hc.Path)
	client := &http.Client{Timeout: 3 * time.Second}

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				proc.mu.Lock()
				proc.ready = true
				proc.mu.Unlock()
				slog.Info("native deployment ready", "name", proc.name)
				return
			}
		}
		time.Sleep(2 * time.Second)
	}

	slog.Warn("health check timeout", "name", proc.name, "url", url)
}

func (r *NativeRuntime) procToStatus(proc *nativeProcess) *DeploymentStatus {
	proc.mu.Lock()
	ready := proc.ready
	proc.mu.Unlock()

	phase := "running"
	if proc.cmd.ProcessState != nil {
		// Process has exited
		if proc.cmd.ProcessState.Success() {
			phase = "stopped"
		} else {
			phase = "failed"
		}
		ready = false
	}

	return &DeploymentStatus{
		Name:      proc.name,
		Phase:     phase,
		Ready:     ready,
		Address:   fmt.Sprintf("127.0.0.1:%d", proc.port),
		Labels:    proc.labels,
		StartTime: proc.startTime.Format(time.RFC3339),
		Runtime:   "native",
	}
}

// readTail reads the last n lines from a file.
func readTail(path string, n int) (string, error) {
	if n <= 0 {
		n = 100
	}

	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open log: %w", err)
	}
	defer f.Close()

	// Read all lines and keep last n
	var lines []string
	scanner := bufio.NewScanner(f)
	// Increase buffer for potentially long log lines
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return "", fmt.Errorf("read log: %w", err)
	}

	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return strings.Join(lines, "\n"), nil
}
