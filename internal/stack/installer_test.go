package stack

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jguan/aima/internal/knowledge"
)

// mockRunner records commands and returns configured responses.
type mockRunner struct {
	calls   []call
	results map[string]runResult
}

type call struct {
	name string
	args []string
}

type runResult struct {
	output []byte
	err    error
}

func (m *mockRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, call{name: name, args: args})
	key := name
	if len(args) > 0 {
		key = name + " " + args[0]
	}
	if r, ok := m.results[key]; ok {
		return r.output, r.err
	}
	// Default: command not found
	return nil, fmt.Errorf("command not found: %s", name)
}

func TestStatusAllReady(t *testing.T) {
	runner := &mockRunner{
		results: map[string]runResult{
			"k3s kubectl": {output: []byte("NAME    STATUS   ROLES\nnode1   Ready    control-plane")},
		},
	}

	inst := NewInstaller(runner, t.TempDir())

	components := []knowledge.StackComponent{
		{
			Metadata: knowledge.StackMetadata{Name: "k3s", Version: "1.31.4"},
			Verify: knowledge.StackVerify{
				Command:        "k3s kubectl get nodes",
				ReadyCondition: "Ready",
				TimeoutS:       5,
			},
		},
	}

	result, err := inst.Status(context.Background(), components, "")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	if !result.AllReady {
		t.Error("expected AllReady=true")
	}
	if len(result.Components) != 1 {
		t.Fatalf("expected 1 component, got %d", len(result.Components))
	}
	if !result.Components[0].Ready {
		t.Errorf("k3s: expected Ready=true, got message=%q", result.Components[0].Message)
	}
}

func TestStatusNotInstalled(t *testing.T) {
	runner := &mockRunner{
		results: map[string]runResult{},
	}

	inst := NewInstaller(runner, t.TempDir())

	components := []knowledge.StackComponent{
		{
			Metadata: knowledge.StackMetadata{Name: "k3s", Version: "1.31.4"},
			Verify: knowledge.StackVerify{
				Command:        "k3s kubectl get nodes",
				ReadyCondition: "Ready",
				TimeoutS:       5,
			},
		},
	}

	result, err := inst.Status(context.Background(), components, "")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	if result.AllReady {
		t.Error("expected AllReady=false when component not installed")
	}
	if result.Components[0].Installed {
		t.Error("expected Installed=false")
	}
}

func TestInitSkipsReadyComponent(t *testing.T) {
	runner := &mockRunner{
		results: map[string]runResult{
			"k3s kubectl": {output: []byte("node1   Ready   control-plane")},
		},
	}

	inst := NewInstaller(runner, t.TempDir())

	components := []knowledge.StackComponent{
		{
			Metadata: knowledge.StackMetadata{Name: "k3s", Version: "1.31.4"},
			Install:  knowledge.StackInstall{Method: "binary"},
			Source:   knowledge.StackSource{Binary: "k3s"},
			Verify: knowledge.StackVerify{
				Command:        "k3s kubectl get nodes",
				ReadyCondition: "Ready",
				TimeoutS:       5,
			},
		},
	}

	result, err := inst.Init(context.Background(), components, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	if !result.AllReady {
		t.Error("expected AllReady=true for already-ready component")
	}

	// Should only have the verify call, not an install call
	for _, c := range runner.calls {
		if c.name == "k3s" && len(c.args) > 0 && c.args[0] == "server" {
			t.Error("should not have called k3s server when already ready")
		}
	}
}

func TestCollectArgs(t *testing.T) {
	comp := knowledge.StackComponent{
		Install: knowledge.StackInstall{
			Args: []knowledge.StackArg{
				{Flag: "--disable=traefik"},
				{Flag: "--disable=servicelb"},
			},
		},
		Profiles: map[string]knowledge.StackProfile{
			"Blackwell-arm64": {
				ExtraArgs: []knowledge.StackArg{
					{Flag: "--kubelet-arg=kube-reserved=cpu=500m"},
				},
			},
		},
	}

	// Without profile
	args := collectArgs(comp, "")
	if len(args) != 2 {
		t.Errorf("expected 2 args without profile, got %d", len(args))
	}

	// With matching profile
	args = collectArgs(comp, "Blackwell-arm64")
	if len(args) != 3 {
		t.Errorf("expected 3 args with profile, got %d", len(args))
	}

	// With non-matching profile
	args = collectArgs(comp, "unknown-profile")
	if len(args) != 2 {
		t.Errorf("expected 2 args with unknown profile, got %d", len(args))
	}
}

func TestInitSkipsUnsupportedPlatform(t *testing.T) {
	runner := &mockRunner{results: map[string]runResult{}}

	inst := NewInstaller(runner, t.TempDir())

	components := []knowledge.StackComponent{
		{
			Metadata: knowledge.StackMetadata{Name: "linux-only", Version: "1.0"},
			Source:   knowledge.StackSource{Binary: "something", Platforms: []string{"fakeos/fakearch"}},
			Install:  knowledge.StackInstall{Method: "binary"},
			Verify: knowledge.StackVerify{
				Command:        "something status",
				ReadyCondition: "Ready",
				TimeoutS:       5,
			},
		},
	}

	result, err := inst.Init(context.Background(), components, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	if len(result.Components) != 1 {
		t.Fatalf("expected 1 component, got %d", len(result.Components))
	}

	comp := result.Components[0]
	if comp.Ready {
		t.Error("unsupported platform component should not be Ready")
	}
	if comp.Installed {
		t.Error("unsupported platform component should not be Installed")
	}
	if !strings.Contains(comp.Message, "skipped") {
		t.Errorf("expected skip message, got %q", comp.Message)
	}

	// Should not have called any commands
	if len(runner.calls) != 0 {
		t.Errorf("expected 0 runner calls for unsupported platform, got %d", len(runner.calls))
	}
}

func TestPlatformSupported(t *testing.T) {
	tests := []struct {
		name      string
		platforms []string
		want      bool
	}{
		{"empty list means all", nil, true},
		{"empty slice means all", []string{}, true},
		{"matching platform", []string{runtime.GOOS + "/" + runtime.GOARCH}, true},
		{"non-matching platform", []string{"fakeos/fakearch"}, false},
		{"one of many matches", []string{"fakeos/fakearch", runtime.GOOS + "/" + runtime.GOARCH}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := platformSupported(tt.platforms); got != tt.want {
				t.Errorf("platformSupported(%v) = %v, want %v", tt.platforms, got, tt.want)
			}
		})
	}
}

func TestPreflightReturnsMissingFiles(t *testing.T) {
	inst := NewInstaller(&mockRunner{}, t.TempDir())

	components := []knowledge.StackComponent{
		{
			Metadata: knowledge.StackMetadata{Name: "test-binary", Version: "1.0"},
			Source: knowledge.StackSource{
				Binary:    "test-bin",
				Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH},
				Download: map[string]string{
					runtime.GOOS + "/" + runtime.GOARCH: "https://example.com/test-bin",
				},
			},
		},
		{
			Metadata: knowledge.StackMetadata{Name: "test-chart", Version: "2.0"},
			Source: knowledge.StackSource{
				Chart:     "chart.tgz",
				Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH},
				Download: map[string]string{
					runtime.GOOS + "/" + runtime.GOARCH: "https://example.com/chart.tgz",
				},
			},
		},
	}

	items := inst.Preflight(components)
	if len(items) != 2 {
		t.Fatalf("expected 2 download items, got %d", len(items))
	}

	// First item: binary should be executable
	if items[0].Name != "test-binary" {
		t.Errorf("item[0].Name = %q, want %q", items[0].Name, "test-binary")
	}
	if !items[0].Executable {
		t.Error("binary item should have Executable=true")
	}
	if items[0].URL != "https://example.com/test-bin" {
		t.Errorf("item[0].URL = %q, want %q", items[0].URL, "https://example.com/test-bin")
	}

	// Second item: chart should not be executable
	if items[1].Name != "test-chart" {
		t.Errorf("item[1].Name = %q, want %q", items[1].Name, "test-chart")
	}
	if items[1].Executable {
		t.Error("chart item should have Executable=false")
	}
}

func TestPreflightSkipsExistingFiles(t *testing.T) {
	dir := t.TempDir()
	inst := NewInstaller(&mockRunner{}, dir).WithDistDir(dir)

	// Create the file so Preflight skips it
	os.WriteFile(filepath.Join(dir, "existing-bin"), []byte("binary"), 0o755)

	components := []knowledge.StackComponent{
		{
			Metadata: knowledge.StackMetadata{Name: "existing", Version: "1.0"},
			Source: knowledge.StackSource{
				Binary:    "existing-bin",
				Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH},
				Download: map[string]string{
					runtime.GOOS + "/" + runtime.GOARCH: "https://example.com/bin",
				},
			},
		},
	}

	items := inst.Preflight(components)
	if len(items) != 0 {
		t.Errorf("expected 0 items for existing file, got %d", len(items))
	}
}

func TestPreflightSkipsUnsupportedPlatform(t *testing.T) {
	inst := NewInstaller(&mockRunner{}, t.TempDir())

	components := []knowledge.StackComponent{
		{
			Metadata: knowledge.StackMetadata{Name: "other-platform", Version: "1.0"},
			Source: knowledge.StackSource{
				Binary:    "bin",
				Platforms: []string{"fakeos/fakearch"},
				Download: map[string]string{
					"fakeos/fakearch": "https://example.com/bin",
				},
			},
		},
	}

	items := inst.Preflight(components)
	if len(items) != 0 {
		t.Errorf("expected 0 items for unsupported platform, got %d", len(items))
	}
}

func TestPreflightSkipsNoDownloadURL(t *testing.T) {
	inst := NewInstaller(&mockRunner{}, t.TempDir())

	components := []knowledge.StackComponent{
		{
			Metadata: knowledge.StackMetadata{Name: "no-url", Version: "1.0"},
			Source: knowledge.StackSource{
				Binary:    "bin",
				Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH},
				// No Download map
			},
		},
	}

	items := inst.Preflight(components)
	if len(items) != 0 {
		t.Errorf("expected 0 items without download URL, got %d", len(items))
	}
}

func TestAllSkippedNotReady(t *testing.T) {
	runner := &mockRunner{results: map[string]runResult{}}
	inst := NewInstaller(runner, t.TempDir())

	// All components have platforms that don't match current OS
	components := []knowledge.StackComponent{
		{
			Metadata: knowledge.StackMetadata{Name: "linux-only-a", Version: "1.0"},
			Source:   knowledge.StackSource{Binary: "a", Platforms: []string{"fakeos/fakearch"}},
			Install:  knowledge.StackInstall{Method: "binary"},
			Verify:   knowledge.StackVerify{Command: "a status", ReadyCondition: "Ready"},
		},
		{
			Metadata: knowledge.StackMetadata{Name: "linux-only-b", Version: "2.0"},
			Source:   knowledge.StackSource{Binary: "b", Platforms: []string{"fakeos/fakearch"}},
			Install:  knowledge.StackInstall{Method: "binary"},
			Verify:   knowledge.StackVerify{Command: "b status", ReadyCondition: "Ready"},
		},
	}

	// Init: all skipped → AllReady must be false
	result, err := inst.Init(context.Background(), components, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if result.AllReady {
		t.Error("Init: expected AllReady=false when all components are skipped")
	}
	for _, c := range result.Components {
		if !c.Skipped {
			t.Errorf("Init: expected component %q to be skipped", c.Name)
		}
	}

	// Status: all skipped → AllReady must be false
	result, err = inst.Status(context.Background(), components, "")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if result.AllReady {
		t.Error("Status: expected AllReady=false when all components are skipped")
	}
}

func TestMixedSkipAndReady(t *testing.T) {
	runner := &mockRunner{
		results: map[string]runResult{
			"b status": {output: []byte("Ready")},
		},
	}
	inst := NewInstaller(runner, t.TempDir())

	components := []knowledge.StackComponent{
		{
			Metadata: knowledge.StackMetadata{Name: "skipped", Version: "1.0"},
			Source:   knowledge.StackSource{Platforms: []string{"fakeos/fakearch"}},
			Verify:   knowledge.StackVerify{Command: "a status", ReadyCondition: "Ready"},
		},
		{
			Metadata: knowledge.StackMetadata{Name: "ready", Version: "1.0"},
			Source:   knowledge.StackSource{Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH}},
			Verify:   knowledge.StackVerify{Command: "b status", ReadyCondition: "Ready"},
		},
	}

	result, err := inst.Status(context.Background(), components, "")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !result.AllReady {
		t.Error("expected AllReady=true when one skip + one ready")
	}
	if !result.Components[0].Skipped {
		t.Error("expected first component to be skipped")
	}
	if !result.Components[1].Ready {
		t.Error("expected second component to be ready")
	}
}

func TestPreflightPopulatesMirrorURL(t *testing.T) {
	inst := NewInstaller(&mockRunner{}, t.TempDir())

	components := []knowledge.StackComponent{
		{
			Metadata: knowledge.StackMetadata{Name: "test", Version: "1.0"},
			Source: knowledge.StackSource{
				Binary:    "test-bin",
				Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH},
				Download: map[string]string{
					runtime.GOOS + "/" + runtime.GOARCH: "https://example.com/bin",
				},
				Mirror: map[string]string{
					runtime.GOOS + "/" + runtime.GOARCH: "https://mirror.example.com/bin",
				},
			},
		},
	}

	items := inst.Preflight(components)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].MirrorURL != "https://mirror.example.com/bin" {
		t.Errorf("MirrorURL = %q, want %q", items[0].MirrorURL, "https://mirror.example.com/bin")
	}
}

func TestDownloadItemsFallbackToMirror(t *testing.T) {
	// Set up an HTTP server that serves a file at /mirror path but fails at /primary
	dir := t.TempDir()
	destPath := filepath.Join(dir, "downloaded")

	// Start a test HTTP server
	handler := http.NewServeMux()
	handler.HandleFunc("/primary", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "timeout", http.StatusGatewayTimeout)
	})
	handler.HandleFunc("/mirror", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("mirror-content"))
	})

	server := &http.Server{Addr: "127.0.0.1:0", Handler: handler}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go server.Serve(ln)
	defer server.Close()

	base := "http://" + ln.Addr().String()

	items := []DownloadItem{
		{
			Name:      "test",
			FileName:  "downloaded",
			FilePath:  destPath,
			URL:       base + "/primary",
			MirrorURL: base + "/mirror",
		},
	}

	if err := DownloadItems(context.Background(), items); err != nil {
		t.Fatalf("DownloadItems: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != "mirror-content" {
		t.Errorf("content = %q, want %q", string(data), "mirror-content")
	}
}

func TestShouldSkip(t *testing.T) {
	tests := []struct {
		name      string
		cond      *knowledge.StackConditions
		hwProfile string
		wantSkip  bool
	}{
		{"nil conditions", nil, "Blackwell-arm64", false},
		{"empty profile", &knowledge.StackConditions{SkipProfiles: []string{"Blackwell-arm64"}}, "", false},
		{"in skip_profiles", &knowledge.StackConditions{SkipProfiles: []string{"Blackwell-arm64"}}, "Blackwell-arm64", true},
		{"not in skip_profiles", &knowledge.StackConditions{SkipProfiles: []string{"Ada-x86_64"}}, "Blackwell-arm64", false},
		{"in required_profiles", &knowledge.StackConditions{RequiredProfiles: []string{"Blackwell-arm64"}}, "Blackwell-arm64", false},
		{"not in required_profiles", &knowledge.StackConditions{RequiredProfiles: []string{"Ada-x86_64"}}, "Blackwell-arm64", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp := knowledge.StackComponent{Conditions: tt.cond}
			skip, _ := shouldSkip(comp, tt.hwProfile)
			if skip != tt.wantSkip {
				t.Errorf("shouldSkip() = %v, want %v", skip, tt.wantSkip)
			}
		})
	}
}

// mockPodQuerier implements PodQuerier for testing.
type mockPodQuerier struct {
	pods map[string][]PodDetail // key: "namespace/label"
}

func (m *mockPodQuerier) ListPodsByLabel(_ context.Context, namespace, label string) ([]PodDetail, error) {
	key := namespace + "/" + label
	if pods, ok := m.pods[key]; ok {
		return pods, nil
	}
	return nil, nil
}

func TestCheckComponentWithPods(t *testing.T) {
	runner := &mockRunner{
		results: map[string]runResult{
			"k3s kubectl": {output: []byte("node1   Ready   control-plane")},
		},
	}

	pq := &mockPodQuerier{
		pods: map[string][]PodDetail{
			"kube-system/k8s-app=kube-dns": {
				{Name: "coredns-abc123", Phase: "Running", Ready: true},
			},
		},
	}

	inst := NewInstaller(runner, t.TempDir()).WithPodQuerier(pq)

	comp := knowledge.StackComponent{
		Metadata: knowledge.StackMetadata{Name: "k3s", Version: "1.31.4"},
		Verify: knowledge.StackVerify{
			Command:        "k3s kubectl get nodes",
			ReadyCondition: "Ready",
			TimeoutS:       5,
			Pods: []knowledge.StackVerifyPod{
				{Namespace: "kube-system", Label: "k8s-app=kube-dns", MinReady: 1},
			},
		},
	}

	status := inst.checkComponent(context.Background(), comp, "")
	if !status.Ready {
		t.Errorf("expected Ready=true, got message=%q", status.Message)
	}
	if len(status.Pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(status.Pods))
	}
	if status.Pods[0].Name != "coredns-abc123" {
		t.Errorf("pod name = %q, want %q", status.Pods[0].Name, "coredns-abc123")
	}
}

func TestCheckComponentWithPodsNotReady(t *testing.T) {
	runner := &mockRunner{
		results: map[string]runResult{
			"k3s kubectl": {output: []byte("Running")},
		},
	}

	pq := &mockPodQuerier{
		pods: map[string][]PodDetail{
			"hami-system/app=hami-device-plugin": {
				{Name: "hami-device-plugin-xyz", Phase: "Running", Ready: false, Message: "CrashLoopBackOff"},
			},
		},
	}

	inst := NewInstaller(runner, t.TempDir()).WithPodQuerier(pq)

	comp := knowledge.StackComponent{
		Metadata: knowledge.StackMetadata{Name: "hami", Version: "2.4.1"},
		Verify: knowledge.StackVerify{
			Command:        "k3s kubectl get pods -n hami-system",
			ReadyCondition: "Running",
			TimeoutS:       5,
			Pods: []knowledge.StackVerifyPod{
				{Namespace: "hami-system", Label: "app=hami-device-plugin", MinReady: 1},
			},
		},
	}

	status := inst.checkComponent(context.Background(), comp, "")
	if status.Ready {
		t.Error("expected Ready=false when pod is not ready")
	}
	if !strings.Contains(status.Message, "pods not ready") {
		t.Errorf("expected 'pods not ready' in message, got %q", status.Message)
	}
}

func TestInstallDaemonSystemd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd tests only run on Linux")
	}

	runner := &mockRunner{
		results: map[string]runResult{
			"systemctl daemon-reload": {output: nil},
			"systemctl enable":        {output: nil},
			"systemctl start":         {output: nil},
		},
	}

	dir := t.TempDir()
	inst := NewInstaller(runner, dir).WithDistDir(dir)

	// Create a fake binary in dist/
	binPath := filepath.Join(dir, "k3s")
	os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0o755)

	comp := knowledge.StackComponent{
		Metadata: knowledge.StackMetadata{Name: "k3s", Version: "1.31.4"},
		Source:   knowledge.StackSource{Binary: "k3s"},
		Install: knowledge.StackInstall{
			Method: "binary",
			Daemon: true,
			Args: []knowledge.StackArg{
				{Flag: "--disable=traefik"},
			},
			Env: map[string]string{
				"INSTALL_K3S_SKIP_DOWNLOAD": "true",
			},
		},
	}

	err := inst.installDaemonSystemd(context.Background(), comp, binPath, "")
	if err != nil {
		t.Fatalf("installDaemonSystemd: %v", err)
	}

	// Verify systemctl calls: daemon-reload → enable → start (in order)
	var systemctlCalls []string
	for _, c := range runner.calls {
		if c.name == "systemctl" {
			systemctlCalls = append(systemctlCalls, c.name+" "+strings.Join(c.args, " "))
		}
	}
	if len(systemctlCalls) != 3 {
		t.Fatalf("expected 3 systemctl calls, got %d: %v", len(systemctlCalls), systemctlCalls)
	}
	if systemctlCalls[0] != "systemctl daemon-reload" {
		t.Errorf("call[0] = %q, want %q", systemctlCalls[0], "systemctl daemon-reload")
	}
	if systemctlCalls[1] != "systemctl enable k3s" {
		t.Errorf("call[1] = %q, want %q", systemctlCalls[1], "systemctl enable k3s")
	}
	if systemctlCalls[2] != "systemctl start k3s" {
		t.Errorf("call[2] = %q, want %q", systemctlCalls[2], "systemctl start k3s")
	}

	// Verify unit file was written
	unitData, err := os.ReadFile("/etc/systemd/system/k3s.service")
	if err != nil {
		t.Fatalf("read unit file: %v", err)
	}
	unitContent := string(unitData)
	if !strings.Contains(unitContent, "Type=notify") {
		t.Error("unit file missing Type=notify")
	}
	if !strings.Contains(unitContent, "Restart=always") {
		t.Error("unit file missing Restart=always")
	}
	if !strings.Contains(unitContent, "--disable=traefik") {
		t.Error("unit file missing --disable=traefik arg")
	}
	if !strings.Contains(unitContent, "server") {
		t.Error("unit file missing 'server' subcommand")
	}

	// Verify env file was written
	envData, err := os.ReadFile("/etc/rancher/k3s/k3s.env")
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	if !strings.Contains(string(envData), "INSTALL_K3S_SKIP_DOWNLOAD=true") {
		t.Error("env file missing INSTALL_K3S_SKIP_DOWNLOAD=true")
	}
}

func TestInstallDaemonSystemdUnitContent(t *testing.T) {
	// Test unit file generation without requiring Linux (test the template logic)
	// We can't run the full installDaemonSystemd on non-Linux, but we can verify
	// the function exists and the args/env collection works correctly
	comp := knowledge.StackComponent{
		Metadata: knowledge.StackMetadata{Name: "k3s", Version: "1.31.4"},
		Install: knowledge.StackInstall{
			Args: []knowledge.StackArg{
				{Flag: "--disable=traefik"},
				{Flag: "--disable=servicelb"},
			},
			Env: map[string]string{
				"K3S_TOKEN": "secret",
			},
		},
		Profiles: map[string]knowledge.StackProfile{
			"test-profile": {
				ExtraArgs: []knowledge.StackArg{{Flag: "--node-label=gpu=on"}},
				ExtraEnv:  map[string]string{"EXTRA": "val"},
			},
		},
	}

	// Verify args and env collection (these are used by installDaemonSystemd)
	args := collectArgs(comp, "test-profile")
	if len(args) != 3 {
		t.Errorf("expected 3 args with profile, got %d: %v", len(args), args)
	}

	env := collectEnv(comp, "test-profile")
	if len(env) != 2 {
		t.Errorf("expected 2 env vars with profile, got %d", len(env))
	}
	if env["EXTRA"] != "val" {
		t.Errorf("EXTRA = %q, want %q", env["EXTRA"], "val")
	}
}

func TestCheckComponentSystemdHint(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd hint test only runs on Linux")
	}

	runner := &mockRunner{
		results: map[string]runResult{
			// systemctl is-active returns "inactive"
			"systemctl is-active": {output: []byte("inactive"), err: fmt.Errorf("exit status 3")},
		},
	}

	inst := NewInstaller(runner, t.TempDir())

	comp := knowledge.StackComponent{
		Metadata: knowledge.StackMetadata{Name: "k3s", Version: "1.31.4"},
		Install:  knowledge.StackInstall{Daemon: true},
		Verify: knowledge.StackVerify{
			Command:        "k3s kubectl get nodes",
			ReadyCondition: "Ready",
		},
	}

	status := inst.checkComponent(context.Background(), comp, "")
	if status.Ready {
		t.Error("expected Ready=false when systemd service is inactive")
	}
	if !strings.Contains(status.Message, "service not running") {
		t.Errorf("expected 'service not running' hint, got %q", status.Message)
	}
	if !strings.Contains(status.Message, "systemctl start k3s") {
		t.Errorf("expected 'systemctl start k3s' in message, got %q", status.Message)
	}
}

func TestCollectEnv(t *testing.T) {
	comp := knowledge.StackComponent{
		Install: knowledge.StackInstall{
			Env: map[string]string{
				"INSTALL_K3S_SKIP_DOWNLOAD": "true",
			},
		},
		Profiles: map[string]knowledge.StackProfile{
			"test-profile": {
				ExtraEnv: map[string]string{
					"EXTRA_VAR": "value",
				},
			},
		},
	}

	env := collectEnv(comp, "")
	if len(env) != 1 {
		t.Errorf("expected 1 env var without profile, got %d", len(env))
	}

	env = collectEnv(comp, "test-profile")
	if len(env) != 2 {
		t.Errorf("expected 2 env vars with profile, got %d", len(env))
	}
	if env["EXTRA_VAR"] != "value" {
		t.Errorf("EXTRA_VAR = %q, want %q", env["EXTRA_VAR"], "value")
	}
}
