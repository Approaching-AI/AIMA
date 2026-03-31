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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/knowledge"
)

// deploymentMeta is persisted to disk so deployments survive across CLI invocations.
type deploymentMeta struct {
	Name               string            `json:"name"`
	PID                int               `json:"pid"`
	ProcessGroupID     int               `json:"process_group_id,omitempty"`
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
	name           string
	cmd            *exec.Cmd // nil when launched via schtasks on Windows
	pid            int       // Process ID; set even when cmd is nil
	processGroupID int
	cancel         context.CancelFunc
	done           chan struct{}
	logFile        *os.File
	logPath        string
	port           int
	labels         map[string]string
	startTime      time.Time
	startupTimeout time.Duration
	ready          bool
	exited         bool
	exitSuccess    bool // true if process exited with code 0
	mu             sync.Mutex
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
	engineAssets  []knowledge.EngineAsset
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

// WithNativeEngineAssets provides engine asset data for startup progress detection.
func WithNativeEngineAssets(assets []knowledge.EngineAsset) NativeOption {
	return func(r *NativeRuntime) {
		r.engineAssets = assets
	}
}

func (r *NativeRuntime) Name() string { return "native" }

func (r *NativeRuntime) Deploy(ctx context.Context, req *DeployRequest) error {
	// Hold lock for the full existence check to prevent concurrent deploys of the same name.
	r.mu.Lock()
	if _, exists := r.processes[req.Name]; exists {
		r.mu.Unlock()
		return fmt.Errorf("deployment %q already exists", req.Name)
	}
	// Check persisted metadata: if a deployment with this name is still alive, reject
	if meta, err := r.loadMeta(req.Name); err == nil {
		status := r.metaToStatus(meta)
		if status.Phase == "running" || status.Phase == "starting" {
			r.mu.Unlock()
			return fmt.Errorf("deployment %q already %s (PID %d, port %d)", req.Name, status.Phase, meta.PID, meta.Port)
		}
		// Stale metadata — clean up
		r.removeMeta(req.Name)
	}
	// Reserve the name with a placeholder to prevent concurrent deploys while lock is released.
	r.processes[req.Name] = nil
	r.mu.Unlock()

	// clearPlaceholder removes the nil reservation on failure paths before cmd.Start.
	clearPlaceholder := func() {
		r.mu.Lock()
		delete(r.processes, req.Name)
		r.mu.Unlock()
	}

	if len(req.Command) == 0 {
		clearPlaceholder()
		return fmt.Errorf("deploy %s: empty command", req.Name)
	}
	if req.Port > 0 {
		if conflict := r.portConflict(req.Port, req.Name); conflict != "" {
			clearPlaceholder()
			return fmt.Errorf("deploy %s: port %d already in use by %s", req.Name, req.Port, conflict)
		}
	}

	// Replace templates with actual values (host path, not /models like containers)
	command := make([]string, len(req.Command))
	for i, c := range req.Command {
		c = strings.ReplaceAll(c, "{{.ModelPath}}", req.ModelPath)
		c = strings.ReplaceAll(c, "{{.ModelName}}", req.Name)
		command[i] = c
	}
	portBindings := portBindingsForRequest(req)
	primaryPort := primaryPortForRequest(req)
	command = knowledge.AppendPortBindings(command, portBindings)

	// Append other config values as CLI flags, with template substitution
	for _, f := range configToFlags(req.Config, req.Command, req.ModelPath, knowledge.PortConfigKeys(req.PortSpecs)) {
		f = strings.ReplaceAll(f, "{{.ModelName}}", req.Name)
		f = strings.ReplaceAll(f, "{{.ModelPath}}", req.ModelPath)
		command = append(command, f)
	}

	// Set up log file
	if err := os.MkdirAll(r.logDir, 0o755); err != nil {
		clearPlaceholder()
		return fmt.Errorf("create log dir: %w", err)
	}
	logPath := filepath.Join(r.logDir, req.Name+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		clearPlaceholder()
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
	slog.Info("native deploy", "name", req.Name, "command", strings.Join(command, " "), "work_dir", req.WorkDir)

	var cmd *exec.Cmd
	var procPID int
	var procGroupID int
	var procLogFile *os.File

	if goruntime.GOOS == "windows" {
		_ = procCtx // schtasks creates its own process context
		// On Windows, launch via schtasks /it to ensure the process runs in the
		// interactive desktop session (Session 1). GPU engines (Vulkan/DirectX)
		// need display session access which is unavailable via SSH (Session 0).
		logFile.Close() // batch file will manage log output
		pid, err := r.launchViaSchtasks(req.Name, command, logPath, req.Env, req.WorkDir)
		if err != nil {
			cancel()
			clearPlaceholder()
			return fmt.Errorf("start %s via schtasks: %w", req.Name, err)
		}
		procPID = pid
		procLogFile = nil
	} else {
		cmd = exec.CommandContext(procCtx, command[0], command[1:]...)
		configureDetachedProcess(cmd)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		// Build environment: start with parent env, add distDir library path, then request env vars.
		env := os.Environ()
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
			env = append(env, ldVar+"="+newVal)
		}
		for k, v := range req.Env {
			env = append(env, k+"="+v)
		}
		if len(env) > 0 {
			cmd.Env = env
		}
		if req.WorkDir != "" {
			cmd.Dir = req.WorkDir
		}
		if err := cmd.Start(); err != nil {
			cancel()
			logFile.Close()
			clearPlaceholder()
			return fmt.Errorf("start %s: %w", req.Name, err)
		}
		procPID = cmd.Process.Pid
		procGroupID = childProcessGroupID(procPID)
		procLogFile = logFile
	}

	now := time.Now()
	proc := &nativeProcess{
		name:           req.Name,
		cmd:            cmd,
		pid:            procPID,
		processGroupID: procGroupID,
		cancel:         cancel,
		done:           make(chan struct{}),
		logFile:        procLogFile,
		logPath:        logPath,
		port:           primaryPort,
		labels:         req.Labels,
		startTime:      now,
		startupTimeout: effectiveHealthTimeout(req.HealthCheck),
	}

	r.mu.Lock()
	r.processes[req.Name] = proc
	r.mu.Unlock()

	// Persist deployment metadata for cross-invocation discovery
	meta := &deploymentMeta{
		Name:           req.Name,
		PID:            procPID,
		ProcessGroupID: procGroupID,
		Port:           primaryPort,
		Engine:         req.Engine,
		Labels:         req.Labels,
		LogPath:        logPath,
		Command:        command,
		StartTime:      now,
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
		defer proc.cancel()
		if proc.cmd != nil {
			// Standard Go child process. When launched detached on Unix, stop the
			// whole process group so engine worker children do not survive the root.
			if proc.processGroupID > 0 {
				if err := killProcessGroup(proc.processGroupID); err != nil {
					slog.Warn("kill process group", "name", name, "pgid", proc.processGroupID, "error", err)
				}
			} else {
				proc.cancel()
			}
			if !waitForProcessExit(proc, 5*time.Second) {
				slog.Warn("process did not exit within timeout; forcing kill", "name", name)
				if proc.cmd.Process != nil {
					if err := proc.cmd.Process.Kill(); err != nil {
						slog.Warn("force kill process", "name", name, "error", err)
					}
				}
				if !waitForProcessExit(proc, 2*time.Second) {
					r.removeMeta(name)
					return fmt.Errorf("stop deployment %q: process did not exit after force kill", name)
				}
			}
		} else if proc.pid > 0 {
			// External process (e.g., Windows schtasks): kill by PID
			if err := killProcessByPID(proc.pid); err != nil {
				slog.Warn("kill process by PID", "name", name, "pid", proc.pid, "error", err)
			}
			if !waitForProcessExit(proc, 5*time.Second) {
				slog.Warn("process did not exit within timeout after PID kill", "name", name, "pid", proc.pid)
			}
		}
	} else {
		// Recover from persisted metadata and kill by PID.
		// Guard against PID reuse: validate the process identity before killing.
		meta, err := r.loadMeta(name)
		if err != nil {
			return fmt.Errorf("deployment %q not found", name)
		}
		if meta.PID > 0 {
			if processMatchesMeta(meta) {
				if meta.ProcessGroupID > 0 {
					if err := killProcessGroup(meta.ProcessGroupID); err != nil {
						slog.Warn("kill process group", "name", name, "pgid", meta.ProcessGroupID, "error", err)
					}
				} else if err := killProcessByPID(meta.PID); err != nil {
					slog.Warn("kill process", "name", name, "pid", meta.PID, "error", err)
				}
			} else {
				slog.Warn("stale PID: process does not match deployment metadata, skipping kill",
					"name", name, "pid", meta.PID)
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

	if ok && proc != nil {
		return r.procStatusWithPersistedOverride(name, proc), nil
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
	for name, proc := range r.processes {
		if proc == nil {
			continue // placeholder from in-progress deploy
		}
		seen[name] = true
		statuses = append(statuses, r.procStatusWithPersistedOverride(name, proc))
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

func (r *NativeRuntime) procStatusWithPersistedOverride(name string, proc *nativeProcess) *DeploymentStatus {
	status := r.procToStatus(proc)
	meta, err := r.loadMeta(name)
	if err != nil {
		return status
	}

	persisted := r.metaToStatus(meta)
	proc.mu.Lock()
	exited := proc.exited
	proc.mu.Unlock()
	switch {
	case persisted.Phase == "failed" && status.Phase != "failed" && !(isStalePortReuseFailure(persisted.Message) && !exited):
		return persisted
	case persisted.Ready && !status.Ready:
		return persisted
	}

	if status.StartupMessage == "" {
		status.StartupMessage = persisted.StartupMessage
	}
	if status.StartupPhase == "" || persisted.StartupProgress > status.StartupProgress {
		status.StartupPhase = persisted.StartupPhase
		status.StartupProgress = persisted.StartupProgress
	}
	if status.ErrorLines == "" {
		status.ErrorLines = persisted.ErrorLines
	}
	if status.Message == "" && !(status.Phase != "failed" && isStalePortReuseFailure(persisted.Message)) {
		status.Message = persisted.Message
	}
	return status
}

func isStalePortReuseFailure(msg string) bool {
	return msg == "deployment metadata is stale; port is in use by another process"
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
	if proc.cmd != nil {
		// Standard Go child process: use Wait() for precise exit detection.
		err := proc.cmd.Wait()
		proc.mu.Lock()
		proc.exited = true
		proc.ready = false
		proc.exitSuccess = err == nil
		proc.mu.Unlock()
		if err != nil {
			slog.Warn("process exited with error", "name", proc.name, "error", err)
		} else {
			slog.Info("process exited", "name", proc.name)
		}
	} else {
		// External process (e.g., Windows schtasks): monitor by port liveness.
		// Phase 1: Wait until health check marks ready, or detect early crash.
		startupTimeout := proc.startupTimeout
		if startupTimeout <= 0 {
			startupTimeout = 60 * time.Second
		}
		startupDeadline := time.Now().Add(startupTimeout)
		for time.Now().Before(startupDeadline) {
			proc.mu.Lock()
			ready := proc.ready
			proc.mu.Unlock()
			if ready {
				break
			}
			// Check if PID vanished (process crashed during startup)
			if proc.pid > 0 && !pidAlive(proc.pid) {
				slog.Warn("process died during startup", "name", proc.name, "pid", proc.pid)
				break
			}
			time.Sleep(3 * time.Second)
		}
		// Phase 2: Monitor running process by port liveness.
		proc.mu.Lock()
		isReady := proc.ready
		proc.mu.Unlock()
		if isReady {
			for {
				time.Sleep(1 * time.Second)
				if proc.pid > 0 && !pidAlive(proc.pid) {
					break
				}
				if !externalProcessAlive(proc) {
					time.Sleep(1 * time.Second)
					if proc.pid > 0 && !pidAlive(proc.pid) {
						break
					}
					if !externalProcessAlive(proc) {
						break
					}
				}
			}
		}
		proc.mu.Lock()
		if !proc.exited {
			proc.exited = true
			proc.ready = false
			proc.exitSuccess = false
		}
		proc.mu.Unlock()
		slog.Info("process exited (detected via monitoring)", "name", proc.name, "pid", proc.pid)
	}
	if proc.logFile != nil {
		_ = proc.logFile.Close()
	}
	if proc.done != nil {
		close(proc.done)
	}
}

func (r *NativeRuntime) healthCheckAndWarmup(proc *nativeProcess, hc *HealthCheckConfig, warmup *WarmupConfig) {
	timeout := effectiveHealthTimeout(hc)
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:%d%s", proc.port, hc.Path)
	client := &http.Client{Timeout: 3 * time.Second}

	for time.Now().Before(deadline) {
		proc.mu.Lock()
		exited := proc.exited
		proc.mu.Unlock()
		if exited {
			slog.Warn("health check aborted: process already exited", "name", proc.name)
			return
		}

		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				slog.Info("health check passed", "name", proc.name)
				// Run warmup before marking ready. A successful health endpoint alone
				// is not enough because another process may already own the same port.
				if warmup != nil && !r.warmup(proc, warmup) {
					time.Sleep(2 * time.Second)
					continue
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
// It returns true only when the engine accepts the request successfully.
func (r *NativeRuntime) warmup(proc *nativeProcess, cfg *WarmupConfig) bool {
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
	modelName := proc.name
	if proc.labels != nil && proc.labels["aima.dev/model"] != "" {
		modelName = proc.labels["aima.dev/model"]
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", proc.port)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}],"max_tokens":%d}`, modelName, prompt, maxTokens)

	slog.Info("warming up engine", "name", proc.name, "url", url)
	client := &http.Client{Timeout: timeout}
	resp, err := client.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		slog.Warn("warmup request failed", "name", proc.name, "error", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		slog.Info("warmup complete", "name", proc.name)
		return true
	} else {
		slog.Warn("warmup returned non-200", "name", proc.name, "status", resp.StatusCode)
		return false
	}
}

func (r *NativeRuntime) procToStatus(proc *nativeProcess) *DeploymentStatus {
	proc.mu.Lock()
	ready := proc.ready
	exited := proc.exited
	exitSuccess := proc.exitSuccess
	proc.mu.Unlock()

	portBound := proc.port > 0 && portAlive(proc.port)

	phase := "running"
	if exited {
		if exitSuccess {
			phase = "stopped"
		} else {
			phase = "failed"
		}
		ready = false
	} else if !ready {
		phase = "starting"
	}

	ds := &DeploymentStatus{
		Name:    proc.name,
		Phase:   phase,
		Ready:   ready,
		Address: fmt.Sprintf("127.0.0.1:%d", proc.port),
		Labels:  proc.labels,
		Runtime: "native",
	}
	setDeploymentStartFromTime(ds, proc.startTime)

	// Enrich with log-based progress for non-ready deployments
	if !ready && proc.logPath != "" {
		if errMsg := r.enrichNativeProgress(ds, proc.logPath, proc.labels); errMsg != "" && ds.Phase != "stopped" {
			ds.Phase = "failed"
			ds.Ready = false
			ds.Message = errMsg
		}
	}
	if !ds.Ready && ds.Phase != "failed" && ds.Phase != "stopped" {
		r.ensureNativeStartingStatus(ds, proc.startTime, portBound, proc.labels)
	}

	return ds
}

// metaToStatus converts persisted metadata to a DeploymentStatus by checking port liveness
// and HTTP health endpoint (not just TCP). vLLM and other engines bind the port early,
// before model weights are loaded, so TCP alive does NOT mean ready to serve.
func (r *NativeRuntime) metaToStatus(meta *deploymentMeta) *DeploymentStatus {
	alive := portAlive(meta.Port)
	processMatches := meta.PID <= 0 || processMatchesMeta(meta)

	phase := "running"
	ready := false
	if !processMatches {
		phase = "failed"
		ready = false
	} else if alive {
		// Port is alive (TCP), but check HTTP health to confirm engine is truly ready.
		// Look up engine asset for the health check path.
		engineName := ""
		if meta.Labels != nil {
			engineName = meta.Labels["aima.dev/engine"]
		}
		if meta.HealthCheckPath != "" {
			ready = httpHealthy(meta.Port, meta.HealthCheckPath)
		} else if asset := findEngineAsset(r.engineAssets, engineName); asset != nil && asset.Startup.HealthCheck.Path != "" {
			ready = httpHealthy(meta.Port, asset.Startup.HealthCheck.Path)
		} else {
			// No health check info available; fall back to TCP alive.
			ready = true
		}
		if !ready {
			phase = "starting"
		}
	} else {
		timeout := meta.HealthCheckTimeout
		if timeout == 0 {
			timeout = 60
		}
		if time.Since(meta.StartTime) < time.Duration(timeout)*time.Second {
			phase = "starting"
		} else {
			// Port dead past health check timeout: process crashed or never started.
			// Intentional stops go through Delete() which removes metadata entirely.
			phase = "failed"
		}
	}

	ds := &DeploymentStatus{
		Name:    meta.Name,
		Phase:   phase,
		Ready:   ready,
		Address: fmt.Sprintf("127.0.0.1:%d", meta.Port),
		Labels:  meta.Labels,
		Runtime: "native",
	}
	setDeploymentStartFromTime(ds, meta.StartTime)

	// Enrich with log-based progress for non-ready deployments
	if !ready && meta.LogPath != "" {
		if errMsg := r.enrichNativeProgress(ds, meta.LogPath, meta.Labels); errMsg != "" {
			ds.Phase = "failed"
			ds.Ready = false
			ds.Message = errMsg
		}
	}
	if !ds.Ready && ds.Phase != "failed" && ds.Phase != "stopped" {
		r.ensureNativeStartingStatus(ds, meta.StartTime, alive, meta.Labels)
	}
	if ds.Phase == "failed" && ds.Message == "" && meta.PID > 0 && !processMatches {
		if alive {
			ds.Message = "deployment metadata is stale; port is in use by another process"
		} else {
			ds.Message = "process exited before readiness"
		}
	}

	return ds
}

// --- Deployment metadata persistence ---

func (r *NativeRuntime) saveMeta(meta *deploymentMeta) error {
	if err := os.MkdirAll(r.deployDir, 0o755); err != nil {
		return fmt.Errorf("create meta dir %s: %w", r.deployDir, err)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal deployment meta %s: %w", meta.Name, err)
	}
	return os.WriteFile(filepath.Join(r.deployDir, meta.Name+".json"), data, 0o644)
}

func (r *NativeRuntime) loadMeta(name string) (*deploymentMeta, error) {
	data, err := os.ReadFile(filepath.Join(r.deployDir, name+".json"))
	if err != nil {
		return nil, fmt.Errorf("read deployment meta %s: %w", name, err)
	}
	var meta deploymentMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse deployment meta %s: %w", name, err)
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

// processMatchesMeta validates that the process at the given PID still matches the
// deployment metadata. This guards against PID reuse — if the OS recycled the PID
// for a different process, we must not kill it.
func processMatchesMeta(meta *deploymentMeta) bool {
	if meta.PID <= 0 || len(meta.Command) == 0 {
		return false
	}
	// On Linux, read /proc/<pid>/cmdline to verify the process identity.
	if goruntime.GOOS == "linux" {
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", meta.PID))
		if err != nil {
			return false // process doesn't exist
		}
		raw := strings.TrimRight(string(cmdline), "\x00")
		if raw == "" {
			return false
		}
		procArgs := strings.Split(raw, "\x00")
		return commandPrefixMatches(procArgs, meta.Command)
	}
	if goruntime.GOOS != "windows" {
		out, err := exec.Command("ps", "-ww", "-p", strconv.Itoa(meta.PID), "-o", "command=").Output()
		if err != nil {
			return false
		}
		return commandLineMatches(strings.TrimSpace(string(out)), meta.Command)
	}
	// On non-Linux (macOS, Windows): fall back to port check as best-effort.
	// If the port the deployment was using is still alive, assume the process is ours.
	if meta.Port > 0 {
		return portAlive(meta.Port)
	}
	return false
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

func (r *NativeRuntime) portConflict(port int, selfName string) string {
	if port <= 0 || !portAlive(port) {
		return ""
	}

	r.mu.RLock()
	for name, proc := range r.processes {
		if name == selfName || proc == nil {
			continue
		}
		if proc.port == port {
			r.mu.RUnlock()
			return fmt.Sprintf("deployment %q", name)
		}
	}
	r.mu.RUnlock()

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

// readTail reads the last n lines from a file by seeking from the end,
// avoiding reading the entire file into memory for large log files.
// For small files (< 256KB), uses a forward scan for reliability on Windows
// where stat size may lag behind writes from child processes.
func readTail(path string, n int) (string, error) {
	if n <= 0 {
		n = 100
	}

	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open log: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat log: %w", err)
	}
	size := stat.Size()

	// For small files or when stat reports size 0 (Windows edge case with
	// unflushed child process writes), use a simple forward scan.
	const seekThreshold = 256 * 1024 // 256KB
	if size < seekThreshold {
		return readTailForward(f, n)
	}

	// Large files: seek from end to avoid reading gigabytes of logs.
	const initialChunk = 64 * 1024 // 64KB
	chunkSize := int64(initialChunk)

	for {
		offset := size - chunkSize
		if offset < 0 {
			offset = 0
			chunkSize = size
		}

		buf := make([]byte, chunkSize)
		nRead, err := f.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("read log: %w", err)
		}
		buf = buf[:nRead]

		// Count newlines from the end
		lineCount := 0
		cutPos := len(buf)
		for i := len(buf) - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				lineCount++
				if lineCount > n {
					cutPos = i + 1
					break
				}
			}
		}

		if lineCount > n || offset == 0 {
			result := strings.TrimRight(string(buf[cutPos:]), "\n\r")
			return result, nil
		}

		// Need more data — double chunk size
		chunkSize *= 2
		if chunkSize > size {
			chunkSize = size
		}
	}
}

// readTailForward reads an already-opened file line by line, keeping the last n lines.
// Used for small files where the seek optimization isn't needed.
func readTailForward(f *os.File, n int) (string, error) {
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lines []string
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

// enrichNativeProgress reads log tail and matches engine patterns to set progress fields.
func (r *NativeRuntime) enrichNativeProgress(ds *DeploymentStatus, logPath string, labels map[string]string) string {
	engineName := ""
	if labels != nil {
		engineName = labels["aima.dev/engine"]
	}
	asset := findEngineAsset(r.engineAssets, engineName)

	if asset != nil && len(asset.TimeConstraints.ColdStartS) >= 2 {
		ds.EstimatedTotalS = asset.TimeConstraints.ColdStartS[1]
	}

	tailLines := 50
	if ds.Phase == "failed" {
		tailLines = 5
	}

	logs, err := readTail(logPath, tailLines)
	if err != nil || logs == "" {
		return ""
	}

	if ds.Phase == "failed" {
		ds.ErrorLines = logs
	}

	if asset == nil || asset.Startup.LogPatterns == nil {
		return ""
	}

	if errMsg := DetectStartupError(logs, asset.Startup.LogPatterns); errMsg != "" {
		ds.StartupMessage = errMsg
		return errMsg
	}

	if ds.Phase == "starting" || (ds.Phase == "running" && !ds.Ready) {
		sp := DetectStartupProgress(logs, asset.Startup.LogPatterns)
		if sp.Progress > 0 {
			ds.StartupPhase = sp.Phase
			ds.StartupProgress = sp.Progress
			ds.StartupMessage = sp.Message
		}
	}
	return ""
}

func (r *NativeRuntime) ensureNativeStartingStatus(ds *DeploymentStatus, startTime time.Time, portBound bool, labels map[string]string) {
	if ds == nil || ds.Ready || ds.Phase == "failed" || ds.Phase == "stopped" {
		return
	}
	ds.Phase = "starting"
	if ds.EstimatedTotalS == 0 {
		ds.EstimatedTotalS = r.nativeEstimatedTotalS(labels)
	}
	if ds.StartupPhase == "" {
		if portBound {
			ds.StartupPhase = "loading_model"
		} else {
			ds.StartupPhase = "initializing"
		}
	}
	if inferred := inferNativeStartupProgress(time.Since(startTime), ds.EstimatedTotalS, portBound); inferred > ds.StartupProgress {
		ds.StartupProgress = inferred
	}
	if ds.StartupMessage == "" {
		ds.StartupMessage = formatPhaseName(ds.StartupPhase)
	}
}

func (r *NativeRuntime) nativeEstimatedTotalS(labels map[string]string) int {
	engineName := ""
	if labels != nil {
		engineName = labels["aima.dev/engine"]
	}
	asset := findEngineAsset(r.engineAssets, engineName)
	if asset != nil && len(asset.TimeConstraints.ColdStartS) >= 2 {
		return asset.TimeConstraints.ColdStartS[1]
	}
	return 0
}

func inferNativeStartupProgress(elapsed time.Duration, estimatedTotalS int, portBound bool) int {
	elapsedS := int(elapsed / time.Second)
	if elapsedS < 0 {
		elapsedS = 0
	}
	if estimatedTotalS <= 0 {
		if portBound {
			return 35
		}
		return 5
	}
	if portBound {
		progress := 25 + (elapsedS * 65 / estimatedTotalS)
		if progress > 90 {
			progress = 90
		}
		if progress < 25 {
			progress = 25
		}
		return progress
	}
	progress := 5 + (elapsedS * 20 / estimatedTotalS)
	if progress > 25 {
		progress = 25
	}
	if progress < 5 {
		progress = 5
	}
	return progress
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

func killProcessByPID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	if goruntime.GOOS == "windows" {
		out, err := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).CombinedOutput()
		if err != nil {
			if !pidAlive(pid) {
				return nil
			}
			return fmt.Errorf("taskkill pid %d: %w (output: %s)", pid, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

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

// pidAlive checks if a process with the given PID exists.
// On Windows, uses tasklist. On other platforms, conservatively returns true
// (schtasks-based launching is Windows-only, so this path is not exercised).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if goruntime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/fi",
			fmt.Sprintf("PID eq %d", pid), "/nh").Output()
		if err != nil {
			return false
		}
		return strings.Contains(string(out), strconv.Itoa(pid))
	}
	return true
}
