package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
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
	Config             map[string]any    `json:"config,omitempty"`
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
	logDir          string
	distDir         string // e.g. ~/.aima/dist/windows-amd64/
	deployDir       string // e.g. ~/.aima/deployments/ — persisted deployment metadata
	resolveBinary   BinaryResolveFunc
	engineAssets    []knowledge.EngineAsset
	processes       map[string]*nativeProcess
	progressTracker *ProgressTracker
	mu              sync.RWMutex
}

func NewNativeRuntime(logDir, distDir, deployDir string, opts ...NativeOption) *NativeRuntime {
	r := &NativeRuntime{
		logDir:          logDir,
		distDir:         distDir,
		deployDir:       deployDir,
		processes:       make(map[string]*nativeProcess),
		progressTracker: NewProgressTracker(),
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
		Config:         req.Config,
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

	port := 0
	if proc != nil {
		port = proc.port
	}

	if inMemory {
		defer proc.cancel()
		if proc.cmd != nil {
			// Standard Go child process. When launched detached on Unix, stop the
			// whole process group so engine worker children do not survive the root.
			// Use SIGTERM first to allow graceful cleanup (e.g., sglang-kt's
			// kill_process_tree), then escalate to SIGKILL if still alive.
			if proc.processGroupID > 0 {
				if err := terminateProcessGroup(proc.processGroupID); err != nil {
					slog.Warn("SIGTERM process group", "name", name, "pgid", proc.processGroupID, "error", err)
				}
			} else {
				proc.cancel()
			}
			if !waitForProcessExit(proc, 5*time.Second) {
				slog.Warn("process did not exit after SIGTERM; sending SIGKILL", "name", name)
				if proc.processGroupID > 0 {
					if err := killProcessGroup(proc.processGroupID); err != nil {
						slog.Warn("SIGKILL process group", "name", name, "pgid", proc.processGroupID, "error", err)
					}
				} else if proc.cmd.Process != nil {
					if err := proc.cmd.Process.Kill(); err != nil {
						slog.Warn("force kill process", "name", name, "error", err)
					}
				}
				if !waitForProcessExit(proc, 5*time.Second) {
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
		port = meta.Port
		if meta.PID > 0 {
			if processMatchesMeta(meta) {
				if meta.ProcessGroupID > 0 {
					// Graceful: SIGTERM first to allow engine cleanup (e.g.,
					// sglang-kt kill_process_tree), then SIGKILL as fallback.
					if err := terminateProcessGroup(meta.ProcessGroupID); err != nil {
						slog.Warn("SIGTERM process group (meta)", "name", name, "pgid", meta.ProcessGroupID, "error", err)
					}
					time.Sleep(5 * time.Second)
					if err := killProcessGroup(meta.ProcessGroupID); err != nil {
						slog.Warn("SIGKILL process group (meta)", "name", name, "pgid", meta.ProcessGroupID, "error", err)
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

	if !waitForPortRelease(port, nativePortReleaseTimeout) {
		r.progressTracker.Remove(name)
		r.removeMeta(name)
		return fmt.Errorf("stop deployment %q: port %d is still in use after process exit", name, port)
	}

	r.progressTracker.Remove(name)
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
	type procEntry struct {
		name string
		proc *nativeProcess
	}
	entries := make([]procEntry, 0, len(r.processes))
	seen := make(map[string]bool, len(r.processes))
	for name, proc := range r.processes {
		if proc == nil {
			continue // placeholder from in-progress deploy
		}
		entries = append(entries, procEntry{name: name, proc: proc})
		seen[name] = true
	}
	r.mu.RUnlock()

	statuses := make([]*DeploymentStatus, 0, len(entries))
	for _, entry := range entries {
		statuses = append(statuses, r.procStatusWithPersistedOverride(entry.name, entry.proc))
	}

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
	if len(status.Config) == 0 && len(persisted.Config) > 0 {
		status.Config = cloneConfigForStatus(persisted.Config)
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
	hcClient := &http.Client{Timeout: 3 * time.Second}

	// Build a warmup client once with appropriate timeout, reused across retries.
	var warmupClient *http.Client
	if warmup != nil {
		wt := time.Duration(warmup.TimeoutS) * time.Second
		if wt == 0 {
			wt = 30 * time.Second
		}
		warmupClient = &http.Client{Timeout: wt}
	}

	for time.Now().Before(deadline) {
		proc.mu.Lock()
		exited := proc.exited
		proc.mu.Unlock()
		if exited {
			slog.Warn("health check aborted: process already exited", "name", proc.name)
			return
		}

		resp, err := hcClient.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				slog.Info("health check passed", "name", proc.name)
				// Run warmup before marking ready. A successful health endpoint alone
				// is not enough because another process may already own the same port.
				if warmup != nil && !r.warmup(proc, warmup, warmupClient) {
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
func (r *NativeRuntime) warmup(proc *nativeProcess, cfg *WarmupConfig, client *http.Client) bool {
	prompt := cfg.Prompt
	if prompt == "" {
		prompt = "Hello"
	}
	maxTokens := cfg.MaxTokens
	if maxTokens == 0 {
		maxTokens = 1
	}
	modelName := proc.name
	if proc.labels != nil && proc.labels["aima.dev/model"] != "" {
		modelName = proc.labels["aima.dev/model"]
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", proc.port)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}],"max_tokens":%d}`, modelName, prompt, maxTokens)

	slog.Info("warming up engine", "name", proc.name, "url", url)
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
		Config:  cloneConfigForStatus(meta.Config),
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

func cloneConfigForStatus(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
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
