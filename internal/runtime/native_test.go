package runtime

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

func TestNativeDeployAndDelete(t *testing.T) {
	rt := newTestRuntime(t)

	// Use a command that exists cross-platform and exits quickly after a while
	var cmd []string
	if runtime.GOOS == "windows" {
		cmd = []string{"cmd", "/c", "echo hello && ping -n 3 127.0.0.1 >nul"}
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
		cmd = []string{"cmd", "/c", "ping -n 10 127.0.0.1 >nul"}
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
		cmd = []string{"cmd", "/c", "echo {{.ModelPath}}"}
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
		cmd = []string{"cmd", "/c", "echo done"}
	} else {
		cmd = []string{"sh", "-c", "echo done"}
	}

	if err := rt.Deploy(context.Background(), &DeployRequest{
		Name:    "quick-exit",
		Engine:  "test",
		Command: cmd,
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
		cmd = []string{"cmd", "/c", "ping -n 30 127.0.0.1 >nul"}
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
