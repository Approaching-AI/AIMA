package zeroclaw

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAvailable_NoBinary(t *testing.T) {
	m := NewManager(WithBinaryPath("/nonexistent/path/to/zeroclaw"))
	if m.Available() {
		t.Error("Available() = true, want false for nonexistent binary")
	}
}

func TestAvailable_WithBinary(t *testing.T) {
	// Create a temp binary
	dir := t.TempDir()
	binPath := filepath.Join(dir, "zeroclaw")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	if err := os.WriteFile(binPath, []byte("fake"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	m := NewManager(WithBinaryPath(binPath))
	if !m.Available() {
		t.Error("Available() = false, want true for existing binary")
	}
}

func TestAvailable_Directory(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(WithBinaryPath(dir))
	if m.Available() {
		t.Error("Available() = true, want false for directory path")
	}
}

func TestNewManager_Defaults(t *testing.T) {
	m := NewManager()
	if m.binaryPath != "zeroclaw" {
		t.Errorf("binaryPath = %q, want zeroclaw", m.binaryPath)
	}
	if m.dataDir != "" {
		t.Errorf("dataDir = %q, want empty", m.dataDir)
	}
}

func TestNewManager_Options(t *testing.T) {
	m := NewManager(
		WithBinaryPath("/usr/local/bin/zeroclaw"),
		WithDataDir("/var/lib/zeroclaw"),
	)
	if m.binaryPath != "/usr/local/bin/zeroclaw" {
		t.Errorf("binaryPath = %q, want /usr/local/bin/zeroclaw", m.binaryPath)
	}
	if m.dataDir != "/var/lib/zeroclaw" {
		t.Errorf("dataDir = %q, want /var/lib/zeroclaw", m.dataDir)
	}
}

func TestHealth_NotRunning(t *testing.T) {
	m := NewManager()
	if m.Health() {
		t.Error("Health() = true, want false when not running")
	}
}

func TestStop_NotRunning(t *testing.T) {
	m := NewManager()
	err := m.Stop(context.Background())
	if err != nil {
		t.Errorf("Stop: %v (expected nil for non-running manager)", err)
	}
}

func TestPlatformBinary(t *testing.T) {
	bin, err := platformBinary()
	if err != nil {
		t.Fatalf("platformBinary: %v", err)
	}

	goos := runtime.GOOS
	goarch := runtime.GOARCH

	switch {
	case goos == "linux" && goarch == "amd64":
		if bin != "zeroclaw-linux-amd64" {
			t.Errorf("bin = %q, want zeroclaw-linux-amd64", bin)
		}
	case goos == "linux" && goarch == "arm64":
		if bin != "zeroclaw-linux-arm64" {
			t.Errorf("bin = %q, want zeroclaw-linux-arm64", bin)
		}
	case goos == "darwin" && goarch == "amd64":
		if bin != "zeroclaw-darwin-amd64" {
			t.Errorf("bin = %q, want zeroclaw-darwin-amd64", bin)
		}
	case goos == "darwin" && goarch == "arm64":
		if bin != "zeroclaw-darwin-arm64" {
			t.Errorf("bin = %q, want zeroclaw-darwin-arm64", bin)
		}
	case goos == "windows" && goarch == "amd64":
		if bin != "zeroclaw-windows-amd64.exe" {
			t.Errorf("bin = %q, want zeroclaw-windows-amd64.exe", bin)
		}
	default:
		t.Skipf("untested platform: %s/%s", goos, goarch)
	}
}

func TestDownloadURL(t *testing.T) {
	url, err := downloadURL()
	if err != nil {
		t.Fatalf("downloadURL: %v", err)
	}

	bin, _ := platformBinary()
	expected := releaseBaseURL + "/" + bin
	if url != expected {
		t.Errorf("url = %q, want %q", url, expected)
	}
}

func TestInstallFrom_MockServer(t *testing.T) {
	bin, err := platformBinary()
	if err != nil {
		t.Fatalf("platformBinary: %v", err)
	}

	// Content must exceed minBinarySize (100KB) to pass the size check
	fakeContent := bytes.Repeat([]byte("x"), 128*1024)

	// Mock HTTP server that serves the binary
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := "/" + bin
		if r.URL.Path != expected {
			t.Errorf("request path = %q, want %q", r.URL.Path, expected)
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(fakeContent)
	}))
	defer server.Close()

	destDir := t.TempDir()
	path, err := installFrom(context.Background(), destDir, server.Client(), server.URL)
	if err != nil {
		t.Fatalf("installFrom: %v", err)
	}

	// Verify file exists and has correct size
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if len(data) != len(fakeContent) {
		t.Errorf("binary size = %d, want %d", len(data), len(fakeContent))
	}

	// Verify file is in the right place
	expectedPath := filepath.Join(destDir, bin)
	if path != expectedPath {
		t.Errorf("path = %q, want %q", path, expectedPath)
	}

	// Verify file is executable
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" {
		if info.Mode()&0o100 == 0 {
			t.Error("binary is not executable")
		}
	}
}

func TestInstallFrom_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	destDir := t.TempDir()
	_, err := installFrom(context.Background(), destDir, server.Client(), server.URL)
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
}

func TestInstallFrom_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block forever
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	destDir := t.TempDir()
	_, err := installFrom(ctx, destDir, server.Client(), server.URL)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestInstallFrom_CreateDestDir(t *testing.T) {
	bin, err := platformBinary()
	if err != nil {
		t.Fatalf("platformBinary: %v", err)
	}

	fakeBinary := bytes.Repeat([]byte("b"), 128*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(fakeBinary)
	}))
	defer server.Close()

	// Use a nested directory that doesn't exist yet
	destDir := filepath.Join(t.TempDir(), "sub", "dir")
	path, err := installFrom(context.Background(), destDir, server.Client(), server.URL)
	if err != nil {
		t.Fatalf("installFrom: %v", err)
	}

	expectedPath := filepath.Join(destDir, bin)
	if path != expectedPath {
		t.Errorf("path = %q, want %q", path, expectedPath)
	}
}

func TestStart_NonexistentBinary(t *testing.T) {
	m := NewManager(WithBinaryPath("/nonexistent/zeroclaw-binary-12345"))
	err := m.Start(context.Background(), StartConfig{})
	if err == nil {
		t.Fatal("expected error for nonexistent binary")
	}
}
