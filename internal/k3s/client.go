package k3s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
)

// CommandRunner abstracts command execution for testing.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// execRunner is the default CommandRunner using os/exec.
type execRunner struct{}

func (e *execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// PodStatus represents the status of a K3S pod.
type PodStatus struct {
	Name          string            `json:"name"`
	Phase         string            `json:"phase"`
	Ready         bool              `json:"ready"`
	IP            string            `json:"ip"`
	Labels        map[string]string `json:"labels"`
	StartTime     string            `json:"start_time"`
	Message       string            `json:"message,omitempty"`
	ContainerPort int               `json:"container_port,omitempty"`
}

// LogOptions configures log retrieval.
type LogOptions struct {
	TailLines int
	Follow    bool
}

// Client wraps kubectl operations for K3S.
type Client struct {
	kubeconfigPath string
	kubectl        string
	k3sMode        bool // when true, prepend "kubectl" subcommand (for k3s binary)
	runner         CommandRunner
}

// Option configures Client.
type Option func(*Client)

func WithKubeconfig(path string) Option {
	return func(c *Client) { c.kubeconfigPath = path }
}

func WithKubectl(path string) Option {
	return func(c *Client) { c.kubectl = path }
}

// WithK3SBinary configures the client to use a k3s binary directly.
// K3S is a multi-call binary; when used this way, "kubectl" is prepended
// as a subcommand (e.g., "k3s kubectl get pods"). K3S auto-detects its
// kubeconfig at /etc/rancher/k3s/k3s.yaml in this mode.
func WithK3SBinary(path string) Option {
	return func(c *Client) {
		c.kubectl = path
		c.k3sMode = true
	}
}

func WithRunner(r CommandRunner) Option {
	return func(c *Client) { c.runner = r }
}

func NewClient(opts ...Option) *Client {
	c := &Client{
		kubectl: "kubectl",
		runner:  &execRunner{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// baseArgs returns common kubectl flags (e.g., --kubeconfig).
// In k3sMode, "kubectl" is prepended so the k3s binary runs its built-in kubectl.
func (c *Client) baseArgs() []string {
	var args []string
	if c.k3sMode {
		args = append(args, "kubectl")
	}
	if c.kubeconfigPath != "" {
		args = append(args, "--kubeconfig", c.kubeconfigPath)
	}
	return args
}

// Apply submits a Pod YAML to K3S via stdin.
func (c *Client) Apply(ctx context.Context, yamlData []byte) error {
	if len(yamlData) == 0 {
		return fmt.Errorf("apply pod: empty yaml data")
	}

	args := append(c.baseArgs(), "apply", "-f", "-")
	out, err := c.runWithStdin(ctx, yamlData, args...)
	if err != nil {
		return fmt.Errorf("apply pod: %w\nkubectl output: %s", err, strings.TrimSpace(string(out)))
	}
	slog.Info("kubectl apply", "output", string(out))
	return nil
}

// validatePodName checks that a pod name is safe to use in kubectl commands.
func validatePodName(name string) error {
	if name == "" {
		return fmt.Errorf("empty pod name")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid pod name %q: must not start with dash", name)
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.') {
			return fmt.Errorf("invalid pod name %q: contains invalid character %q", name, string(r))
		}
	}
	if len(name) > 253 {
		return fmt.Errorf("invalid pod name %q: exceeds 253 characters", name)
	}
	return nil
}

// Delete removes a pod by name.
func (c *Client) Delete(ctx context.Context, podName string) error {
	if err := validatePodName(podName); err != nil {
		return fmt.Errorf("delete pod: %w", err)
	}

	args := append(c.baseArgs(), "delete", "pod", podName)
	_, err := c.runner.Run(ctx, c.kubectl, args...)
	if err != nil {
		return fmt.Errorf("delete pod %s: %w", podName, err)
	}
	return nil
}

// GetPod returns pod status information.
func (c *Client) GetPod(ctx context.Context, podName string) (*PodStatus, error) {
	if err := validatePodName(podName); err != nil {
		return nil, fmt.Errorf("get pod: %w", err)
	}

	args := append(c.baseArgs(), "get", "pod", podName, "-o", "json")
	out, err := c.runner.Run(ctx, c.kubectl, args...)
	if err != nil {
		return nil, fmt.Errorf("get pod %s: %w", podName, err)
	}
	return parsePodJSON(out)
}

// ListPods lists pods with aima labels.
func (c *Client) ListPods(ctx context.Context) ([]*PodStatus, error) {
	args := append(c.baseArgs(), "get", "pods", "-l", "aima.dev/engine", "-o", "json")
	out, err := c.runner.Run(ctx, c.kubectl, args...)
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("list pods: parse response: %w", err)
	}

	pods := make([]*PodStatus, 0, len(list.Items))
	for _, item := range list.Items {
		p, err := parsePodJSON(item)
		if err != nil {
			slog.Warn("skip unparseable pod", "error", err)
			continue
		}
		pods = append(pods, p)
	}
	return pods, nil
}

// Logs retrieves container logs.
func (c *Client) Logs(ctx context.Context, podName string, opts LogOptions) (string, error) {
	if err := validatePodName(podName); err != nil {
		return "", fmt.Errorf("logs: %w", err)
	}

	args := append(c.baseArgs(), "logs", podName)
	tail := opts.TailLines
	if tail <= 0 {
		tail = 100
	}
	args = append(args, "--tail="+strconv.Itoa(tail))

	out, err := c.runner.Run(ctx, c.kubectl, args...)
	if err != nil {
		return "", fmt.Errorf("logs %s: %w", podName, err)
	}
	return string(out), nil
}

// runWithStdin is a helper for commands that need stdin piped (like apply -f -).
// Because CommandRunner doesn't support stdin, we use a special protocol:
// the mock runner ignores stdin, while the real runner uses exec.Cmd.Stdin.
func (c *Client) runWithStdin(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if _, ok := c.runner.(*execRunner); ok {
		cmd := exec.CommandContext(ctx, c.kubectl, args...)
		cmd.Stdin = bytes.NewReader(stdin)
		return cmd.CombinedOutput()
	}
	// For mock runners, just call Run directly (stdin is ignored)
	return c.runner.Run(ctx, c.kubectl, args...)
}

// kubePod is the minimal subset of Kubernetes Pod JSON we need to parse.
type kubePod struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Containers []struct {
			Ports []struct {
				ContainerPort int `json:"containerPort"`
			} `json:"ports"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase             string `json:"phase"`
		PodIP             string `json:"podIP"`
		StartTime         string `json:"startTime"`
		Message           string `json:"message"`
		ContainerStatuses []struct {
			Ready bool `json:"ready"`
			State struct {
				Waiting *struct {
					Reason  string `json:"reason"`
					Message string `json:"message"`
				} `json:"waiting"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

func parsePodJSON(data []byte) (*PodStatus, error) {
	var kp kubePod
	if err := json.Unmarshal(data, &kp); err != nil {
		return nil, fmt.Errorf("parse pod json: %w", err)
	}

	ready := false
	msg := kp.Status.Message
	if len(kp.Status.ContainerStatuses) > 0 {
		cs := kp.Status.ContainerStatuses[0]
		ready = cs.Ready
		// Use container waiting reason as message when pod-level message is empty
		if msg == "" && cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			msg = cs.State.Waiting.Reason
			if cs.State.Waiting.Message != "" {
				msg += ": " + cs.State.Waiting.Message
			}
		}
	}

	containerPort := 0
	if len(kp.Spec.Containers) > 0 && len(kp.Spec.Containers[0].Ports) > 0 {
		containerPort = kp.Spec.Containers[0].Ports[0].ContainerPort
	}

	return &PodStatus{
		Name:          kp.Metadata.Name,
		Phase:         kp.Status.Phase,
		Ready:         ready,
		IP:            kp.Status.PodIP,
		Labels:        kp.Metadata.Labels,
		StartTime:     kp.Status.StartTime,
		Message:       msg,
		ContainerPort: containerPort,
	}, nil
}

// ListPodsByLabel lists pods matching a label selector in a given namespace.
func (c *Client) ListPodsByLabel(ctx context.Context, namespace, label string) ([]*PodStatus, error) {
	args := append(c.baseArgs(), "get", "pods", "-n", namespace, "-l", label, "-o", "json")
	out, err := c.runner.Run(ctx, c.kubectl, args...)
	if err != nil {
		return nil, fmt.Errorf("list pods -n %s -l %s: %w", namespace, label, err)
	}

	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("parse pod list: %w", err)
	}

	pods := make([]*PodStatus, 0, len(list.Items))
	for _, item := range list.Items {
		p, err := parsePodJSON(item)
		if err != nil {
			slog.Warn("skip unparseable pod", "error", err)
			continue
		}
		pods = append(pods, p)
	}
	return pods, nil
}
