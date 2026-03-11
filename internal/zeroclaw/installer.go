package zeroclaw

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const releaseBaseURL = "https://github.com/zeroclaw-labs/zeroclaw/releases/latest/download"

// ManagedConfig is the AIMA-managed ZeroClaw runtime configuration.
type ManagedConfig struct {
	Provider string
	Model    string
	APIURL   string
	APIKey   string
}

// platformAsset returns the official release asset name for the current platform.
func platformAsset() (string, error) {
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return "zeroclaw-x86_64-unknown-linux-gnu.tar.gz", nil
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		return "zeroclaw-aarch64-unknown-linux-gnu.tar.gz", nil
	case runtime.GOOS == "darwin" && runtime.GOARCH == "amd64":
		return "zeroclaw-x86_64-apple-darwin.tar.gz", nil
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		return "zeroclaw-aarch64-apple-darwin.tar.gz", nil
	case runtime.GOOS == "windows" && runtime.GOARCH == "amd64":
		return "zeroclaw-x86_64-pc-windows-msvc.zip", nil
	default:
		return "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

func executableName() string {
	if runtime.GOOS == "windows" {
		return "zeroclaw.exe"
	}
	return "zeroclaw"
}

// downloadURL returns the download URL for the current platform.
func downloadURL() (string, error) {
	asset, err := platformAsset()
	if err != nil {
		return "", err
	}
	return releaseBaseURL + "/" + asset, nil
}

// InstalledBinaryPath returns the expected path of the installed ZeroClaw binary
// inside the given destination directory.
func InstalledBinaryPath(destDir string) (string, error) {
	if _, err := platformAsset(); err != nil {
		return "", err
	}
	return filepath.Join(destDir, executableName()), nil
}

// Install downloads the ZeroClaw binary for the current platform.
// Returns the path to the installed binary.
func Install(ctx context.Context, destDir string) (string, error) {
	return InstallWith(ctx, destDir, http.DefaultClient)
}

// InstallWith downloads the ZeroClaw binary using the given HTTP client.
func InstallWith(ctx context.Context, destDir string, client *http.Client) (string, error) {
	return installFrom(ctx, destDir, client, releaseBaseURL)
}

// installFrom downloads from a custom base URL (for testing).
func installFrom(ctx context.Context, destDir string, client *http.Client, baseURL string) (string, error) {
	asset, err := platformAsset()
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/"+asset, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download zeroclaw: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download zeroclaw: HTTP %d", resp.StatusCode)
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create dest dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(destDir, "zeroclaw-download-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	written, err := io.Copy(tmpFile, resp.Body)
	closeErr := tmpFile.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("write download: %w", err)
	}

	const minArchiveSize = 100 * 1024
	if written < minArchiveSize {
		return "", fmt.Errorf("downloaded archive too small (%d bytes), possibly corrupt or an error page", written)
	}

	destPath, err := InstalledBinaryPath(destDir)
	if err != nil {
		return "", err
	}

	if strings.HasSuffix(asset, ".zip") {
		if err := extractZipBinary(tmpPath, destPath); err != nil {
			return "", err
		}
	} else {
		if err := extractTarGzBinary(tmpPath, destPath); err != nil {
			return "", err
		}
	}

	if err := os.Chmod(destPath, 0o755); err != nil && runtime.GOOS != "windows" {
		return "", fmt.Errorf("chmod binary: %w", err)
	}
	return destPath, nil
}

func extractTarGzBinary(archivePath, destPath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Base(hdr.Name)
		if name != "zeroclaw" && name != "zeroclaw.exe" {
			continue
		}
		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return fmt.Errorf("create binary: %w", err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			os.Remove(destPath)
			return fmt.Errorf("extract binary: %w", err)
		}
		if err := out.Close(); err != nil {
			return fmt.Errorf("close binary: %w", err)
		}
		return nil
	}
	return fmt.Errorf("zeroclaw binary not found in archive")
}

func extractZipBinary(archivePath, destPath string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}
	defer zr.Close()

	for _, file := range zr.File {
		name := filepath.Base(file.Name)
		if name != "zeroclaw" && name != "zeroclaw.exe" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return fmt.Errorf("open zip entry: %w", err)
		}
		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return fmt.Errorf("create binary: %w", err)
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			os.Remove(destPath)
			return fmt.Errorf("extract binary: %w", err)
		}
		if err := out.Close(); err != nil {
			rc.Close()
			return fmt.Errorf("close binary: %w", err)
		}
		if err := rc.Close(); err != nil {
			return fmt.Errorf("close zip entry: %w", err)
		}
		return nil
	}
	return fmt.Errorf("zeroclaw binary not found in archive")
}

// WriteManagedConfig writes an AIMA-managed ZeroClaw config into dataDir/zeroclaw/config.toml.
// This keeps ZeroClaw aligned with AIMA's llm.* settings instead of upstream defaults.
func WriteManagedConfig(dataDir string, cfg ManagedConfig) error {
	if dataDir == "" {
		return fmt.Errorf("data dir is required")
	}

	configDir := filepath.Join(dataDir, "zeroclaw")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("create zeroclaw config dir: %w", err)
	}

	var b strings.Builder
	b.WriteString("# Managed by AIMA. Update AIMA llm.* settings instead of editing this file.\n")
	if cfg.Provider != "" {
		b.WriteString("default_provider = ")
		b.WriteString(strconv.Quote(cfg.Provider))
		b.WriteString("\n")
	}
	if cfg.Model != "" {
		b.WriteString("default_model = ")
		b.WriteString(strconv.Quote(cfg.Model))
		b.WriteString("\n")
	}
	if cfg.APIURL != "" {
		b.WriteString("api_url = ")
		b.WriteString(strconv.Quote(cfg.APIURL))
		b.WriteString("\n")
	}
	if cfg.APIKey != "" {
		b.WriteString("api_key = ")
		b.WriteString(strconv.Quote(cfg.APIKey))
		b.WriteString("\n")
	}
	b.WriteString("\n[gateway]\n")
	b.WriteString("host = \"127.0.0.1\"\n")
	b.WriteString("require_pairing = false\n")
	b.WriteString("\n[agent]\n")
	b.WriteString("compact_context = true\n")
	b.WriteString("max_history_messages = 8\n")

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write zeroclaw config: %w", err)
	}
	return nil
}

// EnsureManagedConfig rewrites ZeroClaw's config from a clean upstream default template,
// then overlays the AIMA-managed provider settings on the top-level keys.
func EnsureManagedConfig(ctx context.Context, binaryPath, dataDir string, cfg ManagedConfig) error {
	if dataDir == "" {
		return fmt.Errorf("data dir is required")
	}
	if binaryPath == "" {
		binaryPath = "zeroclaw"
	}

	configDir := filepath.Join(dataDir, "zeroclaw")
	configPath := filepath.Join(configDir, "config.toml")

	content, err := bootstrapDefaultConfig(ctx, binaryPath)
	if err != nil {
		return err
	}

	text := string(content)
	text = setTopLevelConfig(text, "default_provider", cfg.Provider)
	text = setTopLevelConfig(text, "default_model", cfg.Model)
	text = setTopLevelConfig(text, "api_url", cfg.APIURL)
	text = setTopLevelConfig(text, "api_key", cfg.APIKey)
	text = setSectionConfig(text, "gateway", "host", `"127.0.0.1"`)
	text = setSectionConfig(text, "gateway", "require_pairing", "false")
	text = setSectionConfig(text, "agent", "compact_context", "true")
	text = setSectionConfig(text, "agent", "max_history_messages", "8")
	if !strings.HasPrefix(text, "# Managed by AIMA.") {
		text = "# Managed by AIMA. Update AIMA llm.* settings instead of editing this file.\n" + text
	}

	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("create zeroclaw config dir: %w", err)
	}
	if err := os.WriteFile(configPath, []byte(text), 0o600); err != nil {
		return fmt.Errorf("write zeroclaw config: %w", err)
	}
	return nil
}

func bootstrapDefaultConfig(ctx context.Context, binaryPath string) ([]byte, error) {
	tmpDir, err := os.MkdirTemp("", "aima-zeroclaw-config-*")
	if err != nil {
		return nil, fmt.Errorf("create temp config dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.CommandContext(ctx, binaryPath, "config", "--config-dir", tmpDir, "schema")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("bootstrap zeroclaw config: %w", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, "config.toml"))
	if err != nil {
		return nil, fmt.Errorf("read bootstrapped zeroclaw config: %w", err)
	}
	return content, nil
}

func setTopLevelConfig(content, key, value string) string {
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(key) + `\s*=.*$`)
	line := key + " = " + strconv.Quote(value)
	lines := strings.Split(content, "\n")
	insertAt := len(lines)
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			insertAt = i
			break
		}
		if re.MatchString(trimmed) {
			if value == "" {
				lines = append(lines[:i], lines[i+1:]...)
			} else {
				lines[i] = line
			}
			return strings.Join(lines, "\n")
		}
	}
	if value == "" {
		return content
	}
	lines = append(lines[:insertAt], append([]string{line}, lines[insertAt:]...)...)
	return strings.Join(lines, "\n")
}

func setSectionConfig(content, section, key, value string) string {
	sectionHeader := "[" + section + "]"
	linePrefix := key + " ="
	lines := strings.Split(content, "\n")
	sectionIndex := -1
	insertAt := -1

	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == sectionHeader {
			sectionIndex = i
			insertAt = i + 1
			continue
		}
		if sectionIndex >= 0 {
			if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
				break
			}
			if strings.HasPrefix(trimmed, linePrefix) {
				lines[i] = key + " = " + value
				return strings.Join(lines, "\n")
			}
			insertAt = i + 1
		}
	}

	if sectionIndex < 0 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, sectionHeader, key+" = "+value)
		return strings.Join(lines, "\n")
	}

	lines = append(lines[:insertAt], append([]string{key + " = " + value}, lines[insertAt:]...)...)
	return strings.Join(lines, "\n")
}
