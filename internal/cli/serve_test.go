package cli

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/proxy"
)

func TestServeBackgroundStartsOnceAndReceivesCancelledContext(t *testing.T) {
	t.Setenv("AIMA_API_KEY", "")
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	stopped := make(chan struct{})
	var calls int32
	app := &App{
		Proxy: proxy.NewServer(),
		ServeBackground: func(ctx context.Context) {
			if atomic.AddInt32(&calls, 1) == 1 {
				close(started)
			}
			<-ctx.Done()
			close(stopped)
		},
	}
	cmd := newServeCmd(app)
	cmd.SetArgs([]string{"--addr=127.0.0.1:0", "--mdns=false"})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("serve background hook did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve command: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve command did not stop after cancellation")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("serve background context was not cancelled")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("serve background calls = %d, want 1", got)
	}
}

func TestServeBackgroundDoesNotStartForInvalidConfiguration(t *testing.T) {
	t.Setenv("AIMA_API_KEY", "")
	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "insecure listen", args: []string{"--addr=0.0.0.0:6188", "--mdns=false"}},
		{name: "invalid static backend", args: []string{"--addr=127.0.0.1:0", "--mdns=false", "--backend=invalid"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls int32
			app := &App{
				Proxy: proxy.NewServer(),
				ServeBackground: func(context.Context) {
					atomic.AddInt32(&calls, 1)
				},
			}
			cmd := newServeCmd(app)
			cmd.SetArgs(tt.args)
			if err := cmd.ExecuteContext(context.Background()); err == nil {
				t.Fatal("serve error = nil, want validation failure")
			}
			if got := atomic.LoadInt32(&calls); got != 0 {
				t.Fatalf("serve background calls = %d, want zero", got)
			}
		})
	}
}

func TestIsLoopbackListenAddr(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{name: "ipv4 loopback", addr: "127.0.0.1:6188", want: true},
		{name: "localhost", addr: "localhost:6188", want: true},
		{name: "ipv6 loopback", addr: "[::1]:6188", want: true},
		{name: "all interfaces shorthand", addr: ":6188", want: false},
		{name: "all interfaces ipv4", addr: "0.0.0.0:6188", want: false},
		{name: "lan address", addr: "192.168.1.2:6188", want: false},
		{name: "invalid", addr: "bad-addr", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLoopbackListenAddr(tt.addr); got != tt.want {
				t.Fatalf("isLoopbackListenAddr(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestValidateServeSecurity(t *testing.T) {
	tests := []struct {
		name          string
		addr          string
		mcpAddr       string
		mcpEnabled    bool
		apiKey        string
		allowInsecure bool
		wantErr       bool
	}{
		{
			name:       "loopback without key allowed",
			addr:       "127.0.0.1:6188",
			mcpAddr:    "127.0.0.1:9090",
			mcpEnabled: false,
			wantErr:    false,
		},
		{
			name:       "wildcard without key rejected",
			addr:       ":6188",
			mcpAddr:    "127.0.0.1:9090",
			mcpEnabled: false,
			wantErr:    true,
		},
		{
			name:       "wildcard with key allowed",
			addr:       ":6188",
			mcpAddr:    "127.0.0.1:9090",
			mcpEnabled: false,
			apiKey:     "secret",
			wantErr:    false,
		},
		{
			name:          "wildcard without key allowed by flag",
			addr:          ":6188",
			mcpAddr:       "127.0.0.1:9090",
			mcpEnabled:    false,
			allowInsecure: true,
			wantErr:       false,
		},
		{
			name:       "mcp non-loopback without key rejected",
			addr:       "127.0.0.1:6188",
			mcpAddr:    "0.0.0.0:9090",
			mcpEnabled: true,
			wantErr:    true,
		},
		{
			name:       "mcp non-loopback with key allowed",
			addr:       "127.0.0.1:6188",
			mcpAddr:    "0.0.0.0:9090",
			mcpEnabled: true,
			apiKey:     "secret",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateServeSecurity(tt.addr, tt.mcpAddr, tt.mcpEnabled, tt.apiKey, tt.allowInsecure)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateServeSecurity() error = %v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestResolveMCPProfile(t *testing.T) {
	tests := []struct {
		name       string
		mcpEnabled bool
		profile    string
		want       mcp.Profile
		wantErr    bool
	}{
		{name: "empty profile", mcpEnabled: false, profile: "", want: mcp.ProfileFull},
		{name: "valid profile", mcpEnabled: true, profile: "operator", want: mcp.ProfileOperator},
		{name: "requires mcp", mcpEnabled: false, profile: "operator", wantErr: true},
		{name: "invalid profile", mcpEnabled: true, profile: "bad", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveMCPProfile(tt.mcpEnabled, tt.profile)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveMCPProfile() error = %v, wantErr=%v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("resolveMCPProfile() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseStaticBackendSpec(t *testing.T) {
	model, backend, err := parseStaticBackendSpec("qwen3.6=http://127.0.0.1:18310/v1,engine=vllm,upstream=qwen3.6-served,param=35B,context=32768")
	if err != nil {
		t.Fatalf("parseStaticBackendSpec() error = %v", err)
	}
	if model != "qwen3.6" {
		t.Fatalf("model = %q, want qwen3.6", model)
	}
	if backend.ModelName != "qwen3.6" {
		t.Fatalf("backend.ModelName = %q, want qwen3.6", backend.ModelName)
	}
	if backend.Address != "127.0.0.1:18310" {
		t.Fatalf("backend.Address = %q, want 127.0.0.1:18310", backend.Address)
	}
	if backend.BasePath != "/v1" {
		t.Fatalf("backend.BasePath = %q, want /v1", backend.BasePath)
	}
	if backend.EngineType != "vllm" {
		t.Fatalf("backend.EngineType = %q, want vllm", backend.EngineType)
	}
	if backend.UpstreamModel != "qwen3.6-served" {
		t.Fatalf("backend.UpstreamModel = %q, want qwen3.6-served", backend.UpstreamModel)
	}
	if !backend.Ready {
		t.Fatal("backend.Ready = false, want true")
	}
	if !backend.Remote {
		t.Fatal("backend.Remote = false, want true")
	}
	if backend.ParameterCount != "35B" {
		t.Fatalf("backend.ParameterCount = %q, want 35B", backend.ParameterCount)
	}
	if backend.ContextWindowTokens != 32768 {
		t.Fatalf("backend.ContextWindowTokens = %d, want 32768", backend.ContextWindowTokens)
	}
}

func TestParseStaticBackendSpecRejectsMissingAddress(t *testing.T) {
	if _, _, err := parseStaticBackendSpec("qwen3.6,engine=vllm"); err == nil {
		t.Fatal("parseStaticBackendSpec() error = nil, want error")
	}
}

func TestBackendFlagPreservesCommaOptions(t *testing.T) {
	cmd := newServeCmd(&App{})
	flag := cmd.Flags().Lookup("backend")
	if flag == nil {
		t.Fatal("backend flag not registered")
	}
	if got := flag.Value.Type(); got != "stringArray" {
		t.Fatalf("backend flag type = %q, want stringArray", got)
	}
}

func TestRunStartupAssetReconcileScansEnginesAndModels(t *testing.T) {
	t.Parallel()

	var engineCalls int
	var modelCalls int
	var externalCalls int
	var gotRuntime string
	var gotAutoImport bool

	deps := &mcp.ToolDeps{
		ScanEngines: func(ctx context.Context, runtime string, autoImport bool) (json.RawMessage, error) {
			engineCalls++
			gotRuntime = runtime
			gotAutoImport = autoImport
			return json.RawMessage(`[]`), nil
		},
		ScanModels: func(ctx context.Context) (json.RawMessage, error) {
			modelCalls++
			return json.RawMessage(`[]`), nil
		},
		ScanExternalServices: func(ctx context.Context) (json.RawMessage, error) {
			externalCalls++
			return json.RawMessage(`[]`), nil
		},
	}

	runStartupAssetReconcile(context.Background(), deps)

	if engineCalls != 1 {
		t.Fatalf("ScanEngines call count = %d, want 1", engineCalls)
	}
	if gotRuntime != "auto" || gotAutoImport {
		t.Fatalf("ScanEngines(%q, %v), want (auto, false)", gotRuntime, gotAutoImport)
	}
	if modelCalls != 1 {
		t.Fatalf("ScanModels call count = %d, want 1", modelCalls)
	}
	if externalCalls != 1 {
		t.Fatalf("ScanExternalServices call count = %d, want 1", externalCalls)
	}
}

func TestRunStartupAssetReconcileContinuesWhenEngineScanFails(t *testing.T) {
	t.Parallel()

	var modelCalls int
	var externalCalls int
	deps := &mcp.ToolDeps{
		ScanEngines: func(ctx context.Context, runtime string, autoImport bool) (json.RawMessage, error) {
			return nil, errors.New("engine scanner unavailable")
		},
		ScanModels: func(ctx context.Context) (json.RawMessage, error) {
			modelCalls++
			return json.RawMessage(`[]`), nil
		},
		ScanExternalServices: func(ctx context.Context) (json.RawMessage, error) {
			externalCalls++
			return json.RawMessage(`[]`), nil
		},
	}

	runStartupAssetReconcile(context.Background(), deps)

	if modelCalls != 1 {
		t.Fatalf("ScanModels call count = %d, want 1", modelCalls)
	}
	if externalCalls != 1 {
		t.Fatalf("ScanExternalServices call count = %d, want 1", externalCalls)
	}
}
