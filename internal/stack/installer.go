package stack

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// ComponentStatus describes the install state of a single stack component.
type ComponentStatus struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
	Ready     bool   `json:"ready"`
	Skipped   bool   `json:"skipped,omitempty"`
	Message   string `json:"message,omitempty"`
}

// InitResult is the aggregate result of aima init.
type InitResult struct {
	Components []ComponentStatus `json:"components"`
	AllReady   bool              `json:"all_ready"`
}

// Installer installs and verifies stack components.
type Installer struct {
	runner  CommandRunner
	distDir string // path to dist/{platform}/
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

// Init runs the full initialization workflow for all stack components.
func (inst *Installer) Init(ctx context.Context, components []knowledge.StackComponent, hwProfile string) (*InitResult, error) {
	result := &InitResult{AllReady: true}

	hasReady := false
	for _, comp := range components {
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
}

// Preflight checks which components need files downloaded.
// It returns a list of missing files that have download URLs configured.
func (inst *Installer) Preflight(components []knowledge.StackComponent) []DownloadItem {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	var items []DownloadItem

	for _, comp := range components {
		if !platformSupported(comp.Source.Platforms) {
			continue
		}

		// Determine the local file to check
		fileName := comp.Source.Binary
		if fileName == "" {
			fileName = comp.Source.Chart
		}
		if fileName == "" {
			continue
		}

		localPath := filepath.Join(inst.distDir, fileName)

		// Skip if file already exists
		if _, err := os.Stat(localPath); err == nil {
			continue
		}

		// Check if download URL is available for this platform
		url := comp.Source.Download[platform]
		if url == "" {
			continue
		}

		items = append(items, DownloadItem{
			Name:       comp.Metadata.Name,
			FileName:   fileName,
			FilePath:   localPath,
			URL:        url,
			MirrorURL:  comp.Source.Mirror[platform],
			Executable: comp.Source.Binary != "",
		})
	}

	return items
}

// DownloadItems downloads all items in the list, creating directories as needed.
// If a primary URL fails and a mirror URL is configured, it retries with the mirror.
func DownloadItems(ctx context.Context, items []DownloadItem) error {
	for _, item := range items {
		slog.Info("downloading", "name", item.Name, "url", item.URL)
		err := downloadFile(ctx, item.URL, item.FilePath)
		if err != nil && item.MirrorURL != "" {
			slog.Warn("primary download failed, trying mirror", "name", item.Name, "error", err, "mirror", item.MirrorURL)
			err = downloadFile(ctx, item.MirrorURL, item.FilePath)
		}
		if err != nil {
			return fmt.Errorf("download %s: %w", item.Name, err)
		}
		if item.Executable {
			if err := os.Chmod(item.FilePath, 0o755); err != nil {
				return fmt.Errorf("chmod %s: %w", item.FilePath, err)
			}
		}
	}
	return nil
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
func (inst *Installer) Status(ctx context.Context, components []knowledge.StackComponent) (*InitResult, error) {
	result := &InitResult{AllReady: true}

	hasReady := false
	for _, comp := range components {
		status := inst.checkComponent(ctx, comp)
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

	// Check if already installed and ready
	existing := inst.checkComponent(ctx, comp)
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

	// Resolve binary: local dist/ first, then PATH
	binary := comp.Source.Binary
	localPath := filepath.Join(inst.distDir, binary)
	if _, err := os.Stat(localPath); err == nil {
		binary = localPath
		slog.Info("using local binary", "path", localPath)
	} else if _, err := exec.LookPath(comp.Source.Binary); err != nil {
		return fmt.Errorf("%s not found: place binary at %s or add to PATH", comp.Source.Binary, localPath)
	}

	// Execute: component binary server <args>
	cmdArgs := append([]string{"server"}, args...)
	out, err := inst.runner.Run(ctx, binary, cmdArgs...)
	if err != nil {
		return fmt.Errorf("run %s: %s: %w", comp.Source.Binary, string(out), err)
	}

	return nil
}

func (inst *Installer) installHelm(ctx context.Context, comp knowledge.StackComponent, hwProfile string) error {
	if comp.Install.Helm == nil {
		return fmt.Errorf("helm config missing for %s", comp.Metadata.Name)
	}

	helmCfg := comp.Install.Helm
	chartPath := filepath.Join(inst.distDir, helmCfg.Chart)
	if _, err := os.Stat(chartPath); err != nil {
		return fmt.Errorf("%s not found: place chart at %s", helmCfg.Chart, chartPath)
	}

	// Find k3s binary (K3S has a built-in helm-controller that handles HelmChart CRDs)
	k3sBin := inst.findK3sBinary()
	if k3sBin == "" {
		return fmt.Errorf("k3s not found: install K3S first (aima init installs k3s before hami)")
	}

	// Copy chart to K3S static charts dir so helm-controller can access it
	staticDir := "/var/lib/rancher/k3s/server/static/charts"
	if err := os.MkdirAll(staticDir, 0o755); err != nil {
		return fmt.Errorf("create static charts dir: %w", err)
	}
	destChart := filepath.Join(staticDir, filepath.Base(chartPath))
	if err := copyFile(chartPath, destChart); err != nil {
		return fmt.Errorf("copy chart to %s: %w", destChart, err)
	}

	// Serialize values to YAML
	valuesYAML, err := yaml.Marshal(helmCfg.Values)
	if err != nil {
		return fmt.Errorf("marshal helm values: %w", err)
	}

	// Build HelmChart CRD manifest
	manifest := fmt.Sprintf(`apiVersion: helm.cattle.io/v1
kind: HelmChart
metadata:
  name: %s
  namespace: kube-system
spec:
  chart: %s
  targetNamespace: %s
  createNamespace: true
  valuesContent: |
    %s
`, comp.Metadata.Name, destChart, helmCfg.Namespace,
		strings.ReplaceAll(strings.TrimSpace(string(valuesYAML)), "\n", "\n    "))

	tmpFile := filepath.Join(os.TempDir(), comp.Metadata.Name+"-helmchart.yaml")
	if err := os.WriteFile(tmpFile, []byte(manifest), 0o644); err != nil {
		return fmt.Errorf("write HelmChart manifest: %w", err)
	}
	defer os.Remove(tmpFile)

	slog.Info("applying HelmChart CRD via k3s kubectl", "name", comp.Metadata.Name, "chart", destChart)
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

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
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

	for time.Now().Before(deadline) {
		out, err := inst.runner.Run(ctx, parts[0], parts[1:]...)
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

func (inst *Installer) checkComponent(ctx context.Context, comp knowledge.StackComponent) ComponentStatus {
	status := ComponentStatus{
		Name:    comp.Metadata.Name,
		Version: comp.Metadata.Version,
	}

	if !platformSupported(comp.Source.Platforms) {
		status.Skipped = true
		status.Message = fmt.Sprintf("skipped: platform %s/%s not supported", runtime.GOOS, runtime.GOARCH)
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

	out, err := inst.runner.Run(ctx, parts[0], parts[1:]...)
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
