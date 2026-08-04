package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jguan/aima/internal/engine"
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

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
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
	port := freeTCPPort(t)
	wantAddr := "127.0.0.1:" + strconv.Itoa(port)

	// Use a command that exists cross-platform and exits quickly after a while
	var cmd []string
	if runtime.GOOS == "windows" {
		script := newWindowsListenerScript(t, port, 30, false)
		cmd = []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}
	} else {
		cmd = []string{"sh", "-c", "echo hello && sleep 10"}
	}

	err := rt.Deploy(context.Background(), &DeployRequest{
		Name:    "test-deploy",
		Engine:  "test",
		Command: cmd,
		Port:    port,
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
	if s.Address != wantAddr {
		t.Errorf("address = %q, want %q", s.Address, wantAddr)
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
	port := freeTCPPort(t)

	var cmd []string
	if runtime.GOOS == "windows" {
		script := newWindowsListenerScript(t, port, 30, false)
		cmd = []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}
	} else {
		cmd = []string{"sleep", "10"}
	}

	err := rt.Deploy(context.Background(), &DeployRequest{
		Name:    "dup",
		Engine:  "test",
		Command: cmd,
		Port:    port,
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

func TestNativeDeployRejectsPortAlreadyInUse(t *testing.T) {
	rt := newTestRuntime(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	cmd := []string{"sleep", "10"}
	if runtime.GOOS == "windows" {
		script := newWindowsListenerScript(t, port+1, 30, false)
		cmd = []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}
	}

	err = rt.Deploy(context.Background(), &DeployRequest{
		Name:    "port-conflict",
		Engine:  "test",
		Command: cmd,
		Port:    port,
	})
	if err == nil {
		t.Fatal("expected port conflict error")
	}
	if !strings.Contains(err.Error(), "port") || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("error = %q, want port conflict", err)
	}
}

func TestNativeDeployRejectsPortUsedByKnownDeployment(t *testing.T) {
	rt := newTestRuntime(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	if err := rt.saveMeta(&deploymentMeta{
		Name:      "existing-deploy",
		PID:       12345,
		Port:      port,
		Engine:    "llamacpp",
		LogPath:   filepath.Join(t.TempDir(), "existing.log"),
		Command:   []string{"llama-server", "--port", strconv.Itoa(port)},
		StartTime: time.Now(),
	}); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	cmd := []string{"sleep", "10"}
	if runtime.GOOS == "windows" {
		script := newWindowsListenerScript(t, port+1, 30, false)
		cmd = []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}
	}

	err = rt.Deploy(context.Background(), &DeployRequest{
		Name:    "new-deploy",
		Engine:  "test",
		Command: cmd,
		Port:    port,
	})
	if err == nil {
		t.Fatal("expected known deployment port conflict error")
	}
	if !strings.Contains(err.Error(), `deployment "existing-deploy"`) {
		t.Fatalf("error = %q, want existing deployment name", err)
	}
}

func TestNativeModelPathSubstitution(t *testing.T) {
	rt := newTestRuntime(t)
	port := freeTCPPort(t)

	// Deploy with a command containing {{.ModelPath}} — use echo to verify substitution
	var cmd []string
	modelPath := "/data/models/test-model"
	if runtime.GOOS == "windows" {
		script := newWindowsListenerScript(t, port, 2, true)
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
		Port:      port,
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

func TestLogsFallsBackToDeterministicPathAfterMetadataRemoval(t *testing.T) {
	rt := newTestRuntime(t)
	if err := os.MkdirAll(rt.logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rt.logDir, "failed-model.log"), []byte("engine root cause\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := rt.Logs(context.Background(), "failed-model", 20)
	if err != nil {
		t.Fatal(err)
	}
	if got != "engine root cause" {
		t.Fatalf("logs = %q", got)
	}
}

func TestLogsFallbackRejectsPathTraversal(t *testing.T) {
	rt := newTestRuntime(t)
	for _, name := range []string{"", ".", "..", "../secret", `..\secret`} {
		t.Run(name, func(t *testing.T) {
			if _, err := rt.Logs(context.Background(), name, 20); err == nil {
				t.Fatalf("unsafe name %q was accepted", name)
			}
		})
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

func TestWaitForPortRelease(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	done := make(chan struct{})
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = ln.Close()
		close(done)
	}()

	start := time.Now()
	if !waitForPortRelease(port, time.Second) {
		t.Fatal("waitForPortRelease = false, want true")
	}
	<-done
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("waitForPortRelease returned too early: %v", elapsed)
	}
}

func TestNativeDeleteWaitsForPortReleaseBeforeReturning(t *testing.T) {
	rt := newTestRuntime(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	oldTimeout := nativePortReleaseTimeout
	oldPoll := nativePortReleasePollInterval
	nativePortReleaseTimeout = time.Second
	nativePortReleasePollInterval = 25 * time.Millisecond
	defer func() {
		nativePortReleaseTimeout = oldTimeout
		nativePortReleasePollInterval = oldPoll
	}()

	if err := rt.saveMeta(&deploymentMeta{
		Name:      "linger-port",
		Port:      port,
		Engine:    "llamacpp",
		LogPath:   filepath.Join(t.TempDir(), "linger.log"),
		Command:   []string{"llama-server", "--port", strconv.Itoa(port)},
		StartTime: time.Now(),
	}); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = ln.Close()
	}()

	start := time.Now()
	if err := rt.Delete(context.Background(), "linger-port"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("Delete returned before port release wait completed: %v", elapsed)
	}
	if _, err := rt.loadMeta("linger-port"); err == nil {
		t.Fatal("expected metadata to be removed after delete")
	}
}

func TestNativeProcessDoneChannelClosedOnExit(t *testing.T) {
	rt := newTestRuntime(t)
	port := freeTCPPort(t)

	var cmd []string
	if runtime.GOOS == "windows" {
		script := newWindowsListenerScript(t, port, 1, false)
		cmd = []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}
	} else {
		cmd = []string{"sh", "-c", "echo done"}
	}

	if err := rt.Deploy(context.Background(), &DeployRequest{
		Name:    "quick-exit",
		Engine:  "test",
		Command: cmd,
		Port:    port,
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
	port := freeTCPPort(t)
	wantAddr := "127.0.0.1:" + strconv.Itoa(port)

	// "First CLI invocation": deploy a long-running process
	rt1 := NewNativeRuntime(logDir, "", deployDir)

	var cmd []string
	if runtime.GOOS == "windows" {
		script := newWindowsListenerScript(t, port, 30, false)
		cmd = []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}
	} else {
		cmd = []string{"sleep", "30"}
	}

	err := rt1.Deploy(context.Background(), &DeployRequest{
		Name:    "persistent",
		Engine:  "test",
		Command: cmd,
		Port:    port,
		Config:  map[string]any{"mem_fraction_static": 0.9},
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
	if s.Address != wantAddr {
		t.Errorf("address = %q, want %q", s.Address, wantAddr)
	}
	if s.Config["mem_fraction_static"] != 0.9 {
		t.Errorf("config mem_fraction_static = %#v, want 0.9", s.Config["mem_fraction_static"])
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

func TestClassifyWindowsProcessMetaState(t *testing.T) {
	tests := []struct {
		name        string
		recordedPID int
		listenerPID int
		pidAlive    bool
		want        processMetaState
	}{
		{name: "alive before port bind", recordedPID: 101, pidAlive: true, want: processMetaStarting},
		{name: "owns listener", recordedPID: 101, listenerPID: 101, pidAlive: true, want: processMetaMatching},
		{name: "different listener", recordedPID: 101, listenerPID: 202, pidAlive: true, want: processMetaStale},
		{name: "exited before port bind", recordedPID: 101, pidAlive: false, want: processMetaExited},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyWindowsProcessMetaState(tt.recordedPID, tt.listenerPID, tt.pidAlive); got != tt.want {
				t.Fatalf("state = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSelectNewProcessPID(t *testing.T) {
	tests := []struct {
		name    string
		before  []int
		current []int
		want    int
	}{
		{name: "one new process", before: []int{10, 20}, current: []int{10, 20, 30}, want: 30},
		{name: "reject pre-existing", before: []int{10}, current: []int{10}, want: 0},
		{name: "reject ambiguity", before: []int{10}, current: []int{10, 20, 30}, want: 0},
		{name: "ignore invalid PIDs", current: []int{0, -1, 30}, want: 30},
		{name: "deduplicate current snapshot", before: []int{10}, current: []int{10, 20, 20}, want: 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectNewProcessPID(tt.before, tt.current); got != tt.want {
				t.Fatalf("pid = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMetaPhaseForProcessState(t *testing.T) {
	started := time.Now()
	tests := []struct {
		name      string
		state     processMetaState
		portBound bool
		startedAt time.Time
		timeoutS  int
		want      string
	}{
		{name: "alive before port bind", state: processMetaStarting, startedAt: started, timeoutS: 60, want: "starting"},
		{name: "matching before port bind", state: processMetaMatching, startedAt: started, timeoutS: 60, want: "starting"},
		{name: "listener bound", state: processMetaMatching, portBound: true, startedAt: started, timeoutS: 60, want: "running"},
		{name: "different listener", state: processMetaStale, portBound: true, startedAt: started, timeoutS: 60, want: "failed"},
		{name: "process exited", state: processMetaExited, startedAt: started, timeoutS: 60, want: "failed"},
		{name: "startup timeout", state: processMetaStarting, startedAt: started.Add(-61 * time.Second), timeoutS: 60, want: "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := metaPhaseForProcessState(tt.state, tt.portBound, tt.startedAt, tt.timeoutS); got != tt.want {
				t.Fatalf("phase = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMetaToStatusDoesNotReportPortReuseWhenPortIsUnbound(t *testing.T) {
	rt := newTestRuntime(t)
	status := rt.metaToStatus(&deploymentMeta{
		Name:      "stale-process-identity",
		PID:       os.Getpid(),
		Port:      freeTCPPort(t),
		Command:   []string{"definitely-not-the-current-test-process"},
		StartTime: time.Now(),
	})

	if status.Phase != "failed" {
		t.Fatalf("phase = %q, want failed", status.Phase)
	}
	if status.Message != "deployment metadata is stale; process identity does not match" {
		t.Fatalf("message = %q, want process identity mismatch", status.Message)
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
	port := freeTCPPort(t)
	if runtime.GOOS == "windows" {
		script := newWindowsListenerScript(t, port, 5, false)
		cmd = []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}
	} else {
		cmd = []string{"sleep", "5"}
	}

	if err := rt.Deploy(context.Background(), &DeployRequest{
		Name:    "stale",
		Engine:  "test",
		Command: cmd,
		Port:    port,
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
	deadline := time.Now().Add(2 * time.Second)
	for {
		if processMatchesMeta(meta) {
			return
		}
		if time.Now().After(deadline) {
			cmdline, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "cmdline"))
			t.Fatalf("processMatchesMeta should allow interpreter prefix, cmdline=%q", string(cmdline))
		}
		time.Sleep(10 * time.Millisecond)
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

func TestCommandPrefixMatchesPortableELFLoader(t *testing.T) {
	expected := []string{
		"/opt/aima/dist/linux-amd64/bin/aima-engine",
		"serve", "--model-dir", "/models/qwen", "--port", "1024",
	}
	actual := []string{
		"/opt/aima/dist/linux-amd64/lib/ld-linux-x86-64.so.2",
		"--inhibit-cache",
		"--library-path", "/opt/aima/dist/linux-amd64/lib",
		"--argv0", "/opt/aima/dist/linux-amd64/bin/aima-engine",
		"/opt/aima/dist/linux-amd64/libexec/aima-engine.real",
		"serve", "--model-dir", "/models/qwen", "--port", "1024",
	}

	if !commandPrefixMatches(actual, expected) {
		t.Fatalf("portable ELF loader command should match deployment metadata")
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
		exited:    true,
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

func TestStatusDoesNotOverrideLiveStartingProcessWithPersistedFailure(t *testing.T) {
	rt := newTestRuntime(t)
	logPath := filepath.Join(t.TempDir(), "slow-start.log")
	if err := os.WriteFile(logPath, []byte("INFO still loading\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	proc := &nativeProcess{
		name:      "slow-start",
		port:      freeTCPPort(t),
		startTime: time.Now(),
		logPath:   logPath,
	}
	rt.processes[proc.name] = proc
	if err := rt.saveMeta(&deploymentMeta{
		Name:      proc.name,
		PID:       999999,
		Port:      proc.port,
		Command:   []string{"llama-server", "--port", strconv.Itoa(proc.port)},
		StartTime: proc.startTime,
		LogPath:   logPath,
	}); err != nil {
		t.Fatal(err)
	}

	status, err := rt.Status(context.Background(), proc.name)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != "starting" {
		t.Fatalf("phase = %q, want starting", status.Phase)
	}
	if status.Message == "process exited before readiness" {
		t.Fatalf("live startup inherited false persisted failure: %#v", status)
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
		exited:    true,
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

func TestListDoesNotHoldRuntimeLockDuringPersistedStatusChecks(t *testing.T) {
	rt := newTestRuntime(t)

	requestStarted := make(chan struct{}, 1)
	releaseResponse := make(chan struct{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		<-releaseResponse
		w.WriteHeader(http.StatusOK)
	})}
	defer srv.Shutdown(context.Background())
	go srv.Serve(ln)

	port := ln.Addr().(*net.TCPAddr).Port
	name := "slow-list"

	if err := rt.saveMeta(&deploymentMeta{
		Name:               name,
		Port:               port,
		Labels:             map[string]string{"aima.dev/engine": "llamacpp"},
		LogPath:            filepath.Join(t.TempDir(), "slow.log"),
		Command:            []string{"/usr/local/bin/llama-server", "--port", strconv.Itoa(port)},
		StartTime:          time.Now(),
		HealthCheckPath:    "/health",
		HealthCheckTimeout: 60,
	}); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	listDone := make(chan struct{})
	go func() {
		defer close(listDone)
		if _, err := rt.List(context.Background()); err != nil {
			t.Errorf("List: %v", err)
		}
	}()

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for persisted health check")
	}

	lockAcquired := make(chan struct{})
	go func() {
		rt.mu.Lock()
		close(lockAcquired)
		rt.mu.Unlock()
	}()

	select {
	case <-lockAcquired:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runtime write lock blocked while List was performing status checks")
	}

	close(releaseResponse)

	select {
	case <-listDone:
	case <-time.After(2 * time.Second):
		t.Fatal("List did not complete after releasing health check")
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
	if status.Phase != "starting" {
		t.Fatalf("phase = %q, want starting", status.Phase)
	}
	if status.Message == "deployment metadata is stale; port is in use by another process" {
		t.Fatal("stale port reuse failure should not override a live in-memory process")
	}
}

func TestProcToStatusMarksNonReadyProcessAsStarting(t *testing.T) {
	rt := newTestRuntime(t)
	rt.engineAssets = []knowledge.EngineAsset{{
		Metadata:        knowledge.EngineMetadata{Name: "llamacpp"},
		TimeConstraints: knowledge.TimeConstraints{ColdStartS: []int{3, 10}},
	}}

	status := rt.procToStatus(&nativeProcess{
		name:      "booting",
		port:      freeTCPPort(t),
		labels:    map[string]string{"aima.dev/engine": "llamacpp"},
		startTime: time.Now().Add(-2 * time.Second),
	})

	if status.Phase != "starting" {
		t.Fatalf("phase = %q, want starting", status.Phase)
	}
	if status.Ready {
		t.Fatal("ready should be false during startup")
	}
	if status.StartupProgress <= 0 {
		t.Fatalf("startup_progress = %d, want > 0", status.StartupProgress)
	}
	if status.StartupMessage == "" {
		t.Fatal("startup_message should not be empty during startup")
	}
}

func TestNativeStatusRechecksHealthAfterReady(t *testing.T) {
	var healthCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&healthCalls, 1) == 1 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	port := server.Listener.Addr().(*net.TCPAddr).Port
	if !httpHealthy(port, "/health") {
		t.Fatal("initial health check should succeed")
	}

	rt := newTestRuntime(t)
	proc := &nativeProcess{
		name:            "health-regressed",
		port:            port,
		healthCheckPath: "/health",
		ready:           true,
		startTime:       time.Now(),
	}
	rt.processes[proc.name] = proc

	status, err := rt.Status(context.Background(), "health-regressed")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Ready {
		t.Fatal("ready should be false after the health endpoint becomes unavailable")
	}
}

func TestNativeStatusUsesLiveHealthObservationOverPersistedMetadata(t *testing.T) {
	var healthCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch atomic.AddInt32(&healthCalls, 1) {
		case 1, 3:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()

	port := server.Listener.Addr().(*net.TCPAddr).Port
	if !httpHealthy(port, "/health") {
		t.Fatal("initial health check should succeed")
	}

	rt := newTestRuntime(t)
	proc := &nativeProcess{
		name:            "live-health-observation",
		port:            port,
		healthCheckPath: "/health",
		ready:           true,
		startTime:       time.Now(),
	}
	rt.processes[proc.name] = proc
	if err := rt.saveMeta(&deploymentMeta{
		Name:            proc.name,
		Port:            port,
		StartTime:       proc.startTime,
		HealthCheckPath: "/health",
	}); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	status, err := rt.Status(context.Background(), proc.name)
	if err != nil {
		t.Fatalf("first Status: %v", err)
	}
	if status.Ready {
		t.Fatal("first live 503 health observation must be visible")
	}
	if status.Phase != "failed" {
		t.Fatalf("first live 503 phase = %q, want failed", status.Phase)
	}
	if !strings.Contains(status.Message, "health check failed") {
		t.Fatalf("first live 503 message = %q", status.Message)
	}
	if got := atomic.LoadInt32(&healthCalls); got != 2 {
		t.Fatalf("health requests after first Status = %d, want 2", got)
	}

	status, err = rt.Status(context.Background(), proc.name)
	if err != nil {
		t.Fatalf("second Status: %v", err)
	}
	if !status.Ready {
		t.Fatal("later live 200 health observation must restore ready")
	}
	if got := atomic.LoadInt32(&healthCalls); got != 3 {
		t.Fatalf("health requests after second Status = %d, want 3", got)
	}
}

func TestProcToStatusReportsExitDuringHealthProbe(t *testing.T) {
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(probeStarted)
		<-releaseProbe
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	proc := &nativeProcess{
		name:            "exits-during-status-probe",
		port:            server.Listener.Addr().(*net.TCPAddr).Port,
		healthCheckPath: "/health",
		ready:           true,
		startTime:       time.Now(),
	}
	rt := newTestRuntime(t)
	statusResult := make(chan *DeploymentStatus, 1)
	go func() {
		statusResult <- rt.procToStatus(proc)
	}()

	<-probeStarted
	proc.mu.Lock()
	proc.exited = true
	proc.exitSuccess = false
	proc.ready = false
	proc.mu.Unlock()
	close(releaseProbe)

	status := <-statusResult
	if status.Ready {
		t.Fatal("exited process must not report ready")
	}
	if status.Phase != "failed" {
		t.Fatalf("phase = %q, want failed", status.Phase)
	}
}

func TestHealthCheckAndWarmupReturnsExitFailureDuringReadinessCommit(t *testing.T) {
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(probeStarted)
		<-releaseProbe
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	proc := &nativeProcess{
		name:      "exits-during-readiness-commit",
		port:      server.Listener.Addr().(*net.TCPAddr).Port,
		startTime: time.Now(),
	}
	results := make(chan error, 1)
	rt := newTestRuntime(t)
	go func() {
		results <- rt.healthCheckAndWarmup(proc, &HealthCheckConfig{Path: "/health", TimeoutS: 1}, nil)
	}()

	<-probeStarted
	proc.mu.Lock()
	proc.exited = true
	proc.exitSuccess = false
	proc.ready = false
	proc.mu.Unlock()
	close(releaseProbe)

	err := <-results
	if err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("healthCheckAndWarmup error = %v, want exit-related failure", err)
	}
	proc.mu.Lock()
	ready := proc.ready
	proc.mu.Unlock()
	if ready {
		t.Fatal("readiness commit must not mark an exited process ready")
	}
}

func TestMetaToStatusMarksAliveUnreadyDeploymentAsStarting(t *testing.T) {
	rt := newTestRuntime(t)
	rt.engineAssets = []knowledge.EngineAsset{{
		Metadata:        knowledge.EngineMetadata{Name: "llamacpp"},
		TimeConstraints: knowledge.TimeConstraints{ColdStartS: []int{3, 10}},
	}}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "starting", http.StatusServiceUnavailable)
	})}
	defer srv.Shutdown(context.Background())
	go srv.Serve(ln)

	port := ln.Addr().(*net.TCPAddr).Port
	status := rt.metaToStatus(&deploymentMeta{
		Name:               "booting-http",
		Port:               port,
		Labels:             map[string]string{"aima.dev/engine": "llamacpp"},
		StartTime:          time.Now().Add(-4 * time.Second),
		HealthCheckPath:    "/health",
		HealthCheckTimeout: 60,
	})

	if status.Phase != "starting" {
		t.Fatalf("phase = %q, want starting", status.Phase)
	}
	if status.Ready {
		t.Fatal("ready should be false while health endpoint is not ready")
	}
	if status.StartupProgress < 25 {
		t.Fatalf("startup_progress = %d, want >= 25 for alive-but-unready service", status.StartupProgress)
	}
	if status.StartupMessage == "" {
		t.Fatal("startup_message should not be empty")
	}
}

func TestNativeHealthTimeoutMarksAliveUnreadyDeploymentFailedAndStalled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "still loading", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	port := server.Listener.Addr().(*net.TCPAddr).Port
	startTime := time.Now().Add(-2 * time.Second)
	rt := newTestRuntime(t)

	for _, tt := range []struct {
		name   string
		status func() *DeploymentStatus
	}{
		{
			name: "in-memory process",
			status: func() *DeploymentStatus {
				return rt.procToStatus(&nativeProcess{
					name:            "stuck-in-memory",
					port:            port,
					healthCheckPath: "/health",
					startTime:       startTime,
					startupTimeout:  time.Second,
				})
			},
		},
		{
			name: "persisted process",
			status: func() *DeploymentStatus {
				return rt.metaToStatus(&deploymentMeta{
					Name:               "stuck-persisted",
					Port:               port,
					StartTime:          startTime,
					HealthCheckPath:    "/health",
					HealthCheckTimeout: 1,
				})
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status := tt.status()
			if status.Phase != "failed" || status.Ready || !status.Stalled {
				t.Fatalf("status = %+v, want failed, unready, and stalled", status)
			}
			if !strings.Contains(status.Message, "health check timed out") {
				t.Fatalf("message = %q, want health timeout", status.Message)
			}
		})
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

func TestNativeDeployRestoresPrivateAdapterContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("command metadata assertion uses a POSIX shell script")
	}

	rt := newTestRuntime(t)
	script := filepath.Join(t.TempDir(), "native-adapter-engine.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	modelPath := filepath.Join(t.TempDir(), "model")
	if err := os.MkdirAll(modelPath, 0o755); err != nil {
		t.Fatalf("create model dir: %v", err)
	}

	if err := rt.Deploy(context.Background(), &DeployRequest{
		Name:      "native-adapter-context",
		Engine:    "native-test",
		Command:   []string{script, "serve", "--model-dir", "{{.ModelPath}}"},
		ModelPath: modelPath,
		Port:      freeTCPPort(t),
		Config:    map[string]any{"context_tokens": 8192},
	}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	t.Cleanup(func() { _ = rt.Delete(context.Background(), "native-adapter-context") })

	instanceID := ""
	assertContext := func(t *testing.T, status *DeploymentStatus) {
		t.Helper()
		if len(status.AdapterCommand) == 0 || status.AdapterCommand[0] != script {
			t.Fatalf("AdapterCommand = %q, want resolved executable %q", status.AdapterCommand, script)
		}
		if status.AdapterModelPath != modelPath {
			t.Fatalf("AdapterModelPath = %q, want %q", status.AdapterModelPath, modelPath)
		}
		if status.AdapterInstanceID == "" {
			t.Fatal("AdapterInstanceID is empty")
		}
		if instanceID == "" {
			instanceID = status.AdapterInstanceID
		} else if status.AdapterInstanceID != instanceID {
			t.Fatalf("AdapterInstanceID = %q, want persisted %q", status.AdapterInstanceID, instanceID)
		}
		encoded, err := json.Marshal(status)
		if err != nil {
			t.Fatalf("marshal status: %v", err)
		}
		if bytes.Contains(encoded, []byte(script)) || bytes.Contains(encoded, []byte(modelPath)) || bytes.Contains(encoded, []byte("AdapterCommand")) || bytes.Contains(encoded, []byte("AdapterInstanceID")) {
			t.Fatalf("private adapter context leaked in status JSON: %s", encoded)
		}
	}

	status, err := rt.Status(context.Background(), "native-adapter-context")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	assertContext(t, status)

	fresh := NewNativeRuntime(rt.logDir, rt.distDir, rt.deployDir)
	restored, err := fresh.Status(context.Background(), "native-adapter-context")
	if err != nil {
		t.Fatalf("fresh Status: %v", err)
	}
	assertContext(t, restored)

	meta, err := fresh.loadMeta("native-adapter-context")
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	if meta.ModelPath != modelPath {
		t.Fatalf("persisted ModelPath = %q, want %q", meta.ModelPath, modelPath)
	}
}

func TestFindInEngineDirsResolvesScannedBinary(t *testing.T) {
	// A native engine binary discovered by scanning AIMA_ENGINE_DIR must also be
	// launchable: the native runtime resolves the bare binary name against the same
	// engine dirs, so "scanned ⇒ launchable" holds even when it is not in dist/PATH.
	dir := t.TempDir()
	fileName := "llama-server"
	if runtime.GOOS == "windows" {
		fileName += ".exe"
	}
	want := filepath.Join(dir, fileName)
	if err := os.WriteFile(want, []byte("stub"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	// Empty dir entries are skipped; the bare name resolves to the absolute path.
	r := &NativeRuntime{engineDirs: []string{"", dir}}
	if got := r.findInEngineDirs("llama-server"); got != want {
		t.Errorf("findInEngineDirs = %q, want %q", got, want)
	}
	// Missing binary and no configured dirs both resolve to empty.
	if got := r.findInEngineDirs("nope"); got != "" {
		t.Errorf("missing binary: got %q, want empty", got)
	}
	if got := (&NativeRuntime{}).findInEngineDirs("llama-server"); got != "" {
		t.Errorf("no engine dirs: got %q, want empty", got)
	}
}

func TestFindLocalBinaryUsesNestedSourcePath(t *testing.T) {
	distDir := t.TempDir()
	want := filepath.Join(distDir, "bin", "aima-engine")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatalf("mkdir nested binary dir: %v", err)
	}
	if err := os.WriteFile(want, []byte("stub"), 0o755); err != nil {
		t.Fatalf("write nested binary: %v", err)
	}

	r := &NativeRuntime{distDir: distDir}
	source := &engine.BinarySource{Binary: "bin/aima-engine"}
	if got := r.findLocalBinary("aima-engine", source); got != want {
		t.Fatalf("findLocalBinary = %q, want %q", got, want)
	}
}

func TestFindLocalBinaryDoesNotBypassPinnedSourceProvenance(t *testing.T) {
	distDir := t.TempDir()
	legacy := filepath.Join(distDir, "bin", "aima-engine")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("unverified"), 0o755); err != nil {
		t.Fatal(err)
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	source := &engine.BinarySource{
		Binary: "bin/aima-engine",
		SHA256: map[string]string{platform: strings.Repeat("a", 64)},
	}

	if got := (&NativeRuntime{distDir: distDir}).findLocalBinary("aima-engine", source); got != "" {
		t.Fatalf("findLocalBinary trusted unverified pinned candidate %q", got)
	}
}
