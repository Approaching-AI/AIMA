package stack

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
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

// tierRank returns the numeric order for a tier name.
// "docker" tier (1) includes components with tier "docker" only.
// "k3s" tier (2) includes both "docker" and "k3s" components (superset).
// Unknown tiers return 0.
func tierRank(tier string) int {
	switch tier {
	case "docker":
		return 1
	case "k3s":
		return 2
	default:
		return 0
	}
}

// FilterByTier returns components whose tier is at or below the requested tier.
// tier "docker" → only tier="docker" components.
// tier "k3s" → tier="docker" and tier="k3s" components.
// Empty tier on a component means it's included in all tiers.
func FilterByTier(components []knowledge.StackComponent, tier string) []knowledge.StackComponent {
	maxOrder := tierRank(tier)
	if maxOrder == 0 {
		return components // unknown tier → include all
	}
	var filtered []knowledge.StackComponent
	for _, c := range components {
		compTier := c.Install.Tier
		if compTier == "" {
			filtered = append(filtered, c) // no tier = always included
			continue
		}
		if compOrder := tierRank(compTier); compOrder > 0 && compOrder <= maxOrder {
			filtered = append(filtered, c)
		}
	}
	return filtered
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
		// Check if all daemon units are already running — if so, no root needed
		allActive := true
		unitNames := []string{comp.Metadata.Name}
		if len(comp.Install.SystemdUnits) > 0 {
			unitNames = make([]string, len(comp.Install.SystemdUnits))
			for i, u := range comp.Install.SystemdUnits {
				unitNames[i] = u.Name
			}
		}
		for _, name := range unitNames {
			out, err := inst.runner.Run(ctx, "systemctl", "is-active", name)
			if err != nil || strings.TrimSpace(string(out)) != "active" {
				allActive = false
				break
			}
		}
		if allActive {
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
	Name       string   `json:"name"`                  // component name
	FileName   string   `json:"file_name"`             // e.g. "k3s" or "hami-chart.tgz"
	FilePath   string   `json:"file_path"`             // full local path in dist/
	URL        string   `json:"url"`                   // primary download URL
	MirrorURLs []string `json:"mirror_urls,omitempty"` // fallback URLs tried before primary (e.g. ghproxy mirrors)
	SHA256     string   `json:"sha256,omitempty"`      // expected SHA-256 hex digest (optional)
	Executable bool     `json:"executable,omitempty"`  // chmod +x after download
	Optional   bool     `json:"optional,omitempty"`    // if true, download failure won't abort init (e.g. airgap tars)
}

// Preflight checks which components need files downloaded.
// It returns a list of missing files that have download URLs configured,
// including airgap image tars when configured.
// Components that are already installed and ready are skipped entirely —
// this avoids slow/failing downloads for airgap tars of already-running services.
func (inst *Installer) Preflight(ctx context.Context, components []knowledge.StackComponent, hwProfile string) []DownloadItem {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	items := make([]DownloadItem, 0)

	for _, comp := range components {
		if !platformSupported(comp.Source.Platforms) {
			continue
		}

		// Skip downloads for components that are already ready
		if existing := inst.checkComponent(ctx, comp, hwProfile); existing.Ready {
			slog.Info("preflight: component already ready, skipping download", "name", comp.Metadata.Name)
			continue
		}

		// Main artifact: binary, chart, or archive
		fileName := comp.Source.Binary
		if fileName == "" {
			fileName = comp.Source.Chart
		}
		if fileName == "" {
			fileName = comp.Source.Archive
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
						MirrorURLs: comp.Source.Mirror[platform],
						SHA256:     comp.Source.SHA256[platform],
						Executable: comp.Source.Binary != "" && comp.Source.Archive == "",
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
						Name:       comp.Metadata.Name + "-airgap",
						FileName:   comp.Source.Airgap,
						FilePath:   airgapPath,
						URL:        url,
						MirrorURLs: comp.Source.AirgapMirror[platform],
						SHA256:     comp.Source.AirgapSHA256[platform],
						Optional:   true,
					})
				}
			}
		}
	}

	return items
}

// DownloadItems downloads all items in parallel, creating directories as needed.
// Each URL is retried up to 3 times with exponential backoff and HTTP Range resume.
// Mirror URLs are tried first (in order), then the primary URL as final fallback.
// Optional items (e.g. airgap tars) log a warning on failure instead of aborting.
// When SHA256 is set, the downloaded file is verified before being accepted.
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

			// Build URL list: mirrors first (faster in China), primary last
			urls := make([]string, 0, len(item.MirrorURLs)+1)
			urls = append(urls, item.MirrorURLs...)
			urls = append(urls, item.URL)

			var err error
			for _, u := range urls {
				slog.Info("downloading", "name", item.Name, "url", u)
				err = downloadFileRetry(ctx, u, item.FilePath, item.SHA256)
				if err == nil {
					break
				}
				slog.Warn("download failed, trying next source", "name", item.Name, "url", u, "error", err)
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

const downloadMaxRetries = 3

// downloadFileRetry wraps downloadFile with retry + exponential backoff.
func downloadFileRetry(ctx context.Context, url, destPath, expectSHA256 string) error {
	var lastErr error
	for attempt := range downloadMaxRetries {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second // 1s, 2s
			slog.Info("retrying download", "url", url, "attempt", attempt+1, "backoff", backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
		lastErr = downloadFile(ctx, url, destPath, expectSHA256)
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

// downloadFile downloads url to destPath via a .partial temp file with HTTP Range resume.
// If expectSHA256 is non-empty, the downloaded file is verified against the expected hex digest.
func downloadFile(ctx context.Context, url, destPath, expectSHA256 string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	partial := destPath + ".partial"

	// Check for existing partial download for resume
	var existingSize int64
	if info, err := os.Stat(partial); err == nil {
		existingSize = info.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if existingSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
	}

	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	var flags int
	switch resp.StatusCode {
	case http.StatusOK:
		existingSize = 0
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	case http.StatusPartialContent:
		flags = os.O_WRONLY | os.O_APPEND
	default:
		return fmt.Errorf("http %d from %s", resp.StatusCode, url)
	}

	f, err := os.OpenFile(partial, flags, 0o644)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return fmt.Errorf("write file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}

	// Verify SHA-256 if expected digest is provided
	if expectSHA256 != "" {
		actual, err := fileSHA256(partial)
		if err != nil {
			os.Remove(partial)
			return fmt.Errorf("compute sha256: %w", err)
		}
		if actual != expectSHA256 {
			os.Remove(partial)
			return fmt.Errorf("sha256 mismatch for %s: expected %s, got %s", url, expectSHA256, actual)
		}
		slog.Info("sha256 verified", "file", filepath.Base(destPath))
	}

	if err := os.Rename(partial, destPath); err != nil {
		os.Remove(partial)
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// fileSHA256 computes the hex-encoded SHA-256 digest of a file.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
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
	case "archive":
		if err := inst.installArchive(ctx, comp); err != nil {
			return status, fmt.Errorf("install %s: %w", comp.Metadata.Name, err)
		}
	case "helm":
		if err := inst.installHelm(ctx, comp, hwProfile); err != nil {
			return status, fmt.Errorf("install %s: %w", comp.Metadata.Name, err)
		}
	default:
		return status, fmt.Errorf("unknown install method %q for %s", comp.Install.Method, comp.Metadata.Name)
	}

	// Run post_install commands (non-fatal on failure)
	for _, rawCmd := range comp.Install.PostInstall {
		cmd := strings.ReplaceAll(rawCmd, "{{.DistDir}}", inst.distDir)
		slog.Info("running post_install", "name", comp.Metadata.Name, "command", cmd)
		out, err := inst.runner.Run(ctx, "sh", "-c", cmd)
		if err != nil {
			slog.Warn("post_install command failed (non-fatal)", "name", comp.Metadata.Name, "command", cmd, "error", err, "output", string(out))
		}
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

	// Build environment for child process without mutating the current process env.
	env := collectEnv(comp, hwProfile)
	cmdEnv := os.Environ()
	for k, v := range env {
		cmdEnv = append(cmdEnv, k+"="+v)
	}

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
		cmd.Env = cmdEnv
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start %s: %w", comp.Source.Binary, err)
		}
		slog.Info("daemon started (no systemd)", "name", comp.Metadata.Name, "pid", cmd.Process.Pid)
		return nil
	}

	cmd := exec.CommandContext(ctx, binary, cmdArgs...)
	cmd.Env = cmdEnv
	out, err := cmd.CombinedOutput()
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

	resolvedBinary := resolveSystemdBinaryPath(binary)

	// Copy binary to /usr/local/bin/ so it's accessible to all users.
	// This matches the K3S official install script convention.
	systemBinary := filepath.Join("/usr/local/bin", name)
	if err := copyFile(resolvedBinary, systemBinary, 0o755); err != nil {
		slog.Warn("failed to copy binary to system path, using original", "error", err)
		systemBinary = resolvedBinary
	} else {
		slog.Info("installed binary to system path", "path", systemBinary)
	}
	absBinary, err := filepath.Abs(systemBinary)
	if err != nil {
		absBinary = systemBinary
	}

	// Write env file: K3S uses /etc/rancher/k3s/, other daemons use /etc/aima/
	envDir := "/etc/aima"
	envDirMode := os.FileMode(0o755)
	if name == "k3s" {
		envDir = "/etc/rancher/k3s"
		envDirMode = 0o750
	}
	if err := os.MkdirAll(envDir, envDirMode); err != nil {
		return fmt.Errorf("create env dir %s: %w", envDir, err)
	}
	// Apply the mode even when the directory already exists so upgrades repair
	// older installs that made /etc/aima unreadable to non-root CLI users.
	if err := os.Chmod(envDir, envDirMode); err != nil {
		return fmt.Errorf("set env dir permissions %s: %w", envDir, err)
	}
	var envLines []string
	for k, v := range env {
		envLines = append(envLines, k+"="+v)
	}
	// Pin AIMA_DATA_DIR to a shared, world-readable path so that CLI commands
	// invoked by any user resolve the same data directory as the systemd service.
	// Using /var/lib/aima (not /root/.aima) because /root is typically mode 700.
	aimaDataDir := "/var/lib/aima"
	if v, exists := env["AIMA_DATA_DIR"]; exists {
		aimaDataDir = v
	} else {
		envLines = append(envLines, "AIMA_DATA_DIR="+aimaDataDir)
	}
	if err := os.MkdirAll(aimaDataDir, 0o755); err != nil {
		slog.Warn("failed to create shared data dir", "path", aimaDataDir, "error", err)
	}
	// Write shared data-dir pointer so any user's CLI resolves the same path.
	if err := os.WriteFile(filepath.Join(envDir, "data-dir"), []byte(aimaDataDir+"\n"), 0o644); err != nil {
		slog.Warn("failed to write shared data-dir config", "error", err)
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

func resolveSystemdBinaryPath(binary string) string {
	if binary == "" {
		return binary
	}
	if filepath.IsAbs(binary) {
		return binary
	}
	if resolved, err := exec.LookPath(binary); err == nil && resolved != "" {
		return resolved
	}
	return binary
}

// installArchive installs a component from a .tar.gz archive:
// 1. Extract specified binaries to /usr/local/bin/
// 2. Write systemd unit files from SystemdUnits list
// 3. daemon-reload + enable + start each unit
func (inst *Installer) installArchive(ctx context.Context, comp knowledge.StackComponent) error {
	archiveName := comp.Source.Archive
	if archiveName == "" {
		return fmt.Errorf("archive name not specified for %s", comp.Metadata.Name)
	}

	archivePath := filepath.Join(inst.distDir, archiveName)
	if _, err := os.Stat(archivePath); err != nil {
		return fmt.Errorf("%s not found: place archive at %s", archiveName, archivePath)
	}

	// 1. Extract binaries (skip if extract_binaries is empty — e.g. deb archives handled by post_install)
	if len(comp.Source.ExtractBinaries) > 0 {
		destDir := "/usr/local/bin"
		if err := extractBinaries(archivePath, comp.Source.ExtractBinaries, destDir); err != nil {
			return fmt.Errorf("extract binaries from %s: %w", archiveName, err)
		}
	}

	// 2. Write systemd units
	for _, unit := range comp.Install.SystemdUnits {
		if err := writeSystemdUnit(comp, unit); err != nil {
			return fmt.Errorf("write systemd unit %s: %w", unit.Name, err)
		}
	}

	// 3. daemon-reload + enable + start
	if len(comp.Install.SystemdUnits) > 0 {
		if out, err := inst.runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("systemctl daemon-reload: %s: %w", string(out), err)
		}
		for _, unit := range comp.Install.SystemdUnits {
			if out, err := inst.runner.Run(ctx, "systemctl", "enable", "--now", unit.Name); err != nil {
				return fmt.Errorf("systemctl enable --now %s: %s: %w", unit.Name, string(out), err)
			}
			slog.Info("systemd unit enabled and started", "name", unit.Name)
		}
	}

	return nil
}

// extractBinaries opens a .tar.gz archive and extracts specific files to destDir.
// paths are matched against tar entry names (e.g. "docker/dockerd").
// Extracted files are made executable (0755).
func extractBinaries(archivePath string, paths []string, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}

	// Build a set for fast lookup
	wanted := make(map[string]bool, len(paths))
	for _, p := range paths {
		wanted[p] = true
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var extracted []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			for _, f := range extracted {
				os.Remove(f)
			}
			return fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if !wanted[hdr.Name] {
			continue
		}

		baseName := filepath.Base(hdr.Name)
		destPath := filepath.Join(destDir, baseName)

		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			for _, f := range extracted {
				os.Remove(f)
			}
			return fmt.Errorf("create %s: %w", destPath, err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			for _, f := range extracted {
				os.Remove(f)
			}
			return fmt.Errorf("write %s: %w", destPath, err)
		}
		out.Close()
		extracted = append(extracted, destPath)
		slog.Info("extracted binary", "path", destPath)
	}

	if len(extracted) == 0 {
		return fmt.Errorf("no matching binaries found in archive (wanted %d)", len(paths))
	}
	slog.Info("extracted binaries from archive", "count", len(extracted), "archive", filepath.Base(archivePath))
	return nil
}

// writeSystemdUnit generates and writes a systemd unit file for an archive component.
func writeSystemdUnit(comp knowledge.StackComponent, unit knowledge.SystemdUnit) error {
	serviceType := unit.Type
	if serviceType == "" {
		serviceType = "simple"
	}
	after := "network-online.target"
	wants := "network-online.target"
	if unit.After != "" {
		after = "network-online.target " + unit.After
		wants = "network-online.target " + unit.After
	}

	content := fmt.Sprintf(`[Unit]
Description=AIMA managed %s (%s)
After=%s
Wants=%s

[Service]
Type=%s
ExecStart=%s
Restart=always
RestartSec=5s
KillMode=process
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`, unit.Name, comp.Metadata.Version, after, wants, serviceType, unit.Exec)

	unitPath := "/etc/systemd/system/" + unit.Name + ".service"
	if err := os.MkdirAll("/etc/systemd/system", 0o755); err != nil {
		return fmt.Errorf("create systemd dir: %w", err)
	}
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", unitPath, err)
	}
	slog.Info("wrote systemd unit", "path", unitPath)
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

var semverRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// versionSatisfied checks if the command output satisfies the ready_condition.
// If ready_condition looks like a semver (e.g. "27.5.1"), it extracts a version
// from output and checks >=. Otherwise, it falls back to substring match.
func versionSatisfied(output, condition string) bool {
	condMatch := semverRe.FindStringSubmatch(condition)
	if condMatch == nil {
		// Not a version string (e.g. "Ready", "Running") — substring match
		return strings.Contains(output, condition)
	}
	outMatch := semverRe.FindStringSubmatch(output)
	if outMatch == nil {
		return strings.Contains(output, condition)
	}
	for i := 1; i <= 3; i++ {
		o, _ := strconv.Atoi(outMatch[i])
		c, _ := strconv.Atoi(condMatch[i])
		if o > c {
			return true
		}
		if o < c {
			return false
		}
	}
	return true // equal
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
		if err == nil && versionSatisfied(string(out), comp.Verify.ReadyCondition) {
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
		unitNames := []string{comp.Metadata.Name}
		if len(comp.Install.SystemdUnits) > 0 {
			unitNames = make([]string, len(comp.Install.SystemdUnits))
			for i, u := range comp.Install.SystemdUnits {
				unitNames[i] = u.Name
			}
		}
		for _, uname := range unitNames {
			out, err := inst.runner.Run(ctx, "systemctl", "is-active", uname)
			if err != nil || strings.TrimSpace(string(out)) != "active" {
				status.Message = fmt.Sprintf("service %s not running; try: sudo systemctl start %s", uname, uname)
				return status
			}
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
	if versionSatisfied(string(out), comp.Verify.ReadyCondition) {
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
		return fmt.Errorf("open source %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create destination %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return out.Close()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writeRegistriesConfig writes container registry mirror config to /etc/rancher/k3s/registries.yaml.
// K3S containerd hot-reloads this file, so no restart is needed.
func writeRegistriesConfig(registries map[string]any) error {
	dir := "/etc/rancher/k3s"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create registries dir: %w", err)
	}

	data, err := yaml.Marshal(registries)
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

func (inst *Installer) writeRegistries(comp knowledge.StackComponent) error {
	return writeRegistriesConfig(comp.Registries)
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
