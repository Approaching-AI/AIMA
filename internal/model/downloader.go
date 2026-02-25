package model

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	if resp.StatusCode == http.StatusPartialContent {
		totalSize = existingSize + resp.ContentLength
	} else {
		totalSize = resp.ContentLength
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

