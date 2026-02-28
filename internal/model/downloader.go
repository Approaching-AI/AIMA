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
	"path/filepath"
	"strings"
	"time"
)

// downloadClient is a shared HTTP client with a generous timeout for large model downloads.
var downloadClient = &http.Client{
	Timeout: 30 * time.Minute,
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

	_, copyErr := io.Copy(f, reader)
	closeErr := f.Close()

	if copyErr != nil {
		return fmt.Errorf("download %s: %w", opts.URL, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close partial file %s: %w", partialPath, closeErr)
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
	Type string
	Repo string
}

// DownloadFromSource tries each source in order until one succeeds.
func DownloadFromSource(ctx context.Context, sources []Source, destPath string) error {
	if len(sources) == 0 {
		return fmt.Errorf("no download sources available")
	}
	var lastErr error
	for _, src := range sources {
		var err error
		switch src.Type {
		case "huggingface":
			err = downloadHuggingFace(ctx, src.Repo, destPath)
		case "modelscope":
			err = downloadModelScope(ctx, src.Repo, destPath)
		default:
			err = Download(ctx, DownloadOptions{URL: src.Repo, DestPath: destPath})
		}
		if err == nil {
			return nil
		}
		slog.Warn("source failed, trying next", "type", src.Type, "repo", src.Repo, "error", err)
		lastErr = err
	}
	return fmt.Errorf("all sources failed: %w", lastErr)
}

func downloadHuggingFace(ctx context.Context, repo, destPath string) error {
	if err := os.MkdirAll(destPath, 0o755); err != nil {
		return fmt.Errorf("create dest dir %s: %w", destPath, err)
	}

	// Prefer huggingface-cli if available (handles auth, multi-file, resume).
	// Users in China can set HF_ENDPOINT=https://hf-mirror.com for the CLI too.
	if hfCLI, err := exec.LookPath("huggingface-cli"); err == nil {
		slog.Info("downloading via huggingface-cli", "repo", repo, "dest", destPath)
		cmd := exec.CommandContext(ctx, hfCLI, "download", repo, "--local-dir", destPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Fallback: list files via API, then download each one.
	endpoints := hfEndpoints()
	for _, ep := range endpoints {
		slog.Info("downloading via HuggingFace HTTP", "repo", repo, "endpoint", ep)
		if err := downloadHFRepo(ctx, ep, repo, destPath); err != nil {
			slog.Warn("HF endpoint failed", "endpoint", ep, "error", err)
			continue
		}
		return nil
	}
	return fmt.Errorf("all HuggingFace endpoints failed for %s", repo)
}

// hfRepoFile represents a file entry from the HuggingFace tree API.
type hfRepoFile struct {
	Type string `json:"type"` // "file" or "directory"
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// downloadHFRepo lists all files in a HuggingFace repo and downloads them.
func downloadHFRepo(ctx context.Context, endpoint, repo, destPath string) error {
	// GET /api/models/{repo}/tree/main to list files
	apiURL := fmt.Sprintf("%s/api/models/%s/tree/main", endpoint, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return fmt.Errorf("create API request: %w", err)
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return fmt.Errorf("list repo files: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("list repo files: HTTP %d", resp.StatusCode)
	}

	var files []hfRepoFile
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return fmt.Errorf("parse file list: %w", err)
	}

	// Filter to downloadable files (skip directories, .gitattributes, etc.)
	var toDownload []hfRepoFile
	var totalSize int64
	for _, f := range files {
		if f.Type != "file" {
			continue
		}
		if strings.HasPrefix(f.Path, ".") {
			continue
		}
		toDownload = append(toDownload, f)
		totalSize += f.Size
	}

	slog.Info("repo file list retrieved",
		"files", len(toDownload),
		"total_size_mb", totalSize/(1024*1024),
	)

	// Download each file, skipping already completed ones
	for i, f := range toDownload {
		fileDest := filepath.Join(destPath, f.Path)
		// Skip if file already exists with correct size
		if info, err := os.Stat(fileDest); err == nil && info.Size() == f.Size {
			slog.Info("skipping already downloaded",
				"progress", fmt.Sprintf("[%d/%d]", i+1, len(toDownload)),
				"file", f.Path,
			)
			continue
		}
		fileURL := fmt.Sprintf("%s/%s/resolve/main/%s", endpoint, repo, f.Path)
		slog.Info("downloading file",
			"progress", fmt.Sprintf("[%d/%d]", i+1, len(toDownload)),
			"file", f.Path,
			"size_mb", f.Size/(1024*1024),
		)
		if err := Download(ctx, DownloadOptions{URL: fileURL, DestPath: fileDest}); err != nil {
			return fmt.Errorf("download %s: %w", f.Path, err)
		}
	}
	return nil
}

// hfEndpoints returns HuggingFace endpoints to try in order.
func hfEndpoints() []string {
	if ep := os.Getenv("HF_ENDPOINT"); ep != "" {
		return []string{ep}
	}
	// hf-mirror.com first (works in China), then official
	return []string{"https://hf-mirror.com", "https://huggingface.co"}
}

func downloadModelScope(ctx context.Context, repo, destPath string) error {
	if err := os.MkdirAll(destPath, 0o755); err != nil {
		return fmt.Errorf("create dest dir %s: %w", destPath, err)
	}

	// Prefer modelscope CLI if available
	if msCLI, err := exec.LookPath("modelscope"); err == nil {
		slog.Info("downloading via modelscope CLI", "repo", repo, "dest", destPath)
		cmd := exec.CommandContext(ctx, msCLI, "download", repo, "--local_dir", destPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Fallback: list files via ModelScope API, download each
	apiURL := fmt.Sprintf("https://modelscope.cn/api/v1/models/%s/repo/tree?Revision=master&Root=&Recursive=true", repo)
	slog.Info("downloading via ModelScope HTTP", "repo", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return fmt.Errorf("create API request: %w", err)
	}
	resp, err := downloadClient.Do(req)
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

	var total int
	for _, f := range msResp.Data.Files {
		if f.Type != "file" || strings.HasPrefix(f.Name, ".") {
			continue
		}
		total++
	}

	idx := 0
	for _, f := range msResp.Data.Files {
		if f.Type != "file" || strings.HasPrefix(f.Name, ".") {
			continue
		}
		idx++
		fileDest := filepath.Join(destPath, f.Name)
		if info, err := os.Stat(fileDest); err == nil && info.Size() == f.Size {
			slog.Info("skipping already downloaded",
				"progress", fmt.Sprintf("[%d/%d]", idx, total),
				"file", f.Name,
			)
			continue
		}
		fileURL := fmt.Sprintf("https://modelscope.cn/models/%s/resolve/master/%s", repo, f.Name)
		slog.Info("downloading file",
			"progress", fmt.Sprintf("[%d/%d]", idx, total),
			"file", f.Name,
			"size_mb", f.Size/(1024*1024),
		)
		if err := Download(ctx, DownloadOptions{URL: fileURL, DestPath: fileDest}); err != nil {
			return fmt.Errorf("download %s: %w", f.Name, err)
		}
	}
	return nil
}

