package stack

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jguan/aima/internal/knowledge"
	"gopkg.in/yaml.v3"
)

// platformSupported checks if the current OS/arch is in the component's platform list.
// An empty list means all platforms are supported.
func platformSupported(platforms []string) bool {
	if len(platforms) == 0 {
		return true
	}
	current := runtime.GOOS + "/" + runtime.GOARCH
	for _, p := range platforms {
		if p == current {
			return true
		}
	}
	return false
}

// CommandRunner executes shell commands.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// PodQuerier queries pod status from K3S. Defined at consumer (stack) per project convention.
type PodQuerier interface {
	ListPodsByLabel(ctx context.Context, namespace, label string) ([]PodDetail, error)
}

// PodDetail describes a single pod's status within a stack component.
type PodDetail struct {
	Name    string `json:"name"`
	Phase   string `json:"phase"`
	Ready   bool   `json:"ready"`
	Message string `json:"message,omitempty"`
}

// ComponentStatus describes the install state of a single stack component.
type ComponentStatus struct {
	Name      string      `json:"name"`
	Version   string      `json:"version"`
	Installed bool        `json:"installed"`
	Ready     bool        `json:"ready"`
	Skipped   bool        `json:"skipped,omitempty"`
	Message   string      `json:"message,omitempty"`
	Pods      []PodDetail `json:"pods,omitempty"`
}

// InitResult is the aggregate result of aima init.
type InitResult struct {
	Components []ComponentStatus `json:"components"`
	AllReady   bool              `json:"all_ready"`
}

// Installer installs and verifies stack components.
type Installer struct {
	runner     CommandRunner
	distDir    string // path to dist/{platform}/
	podQuerier PodQuerier
}

// NewInstaller creates a stack installer.
func NewInstaller(runner CommandRunner, dataDir string) *Installer {
	platform := runtime.GOOS + "-" + runtime.GOARCH
	return &Installer{
		runner:  runner,
		distDir: filepath.Join(dataDir, "dist", platform),
	}
}

// WithDistDir overrides the dist directory (for testing).
func (inst *Installer) WithDistDir(dir string) *Installer {
	inst.distDir = dir
	return inst
}

// WithPodQuerier sets a PodQuerier for pod-level status checks.
func (inst *Installer) WithPodQuerier(pq PodQuerier) *Installer {
	inst.podQuerier = pq
	return inst
}

// shouldSkip checks if a component should be skipped based on conditions and hwProfile.
func shouldSkip(comp knowledge.StackComponent, hwProfile string) (bool, string) {
	if comp.Conditions == nil || hwProfile == "" {
		return false, ""
	}
	for _, p := range comp.Conditions.SkipProfiles {
		if p == hwProfile {
			return true, fmt.Sprintf("skipped: profile %s in skip_profiles", hwProfile)
		}
	}
	if len(comp.Conditions.RequiredProfiles) > 0 {
		for _, p := range comp.Conditions.RequiredProfiles {
			if p == hwProfile {
				return false, ""
			}
		}
		return true, fmt.Sprintf("skipped: profile %s not in required_profiles", hwProfile)
	}
	return false, ""
}

// PreCheck verifies prerequisites before downloading or installing.
// On Linux, daemon components (e.g. K3S) require root to install systemd units
// and write to /etc. This check runs early to fail fast before wasting time
// downloading large files.
func (inst *Installer) PreCheck(ctx context.Context, components []knowledge.StackComponent) error {
	if runtime.GOOS != "linux" || os.Getuid() == 0 {
		return nil
	}

	for _, comp := range components {
		if !platformSupported(comp.Source.Platforms) {
			continue
		}
		if !comp.Install.Daemon {
			continue
		}
		// If this daemon is already running, no root needed
		out, err := inst.runner.Run(ctx, "systemctl", "is-active", comp.Metadata.Name)
		if err == nil && strings.TrimSpace(string(out)) == "active" {
			continue
		}
		return fmt.Errorf("root privileges required: installing %s needs to write to /etc and /usr/local/bin\n  run: sudo aima init", comp.Metadata.Name)
	}

	return nil
}

// Init runs the full initialization workflow for all stack components.
func (inst *Installer) Init(ctx context.Context, components []knowledge.StackComponent, hwProfile string) (*InitResult, error) {
	result := &InitResult{AllReady: true}

	// Sort by install priority (lower = first) to respect dependencies
	sorted := make([]knowledge.StackComponent, len(components))
	copy(sorted, components)
	slices.SortStableFunc(sorted, func(a, b knowledge.StackComponent) int {
		return a.Install.Priority - b.Install.Priority
	})

	hasReady := false
	for _, comp := range sorted {
		status, err := inst.initComponent(ctx, comp, hwProfile)
		if err != nil {
			status = ComponentStatus{
				Name:    comp.Metadata.Name,
				Version: comp.Metadata.Version,
				Message: err.Error(),
			}
		}
		if !status.Ready && !status.Skipped {
			result.AllReady = false
		}
		if status.Ready {
			hasReady = true
		}
		result.Components = append(result.Components, status)
	}

	if !hasReady {
		result.AllReady = false
	}

	return result, nil
}

// DownloadItem describes a file that needs to be downloaded.
type DownloadItem struct {
	Name       string `json:"name"`                  // component name
	FileName   string `json:"file_name"`             // e.g. "k3s" or "hami-chart.tgz"
	FilePath   string `json:"file_path"`             // full local path in dist/
	URL        string `json:"url"`                   // download URL
	MirrorURL  string `json:"mirror_url,omitempty"`  // fallback URL (e.g. ghproxy mirror)
	Executable bool   `json:"executable,omitempty"`  // chmod +x after download
	Optional   bool   `json:"optional,omitempty"`    // if true, download failure won't abort init (e.g. airgap tars)
}

// Preflight checks which components need files downloaded.
// It returns a list of missing files that have download URLs configured,
// including airgap image tars when configured.
func (inst *Installer) Preflight(components []knowledge.StackComponent) []DownloadItem {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	var items []DownloadItem

	for _, comp := range components {
		if !platformSupported(comp.Source.Platforms) {
			continue
		}

		// Main artifact: binary or chart
		fileName := comp.Source.Binary
		if fileName == "" {
			fileName = comp.Source.Chart
		}
		if fileName != "" {
			localPath := filepath.Join(inst.distDir, fileName)
			if _, err := os.Stat(localPath); err != nil {
				if url := comp.Source.Download[platform]; url != "" {
					items = append(items, DownloadItem{
						Name:       comp.Metadata.Name,
						FileName:   fileName,
						FilePath:   localPath,
						URL:        url,
						MirrorURL:  comp.Source.Mirror[platform],
						Executable: comp.Source.Binary != "",
					})
				}
			}
		}

		// Airgap image tar (optional — init can still succeed via online pull)
		if comp.Source.Airgap != "" {
			airgapPath := filepath.Join(inst.distDir, comp.Source.Airgap)
			if _, err := os.Stat(airgapPath); err != nil {
				if url := comp.Source.AirgapDownload[platform]; url != "" {
					items = append(items, DownloadItem{
						Name:      comp.Metadata.Name + "-airgap",
						FileName:  comp.Source.Airgap,
						FilePath:  airgapPath,
						URL:       url,
						MirrorURL: comp.Source.AirgapMirror[platform],
						Optional:  true,
					})
				}
			}
		}
	}

	return items
}

// DownloadItems downloads all items in parallel, creating directories as needed.
// If a primary URL fails and a mirror URL is configured, it retries with the mirror.
// Optional items (e.g. airgap tars) log a warning on failure instead of aborting.
func DownloadItems(ctx context.Context, items []DownloadItem) error {
	if len(items) == 0 {
		return nil
	}

	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)

	for _, item := range items {
		wg.Add(1)
		go func(item DownloadItem) {
			defer wg.Done()
			slog.Info("downloading", "name", item.Name, "url", item.URL)
			err := downloadFile(ctx, item.URL, item.FilePath)
			if err != nil && item.MirrorURL != "" {
				slog.Warn("primary download failed, trying mirror", "name", item.Name, "error", err, "mirror", item.MirrorURL)
				err = downloadFile(ctx, item.MirrorURL, item.FilePath)
			}
			if err != nil {
				if item.Optional {
					slog.Warn("optional download failed, skipping", "name", item.Name, "error", err)
					return
				}
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("download %s: %w", item.Name, err)
				}
				mu.Unlock()
				return
			}
			if item.Executable {
				if err := os.Chmod(item.FilePath, 0o755); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("chmod %s: %w", item.FilePath, err)
					}
					mu.Unlock()
				}
			}
		}(item)
	}

	wg.Wait()
	return firstErr
}

// downloadFile downloads url to destPath via a .partial temp file.
func downloadFile(ctx context.Context, url, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d from %s", resp.StatusCode, url)
	}

	partial := destPath + ".partial"
	f, err := os.Create(partial)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(partial)
		return fmt.Errorf("write file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(partial)
		return fmt.Errorf("close file: %w", err)
	}

	if err := os.Rename(partial, destPath); err != nil {
		os.Remove(partial)
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// Status checks whether all stack components are installed and ready.
func (inst *Installer) Status(ctx context.Context, components []knowledge.StackComponent, hwProfile string) (*InitResult, error) {
	result := &InitResult{AllReady: true}

	hasReady := false
	for _, comp := range components {
		status := inst.checkComponent(ctx, comp, hwProfile)
		if !status.Ready && !status.Skipped {
			result.AllReady = false
		}
		if status.Ready {
			hasReady = true
		}
		result.Components = append(result.Components, status)
	}

	if !hasReady {
		result.AllReady = false
	}

	return result, nil
}

func (inst *Installer) initComponent(ctx context.Context, comp knowledge.StackComponent, hwProfile string) (ComponentStatus, error) {
	status := ComponentStatus{
		Name:    comp.Metadata.Name,
		Version: comp.Metadata.Version,
	}

	// Check platform compatibility
	if !platformSupported(comp.Source.Platforms) {
		platform := runtime.GOOS + "/" + runtime.GOARCH
		status.Skipped = true
		status.Message = fmt.Sprintf("skipped: platform %s not supported (requires %s)",
			platform, strings.Join(comp.Source.Platforms, ", "))
		slog.Info("skipping incompatible component", "name", comp.Metadata.Name, "platform", platform)
		return status, nil
	}

	// Check conditions (skip_profiles / required_profiles)
	if skip, msg := shouldSkip(comp, hwProfile); skip {
		status.Skipped = true
		status.Message = msg
		slog.Info("skipping component by conditions", "name", comp.Metadata.Name, "reason", msg)
		return status, nil
	}

	// Always write registries config if configured (K3S hot-reloads registries.yaml)
	if comp.Registries != nil {
		if err := inst.writeRegistries(comp); err != nil {
			slog.Warn("failed to write registries config", "error", err)
		}
	}

	// Ensure kubectl symlink exists for K3S binary components on Linux.
	// This must run regardless of whether K3S is already running or being freshly installed,
	// because other tools (k3s.Client, aima deploy) need "kubectl" in PATH.
	if comp.Source.Binary != "" && runtime.GOOS == "linux" {
		inst.ensureKubectlLink(comp.Source.Binary)
	}

	// Always prepare airgap images, even for already-ready components.
	// K3S may be running but klipper-helm image could be missing (needed by HAMi helm install).
	inst.prepareAirgapImages(ctx, comp)

	// Check if already installed and ready
	existing := inst.checkComponent(ctx, comp, hwProfile)
	if existing.Ready {
		slog.Info("stack component already ready", "name", comp.Metadata.Name)
		return existing, nil
	}

	// Install based on method
	slog.Info("installing stack component", "name", comp.Metadata.Name, "method", comp.Install.Method)

	switch comp.Install.Method {
	case "binary":
		if err := inst.installBinary(ctx, comp, hwProfile); err != nil {
			return status, fmt.Errorf("install %s: %w", comp.Metadata.Name, err)
		}
	case "helm":
		if err := inst.installHelm(ctx, comp, hwProfile); err != nil {
			return status, fmt.Errorf("install %s: %w", comp.Metadata.Name, err)
		}
	default:
		return status, fmt.Errorf("unknown install method %q for %s", comp.Install.Method, comp.Metadata.Name)
	}

	status.Installed = true

	// Verify
	if err := inst.verify(ctx, comp); err != nil {
		status.Message = fmt.Sprintf("installed but verification failed: %v", err)
		return status, nil
	}

	status.Ready = true
	status.Message = "installed and verified"
	return status, nil
}

func (inst *Installer) installBinary(ctx context.Context, comp knowledge.StackComponent, hwProfile string) error {
	// Build install command args from stack YAML
	args := collectArgs(comp, hwProfile)

	// Set environment variables for child process, then restore on return.
	env := collectEnv(comp, hwProfile)
	saved := make(map[string]*string, len(env))
	for k, v := range env {
		if old, ok := os.LookupEnv(k); ok {
			saved[k] = &old
		} else {
			saved[k] = nil
		}
		os.Setenv(k, v)
	}
	defer func() {
		for k := range env {
			if old := saved[k]; old != nil {
				os.Setenv(k, *old)
			} else {
				os.Unsetenv(k)
			}
		}
	}()

	// Resolve binary: local dist/ first, then PATH, then os.Executable() (self)
	binary := comp.Source.Binary
	localPath := filepath.Join(inst.distDir, binary)
	if _, err := os.Stat(localPath); err == nil {
		binary = localPath
		slog.Info("using local binary", "path", localPath)
	} else if _, err := exec.LookPath(comp.Source.Binary); err != nil {
		// Fallback: if the component binary name matches our own binary (e.g., "aima"),
		// use os.Executable() to find ourselves.
		selfPath, exeErr := os.Executable()
		baseName := strings.TrimSuffix(filepath.Base(selfPath), ".exe")
		if exeErr == nil && (baseName == comp.Source.Binary || filepath.Base(selfPath) == comp.Source.Binary) {
			binary = selfPath
			slog.Info("using self as binary", "path", selfPath)
		} else {
			return fmt.Errorf("%s not found: place binary at %s or add to PATH", comp.Source.Binary, localPath)
		}
	}

	// Execute: component binary <subcommand> <args>
	subcommand := comp.Install.Subcommand
	if subcommand == "" {
		subcommand = "server"
	}
	cmdArgs := append([]string{subcommand}, args...)

	if comp.Install.Daemon {
		if runtime.GOOS == "linux" {
			return inst.installDaemonSystemd(ctx, comp, binary, hwProfile)
		}
		// Non-Linux fallback: start in background, verify step will poll for readiness
		cmd := exec.CommandContext(ctx, binary, cmdArgs...)
		cmd.Env = os.Environ()
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start %s: %w", comp.Source.Binary, err)
		}
		slog.Info("daemon started (no systemd)", "name", comp.Metadata.Name, "pid", cmd.Process.Pid)
		return nil
	}

	out, err := inst.runner.Run(ctx, binary, cmdArgs...)
	if err != nil {
		return fmt.Errorf("run %s: %s: %w", comp.Source.Binary, string(out), err)
	}

	return nil
}

// installDaemonSystemd installs a daemon component as a systemd service on Linux.
// It writes an env file + unit file, then runs daemon-reload → enable → start.
func (inst *Installer) installDaemonSystemd(ctx context.Context, comp knowledge.StackComponent, binary string, hwProfile string) error {
	name := comp.Metadata.Name

	// Build args and env from stack YAML (reuse existing logic)
	args := collectArgs(comp, hwProfile)
	env := collectEnv(comp, hwProfile)

	// Copy binary to /usr/local/bin/ so it's accessible to all users.
	// This matches the K3S official install script convention.
	systemBinary := filepath.Join("/usr/local/bin", name)
	if err := copyFile(binary, systemBinary, 0o755); err != nil {
		slog.Warn("failed to copy binary to system path, using original", "error", err)
		systemBinary = binary
	} else {
		slog.Info("installed binary to system path", "path", systemBinary)
	}
	absBinary, err := filepath.Abs(systemBinary)
	if err != nil {
		absBinary = systemBinary
	}

	// Write env file: K3S uses /etc/rancher/k3s/, other daemons use /etc/aima/
	envDir := "/etc/aima"
	if name == "k3s" {
		envDir = "/etc/rancher/k3s"
	}
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		return fmt.Errorf("create env dir %s: %w", envDir, err)
	}
	var envLines []string
	for k, v := range env {
		envLines = append(envLines, k+"="+v)
	}
	envFile := filepath.Join(envDir, name+".env")
	if err := os.WriteFile(envFile, []byte(strings.Join(envLines, "\n")+"\n"), 0o600); err != nil {
		return fmt.Errorf("write env file %s: %w", envFile, err)
	}

	// Build ExecStart line: binary <subcommand> <args>
	subcommand := comp.Install.Subcommand
	if subcommand == "" {
		subcommand = "server" // backward compat for K3S
	}
	execParts := []string{absBinary, subcommand}
	execParts = append(execParts, args...)
	execStart := strings.Join(execParts, " ")

	// Generate systemd unit file
	serviceType := comp.Install.ServiceType
	if serviceType == "" {
		serviceType = "notify" // backward compat for K3S
	}
	unit := fmt.Sprintf(`[Unit]
Description=AIMA managed %s (%s)
After=network-online.target
Wants=network-online.target

[Service]
Type=%s
Environment=HOME=/root
EnvironmentFile=%s
ExecStart=%s
Restart=always
RestartSec=5s
KillMode=process
LimitNOFILE=1048576
LimitNPROC=infinity

[Install]
WantedBy=multi-user.target
`, name, comp.Metadata.Version, serviceType, envFile, execStart)

	unitPath := "/etc/systemd/system/" + name + ".service"
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit file %s: %w", unitPath, err)
	}
	slog.Info("wrote systemd unit", "path", unitPath)

	// daemon-reload → enable → start
	if out, err := inst.runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %s: %w", string(out), err)
	}
	if out, err := inst.runner.Run(ctx, "systemctl", "enable", name); err != nil {
		return fmt.Errorf("systemctl enable %s: %s: %w", name, string(out), err)
	}
	if out, err := inst.runner.Run(ctx, "systemctl", "start", name); err != nil {
		return fmt.Errorf("systemctl start %s: %s: %w", name, string(out), err)
	}

	slog.Info("daemon installed as systemd service", "name", name, "unit", unitPath)
	return nil
}

func (inst *Installer) installHelm(ctx context.Context, comp knowledge.StackComponent, hwProfile string) error {
	if comp.Install.Helm == nil {
		return fmt.Errorf("helm config missing for %s", comp.Metadata.Name)
	}

	helmCfg := comp.Install.Helm
	chartPath := filepath.Join(inst.distDir, helmCfg.Chart)
	chartData, err := os.ReadFile(chartPath)
	if err != nil {
		return fmt.Errorf("%s not found: place chart at %s", helmCfg.Chart, chartPath)
	}

	// Find k3s binary (K3S has a built-in helm-controller that handles HelmChart CRDs)
	k3sBin := inst.findK3sBinary()
	if k3sBin == "" {
		return fmt.Errorf("k3s not found: install K3S first (aima init installs k3s before hami)")
	}

	// Base64-encode chart for inline embedding in HelmChart CRD
	chartB64 := base64.StdEncoding.EncodeToString(chartData)

	// Serialize values to YAML
	valuesYAML, err := yaml.Marshal(helmCfg.Values)
	if err != nil {
		return fmt.Errorf("marshal helm values: %w", err)
	}

	// Build HelmChart CRD manifest with chartContent (not chart path)
	// chartContent embeds the chart inline so klipper-helm pod doesn't need host filesystem access
	manifest := fmt.Sprintf(`apiVersion: helm.cattle.io/v1
kind: HelmChart
metadata:
  name: %s
  namespace: kube-system
spec:
  chartContent: %s
  targetNamespace: %s
  createNamespace: true
  valuesContent: |
    %s
`, comp.Metadata.Name, chartB64, helmCfg.Namespace,
		strings.ReplaceAll(strings.TrimSpace(string(valuesYAML)), "\n", "\n    "))

	tmpFile := filepath.Join(os.TempDir(), comp.Metadata.Name+"-helmchart.yaml")
	if err := os.WriteFile(tmpFile, []byte(manifest), 0o644); err != nil {
		return fmt.Errorf("write HelmChart manifest: %w", err)
	}
	defer os.Remove(tmpFile)

	slog.Info("applying HelmChart CRD via k3s kubectl", "name", comp.Metadata.Name)
	out, err := inst.runner.Run(ctx, k3sBin, "kubectl", "apply", "-f", tmpFile)
	if err != nil {
		return fmt.Errorf("apply HelmChart CRD: %s: %w", string(out), err)
	}
	return nil
}

// findK3sBinary locates the k3s binary: dist dir first, then PATH.
func (inst *Installer) findK3sBinary() string {
	local := filepath.Join(inst.distDir, "k3s")
	if _, err := os.Stat(local); err == nil {
		return local
	}
	if p, err := exec.LookPath("k3s"); err == nil {
		return p
	}
	return ""
}

func (inst *Installer) verify(ctx context.Context, comp knowledge.StackComponent) error {
	if comp.Verify.Command == "" {
		return nil
	}

	timeout := time.Duration(comp.Verify.TimeoutS) * time.Second
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	deadline := time.Now().Add(timeout)
	parts := strings.Fields(comp.Verify.Command)
	if len(parts) == 0 {
		return fmt.Errorf("empty verify command for %s", comp.Metadata.Name)
	}

	// Resolve binary from dist/ if not in PATH
	binary := parts[0]
	if localPath := filepath.Join(inst.distDir, binary); fileExists(localPath) {
		binary = localPath
	}

	for time.Now().Before(deadline) {
		out, err := inst.runner.Run(ctx, binary, parts[1:]...)
		if err == nil && strings.Contains(string(out), comp.Verify.ReadyCondition) {
			slog.Info("stack component verified", "name", comp.Metadata.Name)
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}

	return fmt.Errorf("timeout waiting for %s to become ready", comp.Metadata.Name)
}

func (inst *Installer) checkComponent(ctx context.Context, comp knowledge.StackComponent, hwProfile string) ComponentStatus {
	status := ComponentStatus{
		Name:    comp.Metadata.Name,
		Version: comp.Metadata.Version,
	}

	if !platformSupported(comp.Source.Platforms) {
		status.Skipped = true
		status.Message = fmt.Sprintf("skipped: platform %s/%s not supported", runtime.GOOS, runtime.GOARCH)
		return status
	}

	if skip, msg := shouldSkip(comp, hwProfile); skip {
		status.Skipped = true
		status.Message = msg
		return status
	}

	if comp.Verify.Command == "" {
		status.Message = "no verify command defined"
		return status
	}

	parts := strings.Fields(comp.Verify.Command)
	if len(parts) == 0 {
		status.Message = "empty verify command"
		return status
	}

	// Early systemd check for daemon components on Linux — gives actionable guidance
	if comp.Install.Daemon && runtime.GOOS == "linux" {
		out, err := inst.runner.Run(ctx, "systemctl", "is-active", comp.Metadata.Name)
		if err != nil || strings.TrimSpace(string(out)) != "active" {
			status.Message = fmt.Sprintf("service not running; try: sudo systemctl start %s", comp.Metadata.Name)
			return status
		}
	}

	// Resolve binary from dist/ if not in PATH
	binary := parts[0]
	if localPath := filepath.Join(inst.distDir, binary); fileExists(localPath) {
		binary = localPath
	}

	out, err := inst.runner.Run(ctx, binary, parts[1:]...)
	if err != nil {
		status.Message = fmt.Sprintf("not installed or not running: %v", err)
		return status
	}

	status.Installed = true
	if strings.Contains(string(out), comp.Verify.ReadyCondition) {
		status.Ready = true
		status.Message = "ready"
	} else {
		status.Message = "installed but not ready"
	}

	// Query pod-level details if PodQuerier is available and pods are defined
	if inst.podQuerier != nil && len(comp.Verify.Pods) > 0 {
		for _, podSpec := range comp.Verify.Pods {
			pods, err := inst.podQuerier.ListPodsByLabel(ctx, podSpec.Namespace, podSpec.Label)
			if err != nil {
				slog.Warn("pod query failed", "component", comp.Metadata.Name, "label", podSpec.Label, "error", err)
				continue
			}
			status.Pods = append(status.Pods, pods...)
			// If pod check requires min_ready, verify and potentially downgrade status
			readyCount := 0
			for _, p := range pods {
				if p.Ready {
					readyCount++
				}
			}
			if readyCount < podSpec.MinReady {
				status.Ready = false
				status.Message = fmt.Sprintf("installed but pods not ready (%d/%d)", readyCount, podSpec.MinReady)
			}
		}
	}

	return status
}

// collectArgs gathers install args from base config + hardware profile.
func collectArgs(comp knowledge.StackComponent, hwProfile string) []string {
	var args []string
	for _, a := range comp.Install.Args {
		args = append(args, a.Flag)
	}

	if hwProfile != "" {
		if profile, ok := comp.Profiles[hwProfile]; ok {
			for _, a := range profile.ExtraArgs {
				args = append(args, a.Flag)
			}
		}
	}

	return args
}

// ensureKubectlLink creates a /usr/local/bin/kubectl symlink pointing to the
// component binary (e.g. k3s). K3S is a multi-call binary: when invoked as
// "kubectl" it auto-detects /etc/rancher/k3s/k3s.yaml and acts as standard kubectl.
func (inst *Installer) ensureKubectlLink(binaryName string) {
	kubectlLink := "/usr/local/bin/kubectl"
	if _, err := os.Lstat(kubectlLink); err == nil {
		return // already exists (symlink, real binary, anything)
	}

	// Prefer system-installed binary (/usr/local/bin/k3s), then dist/, then PATH
	var binary string
	systemPath := filepath.Join("/usr/local/bin", binaryName)
	switch {
	case fileExists(systemPath):
		binary = systemPath
	case fileExists(filepath.Join(inst.distDir, binaryName)):
		binary = filepath.Join(inst.distDir, binaryName)
	default:
		if p, err := exec.LookPath(binaryName); err == nil {
			binary = p
		} else {
			return
		}
	}

	absBinary, err := filepath.Abs(binary)
	if err != nil {
		return
	}

	if err := os.Symlink(absBinary, kubectlLink); err != nil {
		slog.Warn("failed to create kubectl symlink", "target", absBinary, "link", kubectlLink, "error", err)
	} else {
		slog.Info("created kubectl symlink", "link", kubectlLink, "target", absBinary)
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writeRegistries writes the component's container registry mirror config to /etc/rancher/k3s/registries.yaml.
// This must happen before K3S starts so containerd picks up the mirrors on first boot.
func (inst *Installer) writeRegistries(comp knowledge.StackComponent) error {
	dir := "/etc/rancher/k3s"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create registries dir: %w", err)
	}

	data, err := yaml.Marshal(comp.Registries)
	if err != nil {
		return fmt.Errorf("marshal registries config: %w", err)
	}

	path := filepath.Join(dir, "registries.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	slog.Info("wrote containerd registries config", "path", path)
	return nil
}

// prepareAirgapImages places or imports airgap image tars before component installation.
// For binary components (K3S): copies tar to /var/lib/rancher/k3s/agent/images/ for auto-import on startup.
// For helm components: imports tar via "k3s ctr images import" since K3S is already running.
func (inst *Installer) prepareAirgapImages(ctx context.Context, comp knowledge.StackComponent) {
	if comp.Source.Airgap == "" {
		return
	}

	airgapPath := filepath.Join(inst.distDir, comp.Source.Airgap)
	if _, err := os.Stat(airgapPath); err != nil {
		slog.Warn("airgap tar not found, skipping", "path", airgapPath)
		return
	}

	k3sBin := inst.findK3sBinary()

	switch comp.Install.Method {
	case "binary":
		// K3S airgap: place tar in auto-import directory for startup import.
		// K3S agent natively handles .tar, .tar.gz, .tar.zst in this directory.
		destDir := "/var/lib/rancher/k3s/agent/images"
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			slog.Warn("failed to create K3S images dir", "error", err)
			return
		}
		dest := filepath.Join(destDir, comp.Source.Airgap)
		if err := copyFile(airgapPath, dest, 0o644); err != nil {
			slog.Warn("failed to place airgap tar", "src", airgapPath, "dest", dest, "error", err)
		} else {
			slog.Info("placed airgap images for K3S auto-import", "path", dest)
		}
		// Also import directly if K3S containerd is already running
		// (auto-import only happens on K3S startup, not for already-running instances).
		// containerd's ctr only handles raw .tar; compressed files need decompression via pipe.
		if k3sBin != "" {
			slog.Info("importing airgap images into running containerd", "file", airgapPath)
			inst.ctrImportAirgap(ctx, k3sBin, airgapPath)
		}

	case "helm":
		// Helm components (HAMi): K3S is already running, import directly via containerd
		if k3sBin == "" {
			slog.Warn("k3s not found, cannot import airgap images")
			return
		}
		slog.Info("importing airgap images via containerd", "file", airgapPath)
		inst.ctrImportAirgap(ctx, k3sBin, airgapPath)
	}
}

// ctrImportAirgap imports an airgap tar into containerd via k3s ctr.
// containerd's ctr only handles raw .tar — compressed files (.tar.zst, .tar.gz)
// are piped through the appropriate decompressor first.
func (inst *Installer) ctrImportAirgap(ctx context.Context, k3sBin, airgapPath string) {
	importCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var out []byte
	var err error

	switch {
	case strings.HasSuffix(airgapPath, ".tar.zst"):
		out, err = inst.runner.Run(importCtx, "sh", "-c",
			fmt.Sprintf("zstd -dc %q | %q ctr images import -", airgapPath, k3sBin))
	case strings.HasSuffix(airgapPath, ".tar.gz"), strings.HasSuffix(airgapPath, ".tgz"):
		out, err = inst.runner.Run(importCtx, "sh", "-c",
			fmt.Sprintf("gzip -dc %q | %q ctr images import -", airgapPath, k3sBin))
	default:
		out, err = inst.runner.Run(importCtx, k3sBin, "ctr", "images", "import", airgapPath)
	}

	if err != nil {
		slog.Debug("airgap import failed (containerd may not be running yet)", "error", err, "output", string(out))
	} else {
		slog.Info("airgap images imported successfully")
	}
}

// collectEnv gathers environment variables from base config + hardware profile.
func collectEnv(comp knowledge.StackComponent, hwProfile string) map[string]string {
	env := make(map[string]string)
	for k, v := range comp.Install.Env {
		env[k] = v
	}

	if hwProfile != "" {
		if profile, ok := comp.Profiles[hwProfile]; ok {
			for k, v := range profile.ExtraEnv {
				env[k] = v
			}
		}
	}

	return env
}
