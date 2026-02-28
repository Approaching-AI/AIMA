package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jguan/aima/internal/engine"
)

// deploymentMeta is persisted to disk so deployments survive across CLI invocations.
type deploymentMeta struct {
	Name               string            `json:"name"`
	PID                int               `json:"pid"`
	Port               int               `json:"port"`
	Engine             string            `json:"engine"`
	Labels             map[string]string `json:"labels"`
	LogPath            string            `json:"log_path"`
	Command            []string          `json:"command"`
	StartTime          time.Time         `json:"start_time"`
	HealthCheckPath    string            `json:"health_check_path,omitempty"`
	HealthCheckTimeout int               `json:"health_check_timeout_s,omitempty"`
}

// nativeProcess tracks a running inference engine process started in THIS CLI session.
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

// BinaryResolveFunc resolves a native engine binary, downloading if needed.
// Returns the absolute path to the binary.
type BinaryResolveFunc func(ctx context.Context, source *engine.BinarySource) (string, error)

// NativeRuntime manages inference engines as direct OS processes.
type NativeRuntime struct {
	logDir        string
	distDir       string // e.g. ~/.aima/dist/windows-amd64/
	deployDir     string // e.g. ~/.aima/deployments/ — persisted deployment metadata
	resolveBinary BinaryResolveFunc
	processes     map[string]*nativeProcess
	mu            sync.RWMutex
}

func NewNativeRuntime(logDir, distDir, deployDir string, opts ...NativeOption) *NativeRuntime {
	r := &NativeRuntime{
		logDir:    logDir,
		distDir:   distDir,
		deployDir: deployDir,
		processes: make(map[string]*nativeProcess),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// NativeOption configures a NativeRuntime.
type NativeOption func(*NativeRuntime)

// WithBinaryResolver sets the function used to auto-download missing engine binaries.
func WithBinaryResolver(fn BinaryResolveFunc) NativeOption {
	return func(r *NativeRuntime) {
		r.resolveBinary = fn
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

	// Check persisted metadata: if a deployment with this name is still alive, reject
	if meta, err := r.loadMeta(req.Name); err == nil {
		if portAlive(meta.Port) {
			return fmt.Errorf("deployment %q already running (PID %d, port %d)", req.Name, meta.PID, meta.Port)
		}
		// Stale metadata — clean up
		r.removeMeta(req.Name)
	}

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

	// Append other config values as CLI flags.
	// Config keys use underscore (e.g. "gpu_memory_utilization") → "--gpu-memory-utilization".
	// "port" is excluded since it is handled above.
	if len(req.Config) > 0 {
		keys := make([]string, 0, len(req.Config))
		for k := range req.Config {
			if k != "port" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			flag := "--" + strings.ReplaceAll(k, "_", "-")
			command = append(command, flag, fmt.Sprintf("%v", req.Config[k]))
		}
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

	// Resolve binary: dist/ first, then auto-download if source is available
	if r.distDir != "" {
		if resolved := r.findInDist(command[0]); resolved != "" {
			command[0] = resolved
		} else if r.resolveBinary != nil && req.BinarySource != nil {
			slog.Info("binary not in dist, attempting auto-download", "binary", command[0])
			if resolved, err := r.resolveBinary(ctx, req.BinarySource); err == nil {
				command[0] = resolved
			} else {
				slog.Warn("auto-download failed, will try PATH", "binary", command[0], "error", err)
			}
		}
	}

	// Create cancellable context for this process
	procCtx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(procCtx, command[0], command[1:]...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Ensure co-bundled shared libraries (.so/.dylib) next to the binary are found.
	if r.distDir != "" {
		ldVar := "LD_LIBRARY_PATH"
		if goruntime.GOOS == "darwin" {
			ldVar = "DYLD_LIBRARY_PATH"
		}
		existing := os.Getenv(ldVar)
		newVal := r.distDir
		if existing != "" {
			newVal = r.distDir + ":" + existing
		}
		cmd.Env = append(os.Environ(), ldVar+"="+newVal)
	}

	slog.Info("native deploy", "name", req.Name, "command", strings.Join(command, " "))

	if err := cmd.Start(); err != nil {
		cancel()
		logFile.Close()
		return fmt.Errorf("start %s: %w", req.Name, err)
	}

	now := time.Now()
	proc := &nativeProcess{
		name:      req.Name,
		cmd:       cmd,
		cancel:    cancel,
		logFile:   logFile,
		logPath:   logPath,
		port:      req.Port,
		labels:    req.Labels,
		startTime: now,
	}

	r.mu.Lock()
	r.processes[req.Name] = proc
	r.mu.Unlock()

	// Persist deployment metadata for cross-invocation discovery
	meta := &deploymentMeta{
		Name:      req.Name,
		PID:       cmd.Process.Pid,
		Port:      req.Port,
		Engine:    req.Engine,
		Labels:    req.Labels,
		LogPath:   logPath,
		Command:   command,
		StartTime: now,
	}
	if req.HealthCheck != nil {
		meta.HealthCheckPath = req.HealthCheck.Path
		meta.HealthCheckTimeout = req.HealthCheck.TimeoutS
	}
	if err := r.saveMeta(meta); err != nil {
		slog.Warn("failed to persist deployment metadata", "name", req.Name, "error", err)
	}

	// Background: wait for process exit and run health checks
	go r.watchProcess(proc)
	if req.HealthCheck != nil && req.HealthCheck.Path != "" {
		go r.healthCheckAndWarmup(proc, req.HealthCheck, req.Warmup)
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
	proc, inMemory := r.processes[name]
	if inMemory {
		delete(r.processes, name)
	}
	r.mu.Unlock()

	if inMemory {
		proc.cancel()
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
		if proc.logFile != nil {
			proc.logFile.Close()
		}
	} else {
		// Recover from persisted metadata and kill by PID
		meta, err := r.loadMeta(name)
		if err != nil {
			return fmt.Errorf("deployment %q not found", name)
		}
		if meta.PID > 0 {
			if p, err := os.FindProcess(meta.PID); err == nil {
				if err := p.Kill(); err != nil {
					slog.Warn("kill process", "name", name, "pid", meta.PID, "error", err)
				}
			}
		}
	}

	r.removeMeta(name)
	return nil
}

func (r *NativeRuntime) Status(_ context.Context, name string) (*DeploymentStatus, error) {
	r.mu.RLock()
	proc, ok := r.processes[name]
	r.mu.RUnlock()

	if ok {
		return r.procToStatus(proc), nil
	}

	// Try persisted metadata
	meta, err := r.loadMeta(name)
	if err != nil {
		return nil, fmt.Errorf("deployment %q not found", name)
	}
	return r.metaToStatus(meta), nil
}

func (r *NativeRuntime) List(_ context.Context) ([]*DeploymentStatus, error) {
	r.mu.RLock()
	seen := make(map[string]bool)
	statuses := make([]*DeploymentStatus, 0)
	for _, proc := range r.processes {
		seen[proc.name] = true
		statuses = append(statuses, r.procToStatus(proc))
	}
	r.mu.RUnlock()

	// Add persisted deployments not in memory (from previous CLI sessions)
	for _, meta := range r.loadAllMeta() {
		if seen[meta.Name] {
			continue
		}
		statuses = append(statuses, r.metaToStatus(meta))
	}

	return statuses, nil
}

func (r *NativeRuntime) Logs(_ context.Context, name string, tailLines int) (string, error) {
	r.mu.RLock()
	proc, ok := r.processes[name]
	r.mu.RUnlock()

	if ok {
		return readTail(proc.logPath, tailLines)
	}

	// Try persisted metadata for log path
	meta, err := r.loadMeta(name)
	if err != nil {
		return "", fmt.Errorf("deployment %q not found", name)
	}
	return readTail(meta.LogPath, tailLines)
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

func (r *NativeRuntime) healthCheckAndWarmup(proc *nativeProcess, hc *HealthCheckConfig, warmup *WarmupConfig) {
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
				slog.Info("health check passed", "name", proc.name)
				// Run warmup before marking ready
				if warmup != nil {
					r.warmup(proc, warmup)
				}
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

// warmup sends a dummy inference request to force model weight loading and CUDA kernel compilation.
func (r *NativeRuntime) warmup(proc *nativeProcess, cfg *WarmupConfig) {
	prompt := cfg.Prompt
	if prompt == "" {
		prompt = "Hello"
	}
	maxTokens := cfg.MaxTokens
	if maxTokens == 0 {
		maxTokens = 1
	}
	timeout := time.Duration(cfg.TimeoutS) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", proc.port)
	body := fmt.Sprintf(`{"model":"warmup","messages":[{"role":"user","content":%q}],"max_tokens":%d}`, prompt, maxTokens)

	slog.Info("warming up engine", "name", proc.name, "url", url)
	client := &http.Client{Timeout: timeout}
	resp, err := client.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		slog.Warn("warmup request failed", "name", proc.name, "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		slog.Info("warmup complete", "name", proc.name)
	} else {
		slog.Warn("warmup returned non-200", "name", proc.name, "status", resp.StatusCode)
	}
}

func (r *NativeRuntime) procToStatus(proc *nativeProcess) *DeploymentStatus {
	proc.mu.Lock()
	ready := proc.ready
	proc.mu.Unlock()

	phase := "running"
	if proc.cmd.ProcessState != nil {
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

// metaToStatus converts persisted metadata to a DeploymentStatus by checking port liveness.
func (r *NativeRuntime) metaToStatus(meta *deploymentMeta) *DeploymentStatus {
	alive := portAlive(meta.Port)

	phase := "running"
	ready := alive
	if !alive {
		timeout := meta.HealthCheckTimeout
		if timeout == 0 {
			timeout = 60
		}
		if time.Since(meta.StartTime) < time.Duration(timeout)*time.Second {
			phase = "starting"
		} else {
			phase = "stopped"
		}
	}

	return &DeploymentStatus{
		Name:      meta.Name,
		Phase:     phase,
		Ready:     ready,
		Address:   fmt.Sprintf("127.0.0.1:%d", meta.Port),
		Labels:    meta.Labels,
		StartTime: meta.StartTime.Format(time.RFC3339),
		Runtime:   "native",
	}
}

// --- Deployment metadata persistence ---

func (r *NativeRuntime) saveMeta(meta *deploymentMeta) error {
	if err := os.MkdirAll(r.deployDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.deployDir, meta.Name+".json"), data, 0o644)
}

func (r *NativeRuntime) loadMeta(name string) (*deploymentMeta, error) {
	data, err := os.ReadFile(filepath.Join(r.deployDir, name+".json"))
	if err != nil {
		return nil, err
	}
	var meta deploymentMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func (r *NativeRuntime) removeMeta(name string) {
	os.Remove(filepath.Join(r.deployDir, name+".json"))
}

func (r *NativeRuntime) loadAllMeta() []*deploymentMeta {
	entries, err := os.ReadDir(r.deployDir)
	if err != nil {
		return nil
	}
	var metas []*deploymentMeta
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if meta, err := r.loadMeta(name); err == nil {
			metas = append(metas, meta)
		}
	}
	return metas
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

// findInDist checks for a binary in the dist directory.
// On Windows, also tries with .exe suffix.
func (r *NativeRuntime) findInDist(name string) string {
	candidates := []string{name}
	if goruntime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
		candidates = append(candidates, name+".exe")
	}
	for _, c := range candidates {
		p := filepath.Join(r.distDir, c)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
