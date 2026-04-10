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
	"strconv"
	"strings"
	"time"

	"github.com/jguan/aima/internal/knowledge"
)

// DockerRuntime manages inference engines as Docker containers.
type DockerRuntime struct {
	engineAssets    []knowledge.EngineAsset
	progressTracker *ProgressTracker
}

func NewDockerRuntime(engineAssets []knowledge.EngineAsset) *DockerRuntime {
	return &DockerRuntime{
		engineAssets:    engineAssets,
		progressTracker: NewProgressTracker(),
	}
}

func (r *DockerRuntime) Name() string { return "docker" }

// DockerAvailable checks whether Docker is accessible on this system.
func DockerAvailable(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, "docker", "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

func (r *DockerRuntime) Deploy(ctx context.Context, req *DeployRequest) error {
	name := knowledge.SanitizePodName(req.Name + "-" + req.Engine)

	// Idempotent redeploy: remove existing container if any
	r.removeContainer(ctx, name)

	args := r.buildRunArgs(name, req)
	slog.Info("docker deploy", "name", name, "args", strings.Join(args, " "))

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err == nil {
		return nil
	}
	if shouldRetryDockerWithLegacyNVIDIAGPU(args, out) {
		slog.Warn("docker CDI GPU path failed, retrying with --gpus all", "name", name)
		r.removeContainer(ctx, name)
		fallbackArgs := dockerArgsWithLegacyNVIDIAGPU(args)
		slog.Info("docker deploy retry", "name", name, "args", strings.Join(fallbackArgs, " "))
		retryOut, retryErr := exec.CommandContext(ctx, "docker", fallbackArgs...).CombinedOutput()
		if retryErr == nil {
			return nil
		}
		r.removeContainer(ctx, name)
		return fmt.Errorf("docker run %s: %w\n%s", name, retryErr, string(retryOut))
	}
	if err != nil {
		r.removeContainer(ctx, name)
		return fmt.Errorf("docker run %s: %w\n%s", name, err, string(out))
	}
	return nil
}

func (r *DockerRuntime) buildRunArgs(name string, req *DeployRequest) []string {
	args := []string{"run", "-d", "--name", name, "--restart", "unless-stopped", "--ipc=host"}

	// Labels
	for k, v := range req.Labels {
		args = append(args, "--label", k+"="+v)
	}
	// Store port in label for status lookup
	if port := primaryPortForRequest(req); port > 0 {
		args = append(args, "--label", "aima.dev/port="+strconv.Itoa(port))
	}

	// --runtime (e.g. ascend)
	if req.Container != nil && req.Container.DockerRuntime != "" {
		args = append(args, "--runtime", req.Container.DockerRuntime)
	}

	// --init
	if req.Container != nil && req.Container.Init {
		args = append(args, "--init")
	}

	// --network host (skip --publish when host network)
	if req.Container != nil && req.Container.NetworkMode == "host" {
		args = append(args, "--network", "host")
	}

	// --shm-size
	if req.Container != nil && req.Container.ShmSize != "" {
		args = append(args, "--shm-size", req.Container.ShmSize)
	}

	// Port publish (skip when using host network)
	if port := primaryPortForRequest(req); port > 0 && (req.Container == nil || req.Container.NetworkMode != "host") {
		portStr := strconv.Itoa(port)
		args = append(args, "--publish", portStr+":"+portStr)
	}

	// AIMA owns readiness via knowledge YAML. Never inherit image-baked
	// health checks blindly; override them when YAML provides one, otherwise
	// disable them to avoid stale ports or incorrect image defaults.
	args = append(args, dockerHealthArgs(req)...)

	modelHostPath := req.ModelPath
	containerModelPath := "/models"
	if isContainerModelFilePath(modelHostPath) {
		containerModelPath = "/models/" + filepath.Base(modelHostPath)
		modelHostPath = filepath.Dir(modelHostPath)
	}

	// Environment variables: merge Container.Env (base) + req.Env (override)
	env := make(map[string]string)
	if req.Container != nil {
		for k, v := range req.Container.Env {
			env[k] = expandRuntimeTemplate(v, containerModelPath, req.Name)
		}
	}
	for k, v := range req.Env {
		env[k] = expandRuntimeTemplate(v, containerModelPath, req.Name)
	}
	for k, v := range env {
		args = append(args, "--env", k+"="+v)
	}

	// GPU access: NVIDIA uses CDI (preferred) or --gpus all (fallback); AMD/DCU uses --device
	if env["NVIDIA_VISIBLE_DEVICES"] != "" {
		if cdiAvailable() {
			// CDI: single binary nvidia-ctk generates /etc/cdi/nvidia.yaml — works with Docker 25+
			args = append(args, "--device", "nvidia.com/gpu=all")
		} else {
			// Fallback: traditional --gpus flag (requires nvidia-container-toolkit installed via distro pkg)
			args = append(args, "--gpus", "all")
		}
	}

	// Container devices (AMD /dev/kfd, /dev/dri, DCU devices)
	if req.Container != nil {
		for _, dev := range req.Container.Devices {
			args = append(args, "--device", dev)
		}
	}

	// Security: privileged, supplemental groups
	if req.Container != nil && req.Container.Security != nil {
		sec := req.Container.Security
		if sec.Privileged {
			args = append(args, "--privileged")
		}
		for _, gid := range sec.SupplementalGroups {
			args = append(args, "--group-add", strconv.Itoa(gid))
		}
	}

	// Model volume
	if req.ModelPath != "" {
		args = append(args, "--volume", modelHostPath+":/models:ro")
	}

	// Container volumes from hardware profile
	if req.Container != nil {
		for _, vol := range req.Container.Volumes {
			v := vol.HostPath + ":" + vol.MountPath
			if vol.ReadOnly {
				v += ":ro"
			}
			args = append(args, "--volume", v)
		}
	}

	// Extra volumes from engine/model YAML
	for _, vol := range req.ExtraVolumes {
		v := vol.HostPath + ":" + vol.MountPath
		if vol.ReadOnly {
			v += ":ro"
		}
		args = append(args, "--volume", v)
	}

	// Build command with template substitution
	command := make([]string, len(req.Command))
	for i, c := range req.Command {
		c = strings.ReplaceAll(c, "{{.ModelPath}}", containerModelPath)
		c = strings.ReplaceAll(c, "{{.ModelName}}", req.Name)
		command[i] = c
	}
	portBindings := portBindingsForRequest(req)
	command = knowledge.AppendPortBindings(command, portBindings)

	// Append config values as CLI flags, with template substitution
	for _, f := range configToFlags(req.Config, req.Command, req.ModelPath, knowledge.PortConfigKeys(req.PortSpecs)) {
		f = strings.ReplaceAll(f, "{{.ModelName}}", req.Name)
		f = strings.ReplaceAll(f, "{{.ModelPath}}", containerModelPath)
		command = append(command, f)
	}

	// Image + command.
	// When YAML defines a command, override the image ENTRYPOINT so the YAML
	// command is the full process invocation (matches K3S `command:` semantics).
	// Without --entrypoint override, Docker would concatenate ENTRYPOINT + our
	// command, causing duplication (e.g. "vllm serve vllm serve /models").
	image := req.Image

	if len(req.InitCommands) > 0 {
		initChain := strings.Join(req.InitCommands, " && ")
		mainCmd := shellJoin(command)
		shellCmd := initChain + " && exec " + mainCmd

		args = append(args, "--entrypoint", "bash", image, "-c", shellCmd)
	} else if len(command) > 0 {
		args = append(args, "--entrypoint", command[0], image)
		args = append(args, command[1:]...)
	} else {
		args = append(args, image)
	}

	return args
}

// shellJoin joins args into a shell-safe command string.
// Args containing shell metacharacters are single-quoted.
func shellJoin(args []string) string {
	var b strings.Builder
	for i, arg := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		if arg == "" || strings.ContainsAny(arg, " \t\n\"'\\|&;(){}$`!#~<>?*[]") {
			b.WriteByte('\'')
			b.WriteString(strings.ReplaceAll(arg, "'", "'\\''"))
			b.WriteByte('\'')
		} else {
			b.WriteString(arg)
		}
	}
	return b.String()
}

func expandRuntimeTemplate(value, modelPath, modelName string) string {
	value = strings.ReplaceAll(value, "{{.ModelPath}}", modelPath)
	value = strings.ReplaceAll(value, "{{.ModelName}}", modelName)
	return value
}

func (r *DockerRuntime) Delete(ctx context.Context, name string) error {
	out, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker rm %s: %w\n%s", name, err, string(out))
	}
	r.progressTracker.Remove(name)
	return nil
}

func (r *DockerRuntime) Status(ctx context.Context, name string) (*DeploymentStatus, error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect", name).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("deployment %q not found", name)
	}

	var inspects []dockerInspect
	if err := json.Unmarshal(out, &inspects); err != nil {
		return nil, fmt.Errorf("parse docker inspect: %w", err)
	}
	if len(inspects) == 0 {
		return nil, fmt.Errorf("deployment %q not found", name)
	}

	di := inspects[0]
	ds := r.inspectToStatus(di)

	// Enrich with log-based startup progress
	if !ds.Ready {
		if errMsg := r.enrichDockerProgress(ctx, ds); errMsg != "" {
			ds.Phase = "failed"
			ds.Ready = false
			ds.Message = errMsg
		}
	}

	return ds, nil
}

func (r *DockerRuntime) List(ctx context.Context) ([]*DeploymentStatus, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "label=aima.dev/engine",
		"--format", "{{json .}}").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w\n%s", err, string(out))
	}

	statuses := make([]*DeploymentStatus, 0)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var ps dockerPsEntry
		if err := json.Unmarshal([]byte(line), &ps); err != nil {
			slog.Warn("skip unparseable docker ps entry", "error", err)
			continue
		}

		labels := parseLabelString(ps.Labels)
		port := 0
		if p, ok := labels["aima.dev/port"]; ok {
			port, _ = strconv.Atoi(p)
		}

		phase := dockerStatusToPhase(ps.Status)
		ready := phase == "running" && port > 0 && portAlive(port)
		if ready {
			if asset := findEngineAsset(r.engineAssets, labels["aima.dev/engine"]); asset != nil && asset.Startup.HealthCheck.Path != "" {
				ready = httpHealthy(port, asset.Startup.HealthCheck.Path)
			}
		}
		addr := ""
		if port > 0 {
			addr = "127.0.0.1:" + strconv.Itoa(port)
		}

		ds := &DeploymentStatus{
			Name:    ps.Names,
			Phase:   phase,
			Ready:   ready,
			Address: addr,
			Labels:  labels,
			Runtime: "docker",
		}
		setDeploymentStartFromString(ds, ps.CreatedAt)

		if !ready {
			if errMsg := r.enrichDockerProgress(ctx, ds); errMsg != "" {
				ds.Phase = "failed"
				ds.Ready = false
				ds.Message = errMsg
			}
		}

		statuses = append(statuses, ds)
	}
	return statuses, nil
}

func (r *DockerRuntime) Logs(ctx context.Context, name string, tailLines int) (string, error) {
	if tailLines <= 0 {
		tailLines = 100
	}
	out, err := exec.CommandContext(ctx, "docker", "logs", "--tail", strconv.Itoa(tailLines), name).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker logs %s: %w", name, err)
	}
	return string(out), nil
}

// --- internal types ---

type dockerInspect struct {
	Name  string `json:"Name"`
	State struct {
		Status     string `json:"Status"` // running, created, exited, paused, restarting
		StartedAt  string `json:"StartedAt"`
		ExitCode   int    `json:"ExitCode"`
		Running    bool   `json:"Running"`
		Restarting bool   `json:"Restarting"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

type dockerPsEntry struct {
	Names     string `json:"Names"`
	Status    string `json:"Status"`
	Labels    string `json:"Labels"`
	CreatedAt string `json:"CreatedAt"`
}

func (r *DockerRuntime) inspectToStatus(di dockerInspect) *DeploymentStatus {
	labels := di.Config.Labels
	port := 0
	if p, ok := labels["aima.dev/port"]; ok {
		port, _ = strconv.Atoi(p)
	}

	phase := "stopped"
	switch di.State.Status {
	case "running":
		phase = "running"
	case "created", "restarting":
		phase = "starting"
	case "exited":
		if di.State.ExitCode != 0 {
			phase = "failed"
		} else {
			phase = "stopped"
		}
	case "paused":
		phase = "stopped"
	}

	ready := phase == "running" && port > 0 && portAlive(port)
	if ready {
		if asset := findEngineAsset(r.engineAssets, labels["aima.dev/engine"]); asset != nil && asset.Startup.HealthCheck.Path != "" {
			ready = httpHealthy(port, asset.Startup.HealthCheck.Path)
		}
	}
	addr := ""
	if port > 0 {
		addr = "127.0.0.1:" + strconv.Itoa(port)
	}

	name := strings.TrimPrefix(di.Name, "/")

	ds := &DeploymentStatus{
		Name:    name,
		Phase:   phase,
		Ready:   ready,
		Address: addr,
		Labels:  labels,
		Runtime: "docker",
	}
	setDeploymentStartFromString(ds, di.State.StartedAt)

	if di.State.Status == "exited" && di.State.ExitCode != 0 {
		ec := di.State.ExitCode
		ds.ExitCode = &ec
	}

	return ds
}

// enrichDockerProgress reads container logs and matches engine patterns.
func (r *DockerRuntime) enrichDockerProgress(ctx context.Context, ds *DeploymentStatus) string {
	engineName := ""
	if ds.Labels != nil {
		engineName = ds.Labels["aima.dev/engine"]
	}
	asset := findEngineAsset(r.engineAssets, engineName)

	if asset != nil && len(asset.TimeConstraints.ColdStartS) >= 2 {
		ds.EstimatedTotalS = asset.TimeConstraints.ColdStartS[1]
	}

	tailLines := 100
	if ds.Phase == "failed" {
		tailLines = 5
	}

	logs, err := r.Logs(ctx, ds.Name, tailLines)
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
		} else {
			ds.StartupPhase = "initializing"
			ds.StartupProgress = 5
			ds.StartupMessage = formatPhaseName("initializing")
		}
	}

	// Stall detection for starting deployments
	if ds.Phase == "starting" || (ds.Phase == "running" && !ds.Ready) {
		stalled, lastAt := r.progressTracker.Update(ds.Name, ds.StartupProgress, ds.EstimatedTotalS)
		ds.Stalled = stalled
		ds.LastProgressAt = lastAt.Unix()
	}
	return ""
}

// dockerStatusToPhase maps `docker ps` Status string to phase.
// Format examples: "Up 2 hours", "Exited (1) 5 minutes ago", "Created".
func dockerStatusToPhase(status string) string {
	s := strings.ToLower(status)
	switch {
	case strings.HasPrefix(s, "up"):
		return "running"
	case strings.HasPrefix(s, "exited"):
		// Parse exit code from "Exited (N) ..." to distinguish stopped vs failed.
		if i := strings.Index(s, "("); i >= 0 {
			if j := strings.Index(s[i:], ")"); j >= 0 {
				if strings.TrimSpace(s[i+1:i+j]) == "0" {
					return "stopped"
				}
			}
		}
		return "failed"
	case strings.HasPrefix(s, "created"):
		return "starting"
	case strings.HasPrefix(s, "restarting"):
		return "starting"
	default:
		return "stopped"
	}
}

// cdiAvailable checks whether CDI (Container Device Interface) is available
// by looking for the nvidia CDI spec file generated by nvidia-ctk.
func cdiAvailable() bool {
	_, err := os.Stat("/etc/cdi/nvidia.yaml")
	return err == nil
}

func (r *DockerRuntime) removeContainer(ctx context.Context, name string) {
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
}

func dockerHealthArgs(req *DeployRequest) []string {
	if req == nil || req.HealthCheck == nil || strings.TrimSpace(req.HealthCheck.Path) == "" {
		return []string{"--no-healthcheck"}
	}
	port := primaryPortForRequest(req)
	if port <= 0 {
		return []string{"--no-healthcheck"}
	}
	path := req.HealthCheck.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	probeTimeoutS := 5
	if req.HealthCheck.TimeoutS > probeTimeoutS && req.HealthCheck.TimeoutS < 30 {
		probeTimeoutS = req.HealthCheck.TimeoutS
	}
	startPeriodS := 60
	if req.HealthCheck.TimeoutS > startPeriodS {
		startPeriodS = req.HealthCheck.TimeoutS
	}
	cmd := fmt.Sprintf(
		`if command -v curl >/dev/null 2>&1; then curl -fsS --max-time %[1]d http://localhost:%[2]d%[3]s >/dev/null; elif command -v python3 >/dev/null 2>&1; then python3 -c "import sys, urllib.request; sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:%[2]d%[3]s', timeout=%[1]d).getcode() == 200 else 1)"; elif command -v python >/dev/null 2>&1; then python -c "import sys, urllib.request; sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:%[2]d%[3]s', timeout=%[1]d).getcode() == 200 else 1)"; else exit 1; fi || exit 1`,
		probeTimeoutS, port, path,
	)
	return []string{
		"--health-cmd", cmd,
		"--health-interval", "10s",
		"--health-timeout", fmt.Sprintf("%ds", probeTimeoutS),
		"--health-start-period", fmt.Sprintf("%ds", startPeriodS),
		"--health-retries", "3",
	}
}

func shouldRetryDockerWithLegacyNVIDIAGPU(args []string, output []byte) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--device" && args[i+1] == "nvidia.com/gpu=all" {
			text := strings.ToLower(string(output))
			return strings.Contains(text, `device driver "cdi"`) ||
				strings.Contains(text, "nvidia.com/gpu=all")
		}
	}
	return false
}

func dockerArgsWithLegacyNVIDIAGPU(args []string) []string {
	rewritten := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if i < len(args)-1 && args[i] == "--device" && args[i+1] == "nvidia.com/gpu=all" {
			rewritten = append(rewritten, "--gpus", "all")
			i++
			continue
		}
		rewritten = append(rewritten, args[i])
	}
	return rewritten
}

func isContainerModelFilePath(modelPath string) bool {
	switch strings.ToLower(filepath.Ext(modelPath)) {
	case ".gguf", ".ggml", ".bin":
		return true
	default:
		return false
	}
}

// httpHealthy checks whether the engine's HTTP health endpoint returns 200.
// Used to distinguish "TCP port open" (vLLM binds early) from "actually ready to serve".
func httpHealthy(port int, path string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// parseLabelString parses Docker's comma-separated label format "k=v,k2=v2".
// Note: AIMA labels never contain commas in values; if that changes, switch to
// docker inspect (which returns a proper JSON map) instead of docker ps format.
func parseLabelString(s string) map[string]string {
	m := make(map[string]string)
	if s == "" {
		return m
	}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(pair, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}
