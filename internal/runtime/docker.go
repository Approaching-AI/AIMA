package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"

	"github.com/jguan/aima/internal/knowledge"
)

// DockerRuntime manages inference engines as Docker containers.
type DockerRuntime struct {
	engineAssets []knowledge.EngineAsset
}

func NewDockerRuntime(engineAssets []knowledge.EngineAsset) *DockerRuntime {
	return &DockerRuntime{engineAssets: engineAssets}
}

func (r *DockerRuntime) Name() string { return "docker" }

// DockerAvailable checks whether Docker is accessible on this system.
func DockerAvailable(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

func (r *DockerRuntime) Deploy(ctx context.Context, req *DeployRequest) error {
	name := knowledge.SanitizePodName(req.Name + "-" + req.Engine)

	// Idempotent redeploy: remove existing container if any
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()

	args := r.buildRunArgs(name, req)
	slog.Info("docker deploy", "name", name, "args", strings.Join(args, " "))

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
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
	if req.Port > 0 {
		args = append(args, "--label", "aima.dev/port="+strconv.Itoa(req.Port))
	}

	// Port publish
	if req.Port > 0 {
		portStr := strconv.Itoa(req.Port)
		args = append(args, "--publish", portStr+":"+portStr)
	}

	// Environment variables: merge Container.Env (base) + req.Env (override)
	env := make(map[string]string)
	if req.Container != nil {
		for k, v := range req.Container.Env {
			env[k] = v
		}
	}
	for k, v := range req.Env {
		env[k] = v
	}
	for k, v := range env {
		args = append(args, "--env", k+"="+v)
	}

	// GPU access: NVIDIA uses --gpus all; AMD/DCU uses --device
	if env["NVIDIA_VISIBLE_DEVICES"] != "" {
		args = append(args, "--gpus", "all")
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
		args = append(args, "--volume", req.ModelPath+":/models:ro")
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

	// Build command with {{.ModelPath}} → /models substitution
	command := make([]string, len(req.Command))
	for i, c := range req.Command {
		command[i] = strings.ReplaceAll(c, "{{.ModelPath}}", "/models")
	}

	// Image
	image := req.Image
	args = append(args, image)

	// InitCommands: wrap with sh -c "init1 && init2 && exec main args..."
	if len(req.InitCommands) > 0 {
		// Clear the image-only append above, replace with entrypoint override
		args = args[:len(args)-1] // remove image

		initChain := strings.Join(req.InitCommands, " && ")
		mainCmd := strings.Join(command, " ")
		shellCmd := initChain + " && exec " + mainCmd

		args = append(args, image, "sh", "-c", shellCmd)
	} else if len(command) > 0 {
		args = append(args, command...)
	}

	return args
}

func (r *DockerRuntime) Delete(ctx context.Context, name string) error {
	out, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker rm %s: %w\n%s", name, err, string(out))
	}
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
		r.enrichDockerProgress(ctx, ds)
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

	var statuses []*DeploymentStatus
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
		addr := ""
		if port > 0 {
			addr = "127.0.0.1:" + strconv.Itoa(port)
		}

		ds := &DeploymentStatus{
			Name:      ps.Names,
			Phase:     phase,
			Ready:     ready,
			Address:   addr,
			Labels:    labels,
			StartTime: ps.CreatedAt,
			Runtime:   "docker",
		}

		if !ready {
			r.enrichDockerProgress(ctx, ds)
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
	addr := ""
	if port > 0 {
		addr = "127.0.0.1:" + strconv.Itoa(port)
	}

	name := strings.TrimPrefix(di.Name, "/")

	ds := &DeploymentStatus{
		Name:      name,
		Phase:     phase,
		Ready:     ready,
		Address:   addr,
		Labels:    labels,
		StartTime: di.State.StartedAt,
		Runtime:   "docker",
	}

	if di.State.Status == "exited" && di.State.ExitCode != 0 {
		ec := di.State.ExitCode
		ds.ExitCode = &ec
	}

	return ds
}

// enrichDockerProgress reads container logs and matches engine patterns.
func (r *DockerRuntime) enrichDockerProgress(ctx context.Context, ds *DeploymentStatus) {
	engineName := ""
	if ds.Labels != nil {
		engineName = ds.Labels["aima.dev/engine"]
	}
	asset := findEngineAsset(r.engineAssets, engineName)

	if asset != nil && len(asset.TimeConstraints.ColdStartS) >= 2 {
		ds.EstimatedTotalS = asset.TimeConstraints.ColdStartS[1]
	}

	tailLines := 50
	if ds.Phase == "failed" {
		tailLines = 5
	}

	logs, err := r.Logs(ctx, ds.Name, tailLines)
	if err != nil || logs == "" {
		return
	}

	if ds.Phase == "failed" {
		ds.ErrorLines = logs
	}

	if asset == nil || asset.Startup.LogPatterns == nil {
		return
	}

	if errMsg := DetectStartupError(logs, asset.Startup.LogPatterns); errMsg != "" {
		ds.StartupMessage = errMsg
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
