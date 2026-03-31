package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jguan/aima/internal/knowledge"
)

func newTestRuntime(t *testing.T) *NativeRuntime {
	t.Helper()
	base := t.TempDir()
	return NewNativeRuntime(
		filepath.Join(base, "logs"),
		"",
		filepath.Join(base, "deployments"),
	)
}

func newWindowsListenerScript(t *testing.T, port int, sleepSeconds int, echoArg bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "listener.ps1")
	lines := []string{
		"param([string]$Arg0)",
	}
	if echoArg {
		lines = append(lines,
			"if ($Arg0) { Write-Output $Arg0 }",
		)
	}
	lines = append(lines,
		"$listener = New-Object System.Net.Sockets.TcpListener([System.Net.IPAddress]::Loopback, "+strconv.Itoa(port)+")",
		"$listener.Start()",
		"Start-Sleep -Seconds "+strconv.Itoa(sleepSeconds),
		"$listener.Stop()",
	)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\r\n")+"\r\n"), 0o644); err != nil {
		t.Fatalf("write windows listener script: %v", err)
	}
	return path
}

func TestNativeDeployAndDelete(t *testing.T) {
	rt := newTestRuntime(t)

	// Use a command that exists cross-platform and exits quickly after a while
	var cmd []string
	if runtime.GOOS == "windows" {
		script := newWindowsListenerScript(t, 9999, 30, false)
		cmd = []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}
	} else {
		cmd = []string{"sh", "-c", "echo hello && sleep 10"}
	}

	err := rt.Deploy(context.Background(), &DeployRequest{
		Name:    "test-deploy",
		Engine:  "test",
		Command: cmd,
		Port:    9999,
		Labels:  map[string]string{"aima.dev/engine": "test"},
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	// Should appear in list
	statuses, err := rt.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].Name != "test-deploy" {
		t.Errorf("name = %q, want %q", statuses[0].Name, "test-deploy")
	}
	if statuses[0].Runtime != "native" {
		t.Errorf("runtime = %q, want %q", statuses[0].Runtime, "native")
	}

	// Status should work
	s, err := rt.Status(context.Background(), "test-deploy")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if s.Address != "127.0.0.1:9999" {
		t.Errorf("address = %q, want %q", s.Address, "127.0.0.1:9999")
	}

	// Delete
	if err := rt.Delete(context.Background(), "test-deploy"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Should be gone from list
	statuses, _ = rt.List(context.Background())
	if len(statuses) != 0 {
		t.Errorf("expected 0 statuses after delete, got %d", len(statuses))
	}
}

func TestNativeDeployDuplicate(t *testing.T) {
	rt := newTestRuntime(t)

	var cmd []string
	if runtime.GOOS == "windows" {
		script := newWindowsListenerScript(t, 8080, 30, false)
		cmd = []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}
	} else {
		cmd = []string{"sleep", "10"}
	}

	err := rt.Deploy(context.Background(), &DeployRequest{
		Name:    "dup",
		Engine:  "test",
		Command: cmd,
		Port:    8080,
	})
	if err != nil {
		t.Fatalf("first Deploy: %v", err)
	}

	err = rt.Deploy(context.Background(), &DeployRequest{
		Name:    "dup",
		Engine:  "test",
		Command: cmd,
		Port:    8081,
	})
	if err == nil {
		t.Error("expected error on duplicate deploy")
	}

	// Clean up before TempDir removal to avoid Windows file lock issues
	rt.Delete(context.Background(), "dup")
	time.Sleep(100 * time.Millisecond)
}

func TestNativeModelPathSubstitution(t *testing.T) {
	rt := newTestRuntime(t)

	// Deploy with a command containing {{.ModelPath}} — use echo to verify substitution
	var cmd []string
	modelPath := "/data/models/test-model"
	if runtime.GOOS == "windows" {
		script := newWindowsListenerScript(t, 8080, 2, true)
		cmd = []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "{{.ModelPath}}"}
		modelPath = "C:\\data\\models\\test-model"
	} else {
		cmd = []string{"sh", "-c", "echo '{{.ModelPath}}'"}
	}

	err := rt.Deploy(context.Background(), &DeployRequest{
		Name:      "subst-test",
		Engine:    "test",
		Command:   cmd,
		ModelPath: modelPath,
		Port:      8080,
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	// Wait for process to finish
	time.Sleep(500 * time.Millisecond)

	// Read logs — should contain the actual path, not the template
	logs, err := rt.Logs(context.Background(), "subst-test", 10)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if strings.Contains(logs, "{{.ModelPath}}") {
		t.Error("log still contains {{.ModelPath}} template — substitution failed")
	}
	if !strings.Contains(logs, "models") {
		t.Errorf("log should contain model path, got: %q", logs)
	}

	rt.Delete(context.Background(), "subst-test")
}

func TestNativeLogsReadTail(t *testing.T) {
	dir := t.TempDir()

	// Create a log file with 10 lines
	logPath := filepath.Join(dir, "test.log")
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, "line-"+strings.Repeat("x", i))
	}
	os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	// Read last 3 lines
	result, err := readTail(logPath, 3)
	if err != nil {
		t.Fatalf("readTail: %v", err)
	}
	got := strings.Split(result, "\n")
	if len(got) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(got), got)
	}
}

func TestEffectiveHealthTimeout(t *testing.T) {
	tests := []struct {
		name string
		hc   *HealthCheckConfig
		want time.Duration
	}{
		{name: "nil health check", hc: nil, want: 60 * time.Second},
		{name: "zero timeout", hc: &HealthCheckConfig{TimeoutS: 0}, want: 60 * time.Second},
		{name: "custom timeout", hc: &HealthCheckConfig{TimeoutS: 600}, want: 600 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveHealthTimeout(tt.hc); got != tt.want {
				t.Fatalf("effectiveHealthTimeout(%+v) = %v, want %v", tt.hc, got, tt.want)
			}
		})
	}
}

func TestNativeDeleteNotFound(t *testing.T) {
	rt := newTestRuntime(t)
	err := rt.Delete(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent deployment")
	}
}

func TestNativeProcessDoneChannelClosedOnExit(t *testing.T) {
	rt := newTestRuntime(t)

	var cmd []string
	if runtime.GOOS == "windows" {
		script := newWindowsListenerScript(t, 18081, 1, false)
		cmd = []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}
	} else {
		cmd = []string{"sh", "-c", "echo done"}
	}

	if err := rt.Deploy(context.Background(), &DeployRequest{
		Name:    "quick-exit",
		Engine:  "test",
		Command: cmd,
		Port:    18081,
	}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	rt.mu.RLock()
	proc := rt.processes["quick-exit"]
	rt.mu.RUnlock()
	if proc == nil {
		t.Fatal("expected in-memory process entry")
	}

	select {
	case <-proc.done:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("process done channel was not closed after process exit")
	}

	if err := rt.Delete(context.Background(), "quick-exit"); err != nil {
		t.Fatalf("Delete exited process: %v", err)
	}
}

func TestNativeEmptyCommand(t *testing.T) {
	rt := newTestRuntime(t)
	err := rt.Deploy(context.Background(), &DeployRequest{
		Name:    "empty",
		Engine:  "test",
		Command: nil,
	})
	if err == nil {
		t.Error("expected error for empty command")
	}
}

// TestNativePersistenceAcrossInvocations simulates two separate CLI invocations
// sharing the same deployDir, verifying that deployments persist.
func TestNativePersistenceAcrossInvocations(t *testing.T) {
	base := t.TempDir()
	logDir := filepath.Join(base, "logs")
	deployDir := filepath.Join(base, "deployments")

	// "First CLI invocation": deploy a long-running process
	rt1 := NewNativeRuntime(logDir, "", deployDir)

	var cmd []string
	if runtime.GOOS == "windows" {
		script := newWindowsListenerScript(t, 19876, 30, false)
		cmd = []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}
	} else {
		cmd = []string{"sleep", "30"}
	}

	err := rt1.Deploy(context.Background(), &DeployRequest{
		Name:    "persistent",
		Engine:  "test",
		Command: cmd,
		Port:    19876,
		Labels:  map[string]string{"aima.dev/engine": "test"},
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	// Verify metadata file was written
	metaPath := filepath.Join(deployDir, "persistent.json")
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("metadata file not created: %v", err)
	}

	// "Second CLI invocation": create a fresh NativeRuntime with same deployDir
	rt2 := NewNativeRuntime(logDir, "", deployDir)

	// Should discover the deployment from persisted metadata
	statuses, err := rt2.List(context.Background())
	if err != nil {
		t.Fatalf("List on rt2: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status from persistence, got %d", len(statuses))
	}
	if statuses[0].Name != "persistent" {
		t.Errorf("name = %q, want %q", statuses[0].Name, "persistent")
	}

	// Status should also work on rt2
	s, err := rt2.Status(context.Background(), "persistent")
	if err != nil {
		t.Fatalf("Status on rt2: %v", err)
	}
	if s.Address != "127.0.0.1:19876" {
		t.Errorf("address = %q, want %q", s.Address, "127.0.0.1:19876")
	}

	// Logs should work via persisted log path
	_, err = rt2.Logs(context.Background(), "persistent", 5)
	if err != nil {
		t.Fatalf("Logs on rt2: %v", err)
	}

	// Delete via rt2 (kills by PID from metadata)
	if err := rt2.Delete(context.Background(), "persistent"); err != nil {
		t.Fatalf("Delete on rt2: %v", err)
	}

	// Metadata file should be removed
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Error("metadata file should be removed after delete")
	}

	// Cleanup: also ensure rt1's in-memory state is cleaned
	rt1.Delete(context.Background(), "persistent")
	time.Sleep(100 * time.Millisecond)
}

func TestMetaToStatusMarksMissingProcessFailed(t *testing.T) {
	rt := newTestRuntime(t)
	meta := &deploymentMeta{
		Name:      "failed-deploy",
		PID:       999999,
		Port:      19999,
		StartTime: time.Now(),
	}

	status := rt.metaToStatus(meta)
	if status.Phase != "failed" {
		t.Fatalf("phase = %q, want failed", status.Phase)
	}
	if status.Ready {
		t.Fatal("ready should be false for missing process")
	}
}

func TestMetaToStatusMarksStalePortReuseFailed(t *testing.T) {
	rt := newTestRuntime(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	meta := &deploymentMeta{
		Name:      "stale-port",
		PID:       999999,
		Port:      ln.Addr().(*net.TCPAddr).Port,
		Command:   []string{"sleep", "30"},
		StartTime: time.Now().Add(-2 * time.Minute),
	}

	status := rt.metaToStatus(meta)
	if status.Phase != "failed" {
		t.Fatalf("phase = %q, want failed", status.Phase)
	}
	if !strings.Contains(status.Message, "stale") {
		t.Fatalf("message = %q, want stale-port hint", status.Message)
	}
}

func TestNativeDeployIgnoresStaleMetadataUsingOccupiedPort(t *testing.T) {
	rt := newTestRuntime(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	if err := rt.saveMeta(&deploymentMeta{
		Name:      "stale",
		PID:       999999,
		Port:      ln.Addr().(*net.TCPAddr).Port,
		Command:   []string{"sleep", "30"},
		StartTime: time.Now().Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	var cmd []string
	if runtime.GOOS == "windows" {
		script := newWindowsListenerScript(t, 18082, 5, false)
		cmd = []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}
	} else {
		cmd = []string{"sleep", "5"}
	}

	if err := rt.Deploy(context.Background(), &DeployRequest{
		Name:    "stale",
		Engine:  "test",
		Command: cmd,
		Port:    18082,
	}); err != nil {
		t.Fatalf("Deploy should ignore stale metadata, got: %v", err)
	}

	if err := rt.Delete(context.Background(), "stale"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestProcessMatchesMetaRejectsCommandMismatch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only /proc cmdline test")
	}
	meta := &deploymentMeta{
		PID:     os.Getpid(),
		Command: []string{"definitely-not-the-current-test-binary", "--serve"},
	}
	if processMatchesMeta(meta) {
		t.Fatal("processMatchesMeta should reject mismatched command lines")
	}
}

func TestProcessMatchesMetaAllowsInterpreterWrappedScript(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only /proc cmdline test")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	script := filepath.Join(t.TempDir(), "wrapped.py")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env python3\nimport time\ntime.sleep(30)\n"), 0o755); err != nil {
		t.Fatalf("write wrapped script: %v", err)
	}

	cmd := exec.Command(script, "--port", "32102")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start wrapped script: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	meta := &deploymentMeta{
		PID:     cmd.Process.Pid,
		Command: []string{script, "--port", "32102"},
	}
	if !processMatchesMeta(meta) {
		cmdline, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "cmdline"))
		t.Fatalf("processMatchesMeta should allow interpreter prefix, cmdline=%q", string(cmdline))
	}
}

func TestCommandLineMatchesAllowsInterpreterPrefix(t *testing.T) {
	actual := "/usr/bin/python3 /usr/local/bin/vllm serve /models/qwen3-8b --port 32102 --swap-space 0"
	expected := []string{"/usr/local/bin/vllm", "serve", "/models/qwen3-8b", "--port", "32102"}
	if !commandLineMatches(actual, expected) {
		t.Fatalf("commandLineMatches should allow interpreter prefix, actual=%q", actual)
	}
}

func TestCommandLineMatchesRejectsUnknownLauncherPrefix(t *testing.T) {
	actual := "sudo /usr/local/bin/vllm serve /models/qwen3-8b --port 32102"
	expected := []string{"/usr/local/bin/vllm", "serve", "/models/qwen3-8b", "--port", "32102"}
	if commandLineMatches(actual, expected) {
		t.Fatalf("commandLineMatches should reject unexpected launcher prefix, actual=%q", actual)
	}
}

func TestProcToStatusUsesStartupErrorAsFailure(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "deploy.log")
	if err := os.WriteFile(logPath, []byte("couldn't bind HTTP server socket: Address already in use\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	rt := newTestRuntime(t)
	rt.engineAssets = []knowledge.EngineAsset{{
		Metadata: knowledge.EngineMetadata{Name: "llamacpp"},
		Startup: knowledge.EngineStartup{
			LogPatterns: &knowledge.StartupLogPatterns{
				Errors: []knowledge.StartupErrorPattern{{
					Pattern: "couldn't bind HTTP server socket|address already in use",
					Message: "Port is already in use",
				}},
			},
		},
	}}

	status := rt.procToStatus(&nativeProcess{
		name:      "llama-bind-error",
		port:      8080,
		logPath:   logPath,
		labels:    map[string]string{"aima.dev/engine": "llamacpp"},
		startTime: time.Now(),
	})
	if status.Phase != "failed" {
		t.Fatalf("phase = %q, want failed", status.Phase)
	}
	if status.Message != "Port is already in use" {
		t.Fatalf("message = %q, want %q", status.Message, "Port is already in use")
	}
}

func TestStatusPrefersPersistedFailureOverInMemoryProcess(t *testing.T) {
	rt := newTestRuntime(t)

	logPath := filepath.Join(t.TempDir(), "deploy.log")
	if err := os.WriteFile(logPath, []byte("INFO still spinning\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	rt.processes["stuck-run"] = &nativeProcess{
		name:      "stuck-run",
		port:      32102,
		logPath:   logPath,
		labels:    map[string]string{"aima.dev/engine": "vllm"},
		startTime: time.Now(),
	}

	meta := &deploymentMeta{
		Name:      "stuck-run",
		PID:       999999,
		Port:      32102,
		Engine:    "vllm",
		Labels:    map[string]string{"aima.dev/engine": "vllm"},
		LogPath:   logPath,
		Command:   []string{"/usr/local/bin/vllm", "serve", "/models"},
		StartTime: time.Now(),
	}
	if err := rt.saveMeta(meta); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	status, err := rt.Status(context.Background(), "stuck-run")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Phase != "failed" {
		t.Fatalf("phase = %q, want failed", status.Phase)
	}
	if status.Message == "" {
		t.Fatal("expected persisted failure message to be preserved")
	}
}

func TestListPrefersPersistedFailureOverInMemoryProcess(t *testing.T) {
	rt := newTestRuntime(t)

	logPath := filepath.Join(t.TempDir(), "deploy.log")
	if err := os.WriteFile(logPath, []byte("INFO still spinning\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	rt.processes["stuck-list"] = &nativeProcess{
		name:      "stuck-list",
		port:      32103,
		logPath:   logPath,
		labels:    map[string]string{"aima.dev/engine": "vllm"},
		startTime: time.Now(),
	}

	meta := &deploymentMeta{
		Name:      "stuck-list",
		PID:       999998,
		Port:      32103,
		Engine:    "vllm",
		Labels:    map[string]string{"aima.dev/engine": "vllm"},
		LogPath:   logPath,
		Command:   []string{"/usr/local/bin/vllm", "serve", "/models"},
		StartTime: time.Now(),
	}
	if err := rt.saveMeta(meta); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	statuses, err := rt.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("len(statuses) = %d, want 1", len(statuses))
	}
	if statuses[0].Phase != "failed" {
		t.Fatalf("phase = %q, want failed", statuses[0].Phase)
	}
}

func TestStatusDoesNotOverrideLiveProcessWithStalePortReuseFailure(t *testing.T) {
	rt := newTestRuntime(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	rt.processes["warming-up"] = &nativeProcess{
		name:      "warming-up",
		port:      port,
		labels:    map[string]string{"aima.dev/engine": "vllm"},
		startTime: time.Now(),
	}

	meta := &deploymentMeta{
		Name:      "warming-up",
		PID:       999997,
		Port:      port,
		Engine:    "vllm",
		Labels:    map[string]string{"aima.dev/engine": "vllm"},
		Command:   []string{"/usr/local/bin/vllm", "serve", "/models"},
		StartTime: time.Now(),
	}
	if err := rt.saveMeta(meta); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	status, err := rt.Status(context.Background(), "warming-up")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Phase != "running" {
		t.Fatalf("phase = %q, want running", status.Phase)
	}
	if status.Message == "deployment metadata is stale; port is in use by another process" {
		t.Fatal("stale port reuse failure should not override a live in-memory process")
	}
}

func TestHealthCheckAndWarmupRequiresSuccessfulWarmup(t *testing.T) {
	rt := newTestRuntime(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/v1/chat/completions":
			http.Error(w, "wrong service", http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	})}
	defer srv.Shutdown(context.Background())
	go srv.Serve(ln)

	proc := &nativeProcess{
		name:   "warmup-fail",
		port:   ln.Addr().(*net.TCPAddr).Port,
		labels: map[string]string{"aima.dev/model": "qwen3-8b"},
	}

	rt.healthCheckAndWarmup(proc, &HealthCheckConfig{Path: "/health", TimeoutS: 1}, &WarmupConfig{Prompt: "hello", TimeoutS: 1})

	proc.mu.Lock()
	defer proc.mu.Unlock()
	if proc.ready {
		t.Fatal("proc.ready should remain false when warmup request fails")
	}
}

func TestHealthCheckAndWarmupUsesActualModelName(t *testing.T) {
	rt := newTestRuntime(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	gotModel := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/v1/chat/completions":
			defer r.Body.Close()
			var payload struct {
				Model string `json:"model"`
			}
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Errorf("unmarshal warmup body: %v", err)
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			gotModel <- payload.Model
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"warmup"}`))
		default:
			http.NotFound(w, r)
		}
	})}
	defer srv.Shutdown(context.Background())
	go srv.Serve(ln)

	proc := &nativeProcess{
		name:   "qwen3-30b-a3b-vllm",
		port:   ln.Addr().(*net.TCPAddr).Port,
		labels: map[string]string{"aima.dev/model": "qwen3-30b-a3b"},
	}

	rt.healthCheckAndWarmup(proc, &HealthCheckConfig{Path: "/health", TimeoutS: 1}, &WarmupConfig{Prompt: "hello", TimeoutS: 1})

	select {
	case model := <-gotModel:
		if model != "qwen3-30b-a3b" {
			t.Fatalf("warmup model = %q, want %q", model, "qwen3-30b-a3b")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("warmup request was not observed")
	}

	proc.mu.Lock()
	defer proc.mu.Unlock()
	if !proc.ready {
		t.Fatal("proc.ready should be true after successful warmup")
	}
}

func TestDeployAppendsCustomPortFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("command metadata assertion uses shell script")
	}
	rt := newTestRuntime(t)
	script := filepath.Join(t.TempDir(), "funasr.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 1\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	req := &DeployRequest{
		Name:      "funasr",
		Engine:    "funasr",
		Command:   []string{script},
		ModelPath: "/opt/models/funasr",
		Config:    map[string]any{"port": 32103},
		PortSpecs: []knowledge.StartupPort{{
			Name:      "grpc",
			Flag:      "--port-id",
			ConfigKey: "port",
			Primary:   true,
		}},
	}

	err := rt.Deploy(context.Background(), req)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	t.Cleanup(func() {
		_ = rt.Delete(context.Background(), "funasr")
	})
	meta, err := rt.loadMeta("funasr")
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	argStr := strings.Join(meta.Command, " ")
	if !strings.Contains(argStr, "--port-id 32103") {
		t.Fatalf("command = %q, want custom --port-id flag", argStr)
	}
	if strings.Contains(argStr, "--port 32103") {
		t.Fatalf("command = %q, should not contain synthesized --port flag", argStr)
	}
}
