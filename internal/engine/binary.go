package engine

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
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

// Supports reports whether this source supports the given platform string (e.g. "linux/amd64").
func (s *BinarySource) Supports(platform string) bool {
	for _, p := range s.Platforms {
		if p == platform {
			return true
		}
	}
	return false
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

	if err := m.download(ctx, url, mirrorURL, m.distDir, name); err != nil {
		return "", fmt.Errorf("download %s: %w", name, err)
	}

	// Binary should now be in distDir
	for _, c := range candidates {
		p := filepath.Join(m.distDir, c)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("binary %s not found in %s after download", name, m.distDir)
}

// Download forces download of the binary for the current platform,
// regardless of whether it already exists in distDir or PATH.
func (m *BinaryManager) Download(ctx context.Context, source *BinarySource) error {
	if source == nil {
		return fmt.Errorf("no binary source configured")
	}

	platform := goruntime.GOOS + "/" + goruntime.GOARCH
	if !platformSupported(platform, source.Platforms) {
		return fmt.Errorf("platform %s not supported (available: %v)", platform, source.Platforms)
	}

	url := source.Download[platform]
	mirrorURL := source.Mirror[platform]
	if url == "" && mirrorURL == "" {
		return fmt.Errorf("no download URL for platform %s", platform)
	}

	return m.download(ctx, url, mirrorURL, m.distDir, source.Binary)
}

// download tries primary URL first, then mirror. Extracts zip/tar.gz archives.
func (m *BinaryManager) download(ctx context.Context, url, mirrorURL, destDir, binaryName string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
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
		slog.Info("downloading engine binary", "url", u, "dest", destDir)
		if err := downloadAndExtract(ctx, u, destDir, binaryName); err != nil {
			slog.Warn("download failed, trying next source", "url", u, "error", err)
			lastErr = err
			continue
		}

		// Make binary executable on non-Windows
		if goruntime.GOOS != "windows" {
			for _, c := range binaryCandidates(binaryName) {
				p := filepath.Join(destDir, c)
				if _, err := os.Stat(p); err == nil {
					os.Chmod(p, 0o755)
					break
				}
			}
		}

		slog.Info("engine binary ready", "dir", destDir, "binary", binaryName)
		return nil
	}

	return fmt.Errorf("all download sources failed: %w", lastErr)
}

// downloadAndExtract downloads url to a temp file then extracts or renames it.
func downloadAndExtract(ctx context.Context, url, destDir, binaryName string) error {
	tmpFile, err := os.CreateTemp(destDir, ".download-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpPath)
	}()

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

	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmpFile, h), resp.Body)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	tmpFile.Close()

	slog.Info("download complete", "bytes", written, "sha256", fmt.Sprintf("%x", h.Sum(nil))[:16])

	// Detect format from URL and extract
	urlLower := strings.ToLower(url)
	switch {
	case strings.HasSuffix(urlLower, ".tar.gz") || strings.HasSuffix(urlLower, ".tgz"):
		return extractTarGz(tmpPath, destDir)
	case strings.HasSuffix(urlLower, ".zip"):
		return extractZip(tmpPath, destDir)
	default:
		// Plain binary — rename directly
		binPath := filepath.Join(destDir, binaryName)
		if goruntime.GOOS == "windows" && !strings.HasSuffix(binPath, ".exe") {
			binPath += ".exe"
		}
		return os.Rename(tmpPath, binPath)
	}
}

// extractZip extracts a zip archive to destDir, stripping a common top-level directory.
func extractZip(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	prefix := zipCommonPrefix(r.File)

	for _, f := range r.File {
		relName := strings.TrimPrefix(f.Name, prefix)
		if relName == "" || strings.HasSuffix(relName, "/") {
			continue // skip directories
		}

		// Prevent path traversal
		cleaned := filepath.Clean(filepath.FromSlash(relName))
		if strings.HasPrefix(cleaned, "..") {
			continue
		}

		destPath := filepath.Join(destDir, cleaned)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// zipCommonPrefix returns the single top-level directory prefix shared by all entries, or "".
func zipCommonPrefix(files []*zip.File) string {
	if len(files) == 0 {
		return ""
	}
	prefix := ""
	for _, f := range files {
		parts := strings.SplitN(f.Name, "/", 2)
		if len(parts) < 2 {
			return "" // file at root
		}
		candidate := parts[0] + "/"
		if prefix == "" {
			prefix = candidate
		} else if candidate != prefix {
			return "" // multiple top-level dirs
		}
	}
	return prefix
}

// extractTarGz extracts a .tar.gz archive to destDir, stripping a common top-level directory.
func extractTarGz(archivePath, destDir string) error {
	// Two passes: first detect common prefix, then extract.
	prefix, err := tarGzCommonPrefix(archivePath)
	if err != nil {
		return fmt.Errorf("detect archive prefix: %w", err)
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tar.gz: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}

		name := normalizeTarPath(hdr.Name)
		name = strings.TrimPrefix(name, prefix)
		if name == "" {
			continue
		}

		// Prevent path traversal
		cleaned := filepath.Clean(filepath.FromSlash(name))
		if strings.HasPrefix(cleaned, "..") {
			continue
		}

		destPath := filepath.Join(destDir, cleaned)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return err
		}

		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// tarGzCommonPrefix reads archive headers to find a shared top-level directory prefix.
// It handles archives with or without explicit directory entries, and with leading "./".
func tarGzCommonPrefix(archivePath string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	prefix := ""
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		name := normalizeTarPath(hdr.Name)
		if name == "" {
			continue // skip "." or empty entries
		}

		idx := strings.Index(name, "/")
		if idx < 0 {
			// Entry is at root level (bare filename or bare dirname with no slash)
			if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA {
				return "", nil // regular file at root: no prefix to strip
			}
			continue // root-level directory entry: skip, look at file entries
		}

		// Entry is inside a subdirectory
		candidate := name[:idx+1] // e.g. "llama-b8149/"
		if prefix == "" {
			prefix = candidate
		} else if candidate != prefix {
			return "", nil // multiple top-level dirs: no common prefix
		}
	}
	return prefix, nil
}

// normalizeTarPath strips leading "./" sequences and trailing "/" from a tar entry name.
func normalizeTarPath(name string) string {
	for strings.HasPrefix(name, "./") {
		name = name[2:]
	}
	return strings.TrimRight(name, "/")
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
