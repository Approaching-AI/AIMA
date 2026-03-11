package zeroclaw

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAvailable_NoBinary(t *testing.T) {
	m := NewManager(WithBinaryPath("/nonexistent/path/to/zeroclaw"))
	if m.Available() {
		t.Error("Available() = true, want false for nonexistent binary")
	}
}

func TestAvailable_WithBinary(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, executableName())
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
	if err := m.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v (expected nil for non-running manager)", err)
	}
}

func TestPlatformAsset(t *testing.T) {
	asset, err := platformAsset()
	if err != nil {
		t.Fatalf("platformAsset: %v", err)
	}

	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		if asset != "zeroclaw-x86_64-unknown-linux-gnu.tar.gz" {
			t.Errorf("asset = %q, want zeroclaw-x86_64-unknown-linux-gnu.tar.gz", asset)
		}
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		if asset != "zeroclaw-aarch64-unknown-linux-gnu.tar.gz" {
			t.Errorf("asset = %q, want zeroclaw-aarch64-unknown-linux-gnu.tar.gz", asset)
		}
	case runtime.GOOS == "darwin" && runtime.GOARCH == "amd64":
		if asset != "zeroclaw-x86_64-apple-darwin.tar.gz" {
			t.Errorf("asset = %q, want zeroclaw-x86_64-apple-darwin.tar.gz", asset)
		}
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		if asset != "zeroclaw-aarch64-apple-darwin.tar.gz" {
			t.Errorf("asset = %q, want zeroclaw-aarch64-apple-darwin.tar.gz", asset)
		}
	case runtime.GOOS == "windows" && runtime.GOARCH == "amd64":
		if asset != "zeroclaw-x86_64-pc-windows-msvc.zip" {
			t.Errorf("asset = %q, want zeroclaw-x86_64-pc-windows-msvc.zip", asset)
		}
	default:
		t.Skipf("untested platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

func TestDownloadURL(t *testing.T) {
	url, err := downloadURL()
	if err != nil {
		t.Fatalf("downloadURL: %v", err)
	}

	asset, _ := platformAsset()
	expected := releaseBaseURL + "/" + asset
	if url != expected {
		t.Errorf("url = %q, want %q", url, expected)
	}
}

func TestInstalledBinaryPath(t *testing.T) {
	path, err := InstalledBinaryPath("/tmp/zc")
	if err != nil {
		t.Fatalf("InstalledBinaryPath: %v", err)
	}

	expected := filepath.Join("/tmp/zc", executableName())
	if path != expected {
		t.Fatalf("path = %q, want %q", path, expected)
	}
}

func TestInstallFrom_MockServer(t *testing.T) {
	asset, err := platformAsset()
	if err != nil {
		t.Fatalf("platformAsset: %v", err)
	}

	fakeBinary := fakeBinaryPayload(128 * 1024)
	fakeArchive := fakeReleaseArchive(t, fakeBinary)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := "/" + asset
		if r.URL.Path != expected {
			t.Errorf("request path = %q, want %q", r.URL.Path, expected)
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fakeArchive)
	}))
	defer server.Close()

	destDir := t.TempDir()
	path, err := installFrom(context.Background(), destDir, server.Client(), server.URL)
	if err != nil {
		t.Fatalf("installFrom: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if len(data) != len(fakeBinary) {
		t.Errorf("binary size = %d, want %d", len(data), len(fakeBinary))
	}

	expectedPath, _ := InstalledBinaryPath(destDir)
	if path != expectedPath {
		t.Errorf("path = %q, want %q", path, expectedPath)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode()&0o100 == 0 {
		t.Error("binary is not executable")
	}
}

func TestInstallFrom_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	_, err := installFrom(context.Background(), t.TempDir(), server.Client(), server.URL)
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
}

func TestInstallFrom_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := installFrom(ctx, t.TempDir(), server.Client(), server.URL)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestInstallFrom_CreateDestDir(t *testing.T) {
	fakeArchive := fakeReleaseArchive(t, fakeBinaryPayload(128*1024))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fakeArchive)
	}))
	defer server.Close()

	destDir := filepath.Join(t.TempDir(), "sub", "dir")
	path, err := installFrom(context.Background(), destDir, server.Client(), server.URL)
	if err != nil {
		t.Fatalf("installFrom: %v", err)
	}

	expectedPath, _ := InstalledBinaryPath(destDir)
	if path != expectedPath {
		t.Errorf("path = %q, want %q", path, expectedPath)
	}
}

func TestWriteManagedConfig(t *testing.T) {
	dataDir := t.TempDir()
	cfg := ManagedConfig{
		Provider: "openai",
		Model:    "qwen3-8b",
		APIURL:   "http://127.0.0.1:6188/v1",
		APIKey:   "secret-key",
	}

	if err := WriteManagedConfig(dataDir, cfg); err != nil {
		t.Fatalf("WriteManagedConfig: %v", err)
	}

	configPath := filepath.Join(dataDir, "zeroclaw", "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		`default_provider = "openai"`,
		`default_model = "qwen3-8b"`,
		`api_url = "http://127.0.0.1:6188/v1"`,
		`api_key = "secret-key"`,
		`[gateway]`,
		`require_pairing = false`,
		`[agent]`,
		`compact_context = true`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestStart_NonexistentBinary(t *testing.T) {
	m := NewManager(WithBinaryPath("/nonexistent/zeroclaw-binary-12345"))
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("expected error for nonexistent binary")
	}
}

func TestStart_RetriesTransientStartupFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script helper not portable on windows")
	}

	binDir := t.TempDir()
	dataDir := t.TempDir()
	stateFile := filepath.Join(t.TempDir(), "start-once")
	t.Setenv("AIMA_TEST_STATE_FILE", stateFile)

	binPath := filepath.Join(binDir, "zeroclaw")
	script := `#!/bin/sh
if [ "$1" != "daemon" ]; then
  echo "bad subcommand: $1" >&2
  exit 1
fi
state="${AIMA_TEST_STATE_FILE}"
if [ ! -f "$state" ]; then
  : > "$state"
  echo "transient failure" >&2
  exit 1
fi
shift
host="127.0.0.1"
port=""
while [ $# -gt 0 ]; do
  case "$1" in
    --config-dir)
      shift 2
      ;;
    -p)
      port="$2"
      shift 2
      ;;
    --host)
      host="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
exec python3 - "$host" "$port" <<'PY'
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

host = sys.argv[1]
port = int(sys.argv[2])

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/health":
            self.send_response(404)
            self.end_headers()
            return
        body = json.dumps({"status": "ok"}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args, **kwargs):
        pass

HTTPServer((host, port), Handler).serve_forever()
PY
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake zeroclaw: %v", err)
	}

	m := NewManager(WithBinaryPath(binPath), WithDataDir(dataDir))
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		if err := m.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	}()
	if !m.Health() {
		t.Fatal("Health() = false, want true after retry startup")
	}
}

func TestAsk_FallsBackToOneShotCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script helper not portable on windows")
	}

	binDir := t.TempDir()
	dataDir := t.TempDir()
	binPath := filepath.Join(binDir, "zeroclaw")
	script := `#!/bin/sh
if [ "$1" != "agent" ]; then
  echo "bad subcommand: $1" >&2
  exit 1
fi
shift
if [ "$1" = "--config-dir" ]; then
  shift 2
fi
if [ "$1" != "-m" ]; then
  echo "bad message flag: $1" >&2
  exit 1
fi
shift
echo "reply:$1"
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake zeroclaw: %v", err)
	}

	m := NewManager(WithBinaryPath(binPath), WithDataDir(dataDir))
	got, err := m.Ask(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got != "reply:hello" {
		t.Fatalf("Ask = %q, want reply:hello", got)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "zeroclaw")); err != nil {
		t.Fatalf("config dir not created: %v", err)
	}
}

func TestAsk_FallsBackToOneShotCLI_WithManagedConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script helper not portable on windows")
	}

	binDir := t.TempDir()
	dataDir := t.TempDir()
	binPath := filepath.Join(binDir, "zeroclaw")
	script := `#!/bin/sh
if [ "$1" != "agent" ]; then
  echo "bad subcommand: $1" >&2
  exit 1
fi
shift
if [ "$1" = "--config-dir" ]; then
  shift 2
fi
if [ "$1" != "-p" ] || [ "$2" != "openai" ]; then
  echo "missing provider: $1 $2" >&2
  exit 1
fi
shift 2
if [ "$1" != "--model" ] || [ "$2" != "local-model" ]; then
  echo "missing model: $1 $2" >&2
  exit 1
fi
shift 2
if [ "$1" != "-m" ]; then
  echo "bad message flag: $1" >&2
  exit 1
fi
shift
if [ "$OPENAI_API_KEY" != "aima-local" ]; then
  echo "missing api key env" >&2
  exit 1
fi
if [ "$OPENAI_BASE_URL" != "http://127.0.0.1:6188/v1" ]; then
  echo "missing base url env" >&2
  exit 1
fi
echo "reply:$1"
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake zeroclaw: %v", err)
	}

	configDir := filepath.Join(dataDir, "zeroclaw")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	config := strings.Join([]string{
		`default_provider = "openai"`,
		`default_model = "local-model"`,
		`api_url = "http://127.0.0.1:6188/v1"`,
		`api_key = "aima-local"`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	m := NewManager(WithBinaryPath(binPath), WithDataDir(dataDir))
	got, err := m.Ask(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got != "reply:hello" {
		t.Fatalf("Ask = %q, want reply:hello", got)
	}
}

func TestPlan_FallsBackToOneShotCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script helper not portable on windows")
	}

	binDir := t.TempDir()
	dataDir := t.TempDir()
	binPath := filepath.Join(binDir, "zeroclaw")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" != \"agent\" ]; then\n" +
		"  echo \"bad subcommand: $1\" >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"shift\n" +
		"if [ \"$1\" = \"--config-dir\" ]; then\n" +
		"  shift 2\n" +
		"fi\n" +
		"if [ \"$1\" != \"-m\" ]; then\n" +
		"  echo \"bad message flag: $1\" >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"printf '%s\\n' '```json'\n" +
		"printf '%s\\n' '{\"kind\":\"validate\",\"goal\":\"planner goal\",\"target\":{\"model\":\"qwen3-8b\",\"engine\":\"vllm\"}}'\n" +
		"printf '%s\\n' '```'\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake zeroclaw: %v", err)
	}

	m := NewManager(WithBinaryPath(binPath), WithDataDir(dataDir))
	plan, err := m.Plan(context.Background(), json.RawMessage(`{"kind":"validate","target":{"model":"qwen3-8b"}}`))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if string(plan) != `{"kind":"validate","goal":"planner goal","target":{"model":"qwen3-8b","engine":"vllm"}}` {
		t.Fatalf("Plan = %s", string(plan))
	}
}

func TestPlan_UsesGatewayWebhook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script helper not portable on windows")
	}

	binDir := t.TempDir()
	dataDir := t.TempDir()
	binPath := filepath.Join(binDir, "zeroclaw")
	script := `#!/bin/sh
if [ "$1" != "daemon" ]; then
  echo "bad subcommand: $1" >&2
  exit 1
fi
shift
host="127.0.0.1"
port=""
while [ $# -gt 0 ]; do
  case "$1" in
    --config-dir)
      shift 2
      ;;
    -p)
      port="$2"
      shift 2
      ;;
    --host)
      host="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
exec python3 - "$host" "$port" <<'PY'
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

host = sys.argv[1]
port = int(sys.argv[2])

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/health":
            self.send_response(404)
            self.end_headers()
            return
        body = json.dumps({"status": "ok"}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        if self.path != "/webhook":
            self.send_response(404)
            self.end_headers()
            return
        length = int(self.headers.get("Content-Length", "0"))
        payload = json.loads(self.rfile.read(length) or b"{}")
        msg = payload.get("message", "")
        if "ExplorationPlan" in msg:
            response = {"response": "{\"kind\":\"validate\",\"goal\":\"planner goal\",\"target\":{\"model\":\"qwen3-8b\",\"engine\":\"vllm\"}}"}
        else:
            response = {"response": "reply:webhook"}
        body = json.dumps(response).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args, **kwargs):
        pass

HTTPServer((host, port), Handler).serve_forever()
PY
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake zeroclaw: %v", err)
	}

	m := NewManager(WithBinaryPath(binPath), WithDataDir(dataDir))
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		if err := m.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	}()

	reply, err := m.Ask(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply != "reply:webhook" {
		t.Fatalf("Ask = %q, want reply:webhook", reply)
	}

	plan, err := m.Plan(context.Background(), json.RawMessage(`{"kind":"validate","target":{"model":"qwen3-8b"}}`))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if string(plan) != `{"kind":"validate","goal":"planner goal","target":{"model":"qwen3-8b","engine":"vllm"}}` {
		t.Fatalf("Plan = %s", string(plan))
	}
}

func fakeReleaseArchive(t *testing.T, binary []byte) []byte {
	t.Helper()

	if runtime.GOOS == "windows" {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		f, err := zw.Create(executableName())
		if err != nil {
			t.Fatalf("create zip entry: %v", err)
		}
		if _, err := f.Write(binary); err != nil {
			t.Fatalf("write zip entry: %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("close zip archive: %v", err)
		}
		return buf.Bytes()
	}

	var tarBuf bytes.Buffer
	gw := gzip.NewWriter(&tarBuf)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{
		Name: executableName(),
		Mode: 0o755,
		Size: int64(len(binary)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(binary); err != nil {
		t.Fatalf("write tar body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar archive: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip archive: %v", err)
	}
	return tarBuf.Bytes()
}

func fakeBinaryPayload(size int) []byte {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		for i := range buf {
			buf[i] = byte((i*37 + 11) % 251)
		}
	}
	return buf
}
