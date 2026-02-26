package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
)

// BinaryManager downloads and caches native engine binaries.
type BinaryManager struct {
	distDir string // e.g. ~/.aima/dist/windows-amd64/
}

// NewBinaryManager creates a BinaryManager for the given distribution directory.
func NewBinaryManager(distDir string) *BinaryManager {
	return &BinaryManager{distDir: distDir}
}

// BinarySource describes where to download a native binary.
type BinarySource struct {
	Binary    string            // e.g. "llama-server"
	Platforms []string          // e.g. ["linux/amd64", "darwin/arm64"]
	Download  map[string]string // platform -> URL
	Mirror    map[string]string // platform -> mirror URL
}

// Resolve returns the path to a native engine binary, downloading it if needed.
// Search order: distDir -> PATH -> download from source.
func (m *BinaryManager) Resolve(ctx context.Context, source *BinarySource) (string, error) {
	if source == nil {
		return "", fmt.Errorf("no binary source configured")
	}

	platform := goruntime.GOOS + "/" + goruntime.GOARCH
	if !platformSupported(platform, source.Platforms) {
		return "", fmt.Errorf("platform %s not supported (available: %v)", platform, source.Platforms)
	}

	name := source.Binary
	candidates := binaryCandidates(name)

	// 1. Check distDir
	for _, c := range candidates {
		p := filepath.Join(m.distDir, c)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	// 2. Check PATH
	for _, c := range candidates {
		if p, err := lookPath(c); err == nil {
			return p, nil
		}
	}

	// 3. Download from source
	url := source.Download[platform]
	mirrorURL := source.Mirror[platform]
	if url == "" && mirrorURL == "" {
		return "", fmt.Errorf("no download URL for platform %s", platform)
	}

	destPath := filepath.Join(m.distDir, name)
	if goruntime.GOOS == "windows" && !strings.HasSuffix(destPath, ".exe") {
		destPath += ".exe"
	}

	if err := m.download(ctx, url, mirrorURL, destPath); err != nil {
		return "", fmt.Errorf("download %s: %w", name, err)
	}

	return destPath, nil
}

// download tries primary URL first, then mirror. Supports .tar.gz and .zip archives.
func (m *BinaryManager) download(ctx context.Context, url, mirrorURL, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create dist dir: %w", err)
	}

	urls := []string{}
	if mirrorURL != "" {
		urls = append(urls, mirrorURL) // mirror first (typically faster for CN users)
	}
	if url != "" {
		urls = append(urls, url)
	}

	var lastErr error
	for _, u := range urls {
		slog.Info("downloading engine binary", "url", u, "dest", destPath)
		if err := downloadToFile(ctx, u, destPath); err != nil {
			slog.Warn("download failed, trying next source", "url", u, "error", err)
			lastErr = err
			continue
		}

		// Make executable on non-Windows
		if goruntime.GOOS != "windows" {
			os.Chmod(destPath, 0o755)
		}

		slog.Info("engine binary downloaded", "path", destPath)
		return nil
	}

	return fmt.Errorf("all download sources failed: %w", lastErr)
}

func downloadToFile(ctx context.Context, url, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(destPath), ".download-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpPath) // clean up on failure; on success it's already renamed
	}()

	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmpFile, h), resp.Body)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	tmpFile.Close()

	slog.Info("download complete", "bytes", written, "sha256", fmt.Sprintf("%x", h.Sum(nil))[:16])

	// Atomic rename
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

func platformSupported(platform string, supported []string) bool {
	for _, p := range supported {
		if p == platform {
			return true
		}
	}
	return false
}

func binaryCandidates(name string) []string {
	candidates := []string{name}
	if goruntime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
		candidates = append(candidates, name+".exe")
	}
	return candidates
}

func lookPath(name string) (string, error) {
	pathEnv := os.Getenv("PATH")
	sep := string(os.PathListSeparator)
	for _, dir := range strings.Split(pathEnv, sep) {
		for _, c := range binaryCandidates(name) {
			p := filepath.Join(dir, c)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("%s not found in PATH", name)
}
