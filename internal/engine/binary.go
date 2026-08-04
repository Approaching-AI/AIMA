package engine

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
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
	Binary       string              // e.g. "llama-server"
	Platforms    []string            // e.g. ["linux/amd64", "darwin/arm64"]
	Download     map[string]string   // platform -> URL
	Mirror       map[string][]string // platform -> mirror URLs (tried in order)
	SHA256       map[string]string   // platform -> expected hex digest (optional)
	InstallType  string              // e.g. "preinstalled"
	ProbePaths   []string            // explicit binary paths for pre-installed engines
	LocalBundles []string            // local archive/binary/dir paths used before network download
}

// Supports reports whether this source supports the given platform string (e.g. "linux/amd64").
func (s *BinarySource) Supports(platform string) bool {
	if s == nil {
		return false
	}
	if s.InstallType == "preinstalled" && len(s.Platforms) == 0 {
		return true
	}
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
	path, _, err := m.Ensure(ctx, source, nil)
	return path, err
}

// ResolveInstalled resolves only a locally installed, provenance-verified
// binary. It never downloads and is used to implement --no-pull safely.
func (m *BinaryManager) ResolveInstalled(source *BinarySource) (string, error) {
	if source == nil {
		return "", fmt.Errorf("no binary source configured")
	}
	platform := goruntime.GOOS + "/" + goruntime.GOARCH
	if !source.Supports(platform) {
		return "", fmt.Errorf("platform %s not supported (available: %v)", platform, source.Platforms)
	}
	if path, ok := m.findExisting(source); ok {
		return path, nil
	}
	return "", fmt.Errorf("binary %s is not installed with verified provenance", source.Binary)
}

// Ensure makes a native engine binary available for the current platform.
// It reuses an existing binary from distDir / probe paths / PATH when possible,
// and downloads it only when missing.
func (m *BinaryManager) Ensure(ctx context.Context, source *BinarySource, onProgress func(ProgressEvent)) (string, bool, error) {
	if source == nil {
		return "", false, fmt.Errorf("no binary source configured")
	}

	platform := goruntime.GOOS + "/" + goruntime.GOARCH
	if !source.Supports(platform) {
		return "", false, fmt.Errorf("platform %s not supported (available: %v)", platform, source.Platforms)
	}

	name := source.Binary
	if path, ok := m.findExisting(source); ok {
		return path, false, nil
	}

	if source.InstallType == "preinstalled" {
		return "", false, fmt.Errorf("preinstalled engine binary not found (probe paths: %v)", source.ProbePaths)
	}

	expectedSHA256 := source.SHA256[platform]
	var localErr error
	if installed, err := m.installFromLocalBundles(ctx, source, expectedSHA256, onProgress); installed {
		if path, ok := m.findExisting(source); ok {
			return path, true, nil
		}
		return "", true, fmt.Errorf("binary %s not found in %s after installing local bundle", name, m.distDir)
	} else if err != nil {
		localErr = err
		slog.Warn("local engine bundle install failed, falling back to download", "binary", name, "error", err)
	}

	url := source.Download[platform]
	mirrorURLs := source.Mirror[platform]
	if url == "" && len(mirrorURLs) == 0 {
		if localErr != nil {
			return "", false, localErr
		}
		return "", false, fmt.Errorf("no download URL for platform %s", platform)
	}

	if err := m.download(ctx, url, mirrorURLs, m.installDir(source), name, expectedSHA256, onProgress); err != nil {
		return "", true, fmt.Errorf("download %s: %w", name, err)
	}

	if path, ok := m.findExisting(source); ok {
		return path, true, nil
	}

	return "", true, fmt.Errorf("binary %s not found in %s after download", name, m.distDir)
}

// Download forces download of the binary for the current platform,
// regardless of whether it already exists in distDir or PATH.
// onProgress is called with download progress events (may be nil).
func (m *BinaryManager) Download(ctx context.Context, source *BinarySource, onProgress func(ProgressEvent)) error {
	if source == nil {
		return fmt.Errorf("no binary source configured")
	}

	platform := goruntime.GOOS + "/" + goruntime.GOARCH
	if !source.Supports(platform) {
		return fmt.Errorf("platform %s not supported (available: %v)", platform, source.Platforms)
	}
	if source.InstallType == "preinstalled" {
		return fmt.Errorf("engine is preinstalled on this host; no downloadable artifact is configured")
	}

	expectedSHA256 := source.SHA256[platform]
	var localErr error
	if installed, err := m.installFromLocalBundles(ctx, source, expectedSHA256, onProgress); installed {
		return nil
	} else if err != nil {
		localErr = err
		slog.Warn("local engine bundle install failed, falling back to download", "binary", source.Binary, "error", err)
	}

	url := source.Download[platform]
	mirrorURLs := source.Mirror[platform]
	if url == "" && len(mirrorURLs) == 0 {
		if localErr != nil {
			return localErr
		}
		return fmt.Errorf("no download URL for platform %s", platform)
	}

	return m.download(ctx, url, mirrorURLs, m.installDir(source), source.Binary, expectedSHA256, onProgress)
}

func (m *BinaryManager) findExisting(source *BinarySource) (string, bool) {
	if source == nil {
		return "", false
	}

	expectedSHA256 := source.SHA256[goruntime.GOOS+"/"+goruntime.GOARCH]
	if strings.TrimSpace(expectedSHA256) != "" {
		installDir := m.installDir(source)
		if path, ok := findBinaryInDir(installDir, source.Binary); ok && verifyBundleProvenance(installDir, path, expectedSHA256) == nil {
			return path, true
		}
		return "", false
	}

	if source.Binary != "" {
		for _, c := range binaryCandidates(source.Binary) {
			p := filepath.Join(m.distDir, c)
			if _, err := os.Stat(p); err == nil {
				return p, true
			}
		}
	}

	for _, p := range source.ProbePaths {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}

	if source.Binary != "" {
		if p, err := lookPath(source.Binary); err == nil {
			return p, true
		}
	}

	return "", false
}

func (m *BinaryManager) installDir(source *BinarySource) string {
	if source == nil {
		return m.distDir
	}
	expected := strings.ToLower(strings.TrimSpace(source.SHA256[goruntime.GOOS+"/"+goruntime.GOARCH]))
	if expected == "" {
		return m.distDir
	}
	key := sha256.Sum256([]byte(filepath.ToSlash(source.Binary)))
	return filepath.Join(m.distDir, ".aima", "bundles", fmt.Sprintf("%x", key[:8]), expected)
}

// ImportBundle installs a native engine archive, directory, or single binary into
// the manager's dist directory. It is used by air-gapped deployments and by
// `aima engine import` for native runtimes.
func (m *BinaryManager) ImportBundle(ctx context.Context, bundlePath, binaryName string, onProgress func(ProgressEvent)) error {
	if strings.TrimSpace(bundlePath) == "" {
		return fmt.Errorf("bundle path is required")
	}
	if err := os.MkdirAll(m.distDir, 0o755); err != nil {
		return fmt.Errorf("create dist dir: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if onProgress != nil {
		onProgress(ProgressEvent{Phase: "importing", Message: "installing local engine bundle"})
	}
	stageDir, err := os.MkdirTemp(filepath.Dir(m.distDir), ".aima-import-stage-*")
	if err != nil {
		return fmt.Errorf("create import staging directory: %w", err)
	}
	defer os.RemoveAll(stageDir)
	if err := installLocalBundle(bundlePath, stageDir, binaryName, onProgress); err != nil {
		return err
	}
	finalizeNativeDist(stageDir, binaryName)
	if binaryName != "" {
		if _, ok := findBinaryInDir(stageDir, binaryName); !ok {
			return fmt.Errorf("binary %s not found after staging local bundle", binaryName)
		}
	}
	if err := mergeStagedDir(stageDir, m.distDir); err != nil {
		return fmt.Errorf("promote imported engine bundle: %w", err)
	}
	if onProgress != nil {
		onProgress(ProgressEvent{Phase: "complete", Message: "engine binary ready"})
	}
	return nil
}

func (m *BinaryManager) installFromLocalBundles(ctx context.Context, source *BinarySource, expectedSHA256 string, onProgress func(ProgressEvent)) (bool, error) {
	if source == nil || len(source.LocalBundles) == 0 {
		return false, nil
	}
	var lastErr error
	for _, candidate := range source.LocalBundles {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := verifyLocalBundleSHA256(candidate, expectedSHA256); err != nil {
			lastErr = err
			slog.Warn("local engine bundle verification failed", "path", candidate, "error", err)
			continue
		}
		installDir := m.installDir(source)
		slog.Info("installing engine binary from local bundle", "path", candidate, "dest", installDir)
		var installErr error
		if expectedSHA256 != "" {
			installErr = installVersionedBundle(candidate, installDir, source.Binary, expectedSHA256, onProgress)
		} else {
			installErr = m.ImportBundle(ctx, candidate, source.Binary, onProgress)
		}
		if installErr != nil {
			lastErr = installErr
			slog.Warn("local engine bundle failed", "path", candidate, "error", installErr)
			continue
		}
		return true, nil
	}
	if lastErr != nil {
		return false, fmt.Errorf("all local engine bundles failed: %w", lastErr)
	}
	return false, nil
}

func verifyLocalBundleSHA256(bundlePath, expectedSHA256 string) error {
	expectedSHA256 = normalizeSHA256(expectedSHA256)
	if expectedSHA256 == "" {
		return nil
	}
	info, err := os.Stat(bundlePath)
	if err != nil {
		return fmt.Errorf("stat local engine bundle %s: %w", bundlePath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("verify local engine bundle %s: sha256 applies only to files", bundlePath)
	}
	actualSHA256, err := fileSHA256(bundlePath)
	if err != nil {
		return fmt.Errorf("verify local engine bundle %s: %w", bundlePath, err)
	}
	if !strings.EqualFold(actualSHA256, expectedSHA256) {
		return fmt.Errorf("sha256 mismatch for %s: expected %s, got %s", bundlePath, expectedSHA256, actualSHA256)
	}
	return nil
}

type bundleProvenance struct {
	SourceSHA256 string `json:"source_sha256"`
	BinaryPath   string `json:"binary_path"`
	BinarySHA256 string `json:"binary_sha256"`
}

const bundleProvenanceFile = ".aima-provenance.json"

func findBinaryInDir(dir, binaryName string) (string, bool) {
	for _, candidate := range binaryCandidates(binaryName) {
		path := filepath.Join(dir, candidate)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, true
		}
	}
	return "", false
}

func writeBundleProvenance(dir, binaryName, sourceSHA256 string) error {
	binaryPath, ok := findBinaryInDir(dir, binaryName)
	if !ok {
		return fmt.Errorf("binary %s not found in staged bundle", binaryName)
	}
	binarySHA256, err := fileSHA256(binaryPath)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(dir, binaryPath)
	if err != nil {
		return err
	}
	data, err := json.Marshal(bundleProvenance{
		SourceSHA256: strings.ToLower(strings.TrimSpace(sourceSHA256)),
		BinaryPath:   filepath.ToSlash(rel),
		BinarySHA256: binarySHA256,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, bundleProvenanceFile), data, 0o644)
}

func verifyBundleProvenance(dir, binaryPath, expectedSourceSHA256 string) error {
	data, err := os.ReadFile(filepath.Join(dir, bundleProvenanceFile))
	if err != nil {
		return err
	}
	var provenance bundleProvenance
	if err := json.Unmarshal(data, &provenance); err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(provenance.SourceSHA256), strings.TrimSpace(expectedSourceSHA256)) {
		return fmt.Errorf("source sha256 provenance mismatch")
	}
	rel, err := filepath.Rel(dir, binaryPath)
	if err != nil || filepath.ToSlash(rel) != provenance.BinaryPath {
		return fmt.Errorf("binary provenance path mismatch")
	}
	actual, err := fileSHA256(binaryPath)
	if err != nil {
		return err
	}
	return verifySHA256(provenance.BinarySHA256, actual, binaryPath)
}

func installVersionedBundle(bundlePath, installDir, binaryName, expectedSHA256 string, onProgress func(ProgressEvent)) error {
	if err := os.MkdirAll(filepath.Dir(installDir), 0o755); err != nil {
		return err
	}
	stageDir, err := os.MkdirTemp(filepath.Dir(installDir), ".stage-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)
	if err := installLocalBundle(bundlePath, stageDir, binaryName, onProgress); err != nil {
		return err
	}
	finalizeNativeDist(stageDir, binaryName)
	if err := writeBundleProvenance(stageDir, binaryName, expectedSHA256); err != nil {
		return err
	}
	return promoteVersionedBundle(stageDir, installDir)
}

func promoteVersionedBundle(stageDir, installDir string) error {
	backupDir := ""
	if _, err := os.Stat(installDir); err == nil {
		backupDir = installDir + fmt.Sprintf(".backup-%d", time.Now().UnixNano())
		if err := os.Rename(installDir, backupDir); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(stageDir, installDir); err != nil {
		if backupDir != "" {
			_ = os.Rename(backupDir, installDir)
		}
		return err
	}
	if backupDir != "" {
		_ = os.RemoveAll(backupDir)
	}
	return nil
}

func fileSHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
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

func normalizeSHA256(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "sha256:") {
		return strings.TrimSpace(value[len("sha256:"):])
	}
	return value
}

func verifySHA256(expected, actual, source string) error {
	expected = normalizeSHA256(expected)
	if expected == "" {
		return nil
	}
	if !strings.EqualFold(expected, strings.TrimSpace(actual)) {
		return fmt.Errorf("sha256 mismatch for %s: expected %s, got %s", source, expected, actual)
	}
	return nil
}

func installLocalBundle(bundlePath, destDir, binaryName string, onProgress func(ProgressEvent)) error {
	info, err := os.Stat(bundlePath)
	if err != nil {
		return fmt.Errorf("stat local engine bundle %s: %w", bundlePath, err)
	}
	if info.IsDir() {
		return copyDirContents(bundlePath, destDir)
	}
	lower := strings.ToLower(bundlePath)
	switch {
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		if onProgress != nil {
			onProgress(ProgressEvent{Phase: "extracting", Message: "extracting local engine archive"})
		}
		return extractTarGz(bundlePath, destDir)
	case strings.HasSuffix(lower, ".tar.zst") || strings.HasSuffix(lower, ".tzst"):
		if onProgress != nil {
			onProgress(ProgressEvent{Phase: "extracting", Message: "extracting local engine archive"})
		}
		return extractTarZst(bundlePath, destDir)
	case strings.HasSuffix(lower, ".zip"):
		if onProgress != nil {
			onProgress(ProgressEvent{Phase: "extracting", Message: "extracting local engine archive"})
		}
		return extractZip(bundlePath, destDir)
	default:
		destName := filepath.Base(bundlePath)
		if binaryName != "" && !isBinaryCandidate(destName, binaryName) {
			destName = binaryName
			if goruntime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(destName), ".exe") {
				destName += ".exe"
			}
		}
		return copyFile(bundlePath, filepath.Join(destDir, destName), info.Mode())
	}
}

func finalizeNativeDist(destDir, binaryName string) {
	if goruntime.GOOS != "windows" {
		for _, c := range binaryCandidates(binaryName) {
			if c == "" {
				continue
			}
			p := filepath.Join(destDir, c)
			if _, err := os.Stat(p); err == nil {
				_ = os.Chmod(p, 0o755)
				break
			}
		}
		createSoSymlinks(destDir)
	}
}

func isBinaryCandidate(name, binaryName string) bool {
	if binaryName == "" {
		return true
	}
	for _, candidate := range binaryCandidates(binaryName) {
		if strings.EqualFold(name, candidate) {
			return true
		}
	}
	return false
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", filepath.Dir(dst), err)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dst, err)
	}
	return nil
}

func copyDirContents(srcDir, dstDir string) error {
	srcClean := filepath.Clean(srcDir)
	return filepath.WalkDir(srcClean, func(srcPath string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcClean, srcPath)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dstPath := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(srcPath, dstPath, info.Mode())
	})
}

// download tries mirror URLs first, then primary. Extracts zip/tar.gz archives.
func (m *BinaryManager) download(ctx context.Context, url string, mirrorURLs []string, destDir, binaryName, expectedSHA256 string, onProgress func(ProgressEvent)) error {
	downloadDir := destDir
	if strings.TrimSpace(expectedSHA256) != "" {
		downloadDir = filepath.Dir(destDir)
	}
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
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
		actualSHA256, err := downloadAndExtract(ctx, u, destDir, binaryName, expectedSHA256, onProgress)
		if err != nil {
			slog.Warn("download failed, trying next source", "url", u, "error", err)
			lastErr = err
			continue
		}

		if normalizeSHA256(expectedSHA256) != "" {
			slog.Info("sha256 verified", "sha256", actualSHA256)
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
		if onProgress != nil {
			onProgress(ProgressEvent{Phase: "complete", Message: "engine binary ready"})
		}
		return nil
	}

	return fmt.Errorf("all download sources failed: %w", lastErr)
}

func buildDownloadSourceList(primary string, mirrors []string) []string {
	urls := make([]string, 0, len(mirrors)+1+4)
	urls = append(urls, envMirrorURLs(primary)...)
	urls = append(urls, mirrors...)
	if primary != "" {
		urls = append(urls, primary)
	}
	return uniqueNonEmpty(urls)
}

func envMirrorURLs(primary string) []string {
	if primary == "" {
		return nil
	}
	filename := downloadFileName(primary)
	var out []string
	for _, base := range splitCommaEnv("AIMA_ENGINE_MIRROR_BASE", "AIMA_ENGINE_MIRROR") {
		if filename == "" {
			continue
		}
		out = append(out, joinURLPath(base, filename))
	}
	for _, tmpl := range splitCommaEnv("AIMA_ENGINE_MIRROR_TEMPLATE") {
		repl := strings.NewReplacer(
			"{url}", primary,
			"{escaped_url}", url.QueryEscape(primary),
			"{filename}", filename,
		)
		out = append(out, repl.Replace(tmpl))
	}
	for _, rule := range splitCommaEnv("AIMA_ENGINE_URL_REWRITE") {
		from, to, ok := strings.Cut(rule, "=>")
		if !ok {
			from, to, ok = strings.Cut(rule, "=")
		}
		from = strings.TrimSpace(from)
		to = strings.TrimSpace(to)
		if ok && from != "" && strings.HasPrefix(primary, from) {
			out = append(out, to+strings.TrimPrefix(primary, from))
		}
	}
	return uniqueNonEmpty(out)
}

func splitCommaEnv(names ...string) []string {
	var values []string
	for _, name := range names {
		raw := os.Getenv(name)
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == '\n'
		}) {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				values = append(values, trimmed)
			}
		}
	}
	return values
}

func downloadFileName(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err == nil && parsed.Path != "" {
		return path.Base(parsed.Path)
	}
	return path.Base(rawURL)
}

func joinURLPath(base, elem string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	elem = strings.TrimLeft(strings.TrimSpace(elem), "/")
	if base == "" {
		return elem
	}
	if elem == "" {
		return base
	}
	return base + "/" + elem
}

func uniqueNonEmpty(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, value := range in {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// downloadAndExtract downloads url to a temp file, verifies it, then extracts
// or renames it. No destination file is created before strict verification.
// Returns the SHA256 hex digest of the downloaded content.
func downloadAndExtract(ctx context.Context, url, destDir, binaryName, expectedSHA256 string, onProgress func(ProgressEvent)) (string, error) {
	tempParent := destDir
	if strings.TrimSpace(expectedSHA256) != "" {
		tempParent = filepath.Dir(destDir)
	}
	tmpFile, err := os.CreateTemp(tempParent, ".download-*")
	if err != nil {
		return "", fmt.Errorf("create temp file in %s: %w", destDir, err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpPath)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create HTTP request for %s: %w", url, err)
	}

	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	h := sha256.New()

	// Wrap body with progress tracking
	var body io.Reader = resp.Body
	if onProgress != nil {
		tracker := newProgressTracker(resp.ContentLength)
		body = &progressReader{
			reader: resp.Body,
			onRead: func(n int) {
				ev := tracker.update(tracker.downloaded + int64(n))
				onProgress(ev)
			},
		}
	}
	written, err := io.Copy(io.MultiWriter(tmpFile, h), body)
	if err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("close downloaded file: %w", err)
	}

	actualSHA256 := fmt.Sprintf("%x", h.Sum(nil))
	slog.Info("download complete", "bytes", written, "sha256", actualSHA256[:16])
	expectedSHA256 = normalizeSHA256(expectedSHA256)
	if expectedSHA256 != "" && !strings.EqualFold(actualSHA256, expectedSHA256) {
		return actualSHA256, fmt.Errorf("sha256 mismatch for %s: expected %s, got %s", url, expectedSHA256, actualSHA256)
	}

	stageDir, err := os.MkdirTemp(filepath.Dir(destDir), ".aima-engine-stage-*")
	if err != nil {
		return actualSHA256, fmt.Errorf("create extraction staging directory: %w", err)
	}
	defer os.RemoveAll(stageDir)

	// Detect format from URL and extract
	urlLower := strings.ToLower(downloadFileName(url))
	switch {
	case strings.HasSuffix(urlLower, ".tar.gz") || strings.HasSuffix(urlLower, ".tgz"):
		if onProgress != nil {
			onProgress(ProgressEvent{Phase: "extracting", Message: "extracting archive"})
		}
		err = extractTarGz(tmpPath, stageDir)
	case strings.HasSuffix(urlLower, ".tar.zst") || strings.HasSuffix(urlLower, ".tzst"):
		if onProgress != nil {
			onProgress(ProgressEvent{Phase: "extracting", Message: "extracting archive"})
		}
		err = extractTarZst(tmpPath, stageDir)
	case strings.HasSuffix(urlLower, ".zip"):
		if onProgress != nil {
			onProgress(ProgressEvent{Phase: "extracting", Message: "extracting archive"})
		}
		err = extractZip(tmpPath, stageDir)
	default:
		// Plain binary — move it into the staging tree.
		binPath := filepath.Join(stageDir, binaryName)
		if goruntime.GOOS == "windows" && !strings.HasSuffix(binPath, ".exe") {
			binPath += ".exe"
		}
		if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
			return actualSHA256, fmt.Errorf("create binary directory: %w", err)
		}
		err = os.Rename(tmpPath, binPath)
	}
	if err != nil {
		return actualSHA256, err
	}
	if strings.TrimSpace(expectedSHA256) != "" {
		finalizeNativeDist(stageDir, binaryName)
		if err := writeBundleProvenance(stageDir, binaryName, expectedSHA256); err != nil {
			return actualSHA256, err
		}
		if err := promoteVersionedBundle(stageDir, destDir); err != nil {
			return actualSHA256, fmt.Errorf("promote versioned engine bundle: %w", err)
		}
		return actualSHA256, nil
	}
	if err := mergeStagedDir(stageDir, destDir); err != nil {
		return actualSHA256, fmt.Errorf("install staged engine bundle: %w", err)
	}
	return actualSHA256, nil
}

func mergeStagedDir(srcDir, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(srcDir, entry.Name())
		destPath := filepath.Join(destDir, entry.Name())
		info, err := os.Lstat(srcPath)
		if err != nil {
			return err
		}
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			if err := mergeStagedDir(srcPath, destPath); err != nil {
				return err
			}
			if err := os.Remove(srcPath); err != nil {
				return err
			}
			continue
		}
		if existing, err := os.Lstat(destPath); err == nil {
			if existing.IsDir() {
				return fmt.Errorf("cannot replace directory %s with file", destPath)
			}
			if err := os.Remove(destPath); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(srcPath, destPath); err != nil {
			return err
		}
	}
	return nil
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

type tarStreamOpener func() (io.ReadCloser, error)

type archiveReadCloser struct {
	io.Reader
	close func() error
}

func (r *archiveReadCloser) Close() error {
	if r == nil || r.close == nil {
		return nil
	}
	return r.close()
}

// extractTarGz extracts a .tar.gz archive to destDir, stripping a common top-level directory.
func extractTarGz(archivePath, destDir string) error {
	return extractTarArchive(destDir, func() (io.ReadCloser, error) {
		file, err := os.Open(archivePath)
		if err != nil {
			return nil, fmt.Errorf("open tar.gz: %w", err)
		}
		reader, err := gzip.NewReader(file)
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		return &archiveReadCloser{
			Reader: reader,
			close: func() error {
				readerErr := reader.Close()
				fileErr := file.Close()
				if readerErr != nil {
					return readerErr
				}
				return fileErr
			},
		}, nil
	})
}

// extractTarZst extracts a .tar.zst/.tzst archive without requiring a system
// zstd binary or CGO.
func extractTarZst(archivePath, destDir string) error {
	return extractTarArchive(destDir, func() (io.ReadCloser, error) {
		file, err := os.Open(archivePath)
		if err != nil {
			return nil, fmt.Errorf("open tar.zst: %w", err)
		}
		reader, err := zstd.NewReader(file)
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("zstd reader: %w", err)
		}
		return &archiveReadCloser{
			Reader: reader,
			close: func() error {
				reader.Close()
				return file.Close()
			},
		}, nil
	})
}

func extractTarArchive(destDir string, open tarStreamOpener) error {
	prefix, err := tarCommonPrefix(open)
	if err != nil {
		return fmt.Errorf("detect archive prefix: %w", err)
	}
	reader, err := open()
	if err != nil {
		return err
	}
	defer reader.Close()

	destRoot, err := filepath.Abs(destDir)
	if err != nil {
		return fmt.Errorf("resolve destination: %w", err)
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return fmt.Errorf("create destination: %w", err)
	}

	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		name, err := cleanTarEntryName(header.Name)
		if err != nil {
			return err
		}
		name = strings.TrimPrefix(name, prefix)
		if name == "" {
			continue
		}
		name, err = cleanTarEntryName(name)
		if err != nil {
			return err
		}
		destPath := filepath.Join(destRoot, filepath.FromSlash(name))
		if !pathWithin(destRoot, destPath) {
			return fmt.Errorf("tar path traversal rejected: %q", header.Name)
		}
		if err := rejectSymlinkParents(destRoot, filepath.Dir(destPath)); err != nil {
			return fmt.Errorf("extract %q: %w", header.Name, err)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destPath, header.FileInfo().Mode().Perm()); err != nil {
				return fmt.Errorf("create directory %s: %w", destPath, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return fmt.Errorf("create directory %s: %w", filepath.Dir(destPath), err)
			}
			if err := os.Remove(destPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("replace file %s: %w", destPath, err)
			}
			out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, header.FileInfo().Mode().Perm())
			if err != nil {
				return fmt.Errorf("create file %s: %w", destPath, err)
			}
			_, copyErr := io.Copy(out, tarReader)
			closeErr := out.Close()
			if copyErr != nil {
				return fmt.Errorf("extract file %s: %w", name, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close file %s: %w", name, closeErr)
			}
		case tar.TypeSymlink:
			linkTarget, err := safeSymlinkTarget(destRoot, destPath, header.Linkname)
			if err != nil {
				return fmt.Errorf("extract symlink %q: %w", header.Name, err)
			}
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return fmt.Errorf("create directory for symlink %s: %w", destPath, err)
			}
			if err := os.Remove(destPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("replace symlink %s: %w", destPath, err)
			}
			if err := os.Symlink(linkTarget, destPath); err != nil {
				return fmt.Errorf("create symlink %s: %w", destPath, err)
			}
		case tar.TypeXHeader, tar.TypeXGlobalHeader:
			continue
		default:
			return fmt.Errorf("unsupported tar entry %q (type %d)", header.Name, header.Typeflag)
		}
	}
}

func tarCommonPrefix(open tarStreamOpener) (string, error) {
	reader, err := open()
	if err != nil {
		return "", err
	}
	defer reader.Close()

	tarReader := tar.NewReader(reader)
	prefix := ""
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return prefix, nil
		}
		if err != nil {
			return "", fmt.Errorf("read tar header: %w", err)
		}
		name, err := cleanTarEntryName(header.Name)
		if err != nil {
			return "", err
		}
		if name == "" {
			continue
		}
		index := strings.IndexByte(name, '/')
		if index < 0 {
			if header.Typeflag != tar.TypeDir {
				return "", nil
			}
			continue
		}
		candidate := name[:index+1]
		if prefix == "" {
			prefix = candidate
		} else if candidate != prefix {
			return "", nil
		}
	}
}

func cleanTarEntryName(name string) (string, error) {
	name = normalizeTarPath(name)
	if name == "" || name == "." {
		return "", nil
	}
	if strings.ContainsRune(name, 0) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("tar path traversal rejected: %q", name)
	}
	cleaned := path.Clean(name)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("tar path traversal rejected: %q", name)
	}
	hostPath := filepath.FromSlash(cleaned)
	if filepath.IsAbs(hostPath) || filepath.VolumeName(hostPath) != "" {
		return "", fmt.Errorf("tar path traversal rejected: %q", name)
	}
	return filepath.ToSlash(hostPath), nil
}

func pathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func rejectSymlinkParents(root, parent string) error {
	rel, err := filepath.Rel(root, parent)
	if err != nil || rel == "." {
		return err
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("parent path %s is a symlink", current)
		}
	}
	return nil
}

func safeSymlinkTarget(root, linkPath, target string) (string, error) {
	if strings.TrimSpace(target) == "" || strings.HasPrefix(target, "/") {
		return "", fmt.Errorf("symlink target escapes destination: %q", target)
	}
	hostTarget := filepath.FromSlash(target)
	if filepath.IsAbs(hostTarget) || filepath.VolumeName(hostTarget) != "" {
		return "", fmt.Errorf("symlink target escapes destination: %q", target)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(linkPath), hostTarget))
	if !pathWithin(root, resolved) {
		return "", fmt.Errorf("symlink target escapes destination: %q", target)
	}
	return hostTarget, nil
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
