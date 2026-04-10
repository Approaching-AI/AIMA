package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jguan/aima/internal/k3s"
	"github.com/jguan/aima/internal/knowledge"
)

// K3SOption configures a K3SRuntime.
type K3SOption func(*K3SRuntime)

// WithEngineAssets provides engine asset data for startup progress detection.
func WithEngineAssets(assets []knowledge.EngineAsset) K3SOption {
	return func(r *K3SRuntime) {
		r.engineAssets = assets
	}
}

// K3SRuntime adapts the existing k3s.Client + knowledge.GeneratePod to the Runtime interface.
type K3SRuntime struct {
	client          *k3s.Client
	engineAssets    []knowledge.EngineAsset
	progressTracker *ProgressTracker
}

func NewK3SRuntime(client *k3s.Client, opts ...K3SOption) *K3SRuntime {
	r := &K3SRuntime{
		client:          client,
		progressTracker: NewProgressTracker(),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

func (r *K3SRuntime) Name() string { return "k3s" }

func (r *K3SRuntime) Deploy(ctx context.Context, req *DeployRequest) error {
	resolved := toResolvedConfig(req)
	podYAML, err := knowledge.GeneratePod(resolved)
	if err != nil {
		return fmt.Errorf("generate pod: %w", err)
	}
	err = r.client.Apply(ctx, podYAML)
	if err != nil && (strings.Contains(err.Error(), "immutable") || strings.Contains(err.Error(), "Forbidden")) {
		// Pod spec has immutable fields that changed (e.g. QoS class, schedulerName).
		// Delete the existing pod and recreate it.
		podName := knowledge.SanitizePodName(req.Name + "-" + req.Engine)
		slog.Warn("deploy: immutable field conflict, deleting and recreating pod", "pod", podName)
		if delErr := r.client.Delete(ctx, podName); delErr != nil {
			slog.Error("deploy: failed to delete conflicting pod", "pod", podName, "error", delErr)
		}
		err = r.client.Apply(ctx, podYAML)
	}
	return err
}

func (r *K3SRuntime) Delete(ctx context.Context, name string) error {
	err := r.client.Delete(ctx, name)
	r.progressTracker.Remove(name)
	return err
}

func (r *K3SRuntime) Status(ctx context.Context, name string) (*DeploymentStatus, error) {
	pod, err := r.client.GetPod(ctx, name)
	if err != nil {
		return nil, err
	}
	ds := podToStatus(pod)
	r.enrichStartupProgress(ctx, pod, ds)
	return ds, nil
}

func (r *K3SRuntime) List(ctx context.Context) ([]*DeploymentStatus, error) {
	pods, err := r.client.ListPods(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]*DeploymentStatus, len(pods))
	for i, p := range pods {
		ds := podToStatus(p)
		r.enrichStartupProgress(ctx, p, ds)
		statuses[i] = ds
	}
	return statuses, nil
}

func (r *K3SRuntime) Logs(ctx context.Context, name string, tailLines int) (string, error) {
	return r.client.Logs(ctx, name, k3s.LogOptions{TailLines: tailLines})
}

// toResolvedConfig maps DeployRequest back to knowledge.ResolvedConfig
// so we can reuse the existing Pod YAML template without modification.
func toResolvedConfig(req *DeployRequest) *knowledge.ResolvedConfig {
	port := primaryPortForRequest(req)

	slot := "default"
	if req.Labels != nil {
		if s, ok := req.Labels["aima.dev/slot"]; ok {
			slot = s
		}
	}

	config := make(map[string]any)
	for k, v := range req.Config {
		config[k] = v
	}
	if port > 0 {
		config["port"] = port
	}

	rc := &knowledge.ResolvedConfig{
		Engine:           req.Engine,
		EngineImage:      req.Image,
		ModelPath:        req.ModelPath,
		ModelName:        req.Name,
		Slot:             slot,
		Config:           config,
		Command:          req.Command,
		PortSpecs:        req.PortSpecs,
		InitCommands:     req.InitCommands,
		ExtraVolumes:     req.ExtraVolumes,
		RuntimeClassName: req.RuntimeClassName,
		CPUArch:          req.CPUArch,
		Env:              req.Env,
		Container:        req.Container,
		GPUResourceName:  req.GPUResourceName,
	}

	if req.HealthCheck != nil {
		rc.HealthCheck = &knowledge.HealthCheck{
			Path:     req.HealthCheck.Path,
			TimeoutS: req.HealthCheck.TimeoutS,
		}
	}

	if req.Partition != nil {
		rc.Partition = &knowledge.PartitionSlot{
			Name:            slot,
			GPUMemoryMiB:    req.Partition.GPUMemoryMiB,
			GPUCoresPercent: req.Partition.GPUCoresPercent,
			CPUCores:        req.Partition.CPUCores,
			RAMMiB:          req.Partition.RAMMiB,
		}
	}

	return rc
}

func podToStatus(pod *k3s.PodStatus) *DeploymentStatus {
	addr := ""
	if pod.IP != "" {
		// Port priority: aima.dev/port label > containerPort from spec > 8080 fallback
		port := "8080"
		if pod.Labels != nil {
			if p, ok := pod.Labels["aima.dev/port"]; ok {
				port = p
			} else if pod.ContainerPort > 0 {
				port = strconv.Itoa(pod.ContainerPort)
			}
		} else if pod.ContainerPort > 0 {
			port = strconv.Itoa(pod.ContainerPort)
		}
		addr = pod.IP + ":" + port
	}

	phase := "stopped"
	ready := pod.Ready
	switch pod.Phase {
	case "Running":
		phase = "running"
	case "Pending":
		phase = "starting"
	case "Failed":
		phase = "failed"
	case "Succeeded":
		phase = "stopped"
	}

	// Detect persistent failure states that K8s may report under various phases.
	// ImagePullBackOff keeps pods in "Pending"; CrashLoopBackOff keeps pods in "Running"
	// with ready=false (container restarts forever). Both should show as "failed".
	if pod.Message != "" && (phase == "starting" || (phase == "running" && !ready)) {
		reason := pod.Message
		if i := strings.Index(reason, ":"); i > 0 {
			reason = reason[:i]
		}
		switch reason {
		case "ImagePullBackOff", "ErrImagePull", "CrashLoopBackOff",
			"CreateContainerConfigError", "InvalidImageName":
			phase = "failed"
		}
	}

	// Container terminated (exited/crashed): always mark as failed.
	if pod.ExitCode != nil {
		phase = "failed"
	}

	// High restart count with not-ready container: unstable, mark failed.
	if pod.RestartCount >= 3 && !ready {
		phase = "failed"
	}

	// A deleting pod may still report Running/Ready briefly. Do not surface it as reusable.
	if pod.DeletionTimestamp != "" {
		phase = "stopped"
		ready = false
	}

	ds := &DeploymentStatus{
		Name:     pod.Name,
		Phase:    phase,
		Ready:    ready,
		Address:  addr,
		Labels:   pod.Labels,
		Message:  pod.Message,
		Runtime:  "k3s",
		Restarts: pod.RestartCount,
		ExitCode: pod.ExitCode,
	}
	setDeploymentStartFromString(ds, pod.StartTime)
	return ds
}

// enrichStartupProgress adds startup progress data to non-ready or failed deployments.
// Note: for List() with N starting pods, this fetches logs per pod (N extra kubectl execs).
// Acceptable at 3-10s poll intervals with typical deployment counts (<10).
func (r *K3SRuntime) enrichStartupProgress(ctx context.Context, pod *k3s.PodStatus, ds *DeploymentStatus) {
	if ds.Ready && ds.Phase == "running" {
		return
	}

	engineName := ""
	if pod.Labels != nil {
		engineName = pod.Labels["aima.dev/engine"]
	}
	asset := findEngineAsset(r.engineAssets, engineName)

	if asset != nil && len(asset.TimeConstraints.ColdStartS) >= 2 {
		ds.EstimatedTotalS = asset.TimeConstraints.ColdStartS[1]
	}

	if ds.Phase == "failed" {
		if logs, err := r.client.Logs(ctx, pod.Name, k3s.LogOptions{TailLines: 5}); err == nil && logs != "" {
			ds.ErrorLines = logs
			if asset != nil && asset.Startup.LogPatterns != nil {
				if errMsg := DetectStartupError(logs, asset.Startup.LogPatterns); errMsg != "" {
					ds.StartupMessage = errMsg
				}
			}
		}
		return
	}

	if ds.Phase != "starting" {
		return
	}

	containerRunning := pod.ContainerStarted != ""
	phase, progress := DetectK3SPhaseFromConditions(pod.Conditions, containerRunning)
	ds.StartupPhase = phase
	ds.StartupProgress = progress
	ds.StartupMessage = formatPhaseName(phase)

	if containerRunning && asset != nil && asset.Startup.LogPatterns != nil {
		logs, err := r.client.Logs(ctx, pod.Name, k3s.LogOptions{TailLines: 100})
		if err == nil && logs != "" {
			sp := DetectStartupProgress(logs, asset.Startup.LogPatterns)
			if sp.Progress > ds.StartupProgress {
				ds.StartupPhase = sp.Phase
				ds.StartupProgress = sp.Progress
				ds.StartupMessage = sp.Message
			}

			if errMsg := DetectStartupError(logs, asset.Startup.LogPatterns); errMsg != "" {
				ds.StartupMessage = errMsg
			}
		}
	}

	// Stall detection for starting deployments
	if ds.Phase == "starting" || (ds.Phase == "running" && !ds.Ready) {
		stalled, lastAt := r.progressTracker.Update(ds.Name, ds.StartupProgress, ds.EstimatedTotalS)
		ds.Stalled = stalled
		ds.LastProgressAt = lastAt.Unix()
	}
}

// K3SAvailable checks whether K3S is accessible on this system.
func K3SAvailable(ctx context.Context, client *k3s.Client) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := client.ListPods(probeCtx)
	return err == nil
}
