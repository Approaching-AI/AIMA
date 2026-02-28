package runtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jguan/aima/internal/k3s"
	"github.com/jguan/aima/internal/knowledge"
)

// K3SRuntime adapts the existing k3s.Client + knowledge.GeneratePod to the Runtime interface.
type K3SRuntime struct {
	client *k3s.Client
}

func NewK3SRuntime(client *k3s.Client) *K3SRuntime {
	return &K3SRuntime{client: client}
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
		_ = r.client.Delete(ctx, podName)
		err = r.client.Apply(ctx, podYAML)
	}
	return err
}

func (r *K3SRuntime) Delete(ctx context.Context, name string) error {
	return r.client.Delete(ctx, name)
}

func (r *K3SRuntime) Status(ctx context.Context, name string) (*DeploymentStatus, error) {
	pod, err := r.client.GetPod(ctx, name)
	if err != nil {
		return nil, err
	}
	return podToStatus(pod), nil
}

func (r *K3SRuntime) List(ctx context.Context) ([]*DeploymentStatus, error) {
	pods, err := r.client.ListPods(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]*DeploymentStatus, len(pods))
	for i, p := range pods {
		statuses[i] = podToStatus(p)
	}
	return statuses, nil
}

func (r *K3SRuntime) Logs(ctx context.Context, name string, tailLines int) (string, error) {
	return r.client.Logs(ctx, name, k3s.LogOptions{TailLines: tailLines})
}

// toResolvedConfig maps DeployRequest back to knowledge.ResolvedConfig
// so we can reuse the existing Pod YAML template without modification.
func toResolvedConfig(req *DeployRequest) *knowledge.ResolvedConfig {
	port := req.Port
	if port == 0 {
		port = 8000
	}

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
	config["port"] = port

	rc := &knowledge.ResolvedConfig{
		Engine:           req.Engine,
		EngineImage:      req.Image,
		ModelPath:        req.ModelPath,
		ModelName:        req.Name,
		Slot:             slot,
		Config:           config,
		Command:          req.Command,
		RuntimeClassName: req.RuntimeClassName,
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
	if pod.Message != "" && (phase == "starting" || (phase == "running" && !pod.Ready)) {
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

	return &DeploymentStatus{
		Name:      pod.Name,
		Phase:     phase,
		Ready:     pod.Ready,
		Address:   addr,
		Labels:    pod.Labels,
		StartTime: pod.StartTime,
		Message:   pod.Message,
		Runtime:   "k3s",
	}
}

// K3SAvailable checks whether K3S is accessible on this system.
func K3SAvailable(ctx context.Context, client *k3s.Client) bool {
	_, err := client.ListPods(ctx)
	return err == nil
}

// FormatPort converts int port to string for labels.
func FormatPort(port int) string {
	return strconv.Itoa(port)
}
