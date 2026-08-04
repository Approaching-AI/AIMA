package engine

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/jguan/aima/internal/knowledge"
	"github.com/klauspost/compress/zstd"
)

// mockRunner implements CommandRunner for tests
type mockRunner struct {
	responses map[string]mockResponse
}

type mockResponse struct {
	output []byte
	err    error
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := name
	for _, a := range args {
		key += " " + a
	}
	if resp, ok := m.responses[key]; ok {
		return resp.output, resp.err
	}
	return nil, fmt.Errorf("command not mocked: %s", key)
}

func (m *mockRunner) Pipe(ctx context.Context, from, to []string) error {
	if _, err := m.Run(ctx, from[0], from[1:]...); err != nil {
		return err
	}
	_, err := m.Run(ctx, to[0], to[1:]...)
	return err
}

func (m *mockRunner) RunStream(ctx context.Context, onLine func(line string), name string, args ...string) error {
	out, err := m.Run(ctx, name, args...)
	if err != nil {
		return err
	}
	if onLine != nil && len(out) > 0 {
		onLine(string(out))
	}
	return nil
}

// --- crictl image list format for tests ---
type crictlImageList struct {
	Images []crictlImage `json:"images"`
}

type crictlImage struct {
	ID          string   `json:"id"`
	RepoTags    []string `json:"repoTags"`
	RepoDigests []string `json:"repoDigests"`
	Size        string   `json:"size"`
}

func TestScanWithCrictl(t *testing.T) {
	images := crictlImageList{
		Images: []crictlImage{
			{
				ID:       "sha256:abc123",
				RepoTags: []string{"vllm/vllm-openai:latest"},
				Size:     "8500000000",
			},
			{
				ID:       "sha256:def456",
				RepoTags: []string{"ghcr.io/ggerganov/llama.cpp:server"},
				Size:     "500000000",
			},
			{
				ID:       "sha256:ghi789",
				RepoTags: []string{"nginx:latest"},
				Size:     "100000000",
			},
		},
	}
	imageJSON, _ := json.Marshal(images)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json": {output: imageJSON},
		},
	}

	engineAssets := map[string][]string{
		"vllm":     {"vllm/vllm-openai"},
		"llamacpp": {"ghcr.io/ggerganov/llama.cpp"},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: engineAssets,
		Runner:        runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 matched engines, got %d", len(results))
	}

	// Check vllm
	var vllm, llamacpp *EngineImage
	for _, r := range results {
		switch r.Type {
		case "vllm":
			vllm = r
		case "llamacpp":
			llamacpp = r
		}
	}

	if vllm == nil {
		t.Fatal("vllm engine not found")
	}
	if vllm.Image != "vllm/vllm-openai" {
		t.Errorf("expected image vllm/vllm-openai, got %s", vllm.Image)
	}
	if vllm.Tag != "latest" {
		t.Errorf("expected tag latest, got %s", vllm.Tag)
	}
	if !vllm.Available {
		t.Error("expected vllm to be available")
	}

	if llamacpp == nil {
		t.Fatal("llamacpp engine not found")
	}
	if llamacpp.Tag != "server" {
		t.Errorf("expected tag server, got %s", llamacpp.Tag)
	}
}

func TestScanK3sCrictlFallback(t *testing.T) {
	// When standalone crictl is not available, scanner should try k3s crictl
	images := crictlImageList{
		Images: []crictlImage{
			{
				ID:       "sha256:abc123",
				RepoTags: []string{"vllm/vllm-openai:qwen3_5-cu130"},
				Size:     "8800000000",
			},
		},
	}
	imageJSON, _ := json.Marshal(images)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json":     {err: fmt.Errorf("crictl not found")},
			"k3s crictl images -o json": {output: imageJSON},
		},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: map[string][]string{"vllm-nightly": {"vllm/vllm-openai:qwen3_5"}},
		Runner:        runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 engine, got %d", len(results))
	}
	if results[0].Type != "vllm-nightly" {
		t.Errorf("expected type vllm-nightly, got %s", results[0].Type)
	}
	if results[0].Tag != "qwen3_5-cu130" {
		t.Errorf("expected tag qwen3_5-cu130, got %s", results[0].Tag)
	}
}

func TestScanTagAwarePatternPriority(t *testing.T) {
	// Tag-aware patterns should take priority over repo-only patterns.
	// vllm/vllm-openai:qwen3_5-cu130 should match vllm-nightly (tag pattern)
	// not vllm (repo pattern "vllm/"), even though both could match.
	images := crictlImageList{
		Images: []crictlImage{
			{
				ID:       "sha256:abc123",
				RepoTags: []string{"vllm/vllm-openai:qwen3_5-cu130"},
				Size:     "8800000000",
			},
			{
				ID:       "sha256:def456",
				RepoTags: []string{"vllm/vllm-openai:v0.8.5"},
				Size:     "9000000000",
			},
		},
	}
	imageJSON, _ := json.Marshal(images)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json": {output: imageJSON},
		},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: map[string][]string{
			"vllm":         {"vllm/vllm-openai"},
			"vllm-nightly": {"vllm/vllm-openai:qwen3_5"},
		},
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 engines, got %d", len(results))
	}

	var nightly, stable *EngineImage
	for _, r := range results {
		switch r.Type {
		case "vllm-nightly":
			nightly = r
		case "vllm":
			stable = r
		}
	}

	if nightly == nil {
		t.Fatal("vllm-nightly engine not found")
	}
	if nightly.Tag != "qwen3_5-cu130" {
		t.Errorf("expected nightly tag qwen3_5-cu130, got %s", nightly.Tag)
	}

	if stable == nil {
		t.Fatal("vllm engine not found")
	}
	if stable.Tag != "v0.8.5" {
		t.Errorf("expected stable tag v0.8.5, got %s", stable.Tag)
	}
}

func TestBinaryManagerEnsureReusesExistingDistBinary(t *testing.T) {
	t.Parallel()

	distDir := t.TempDir()
	binaryPath := filepath.Join(distDir, "llama-server")
	if err := os.WriteFile(binaryPath, []byte("bin"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	mgr := NewBinaryManager(distDir)
	source := &BinarySource{
		Binary:    "llama-server",
		Platforms: []string{goruntime.GOOS + "/" + goruntime.GOARCH},
	}

	path, downloaded, err := mgr.Ensure(context.Background(), source, nil)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if downloaded {
		t.Fatal("Ensure should reuse an existing dist binary")
	}
	if path != binaryPath {
		t.Fatalf("Ensure path = %q, want %q", path, binaryPath)
	}
}

func TestBinaryManagerEnsureUsesProbePathsForPreinstalledEngine(t *testing.T) {
	t.Parallel()

	distDir := t.TempDir()
	probeDir := t.TempDir()
	probePath := filepath.Join(probeDir, "vllm")
	if err := os.WriteFile(probePath, []byte("bin"), 0o755); err != nil {
		t.Fatalf("write probe binary: %v", err)
	}

	mgr := NewBinaryManager(distDir)
	source := &BinarySource{
		InstallType: "preinstalled",
		ProbePaths:  []string{probePath},
	}

	path, downloaded, err := mgr.Ensure(context.Background(), source, nil)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if downloaded {
		t.Fatal("Ensure should not download a preinstalled engine")
	}
	if path != probePath {
		t.Fatalf("Ensure path = %q, want %q", path, probePath)
	}
}

func TestBinaryManagerEnsureInstallsLocalZipBundle(t *testing.T) {
	t.Parallel()

	distDir := t.TempDir()
	bundleDir := t.TempDir()
	binaryName := "llama-server"
	binaryFile := binaryName
	if goruntime.GOOS == "windows" {
		binaryFile += ".exe"
	}
	archivePath := filepath.Join(bundleDir, "llama-runtime.zip")
	zipFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(zipFile)
	header := &zip.FileHeader{Name: "llama-runtime/" + binaryFile, Method: zip.Deflate}
	header.SetMode(0o755)
	w, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := w.Write([]byte("bin")); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := zipFile.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	mgr := NewBinaryManager(distDir)
	source := &BinarySource{
		Binary:       binaryName,
		Platforms:    []string{goruntime.GOOS + "/" + goruntime.GOARCH},
		LocalBundles: []string{archivePath},
	}

	path, downloaded, err := mgr.Ensure(context.Background(), source, nil)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !downloaded {
		t.Fatal("Ensure should report local bundle installation")
	}
	want := filepath.Join(distDir, binaryFile)
	if path != want {
		t.Fatalf("Ensure path = %q, want %q", path, want)
	}
	if data, err := os.ReadFile(want); err != nil || string(data) != "bin" {
		t.Fatalf("installed binary = %q, %v; want bin", data, err)
	}
}

type testTarEntry struct {
	name     string
	body     string
	mode     int64
	typeflag byte
	linkname string
}

func writeTarZst(t *testing.T, archivePath string, entries []testTarEntry) {
	t.Helper()

	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create tar.zst: %v", err)
	}
	encoder, err := zstd.NewWriter(file)
	if err != nil {
		file.Close()
		t.Fatalf("create zstd writer: %v", err)
	}
	tw := tar.NewWriter(encoder)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{
			Name:     entry.name,
			Mode:     mode,
			Typeflag: typeflag,
			Linkname: entry.linkname,
		}
		if typeflag == tar.TypeReg || typeflag == tar.TypeRegA {
			header.Size = int64(len(entry.body))
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write tar header %q: %v", entry.name, err)
		}
		if header.Size > 0 {
			if _, err := io.Copy(tw, strings.NewReader(entry.body)); err != nil {
				t.Fatalf("write tar body %q: %v", entry.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatalf("close zstd writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close tar.zst: %v", err)
	}
}

func TestBinaryManagerEnsureInstallsLocalTarZstBundle(t *testing.T) {
	t.Parallel()

	distDir := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "aima-engine.tar.zst")
	writeTarZst(t, archivePath, []testTarEntry{{
		name: "aima-engine-native/bin/aima-engine",
		body: "portable-bin",
		mode: 0o755,
	}})

	mgr := NewBinaryManager(distDir)
	source := &BinarySource{
		Binary:       "bin/aima-engine",
		Platforms:    []string{goruntime.GOOS + "/" + goruntime.GOARCH},
		LocalBundles: []string{archivePath},
	}

	got, installed, err := mgr.Ensure(context.Background(), source, nil)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !installed {
		t.Fatal("Ensure should report local tar.zst installation")
	}
	want := filepath.Join(distDir, "bin", "aima-engine")
	if got != want {
		t.Fatalf("Ensure path = %q, want %q", got, want)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat installed binary: %v", err)
	}
	if goruntime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("installed mode = %o, want executable", info.Mode().Perm())
	}
}

func TestExtractTarZstStripsCommonPrefixAndPreservesMode(t *testing.T) {
	t.Parallel()

	archivePath := filepath.Join(t.TempDir(), "runtime.tzst")
	writeTarZst(t, archivePath, []testTarEntry{
		{name: "runtime/bin/aima-engine", body: "bin", mode: 0o755},
		{name: "runtime/lib/libengine.so.1", body: "so", mode: 0o644},
	})
	destDir := t.TempDir()

	if err := extractTarZst(archivePath, destDir); err != nil {
		t.Fatalf("extractTarZst: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, "runtime")); !os.IsNotExist(err) {
		t.Fatalf("common prefix was not stripped: %v", err)
	}
	info, err := os.Stat(filepath.Join(destDir, "bin", "aima-engine"))
	if err != nil {
		t.Fatalf("stat executable: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("executable mode = %o, want 755", info.Mode().Perm())
	}
}

func TestExtractTarZstCreatesSafeRelativeSymlink(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}

	archivePath := filepath.Join(t.TempDir(), "runtime.tar.zst")
	writeTarZst(t, archivePath, []testTarEntry{
		{name: "runtime/lib/aima-engine-real", body: "bin", mode: 0o755},
		{name: "runtime/bin/aima-engine", typeflag: tar.TypeSymlink, linkname: "../lib/aima-engine-real", mode: 0o777},
	})
	destDir := t.TempDir()

	if err := extractTarZst(archivePath, destDir); err != nil {
		t.Fatalf("extractTarZst: %v", err)
	}
	target, err := os.Readlink(filepath.Join(destDir, "bin", "aima-engine"))
	if err != nil {
		t.Fatalf("read installed symlink: %v", err)
	}
	if target != "../lib/aima-engine-real" {
		t.Fatalf("symlink target = %q", target)
	}
}

func TestExtractTarZstRejectsTraversal(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	archivePath := filepath.Join(root, "traversal.tar.zst")
	writeTarZst(t, archivePath, []testTarEntry{{name: "../escaped", body: "bad"}})
	destDir := filepath.Join(root, "dest")

	err := extractTarZst(archivePath, destDir)
	if err == nil || !strings.Contains(err.Error(), "path traversal") {
		t.Fatalf("extractTarZst error = %v, want path traversal rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "escaped")); !os.IsNotExist(statErr) {
		t.Fatalf("archive wrote outside destination: %v", statErr)
	}
}

func TestExtractTarZstRejectsEscapingSymlink(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}

	archivePath := filepath.Join(t.TempDir(), "symlink.tar.zst")
	writeTarZst(t, archivePath, []testTarEntry{{
		name:     "runtime/bin/aima-engine",
		mode:     0o777,
		typeflag: tar.TypeSymlink,
		linkname: "../../../outside",
	}})
	destDir := t.TempDir()

	err := extractTarZst(archivePath, destDir)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("extractTarZst error = %v, want escaping symlink rejection", err)
	}
}

func TestExtractTarZstRejectsCorruption(t *testing.T) {
	t.Parallel()

	archivePath := filepath.Join(t.TempDir(), "corrupt.tar.zst")
	if err := os.WriteFile(archivePath, bytes.Repeat([]byte{0xff}, 64), 0o644); err != nil {
		t.Fatalf("write corrupt archive: %v", err)
	}

	if err := extractTarZst(archivePath, t.TempDir()); err == nil {
		t.Fatal("extractTarZst accepted corrupt archive")
	}
}

func mustFileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func TestBinaryManagerDownloadRejectsSHA256Mismatch(t *testing.T) {
	t.Parallel()

	archivePath := filepath.Join(t.TempDir(), "engine.tar.zst")
	writeTarZst(t, archivePath, []testTarEntry{{
		name: "runtime/bin/aima-engine",
		body: "untrusted",
		mode: 0o755,
	}})
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	platform := goruntime.GOOS + "/" + goruntime.GOARCH
	distDir := t.TempDir()
	mgr := NewBinaryManager(distDir)
	source := &BinarySource{
		Binary:    "bin/aima-engine",
		Platforms: []string{platform},
		Download:  map[string]string{platform: server.URL + "/engine.tar.zst"},
		SHA256:    map[string]string{platform: strings.Repeat("0", 64)},
	}

	err = mgr.Download(context.Background(), source, nil)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("Download error = %v, want sha256 mismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(distDir, "bin", "aima-engine")); !os.IsNotExist(statErr) {
		t.Fatalf("mismatched archive was installed: %v", statErr)
	}
}

func TestBinaryManagerDownloadFallsBackAfterSHA256Mismatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	goodPath := filepath.Join(dir, "good.tar.zst")
	badPath := filepath.Join(dir, "bad.tar.zst")
	writeTarZst(t, goodPath, []testTarEntry{{name: "runtime/bin/aima-engine", body: "good", mode: 0o755}})
	writeTarZst(t, badPath, []testTarEntry{{name: "runtime/bin/aima-engine", body: "bad", mode: 0o755}})
	goodArchive, err := os.ReadFile(goodPath)
	if err != nil {
		t.Fatalf("read good archive: %v", err)
	}
	badArchive, err := os.ReadFile(badPath)
	if err != nil {
		t.Fatalf("read bad archive: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "bad") {
			_, _ = w.Write(badArchive)
			return
		}
		_, _ = w.Write(goodArchive)
	}))
	defer server.Close()

	platform := goruntime.GOOS + "/" + goruntime.GOARCH
	distDir := t.TempDir()
	mgr := NewBinaryManager(distDir)
	source := &BinarySource{
		Binary:    "bin/aima-engine",
		Platforms: []string{platform},
		Download:  map[string]string{platform: server.URL + "/good.tar.zst"},
		Mirror:    map[string][]string{platform: {server.URL + "/bad.tar.zst"}},
		SHA256:    map[string]string{platform: mustFileSHA256(t, goodPath)},
	}

	if err := mgr.Download(context.Background(), source, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	installedPath, err := mgr.ResolveInstalled(source)
	if err != nil {
		t.Fatalf("resolve verified install: %v", err)
	}
	installed, err := os.ReadFile(installedPath)
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if string(installed) != "good" {
		t.Fatalf("installed binary = %q, want matching fallback", installed)
	}
}

func TestBinaryManagerLocalBundleRejectsSHA256Mismatch(t *testing.T) {
	t.Parallel()

	archivePath := filepath.Join(t.TempDir(), "local.tar.zst")
	writeTarZst(t, archivePath, []testTarEntry{{
		name: "runtime/bin/aima-engine",
		body: "local",
		mode: 0o755,
	}})
	platform := goruntime.GOOS + "/" + goruntime.GOARCH
	distDir := t.TempDir()
	mgr := NewBinaryManager(distDir)
	source := &BinarySource{
		Binary:       "bin/aima-engine",
		Platforms:    []string{platform},
		LocalBundles: []string{archivePath},
		SHA256:       map[string]string{platform: strings.Repeat("f", 64)},
	}

	_, _, err := mgr.Ensure(context.Background(), source, nil)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("Ensure error = %v, want sha256 mismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(distDir, "bin", "aima-engine")); !os.IsNotExist(statErr) {
		t.Fatalf("mismatched local bundle was installed: %v", statErr)
	}
}

func TestBinaryManagerEnsureRejectsUnverifiedPinnedDistBinary(t *testing.T) {
	t.Parallel()

	distDir := t.TempDir()
	legacyPath := filepath.Join(distDir, "bin", "aima-engine")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	platform := goruntime.GOOS + "/" + goruntime.GOARCH
	source := &BinarySource{
		Binary:    "bin/aima-engine",
		Platforms: []string{platform},
		SHA256:    map[string]string{platform: strings.Repeat("a", 64)},
	}

	if _, _, err := NewBinaryManager(distDir).Ensure(context.Background(), source, nil); err == nil {
		t.Fatal("Ensure reused an unverified legacy binary for a pinned source")
	}
}

func TestBinaryManagerEnsureReusesVerifiedPinnedBundle(t *testing.T) {
	t.Parallel()

	archivePath := filepath.Join(t.TempDir(), "engine.tar.zst")
	writeTarZst(t, archivePath, []testTarEntry{{
		name: "runtime/bin/aima-engine", body: "verified", mode: 0o755,
	}})
	platform := goruntime.GOOS + "/" + goruntime.GOARCH
	source := &BinarySource{
		Binary:       "bin/aima-engine",
		Platforms:    []string{platform},
		LocalBundles: []string{archivePath},
		SHA256:       map[string]string{platform: mustFileSHA256(t, archivePath)},
	}
	mgr := NewBinaryManager(t.TempDir())
	firstPath, installed, err := mgr.Ensure(context.Background(), source, nil)
	if err != nil || !installed {
		t.Fatalf("first Ensure = (%q, %v, %v), want installed", firstPath, installed, err)
	}
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	secondPath, installed, err := mgr.Ensure(context.Background(), source, nil)
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if installed || secondPath != firstPath {
		t.Fatalf("second Ensure = (%q, %v), want verified reuse of %q", secondPath, installed, firstPath)
	}
}

func TestBinaryManagerImportBundleDoesNotPartiallyInstallInvalidArchive(t *testing.T) {
	t.Parallel()

	archivePath := filepath.Join(t.TempDir(), "invalid.tar.zst")
	writeTarZst(t, archivePath, []testTarEntry{
		{name: "runtime/bin/aima-engine", body: "partial", mode: 0o755},
		{name: "runtime/bad-hardlink", typeflag: tar.TypeLink, linkname: "bin/aima-engine"},
	})
	distDir := t.TempDir()
	mgr := NewBinaryManager(distDir)
	if err := mgr.ImportBundle(context.Background(), archivePath, "bin/aima-engine", nil); err == nil {
		t.Fatal("ImportBundle accepted an unsupported hardlink")
	}
	if _, err := os.Stat(filepath.Join(distDir, "bin", "aima-engine")); !os.IsNotExist(err) {
		t.Fatalf("failed import left a partial executable: %v", err)
	}
}

func TestBuildDownloadSourceListUsesEnterpriseMirrors(t *testing.T) {
	t.Setenv("AIMA_ENGINE_MIRROR_BASE", "https://repo.local/aima")
	t.Setenv("AIMA_ENGINE_MIRROR_TEMPLATE", "https://proxy.local/{filename},https://encoded.local/{escaped_url}")
	t.Setenv("AIMA_ENGINE_URL_REWRITE", "https://github.com/=>https://gitcache.local/")

	primary := "https://github.com/ggml-org/llama.cpp/releases/download/b9330/llama-b9330-bin-win-hip-radeon-x64.zip"
	got := buildDownloadSourceList(primary, []string{"https://catalog.local/llama.zip"})
	wantPrefix := []string{
		"https://repo.local/aima/llama-b9330-bin-win-hip-radeon-x64.zip",
		"https://proxy.local/llama-b9330-bin-win-hip-radeon-x64.zip",
		"https://encoded.local/https%3A%2F%2Fgithub.com%2Fggml-org%2Fllama.cpp%2Freleases%2Fdownload%2Fb9330%2Fllama-b9330-bin-win-hip-radeon-x64.zip",
		"https://gitcache.local/ggml-org/llama.cpp/releases/download/b9330/llama-b9330-bin-win-hip-radeon-x64.zip",
		"https://catalog.local/llama.zip",
		primary,
	}
	if len(got) != len(wantPrefix) {
		t.Fatalf("sources = %#v, want %#v", got, wantPrefix)
	}
	for i := range wantPrefix {
		if got[i] != wantPrefix[i] {
			t.Fatalf("sources[%d] = %q, want %q\nall=%#v", i, got[i], wantPrefix[i], got)
		}
	}
}

func TestPatternMatchExactAnchors(t *testing.T) {
	// ^pattern$ should match exactly
	patterns := []patternEntry{
		{pattern: "^vllm-nightly$", engineType: "vllm-nightly"},
	}

	if got := patternMatch("vllm-nightly", patterns); got != "vllm-nightly" {
		t.Errorf("^vllm-nightly$ should match 'vllm-nightly', got %q", got)
	}
	if got := patternMatch("vllm-nightly-extra", patterns); got != "" {
		t.Errorf("^vllm-nightly$ should NOT match 'vllm-nightly-extra', got %q", got)
	}
	if got := patternMatch("pre-vllm-nightly", patterns); got != "" {
		t.Errorf("^vllm-nightly$ should NOT match 'pre-vllm-nightly', got %q", got)
	}
}

func TestPatternMatchDeterministicPriority(t *testing.T) {
	patterns := []patternEntry{
		{pattern: "vllm", engineType: "contains"},
		{pattern: "^vllm$", engineType: "exact"},
		{pattern: "^vllm-nightly$", engineType: "nightly"},
	}
	for i := 0; i < 100; i++ {
		if got := patternMatch("vllm", patterns); got != "exact" {
			t.Fatalf("iteration %d: expected exact, got %q", i, got)
		}
	}
}

func TestScanFallbackToDocker(t *testing.T) {
	// Docker image list JSON format (one JSON object per line)
	dockerImages := []string{
		`{"Repository":"vllm/vllm-openai","Tag":"v0.8","ID":"abc123","Size":"8.5GB"}`,
	}
	dockerOutput := ""
	for _, img := range dockerImages {
		dockerOutput += img + "\n"
	}

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json":                        {err: fmt.Errorf("crictl not found")},
			"docker images --format {{json .}} --no-trunc": {output: []byte(dockerOutput)},
		},
	}

	engineAssets := map[string][]string{
		"vllm": {"vllm/vllm-openai"},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: engineAssets,
		Runner:        runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 engine, got %d", len(results))
	}
	if results[0].Type != "vllm" {
		t.Errorf("expected type vllm, got %s", results[0].Type)
	}
	if results[0].Tag != "v0.8" {
		t.Errorf("expected tag v0.8, got %s", results[0].Tag)
	}
}

func TestScanBothFail(t *testing.T) {
	// ScanUnified gracefully degrades: if no container runtime is available
	// it returns an empty list without error (native scan still runs).
	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json":                        {err: fmt.Errorf("crictl not found")},
			"docker images --format {{json .}} --no-trunc": {err: fmt.Errorf("docker not found")},
		},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: map[string][]string{"vllm": {"vllm/vllm-openai"}},
		Runner:        runner,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results when no runtime available, got %d", len(results))
	}
}

func TestScanNoMatchingImages(t *testing.T) {
	images := crictlImageList{
		Images: []crictlImage{
			{
				ID:       "sha256:xyz",
				RepoTags: []string{"nginx:latest"},
				Size:     "100000000",
			},
		},
	}
	imageJSON, _ := json.Marshal(images)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json": {output: imageJSON},
		},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: map[string][]string{"vllm": {"vllm/vllm-openai"}},
		Runner:        runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 matched engines, got %d", len(results))
	}
}

func TestScanEmptyAssetPatterns(t *testing.T) {
	images := crictlImageList{
		Images: []crictlImage{
			{
				ID:       "sha256:abc",
				RepoTags: []string{"vllm/vllm-openai:latest"},
				Size:     "8500000000",
			},
		},
	}
	imageJSON, _ := json.Marshal(images)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json": {output: imageJSON},
		},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: map[string][]string{},
		Runner:        runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 engines when no assets configured, got %d", len(results))
	}
}

// --- Pull tests ---

func TestPullFirstRegistrySucceeds(t *testing.T) {
	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl pull docker.io/vllm/vllm-openai:latest": {output: []byte("pulled")},
		},
	}

	err := Pull(context.Background(), PullOptions{
		Image:      "vllm/vllm-openai",
		Tag:        "latest",
		Registries: []string{"docker.io"},
		Runner:     runner,
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
}

func TestPullFirstFailsSecondSucceeds(t *testing.T) {
	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl pull docker.io/vllm/vllm-openai:latest":                         {err: fmt.Errorf("timeout")},
			"crictl pull registry.cn-hangzhou.aliyuncs.com/aima/vllm-openai:latest": {output: []byte("pulled")},
		},
	}

	err := Pull(context.Background(), PullOptions{
		Image:      "vllm/vllm-openai",
		Tag:        "latest",
		Registries: []string{"docker.io", "registry.cn-hangzhou.aliyuncs.com/aima"},
		Runner:     runner,
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
}

func TestPullAllRegistriesFail(t *testing.T) {
	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl pull docker.io/vllm/vllm-openai:latest":                         {err: fmt.Errorf("timeout")},
			"crictl pull registry.cn-hangzhou.aliyuncs.com/aima/vllm-openai:latest": {err: fmt.Errorf("auth fail")},
		},
	}

	err := Pull(context.Background(), PullOptions{
		Image:      "vllm/vllm-openai",
		Tag:        "latest",
		Registries: []string{"docker.io", "registry.cn-hangzhou.aliyuncs.com/aima"},
		Runner:     runner,
	})
	if err == nil {
		t.Error("expected error when all registries fail")
	}
}

func TestPullFallbackToDocker(t *testing.T) {
	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl pull docker.io/vllm/vllm-openai:latest": {err: fmt.Errorf("crictl not found")},
			"docker pull docker.io/vllm/vllm-openai:latest": {output: []byte("pulled")},
		},
	}

	err := Pull(context.Background(), PullOptions{
		Image:      "vllm/vllm-openai",
		Tag:        "latest",
		Registries: []string{"docker.io"},
		Runner:     runner,
	})
	if err != nil {
		t.Fatalf("pull with docker fallback: %v", err)
	}
}

func TestPullContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runner := &mockRunner{
		responses: map[string]mockResponse{},
	}

	err := Pull(ctx, PullOptions{
		Image:      "vllm/vllm-openai",
		Tag:        "latest",
		Registries: []string{"docker.io"},
		Runner:     runner,
	})
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

// --- Import tests ---

func TestImportWithCtr(t *testing.T) {
	tarPath := filepath.Join(t.TempDir(), "image.tar")
	os.WriteFile(tarPath, []byte("fake tar"), 0o644)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			fmt.Sprintf("ctr -n k8s.io images import %s", tarPath): {output: []byte("imported")},
		},
	}

	err := Import(context.Background(), tarPath, runner)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
}

func TestImportFallbackToDocker(t *testing.T) {
	tarPath := filepath.Join(t.TempDir(), "image.tar")
	os.WriteFile(tarPath, []byte("fake tar"), 0o644)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			fmt.Sprintf("ctr -n k8s.io images import %s", tarPath): {err: fmt.Errorf("ctr not found")},
			fmt.Sprintf("docker load -i %s", tarPath):              {output: []byte("loaded")},
		},
	}

	err := Import(context.Background(), tarPath, runner)
	if err != nil {
		t.Fatalf("import with docker fallback: %v", err)
	}
}

func TestImportBothFail(t *testing.T) {
	tarPath := filepath.Join(t.TempDir(), "image.tar")
	os.WriteFile(tarPath, []byte("fake tar"), 0o644)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			fmt.Sprintf("ctr -n k8s.io images import %s", tarPath): {err: fmt.Errorf("ctr not found")},
			fmt.Sprintf("docker load -i %s", tarPath):              {err: fmt.Errorf("docker not found")},
		},
	}

	err := Import(context.Background(), tarPath, runner)
	if err == nil {
		t.Error("expected error when both ctr and docker fail")
	}
}

func TestImportNonExistentFile(t *testing.T) {
	runner := &mockRunner{responses: map[string]mockResponse{}}

	err := Import(context.Background(), "/nonexistent/image.tar", runner)
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestImportDockerToContainerdPipe(t *testing.T) {
	runner := &mockRunner{
		responses: map[string]mockResponse{
			"docker save vllm/vllm-openai:latest": {output: []byte("ok")},
			"k3s ctr -n k8s.io images import -":   {output: []byte("ok")},
		},
	}

	if err := ImportDockerToContainerd(context.Background(), "vllm/vllm-openai:latest", runner); err != nil {
		t.Fatalf("ImportDockerToContainerd: %v", err)
	}
}

func TestImportDockerToContainerdPipeError(t *testing.T) {
	runner := &mockRunner{
		responses: map[string]mockResponse{
			"docker save vllm/vllm-openai:latest": {err: fmt.Errorf("save failed")},
		},
	}

	if err := ImportDockerToContainerd(context.Background(), "vllm/vllm-openai:latest", runner); err == nil {
		t.Fatal("expected error when docker save fails")
	}
}

func TestScanContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json": {output: []byte(`{"images":[]}`)},
		},
	}

	_, err := ScanUnified(ctx, ScanOptions{
		AssetPatterns: map[string][]string{"vllm": {"vllm/vllm-openai"}},
		Runner:        runner,
	})
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestScanImageWithRegistry(t *testing.T) {
	// Test that images with full registry prefix are matched
	images := crictlImageList{
		Images: []crictlImage{
			{
				ID:       "sha256:abc123",
				RepoTags: []string{"registry.cn-hangzhou.aliyuncs.com/aima/vllm-openai:v0.8"},
				Size:     "8500000000",
			},
		},
	}
	imageJSON, _ := json.Marshal(images)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json": {output: imageJSON},
		},
	}

	engineAssets := map[string][]string{
		"vllm": {"vllm/vllm-openai", "registry.cn-hangzhou.aliyuncs.com/aima/vllm-openai"},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: engineAssets,
		Runner:        runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 engine, got %d", len(results))
	}
	if results[0].Type != "vllm" {
		t.Errorf("expected type vllm, got %s", results[0].Type)
	}
}

func TestScanCustomFastAPIContainers(t *testing.T) {
	// Verify pattern matching for custom FastAPI TTS/ASR containers on GB10.
	// qwen-tts-fastapi patterns must match "qwen3-tts" (with digit 3) in image names.
	// glm-asr-fastapi patterns must match "glm-asr" and "asr-nano" in image names.
	images := crictlImageList{
		Images: []crictlImage{
			{
				ID:       "sha256:tts1",
				RepoTags: []string{"qujing-qwen3-tts-real:latest"},
				Size:     "2500000000",
			},
			{
				ID:       "sha256:tts2",
				RepoTags: []string{"qujing-qwen3-tts:latest"},
				Size:     "2400000000",
			},
			{
				ID:       "sha256:asr1",
				RepoTags: []string{"qujing-glm-asr-nano:latest"},
				Size:     "3000000000",
			},
		},
	}
	imageJSON, _ := json.Marshal(images)

	runner := &mockRunner{
		responses: map[string]mockResponse{
			"crictl images -o json": {output: imageJSON},
		},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		AssetPatterns: map[string][]string{
			"qwen-tts-fastapi": {"^qwen-tts-fastapi$", "qwen3-tts", "qwen-tts", "tts-fastapi"},
			"glm-asr-fastapi":  {"^glm-asr-fastapi$", "glm-asr", "asr-nano"},
		},
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 matched engines, got %d", len(results))
	}

	typeCount := map[string]int{}
	for _, r := range results {
		typeCount[r.Type]++
	}
	if typeCount["qwen-tts-fastapi"] != 2 {
		t.Errorf("expected 2 qwen-tts-fastapi matches, got %d", typeCount["qwen-tts-fastapi"])
	}
	if typeCount["glm-asr-fastapi"] != 1 {
		t.Errorf("expected 1 glm-asr-fastapi match, got %d", typeCount["glm-asr-fastapi"])
	}
}

func TestScanPreinstalledProbeUsesDiscoveredBinaryPath(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "fake-engine")
	if err := os.WriteFile(binPath, []byte("stub"), 0o755); err != nil {
		t.Fatalf("write fake engine: %v", err)
	}

	runner := &mockRunner{
		responses: map[string]mockResponse{
			binPath + " --version": {output: []byte("FakeEngine 1.2.3")},
		},
	}

	results, err := ScanUnified(context.Background(), ScanOptions{
		Runner:   runner,
		Platform: "linux/arm64",
		PreinstalledProbes: map[string]*knowledge.EngineSourceProbe{
			"fake-engine": {
				Paths:          []string{binPath},
				VersionCommand: []string{"./fake-engine", "--version"},
				VersionPattern: `FakeEngine ([\d.]+)`,
			},
		},
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 preinstalled engine, got %d", len(results))
	}
	if got := results[0].BinaryPath; got != binPath {
		t.Errorf("BinaryPath = %q, want %q", got, binPath)
	}
	if got := results[0].DetectedVersion; got != "1.2.3" {
		t.Errorf("DetectedVersion = %q, want 1.2.3", got)
	}
	if got := results[0].VersionMatch; got != "exact" {
		t.Errorf("VersionMatch = %q, want exact", got)
	}
}

func TestScanNativeFindsBinaryInExtraDirs(t *testing.T) {
	// Engines installed off the system drive in arbitrary dirs (e.g. Windows
	// D:\tools\llama-b9180-win-hip-radeon-x64) are neither in distDir nor on
	// PATH. AIMA_ENGINE_DIR feeds those dirs in via ScanOptions.ExtraDirs.
	distDir := t.TempDir()
	engineDir := t.TempDir()
	binPath := filepath.Join(engineDir, "llama-server.exe")
	if err := os.WriteFile(binPath, []byte("stub"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	results, err := ScanNative(context.Background(), ScanOptions{
		DistDir:      distDir,
		Platform:     "windows-amd64",
		BinaryAssets: map[string]string{"llama-server": "llamacpp", "llama-server.exe": "llamacpp"},
		ExtraDirs:    []string{engineDir},
	})
	if err != nil {
		t.Fatalf("scan native: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 native engine, got %d", len(results))
	}
	if got := results[0].Type; got != "llamacpp" {
		t.Errorf("Type = %q, want llamacpp", got)
	}
	if got := results[0].BinaryPath; got != binPath {
		t.Errorf("BinaryPath = %q, want %q", got, binPath)
	}
	if got := results[0].RuntimeType; got != "native" {
		t.Errorf("RuntimeType = %q, want native", got)
	}
}

func TestPullImageNameConstruction(t *testing.T) {
	// Verify image refs are built correctly for host-only registries, namespace
	// prefixes, and fully-qualified repository overrides.
	tests := []struct {
		image    string
		registry string
		tag      string
		wantRef  string
	}{
		{"vllm/vllm-openai", "docker.io", "latest", "docker.io/vllm/vllm-openai:latest"},
		{"vllm/vllm-openai", "registry.cn-hangzhou.aliyuncs.com/aima", "v0.8", "registry.cn-hangzhou.aliyuncs.com/aima/vllm-openai:v0.8"},
		{"vllm/vllm-openai", "docker.io/vllm/vllm-openai", "v0.8.5", "docker.io/vllm/vllm-openai:v0.8.5"},
		{"ghcr.io/ggml-org/llama.cpp", "ghcr.io/ggml-org/llama.cpp", "server", "ghcr.io/ggml-org/llama.cpp:server"},
	}

	for _, tt := range tests {
		t.Run(tt.wantRef, func(t *testing.T) {
			ref := buildImageRef(tt.registry, tt.image, tt.tag)
			if ref != tt.wantRef {
				t.Errorf("buildImageRef(%q, %q, %q) = %q, want %q", tt.registry, tt.image, tt.tag, ref, tt.wantRef)
			}
		})
	}
}
