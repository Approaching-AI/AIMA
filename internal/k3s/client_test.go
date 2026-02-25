package k3s

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// mockRunner implements CommandRunner for testing
type mockRunner struct {
	output []byte
	err    error
	// capture last invocation
	lastCmd  string
	lastArgs []string
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	m.lastCmd = name
	m.lastArgs = args
	return m.output, m.err
}

func TestNewClient_Defaults(t *testing.T) {
	c := NewClient()
	if c.kubectl != "kubectl" {
		t.Errorf("expected default kubectl path 'kubectl', got %q", c.kubectl)
	}
	if c.runner == nil {
		t.Error("expected non-nil runner")
	}
}

func TestNewClient_Options(t *testing.T) {
	r := &mockRunner{}
	c := NewClient(
		WithKubeconfig("/custom/kubeconfig"),
		WithKubectl("/usr/local/bin/k3s"),
		WithRunner(r),
	)
	if c.kubeconfigPath != "/custom/kubeconfig" {
		t.Errorf("kubeconfig = %q, want /custom/kubeconfig", c.kubeconfigPath)
	}
	if c.kubectl != "/usr/local/bin/k3s" {
		t.Errorf("kubectl = %q, want /usr/local/bin/k3s", c.kubectl)
	}
}

func TestApply(t *testing.T) {
	tests := []struct {
		name       string
		yaml       []byte
		runnerErr  error
		wantErr    bool
		wantErrMsg string
	}{
		{
			name:    "valid yaml",
			yaml:    []byte("apiVersion: v1\nkind: Pod"),
			wantErr: false,
		},
		{
			name:    "empty yaml",
			yaml:    []byte{},
			wantErr: true,
		},
		{
			name:      "kubectl error",
			yaml:      []byte("apiVersion: v1\nkind: Pod"),
			runnerErr: fmt.Errorf("connection refused"),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{err: tt.runnerErr}
			c := NewClient(WithRunner(r))
			err := c.Apply(context.Background(), tt.yaml)
			if (err != nil) != tt.wantErr {
				t.Errorf("Apply() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && r.lastCmd != "kubectl" {
				t.Errorf("expected kubectl command, got %q", r.lastCmd)
			}
		})
	}
}

func TestApply_WithKubeconfig(t *testing.T) {
	r := &mockRunner{}
	c := NewClient(WithRunner(r), WithKubeconfig("/etc/rancher/k3s/k3s.yaml"))
	_ = c.Apply(context.Background(), []byte("apiVersion: v1\nkind: Pod"))

	found := false
	for i, arg := range r.lastArgs {
		if arg == "--kubeconfig" && i+1 < len(r.lastArgs) && r.lastArgs[i+1] == "/etc/rancher/k3s/k3s.yaml" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected --kubeconfig flag in args: %v", r.lastArgs)
	}
}

func TestDelete(t *testing.T) {
	tests := []struct {
		name      string
		podName   string
		runnerErr error
		wantErr   bool
	}{
		{
			name:    "delete existing pod",
			podName: "aima-vllm-qwen3",
			wantErr: false,
		},
		{
			name:    "empty pod name",
			podName: "",
			wantErr: true,
		},
		{
			name:      "kubectl error",
			podName:   "aima-vllm-qwen3",
			runnerErr: fmt.Errorf("pod not found"),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{err: tt.runnerErr}
			c := NewClient(WithRunner(r))
			err := c.Delete(context.Background(), tt.podName)
			if (err != nil) != tt.wantErr {
				t.Errorf("Delete() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// Sample kubectl get pod -o json output for a running pod
const runningPodJSON = `{
  "apiVersion": "v1",
  "kind": "Pod",
  "metadata": {
    "name": "aima-vllm-qwen3",
    "labels": {
      "aima.dev/engine": "vllm",
      "aima.dev/model": "qwen3-8b"
    }
  },
  "status": {
    "phase": "Running",
    "podIP": "10.42.0.5",
    "startTime": "2025-01-15T08:30:00Z",
    "containerStatuses": [
      {
        "ready": true
      }
    ]
  }
}`

const pendingPodJSON = `{
  "apiVersion": "v1",
  "kind": "Pod",
  "metadata": {
    "name": "aima-llamacpp-glm4",
    "labels": {
      "aima.dev/engine": "llamacpp"
    }
  },
  "status": {
    "phase": "Pending",
    "containerStatuses": [
      {
        "ready": false
      }
    ]
  }
}`

const failedPodJSON = `{
  "apiVersion": "v1",
  "kind": "Pod",
  "metadata": {
    "name": "aima-failed-pod",
    "labels": {}
  },
  "status": {
    "phase": "Failed",
    "message": "OOMKilled",
    "containerStatuses": [
      {
        "ready": false
      }
    ]
  }
}`

func TestGetPod(t *testing.T) {
	tests := []struct {
		name       string
		podName    string
		output     string
		runnerErr  error
		wantErr    bool
		wantPhase  string
		wantReady  bool
		wantIP     string
		wantLabels map[string]string
	}{
		{
			name:      "running pod",
			podName:   "aima-vllm-qwen3",
			output:    runningPodJSON,
			wantPhase: "Running",
			wantReady: true,
			wantIP:    "10.42.0.5",
			wantLabels: map[string]string{
				"aima.dev/engine": "vllm",
				"aima.dev/model":  "qwen3-8b",
			},
		},
		{
			name:      "pending pod",
			podName:   "aima-llamacpp-glm4",
			output:    pendingPodJSON,
			wantPhase: "Pending",
			wantReady: false,
			wantIP:    "",
		},
		{
			name:      "failed pod",
			podName:   "aima-failed-pod",
			output:    failedPodJSON,
			wantPhase: "Failed",
			wantReady: false,
		},
		{
			name:    "empty pod name",
			podName: "",
			wantErr: true,
		},
		{
			name:      "kubectl error",
			podName:   "nonexistent",
			runnerErr: fmt.Errorf("not found"),
			wantErr:   true,
		},
		{
			name:    "invalid json",
			podName: "bad-pod",
			output:  "not json",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{output: []byte(tt.output), err: tt.runnerErr}
			c := NewClient(WithRunner(r))
			pod, err := c.GetPod(context.Background(), tt.podName)
			if (err != nil) != tt.wantErr {
				t.Fatalf("GetPod() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if pod.Phase != tt.wantPhase {
				t.Errorf("Phase = %q, want %q", pod.Phase, tt.wantPhase)
			}
			if pod.Ready != tt.wantReady {
				t.Errorf("Ready = %v, want %v", pod.Ready, tt.wantReady)
			}
			if pod.IP != tt.wantIP {
				t.Errorf("IP = %q, want %q", pod.IP, tt.wantIP)
			}
			for k, v := range tt.wantLabels {
				if pod.Labels[k] != v {
					t.Errorf("Label %q = %q, want %q", k, pod.Labels[k], v)
				}
			}
		})
	}
}

func TestListPods(t *testing.T) {
	podList := map[string]interface{}{
		"items": []json.RawMessage{
			json.RawMessage(runningPodJSON),
			json.RawMessage(pendingPodJSON),
		},
	}
	data, _ := json.Marshal(podList)

	tests := []struct {
		name      string
		output    []byte
		runnerErr error
		wantErr   bool
		wantCount int
	}{
		{
			name:      "two pods",
			output:    data,
			wantCount: 2,
		},
		{
			name:      "empty list",
			output:    []byte(`{"items":[]}`),
			wantCount: 0,
		},
		{
			name:      "kubectl error",
			runnerErr: fmt.Errorf("cluster unreachable"),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{output: tt.output, err: tt.runnerErr}
			c := NewClient(WithRunner(r))
			pods, err := c.ListPods(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("ListPods() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(pods) != tt.wantCount {
				t.Errorf("got %d pods, want %d", len(pods), tt.wantCount)
			}
		})
	}
}

func TestListPods_VerifyLabel(t *testing.T) {
	r := &mockRunner{output: []byte(`{"items":[]}`)}
	c := NewClient(WithRunner(r))
	_, _ = c.ListPods(context.Background())

	// Verify -l aima.dev/engine label selector is used
	found := false
	for _, arg := range r.lastArgs {
		if arg == "aima.dev/engine" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected label selector 'aima.dev/engine' in args: %v", r.lastArgs)
	}
}

func TestLogs(t *testing.T) {
	tests := []struct {
		name      string
		podName   string
		opts      LogOptions
		output    string
		runnerErr error
		wantErr   bool
		wantLogs  string
	}{
		{
			name:     "basic logs",
			podName:  "aima-vllm-qwen3",
			opts:     LogOptions{TailLines: 100},
			output:   "INFO: Server started\nINFO: Model loaded",
			wantLogs: "INFO: Server started\nINFO: Model loaded",
		},
		{
			name:    "default tail lines",
			podName: "aima-vllm-qwen3",
			opts:    LogOptions{},
			output:  "some logs",
		},
		{
			name:    "empty pod name",
			podName: "",
			wantErr: true,
		},
		{
			name:      "kubectl error",
			podName:   "nonexistent",
			runnerErr: fmt.Errorf("pod not found"),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{output: []byte(tt.output), err: tt.runnerErr}
			c := NewClient(WithRunner(r))
			logs, err := c.Logs(context.Background(), tt.podName, tt.opts)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Logs() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && tt.wantLogs != "" && logs != tt.wantLogs {
				t.Errorf("Logs = %q, want %q", logs, tt.wantLogs)
			}
		})
	}
}

func TestLogs_TailFlag(t *testing.T) {
	r := &mockRunner{output: []byte("log line")}
	c := NewClient(WithRunner(r))
	_, _ = c.Logs(context.Background(), "test-pod", LogOptions{TailLines: 50})

	found := false
	for _, arg := range r.lastArgs {
		if arg == "--tail=50" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected --tail=50 in args: %v", r.lastArgs)
	}
}

func TestGetPod_StartTime(t *testing.T) {
	r := &mockRunner{output: []byte(runningPodJSON)}
	c := NewClient(WithRunner(r))
	pod, err := c.GetPod(context.Background(), "aima-vllm-qwen3")
	if err != nil {
		t.Fatal(err)
	}
	if pod.StartTime != "2025-01-15T08:30:00Z" {
		t.Errorf("StartTime = %q, want %q", pod.StartTime, "2025-01-15T08:30:00Z")
	}
}

func TestGetPod_FailedMessage(t *testing.T) {
	r := &mockRunner{output: []byte(failedPodJSON)}
	c := NewClient(WithRunner(r))
	pod, err := c.GetPod(context.Background(), "aima-failed-pod")
	if err != nil {
		t.Fatal(err)
	}
	if pod.Message != "OOMKilled" {
		t.Errorf("Message = %q, want %q", pod.Message, "OOMKilled")
	}
}
