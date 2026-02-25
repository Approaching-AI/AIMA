package zeroclaw

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

const (
	releaseBaseURL = "https://github.com/zeroclaw/zeroclaw/releases/latest/download"
)

// platformBinary returns the binary name for the current platform.
func platformBinary() (string, error) {
	os := runtime.GOOS
	arch := runtime.GOARCH

	switch {
	case os == "linux" && arch == "amd64":
		return "zeroclaw-linux-amd64", nil
	case os == "linux" && arch == "arm64":
		return "zeroclaw-linux-arm64", nil
	case os == "darwin" && arch == "amd64":
		return "zeroclaw-darwin-amd64", nil
	case os == "darwin" && arch == "arm64":
		return "zeroclaw-darwin-arm64", nil
	case os == "windows" && arch == "amd64":
		return "zeroclaw-windows-amd64.exe", nil
	default:
		return "", fmt.Errorf("unsupported platform: %s/%s", os, arch)
	}
}

// downloadURL returns the download URL for the current platform.
func downloadURL() (string, error) {
	bin, err := platformBinary()
	if err != nil {
		return "", err
	}
	return releaseBaseURL + "/" + bin, nil
}

// Install downloads the ZeroClaw binary for the current platform.
// Returns the path to the installed binary.
func Install(ctx context.Context, destDir string) (string, error) {
	return InstallWith(ctx, destDir, http.DefaultClient)
}

// InstallWith downloads the ZeroClaw binary using the given HTTP client.
// This is useful for testing with a mock server.
func InstallWith(ctx context.Context, destDir string, client *http.Client) (string, error) {
	return installFrom(ctx, destDir, client, releaseBaseURL)
}

// installFrom downloads from a custom base URL (for testing).
func installFrom(ctx context.Context, destDir string, client *http.Client, baseURL string) (string, error) {
	binName, err := platformBinary()
	if err != nil {
		return "", err
	}

	url := baseURL + "/" + binName

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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

	destPath := filepath.Join(destDir, binName)
	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	written, err := io.Copy(f, resp.Body)
	if err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("write binary: %w", err)
	}

	// Verify download is not empty or truncated (error pages are small)
	const minBinarySize = 100 * 1024 // 100KB minimum for a real binary
	if written < minBinarySize {
		os.Remove(destPath)
		return "", fmt.Errorf("downloaded binary too small (%d bytes), possibly corrupt or an error page", written)
	}

	return destPath, nil
}
