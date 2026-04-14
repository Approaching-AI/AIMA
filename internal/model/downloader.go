package model

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// downloadClient is a shared HTTP client for large model downloads.
// No overall Timeout: multi-GB files at slow speeds can take hours.
// Connection and TLS timeouts are handled by the default transport.
var downloadClient = &http.Client{
	// API calls use apiClient (below) with a shorter timeout.
}

// apiClient is used for metadata API calls (file listing, etc.) which should be fast.
var apiClient = &http.Client{
	Timeout: 2 * time.Minute,
}

// DownloadOptions configures a file download.
type DownloadOptions struct {
	URL        string
	DestPath   string
	OnProgress func(downloaded, total int64)
}

// Download fetches a file with HTTP Range support for resume.
func Download(ctx context.Context, opts DownloadOptions) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("download %s: %w", opts.URL, err)
	}

	partialPath := opts.DestPath + ".partial"

	// Check for existing partial download
	var existingSize int64
	if info, err := os.Stat(partialPath); err == nil {
		existingSize = info.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.URL, nil)
	if err != nil {
		return fmt.Errorf("create request for %s: %w", opts.URL, err)
	}

	if existingSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
	}

	resp, err := downloadClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", opts.URL, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// Full download - start from scratch
		existingSize = 0
	case http.StatusPartialContent:
		// Resume download - append to existing
	default:
		return fmt.Errorf("download %s: HTTP %d", opts.URL, resp.StatusCode)
	}

	// Determine total size
	var totalSize int64
	if resp.StatusCode == http.StatusPartialContent && resp.ContentLength >= 0 {
		totalSize = existingSize + resp.ContentLength
	} else if resp.ContentLength >= 0 {
		totalSize = resp.ContentLength
	} else {
		totalSize = -1 // unknown
	}

	// Ensure dest directory exists
	if err := os.MkdirAll(filepath.Dir(opts.DestPath), 0o755); err != nil {
		return fmt.Errorf("create download directory: %w", err)
	}

	// Open partial file for writing
	var flags int
	if existingSize > 0 && resp.StatusCode == http.StatusPartialContent {
		flags = os.O_WRONLY | os.O_APPEND
	} else {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}

	f, err := os.OpenFile(partialPath, flags, 0o644)
	if err != nil {
		return fmt.Errorf("open partial file %s: %w", partialPath, err)
	}

	// Track progress
	downloaded := existingSize
	reader := &progressReader{
		reader: resp.Body,
		onRead: func(n int) {
			downloaded += int64(n)
			if opts.OnProgress != nil {
				opts.OnProgress(downloaded, totalSize)
			}
		},
	}

	written, copyErr := io.Copy(f, reader)
	closeErr := f.Close()

	if copyErr != nil {
		return fmt.Errorf("download %s: %w", opts.URL, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close partial file %s: %w", partialPath, closeErr)
	}

	// Verify byte count matches Content-Length to detect truncated downloads.
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		return fmt.Errorf("download %s: incomplete transfer (%d of %d bytes)", opts.URL, written, resp.ContentLength)
	}

	// Rename .partial to final destination
	if err := os.Rename(partialPath, opts.DestPath); err != nil {
		return fmt.Errorf("rename %s to %s: %w", partialPath, opts.DestPath, err)
	}

	return nil
}

type progressReader struct {
	reader io.Reader
	onRead func(n int)
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	if n > 0 {
		pr.onRead(n)
	}
	return n, err
}

// Source describes a model download source.
type Source struct {
	Type         string
	Repo         string
	Path         string
	Format       string
	Quantization string
}

// DownloadPlan describes the local/downloaded asset shape required for a model pull.
type DownloadPlan struct {
	Format       string
	Quantization string
	OnProgress   func(downloaded, total int64)
}

// DownloadFromSource tries each source in order until one succeeds.
func DownloadFromSource(ctx context.Context, sources []Source, destPath string, plan DownloadPlan) error {
	if len(sources) == 0 {
		return fmt.Errorf("no download sources available")
	}
	if PathLooksUsable(destPath, plan.Format) {
		return nil
	}
	var attemptErrs []string
	for _, src := range sources {
		var err error
		switch src.Type {
		case "huggingface":
			err = downloadHuggingFace(ctx, src.Repo, destPath, plan)
		case "modelscope":
			err = downloadModelScope(ctx, src.Repo, destPath, plan)
		case "local_path":
			err = fmt.Errorf("local_path source %s is not downloadable", src.Path)
		default:
			err = Download(ctx, DownloadOptions{URL: src.Repo, DestPath: destPath, OnProgress: plan.OnProgress})
		}
		if err == nil {
			return nil
		}
		slog.Warn("source failed, trying next", "type", src.Type, "repo", src.Repo, "error", err)
		attemptErrs = append(attemptErrs, fmt.Sprintf("%s %s: %v", src.Type, src.Repo, err))
	}
	return fmt.Errorf("all sources failed: %s", strings.Join(attemptErrs, "; "))
}

func hasResumeFriendlyDownloadState(destPath string) bool {
	entries, err := os.ReadDir(destPath)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".cache" || strings.HasPrefix(name, ".") {
			continue
		}
		return true
	}
	return false
}

func huggingFaceCLIEnv(destPath, endpoint string) []string {
	cacheRoot := filepath.Join(destPath, ".cache", "huggingface")
	return append(
		os.Environ(),
		"HF_ENDPOINT="+endpoint,
		"HF_HOME="+cacheRoot,
		"HF_HUB_CACHE="+filepath.Join(cacheRoot, "hub"),
		"HF_ASSETS_CACHE="+filepath.Join(cacheRoot, "assets"),
	)
}

func downloadHuggingFace(ctx context.Context, repo, destPath string, plan DownloadPlan) error {
	if err := os.MkdirAll(destPath, 0o755); err != nil {
		return fmt.Errorf("create dest dir %s: %w", destPath, err)
	}
	if PathLooksUsable(destPath, plan.Format) {
		return nil
	}

	endpoints := hfEndpoints()
	if hasResumeFriendlyDownloadState(destPath) {
		slog.Info("resuming HuggingFace download via HTTP", "repo", repo, "dest", destPath)
		return downloadHFRepoViaEndpoints(ctx, endpoints, repo, destPath, plan)
	}

	// Prefer huggingface-cli if available (handles auth, multi-file, resume).
	if hfCLI, err := exec.LookPath("huggingface-cli"); err == nil {
		slog.Info("downloading via huggingface-cli", "repo", repo, "dest", destPath, "endpoint", endpoints[0])
		cmd := exec.CommandContext(ctx, hfCLI, "download", repo, "--local-dir", destPath)
		cmd.Env = huggingFaceCLIEnv(destPath, endpoints[0])
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err == nil {
			return nil
		}
		slog.Warn("huggingface-cli failed, falling back to HTTP repo download", "repo", repo, "error", err)
	}

	return downloadHFRepoViaEndpoints(ctx, endpoints, repo, destPath, plan)
}

func downloadHFRepoViaEndpoints(ctx context.Context, endpoints []string, repo, destPath string, plan DownloadPlan) error {
	var attemptErrs []string
	for _, ep := range endpoints {
		slog.Info("downloading via HuggingFace HTTP", "repo", repo, "endpoint", ep)
		if err := downloadHFRepo(ctx, ep, repo, destPath, plan); err != nil {
			slog.Warn("HF endpoint failed", "endpoint", ep, "error", err)
			attemptErrs = append(attemptErrs, fmt.Sprintf("%s: %v", ep, err))
			continue
		}
		return nil
	}
	return fmt.Errorf("all HuggingFace endpoints failed for %s: %s", repo, strings.Join(attemptErrs, "; "))
}

// hfRepoFile represents a file entry from the HuggingFace tree API.
type hfRepoFile struct {
	Type string `json:"type"` // "file" or "directory"
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// downloadHFRepo lists files in a HuggingFace repo, selects the minimal subset
// required by the resolved variant, and downloads only those files.
func downloadHFRepo(ctx context.Context, endpoint, repo, destPath string, plan DownloadPlan) error {
	// Collect all files via paginated, recursive tree API calls.
	var repoFiles []hfRepoFile
	var totalSize int64

	// Use a queue for recursive directory traversal.
	queue := []string{""} // "" = root path
	for len(queue) > 0 {
		treePath := queue[0]
		queue = queue[1:]

		files, err := hfListTree(ctx, endpoint, repo, treePath)
		if err != nil {
			return err
		}
		for _, f := range files {
			if f.Type == "directory" {
				queue = append(queue, f.Path)
				continue
			}
			if f.Type != "file" || strings.HasPrefix(path.Base(f.Path), ".") {
				continue
			}
			repoFiles = append(repoFiles, f)
		}
	}

	toDownload, err := selectRepoFiles(repoFiles, plan)
	if err != nil {
		return err
	}
	for _, f := range toDownload {
		totalSize += f.Size
	}

	slog.Info("repo file list retrieved",
		"files", len(toDownload),
		"total_size_mb", totalSize/(1024*1024),
	)

	var downloadedBase int64
	if plan.OnProgress != nil {
		plan.OnProgress(downloadedBase, totalSize)
	}

	// Download each file, skipping already completed ones
	for i, f := range toDownload {
		fileDest := filepath.Join(destPath, filepath.FromSlash(f.Path))
		// Guard against path traversal from API-provided paths
		if !isSubPath(destPath, fileDest) {
			return fmt.Errorf("path traversal blocked: %s", f.Path)
		}
		// Skip if file already exists with correct size
		if info, err := os.Stat(fileDest); err == nil && info.Size() == f.Size {
			slog.Info("skipping already downloaded",
				"progress", fmt.Sprintf("[%d/%d]", i+1, len(toDownload)),
				"file", f.Path,
			)
			downloadedBase += f.Size
			if plan.OnProgress != nil {
				plan.OnProgress(downloadedBase, totalSize)
			}
			continue
		}
		fileURL := fmt.Sprintf("%s/%s/resolve/main/%s", endpoint, repo, f.Path)
		slog.Info("downloading file",
			"progress", fmt.Sprintf("[%d/%d]", i+1, len(toDownload)),
			"file", f.Path,
			"size_mb", f.Size/(1024*1024),
		)
		if err := Download(ctx, DownloadOptions{
			URL:      fileURL,
			DestPath: fileDest,
			OnProgress: func(downloaded, _ int64) {
				if plan.OnProgress != nil {
					plan.OnProgress(downloadedBase+downloaded, totalSize)
				}
			},
		}); err != nil {
			return fmt.Errorf("download %s: %w", f.Path, err)
		}
		// Verify downloaded file size matches API metadata.
		if f.Size > 0 {
			if info, err := os.Stat(fileDest); err != nil || info.Size() != f.Size {
				actualSize := int64(0)
				if info != nil {
					actualSize = info.Size()
				}
				return fmt.Errorf("size mismatch for %s: expected %d, got %d", f.Path, f.Size, actualSize)
			}
		}
		downloadedBase += f.Size
		if plan.OnProgress != nil {
			plan.OnProgress(downloadedBase, totalSize)
		}
	}
	return nil
}

// hfListTree fetches one page of the HuggingFace tree API for the given path,
// following pagination cursors to return all entries.
func hfListTree(ctx context.Context, endpoint, repo, treePath string) ([]hfRepoFile, error) {
	var allFiles []hfRepoFile
	apiURL := fmt.Sprintf("%s/api/models/%s/tree/main", endpoint, repo)
	if treePath != "" {
		apiURL += "/" + treePath
	}

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			return nil, fmt.Errorf("create API request: %w", err)
		}
		resp, err := apiClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list repo files: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("list repo files: HTTP %d", resp.StatusCode)
		}

		var files []hfRepoFile
		if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("parse file list: %w", err)
		}
		resp.Body.Close()

		allFiles = append(allFiles, files...)

		// Follow pagination via Link header (HuggingFace uses cursor-based pagination).
		nextURL := parseLinkNext(resp.Header.Get("Link"))
		if nextURL == "" {
			break
		}
		apiURL = nextURL
	}
	return allFiles, nil
}

// parseLinkNext extracts the "next" URL from an HTTP Link header.
// Format: <https://...?cursor=...>; rel="next"
func parseLinkNext(link string) string {
	if link == "" {
		return ""
	}
	for _, part := range strings.Split(link, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		start := strings.Index(part, "<")
		end := strings.Index(part, ">")
		if start >= 0 && end > start {
			return part[start+1 : end]
		}
	}
	return ""
}

// hfEndpoints returns HuggingFace endpoints to try in order.
func hfEndpoints() []string {
	if ep := os.Getenv("HF_ENDPOINT"); ep != "" {
		return []string{ep}
	}
	// hf-mirror.com first (works in China), then official
	return []string{"https://hf-mirror.com", "https://huggingface.co"}
}

func downloadModelScope(ctx context.Context, repo, destPath string, plan DownloadPlan) error {
	if err := os.MkdirAll(destPath, 0o755); err != nil {
		return fmt.Errorf("create dest dir %s: %w", destPath, err)
	}
	if PathLooksUsable(destPath, plan.Format) {
		return nil
	}

	if hasResumeFriendlyDownloadState(destPath) {
		slog.Info("resuming ModelScope download via HTTP", "repo", repo, "dest", destPath)
		return downloadModelScopeHTTP(ctx, repo, destPath, plan)
	}

	// Prefer modelscope CLI if available
	if msCLI, err := exec.LookPath("modelscope"); err == nil {
		slog.Info("downloading via modelscope CLI", "repo", repo, "dest", destPath)
		cmd := exec.CommandContext(ctx, msCLI, "download", repo, "--local_dir", destPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err == nil {
			return nil
		}
		slog.Warn("modelscope CLI failed, falling back to HTTP repo download", "repo", repo, "error", err)
	}

	return downloadModelScopeHTTP(ctx, repo, destPath, plan)
}

func downloadModelScopeHTTP(ctx context.Context, repo, destPath string, plan DownloadPlan) error {
	// Fallback: list files via ModelScope API, download each
	apiURL := fmt.Sprintf("https://modelscope.cn/api/v1/models/%s/repo/tree?Revision=master&Root=&Recursive=true", repo)
	slog.Info("downloading via ModelScope HTTP", "repo", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return fmt.Errorf("create API request: %w", err)
	}
	resp, err := apiClient.Do(req)
	if err != nil {
		return fmt.Errorf("list ModelScope repo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("list ModelScope repo: HTTP %d", resp.StatusCode)
	}

	var msResp struct {
		Data struct {
			Files []struct {
				Name string `json:"Name"`
				Size int64  `json:"Size"`
				Type string `json:"Type"` // "file" or "tree"
			} `json:"Files"`
		} `json:"Data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&msResp); err != nil {
		return fmt.Errorf("parse ModelScope file list: %w", err)
	}

	repoFiles := make([]hfRepoFile, 0, len(msResp.Data.Files))
	for _, f := range msResp.Data.Files {
		if f.Type != "file" || strings.HasPrefix(path.Base(f.Name), ".") {
			continue
		}
		repoFiles = append(repoFiles, hfRepoFile{Type: "file", Path: f.Name, Size: f.Size})
	}
	selected, err := selectRepoFiles(repoFiles, plan)
	if err != nil {
		return err
	}
	var totalSize int64
	for _, f := range selected {
		totalSize += f.Size
	}

	idx := 0
	var downloadedBase int64
	if plan.OnProgress != nil {
		plan.OnProgress(downloadedBase, totalSize)
	}
	for _, f := range selected {
		idx++
		fileDest := filepath.Join(destPath, filepath.FromSlash(f.Path))
		// Guard against path traversal from API-provided paths
		if !isSubPath(destPath, fileDest) {
			return fmt.Errorf("path traversal blocked: %s", f.Path)
		}
		if info, err := os.Stat(fileDest); err == nil && info.Size() == f.Size {
			slog.Info("skipping already downloaded",
				"progress", fmt.Sprintf("[%d/%d]", idx, len(selected)),
				"file", f.Path,
			)
			downloadedBase += f.Size
			if plan.OnProgress != nil {
				plan.OnProgress(downloadedBase, totalSize)
			}
			continue
		}
		fileURL := fmt.Sprintf("https://modelscope.cn/models/%s/resolve/master/%s", repo, f.Path)
		slog.Info("downloading file",
			"progress", fmt.Sprintf("[%d/%d]", idx, len(selected)),
			"file", f.Path,
			"size_mb", f.Size/(1024*1024),
		)
		if err := Download(ctx, DownloadOptions{
			URL:      fileURL,
			DestPath: fileDest,
			OnProgress: func(downloaded, _ int64) {
				if plan.OnProgress != nil {
					plan.OnProgress(downloadedBase+downloaded, totalSize)
				}
			},
		}); err != nil {
			return fmt.Errorf("download %s: %w", f.Path, err)
		}
		if fi, err := os.Stat(fileDest); err == nil && f.Size > 0 && fi.Size() != f.Size {
			return fmt.Errorf("size mismatch for %s: got %d, expected %d", f.Path, fi.Size(), f.Size)
		}
		downloadedBase += f.Size
		if plan.OnProgress != nil {
			plan.OnProgress(downloadedBase, totalSize)
		}
	}
	return nil
}

// PathLooksUsable reports whether a local path already contains a usable model
// for the requested format, so pull can reuse it without touching the network.
func PathLooksUsable(modelPath, format string) bool {
	info, err := os.Stat(modelPath)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return fileMatchesFormat(modelPath, format)
	}

	switch strings.ToLower(format) {
	case "gguf":
		return dirHasAnyExt(modelPath, ".gguf", ".ggml", ".bin")
	case "onnx":
		return dirHasAnyExt(modelPath, ".onnx")
	case "mnn":
		return dirHasAnyExt(modelPath, ".mnn")
	case "safetensors":
		return dirHasCompleteSafetensorsModel(modelPath) || dirHasCompleteDiffusersModel(modelPath)
	case "":
		return dirHasAnyExt(modelPath, ".gguf", ".ggml", ".bin", ".onnx", ".mnn", ".pt", ".pth") ||
			dirHasCompleteSafetensorsModel(modelPath) ||
			dirHasCompleteDiffusersModel(modelPath)
	default:
		return dirHasCompleteSafetensorsModel(modelPath) ||
			dirHasCompleteDiffusersModel(modelPath)
	}
}

// PathLooksCompatible reports whether a local path is both structurally usable
// for the requested format and compatible with the expected quantization hint.
// Unknown quantization metadata is treated as compatible so callers degrade
// gracefully when only partial metadata is available.
func PathLooksCompatible(modelPath, format, quantization string) bool {
	if !PathLooksUsable(modelPath, format) {
		return false
	}
	expected := normalizeQuantString(quantization)
	if expected == "" || expected == "unknown" {
		return true
	}
	actual, ok := detectLocalQuantization(modelPath, format)
	if !ok {
		if requiresExplicitQuantizationMetadata(modelPath, format, expected) {
			return false
		}
		return true
	}
	return normalizeQuantString(actual) == expected
}

func requiresExplicitQuantizationMetadata(modelPath, format, expected string) bool {
	if !isCompressedQuantization(expected) {
		return false
	}
	info, err := os.Stat(modelPath)
	if err != nil || !info.IsDir() {
		return false
	}
	return format == "" || strings.EqualFold(format, "safetensors")
}

func isCompressedQuantization(q string) bool {
	switch normalizeQuantString(q) {
	case "int4", "int5", "int6", "int8", "nf4", "fp8":
		return true
	default:
		return false
	}
}

func detectLocalQuantization(modelPath, format string) (string, bool) {
	info, err := os.Stat(modelPath)
	if err != nil {
		return "", false
	}
	if !info.IsDir() {
		q, _ := detectQuantization(nil, filepath.Base(modelPath), format)
		if q == "" || q == "unknown" {
			return "", false
		}
		return q, true
	}

	var config map[string]any
	for _, name := range []string{"config.json", "configuration.json"} {
		data, err := os.ReadFile(filepath.Join(modelPath, name))
		if err != nil {
			continue
		}
		if err := json.Unmarshal(data, &config); err != nil {
			break
		}
		break
	}
	if q := quantizationFromSidecarConfig(modelPath); q != "" {
		return q, true
	}

	entries, err := os.ReadDir(modelPath)
	if err != nil {
		return "", false
	}
	weightName := ""
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".safetensors") ||
			strings.HasSuffix(lower, ".gguf") ||
			strings.HasSuffix(lower, ".ggml") ||
			strings.HasSuffix(lower, ".bin") {
			weightName = name
			break
		}
	}

	q, _ := detectQuantization(config, weightName, format)
	if q == "" || q == "unknown" {
		q, _ = detectQuantization(config, filepath.Base(modelPath), format)
	}
	if q == "" || q == "unknown" {
		return "", false
	}
	return q, true
}

func quantizationFromSidecarConfig(modelPath string) string {
	for _, name := range []string{"quantize_config.json", "quantization_config.json", "quant_config.json"} {
		data, err := os.ReadFile(filepath.Join(modelPath, name))
		if err != nil {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}
		if wrapped, ok := raw["quantization_config"].(map[string]any); ok {
			if q := quantFromConfig(map[string]any{"quantization_config": wrapped}); q != "" {
				return q
			}
		}
		if q := quantFromConfig(map[string]any{"quantization_config": raw}); q != "" {
			return q
		}
	}
	return ""
}

func fileMatchesFormat(modelPath, format string) bool {
	ext := strings.ToLower(filepath.Ext(modelPath))
	switch strings.ToLower(format) {
	case "gguf":
		return ext == ".gguf" || ext == ".ggml" || ext == ".bin"
	case "safetensors":
		return false
	case "":
		return ext == ".gguf" || ext == ".ggml" || ext == ".bin"
	default:
		return false
	}
}

func dirHasAnyExt(dir string, exts ...string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		for _, want := range exts {
			if ext == want {
				return true
			}
		}
	}
	return false
}

func dirHasCompleteSafetensorsModel(dir string) bool {
	if !dirHasReadableFile(dir, "config.json") {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	weights := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(strings.ToLower(name), ".safetensors") && dirHasReadableFile(dir, name) {
			weights[name] = struct{}{}
		}
	}
	if len(weights) == 0 {
		return false
	}
	if !dirHasUsableTokenizerAssets(dir) {
		return false
	}

	indexPath := filepath.Join(dir, "model.safetensors.index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return os.IsNotExist(err)
	}
	var index struct {
		WeightMap map[string]string `json:"weight_map"`
	}
	if err := json.Unmarshal(data, &index); err != nil || len(index.WeightMap) == 0 {
		return false
	}
	required := make(map[string]struct{})
	for _, shard := range index.WeightMap {
		required[shard] = struct{}{}
	}
	for shard := range required {
		if _, ok := weights[shard]; !ok {
			return false
		}
	}
	return true
}

func dirHasCompleteDiffusersModel(dir string) bool {
	index, ok := readDiffusersPipelineIndex(dir)
	if !ok {
		return false
	}
	components := diffusersRequiredComponents(index)
	if len(components) == 0 {
		return false
	}

	seenWeights := false
	indexOK := true
	walkModelFilesRecursive(dir, func(currentDir, name string) {
		if strings.HasSuffix(strings.ToLower(name), ".safetensors") && dirHasReadableFile(currentDir, name) {
			seenWeights = true
		}
		if strings.HasSuffix(strings.ToLower(name), ".safetensors.index.json") && !diffusersShardIndexComplete(currentDir, name) {
			indexOK = false
		}
	})
	if !seenWeights || !indexOK {
		return false
	}
	for componentName, className := range components {
		if !diffusersComponentLooksUsable(filepath.Join(dir, componentName), className) {
			return false
		}
	}
	return true
}

func dirHasDiffusersPipelineIndex(dir string) bool {
	_, ok := readDiffusersPipelineIndex(dir)
	return ok
}

func readDiffusersPipelineIndex(dir string) (map[string]any, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "model_index.json"))
	if err != nil {
		return nil, false
	}
	var index map[string]any
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, false
	}
	className, _ := index["_class_name"].(string)
	if !strings.Contains(className, "Pipeline") {
		return nil, false
	}
	return index, true
}

func diffusersRequiredComponents(index map[string]any) map[string]string {
	components := make(map[string]string)
	for key, raw := range index {
		if strings.HasPrefix(key, "_") {
			continue
		}
		className, required := diffusersComponentClass(raw)
		if !required {
			continue
		}
		components[key] = className
	}
	return components
}

func diffusersComponentClass(raw any) (string, bool) {
	switch value := raw.(type) {
	case nil:
		return "", false
	case string:
		return value, value != ""
	case []any:
		required := false
		className := ""
		for _, item := range value {
			switch typed := item.(type) {
			case nil:
				continue
			case string:
				if typed == "" {
					continue
				}
				required = true
				className = typed
			default:
				required = true
			}
		}
		return className, required
	default:
		return "", true
	}
}

func diffusersComponentLooksUsable(path, className string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return dirHasReadableFile(filepath.Dir(path), filepath.Base(path))
	}

	lowerClass := strings.ToLower(className)
	switch {
	case strings.Contains(lowerClass, "tokenizer"):
		return dirHasUsableTokenizerAssets(path)
	case strings.Contains(lowerClass, "scheduler"):
		return dirHasAnyReadableFile(path, "scheduler_config.json", "config.json")
	case strings.Contains(lowerClass, "processor"),
		strings.Contains(lowerClass, "extractor"),
		strings.Contains(lowerClass, "imageprocessor"):
		return dirHasAnyReadableFile(path, "preprocessor_config.json", "processor_config.json", "config.json")
	default:
		return dirHasAnyReadableFile(path, "config.json") && dirHasCompleteDiffusersWeights(path)
	}
}

func dirHasCompleteDiffusersWeights(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	seenWeights := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".safetensors") && dirHasReadableFile(dir, name) {
			seenWeights = true
		}
		if strings.HasSuffix(lower, ".safetensors.index.json") && !diffusersShardIndexComplete(dir, name) {
			return false
		}
	}
	return seenWeights
}

func diffusersShardIndexComplete(dir, name string) bool {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return false
	}
	var index struct {
		WeightMap map[string]string `json:"weight_map"`
	}
	if err := json.Unmarshal(data, &index); err != nil || len(index.WeightMap) == 0 {
		return false
	}
	required := make(map[string]struct{})
	for _, shard := range index.WeightMap {
		required[shard] = struct{}{}
	}
	for shard := range required {
		if !dirHasReadableFile(dir, shard) {
			return false
		}
	}
	return true
}

func walkModelFilesRecursive(dir string, visit func(currentDir, name string)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".cache" || strings.HasPrefix(name, ".") {
			continue
		}
		fullPath := filepath.Join(dir, name)
		if entry.IsDir() {
			walkModelFilesRecursive(fullPath, visit)
			continue
		}
		visit(dir, name)
	}
}

func dirHasReadableFile(dir, name string) bool {
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	return f.Close() == nil
}

func dirHasUsableTokenizerAssets(dir string) bool {
	if dirHasAnyReadableFile(dir, "tokenizer.json", "tokenizer.model") {
		return true
	}
	return dirHasAllReadableFiles(dir, "vocab.json", "merges.txt")
}

func dirHasAnyReadableFile(dir string, names ...string) bool {
	for _, name := range names {
		if dirHasReadableFile(dir, name) {
			return true
		}
	}
	return false
}

func dirHasAllReadableFiles(dir string, names ...string) bool {
	for _, name := range names {
		if !dirHasReadableFile(dir, name) {
			return false
		}
	}
	return true
}

func selectRepoFiles(files []hfRepoFile, plan DownloadPlan) ([]hfRepoFile, error) {
	switch strings.ToLower(plan.Format) {
	case "gguf":
		return selectGGUFFiles(files, plan.Quantization)
	case "safetensors":
		return selectSafetensorFiles(files)
	default:
		selected := make([]hfRepoFile, 0, len(files))
		for _, f := range files {
			if f.Type != "file" || strings.HasPrefix(path.Base(f.Path), ".") {
				continue
			}
			selected = append(selected, f)
		}
		return selected, nil
	}
}

func selectGGUFFiles(files []hfRepoFile, quantization string) ([]hfRepoFile, error) {
	candidates := make([]hfRepoFile, 0)
	for _, f := range files {
		lower := strings.ToLower(f.Path)
		if strings.HasSuffix(lower, ".gguf") || strings.HasSuffix(lower, ".ggml") || strings.HasSuffix(lower, ".bin") {
			candidates = append(candidates, f)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("repo does not contain GGUF model files")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Path < candidates[j].Path })

	tokens := quantizationTokens(quantization)
	if len(tokens) > 0 {
		for _, f := range candidates {
			lower := strings.ToLower(f.Path)
			for _, token := range tokens {
				if strings.Contains(lower, token) {
					return []hfRepoFile{f}, nil
				}
			}
		}
	}
	return []hfRepoFile{candidates[0]}, nil
}

func selectSafetensorFiles(files []hfRepoFile) ([]hfRepoFile, error) {
	selected := make(map[string]hfRepoFile)
	for _, f := range files {
		base := strings.ToLower(path.Base(f.Path))
		if strings.HasSuffix(base, ".safetensors") || isRequiredModelMetadataFile(base) {
			selected[f.Path] = f
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("repo does not contain a usable safetensors model")
	}
	out := make([]hfRepoFile, 0, len(selected))
	for _, f := range selected {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func isRequiredModelMetadataFile(base string) bool {
	switch base {
	case "config.json",
		"generation_config.json",
		"model.safetensors.index.json",
		"tokenizer.json",
		"tokenizer_config.json",
		"tokenizer.model",
		"special_tokens_map.json",
		"added_tokens.json",
		"vocab.json",
		"merges.txt",
		"chat_template.json",
		"preprocessor_config.json",
		"processor_config.json":
		return true
	default:
		return false
	}
}

func quantizationTokens(q string) []string {
	q = strings.ToLower(strings.TrimSpace(q))
	switch q {
	case "", "unknown":
		return nil
	case "q4", "int4":
		return []string{"q4_k_m", "q4_k_s", "q4_1", "q4_0", "q4", "4bit", "int4"}
	case "q5", "int5":
		return []string{"q5_k_m", "q5_k_s", "q5_1", "q5_0", "q5", "5bit", "int5"}
	case "q6", "int6":
		return []string{"q6_k", "q6", "6bit", "int6"}
	case "q8", "int8":
		return []string{"q8_0", "q8", "8bit", "int8"}
	default:
		return []string{q}
	}
}

// isSubPath returns true if child is under parent after cleaning both paths.
func isSubPath(parent, child string) bool {
	p := filepath.Clean(parent) + string(os.PathSeparator)
	c := filepath.Clean(child)
	return strings.HasPrefix(c, p) || c == filepath.Clean(parent)
}
