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
	"time"
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
	Binary    string              // e.g. "llama-server"
	Platforms []string            // e.g. ["linux/amd64", "darwin/arm64"]
	Download  map[string]string   // platform -> URL
	Mirror    map[string][]string // platform -> mirror URLs (tried in order)
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
	mirrorURLs := source.Mirror[platform]
	if url == "" && len(mirrorURLs) == 0 {
		return "", fmt.Errorf("no download URL for platform %s", platform)
	}

	if err := m.download(ctx, url, mirrorURLs, m.distDir, name); err != nil {
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
	mirrorURLs := source.Mirror[platform]
	if url == "" && len(mirrorURLs) == 0 {
		return fmt.Errorf("no download URL for platform %s", platform)
	}

	return m.download(ctx, url, mirrorURLs, m.distDir, source.Binary)
}

// download tries mirror URLs first, then primary. Extracts zip/tar.gz archives.
func (m *BinaryManager) download(ctx context.Context, url string, mirrorURLs []string, destDir, binaryName string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create dist dir: %w", err)
	}

	// Mirrors first (typically faster for CN users), primary last
	urls := make([]string, 0, len(mirrorURLs)+1)
	urls = append(urls, mirrorURLs...)
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

		// Create missing .so.X → .so.X.Y.Z symlinks so dlopen finds versioned libraries.
		if goruntime.GOOS != "windows" {
			createSoSymlinks(destDir)
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
		return fmt.Errorf("create temp file in %s: %w", destDir, err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpPath)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create HTTP request for %s: %w", url, err)
	}

	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download from %s: %w", url, err)
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
			return fmt.Errorf("create directory %s: %w", filepath.Dir(destPath), err)
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}
		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return fmt.Errorf("create file %s: %w", destPath, err)
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return fmt.Errorf("extract file %s: %w", cleaned, err)
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

		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return fmt.Errorf("create directory %s: %w", filepath.Dir(destPath), err)
			}
			out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode())
			if err != nil {
				return fmt.Errorf("create file %s: %w", destPath, err)
			}
			_, err = io.Copy(out, tr)
			out.Close()
			if err != nil {
				return fmt.Errorf("extract file %s: %w", cleaned, err)
			}
		case tar.TypeSymlink:
			// Validate symlink target: must resolve within destDir
			resolvedTarget := filepath.Clean(filepath.Join(filepath.Dir(destPath), hdr.Linkname))
			if !strings.HasPrefix(resolvedTarget, filepath.Clean(destDir)+string(filepath.Separator)) &&
				resolvedTarget != filepath.Clean(destDir) {
				slog.Warn("skipping symlink with target outside destDir", "link", destPath, "target", hdr.Linkname)
				continue
			}
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return fmt.Errorf("create directory for symlink %s: %w", destPath, err)
			}
			os.Remove(destPath) // remove stale symlink or file if present
			if err := os.Symlink(hdr.Linkname, destPath); err != nil {
				slog.Warn("create symlink", "link", destPath, "target", hdr.Linkname, "error", err)
			}
		}
	}
	return nil
}

// tarGzCommonPrefix reads archive headers to find a shared top-level directory prefix.
// It handles archives with or without explicit directory entries, and with leading "./".
func tarGzCommonPrefix(archivePath string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open archive %s: %w", archivePath, err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("gzip reader for %s: %w", archivePath, err)
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
			return "", fmt.Errorf("read tar header in %s: %w", archivePath, err)
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

// createSoSymlinks scans dir for versioned shared libraries (e.g. libfoo.so.1.2.3)
// and creates the SONAME symlink (libfoo.so.1) if it does not already exist.
// This mirrors what ldconfig does and is needed when extracting tar.gz bundles
// that include .so.X.Y.Z files but not the .so.X symlinks.
func createSoSymlinks(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		soIdx := strings.Index(name, ".so.")
		if soIdx < 0 {
			continue
		}
		rest := name[soIdx+4:] // e.g. "0.0.8149" or "0.9.7"
		if rest == "" || !strings.ContainsAny(rest, ".") {
			continue // already a soname (libfoo.so.1) or no version number
		}
		major := rest
		if dot := strings.Index(rest, "."); dot >= 0 {
			major = rest[:dot]
		}
		soname := name[:soIdx+4+len(major)] // e.g. "libmtmd.so.0"
		symlinkPath := filepath.Join(dir, soname)
		if _, err := os.Lstat(symlinkPath); os.IsNotExist(err) {
			if err := os.Symlink(name, symlinkPath); err == nil {
				slog.Debug("created so symlink", "link", soname, "target", name)
			}
		}
	}
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
