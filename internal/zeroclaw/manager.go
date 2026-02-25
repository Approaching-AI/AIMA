package zeroclaw

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
)

// Manager handles the ZeroClaw sidecar process lifecycle.
type Manager struct {
	binaryPath string
	dataDir    string
	process    *os.Process
	stdin      io.WriteCloser
	stdout     *bufio.Scanner
	mu         sync.Mutex
}

// ManagerOption configures the Manager.
type ManagerOption func(*Manager)

// WithBinaryPath sets the path to the ZeroClaw binary.
func WithBinaryPath(path string) ManagerOption {
	return func(m *Manager) {
		m.binaryPath = path
	}
}

// WithDataDir sets the data directory for ZeroClaw.
func WithDataDir(dir string) ManagerOption {
	return func(m *Manager) {
		m.dataDir = dir
	}
}

// NewManager creates a new ZeroClaw lifecycle manager.
func NewManager(opts ...ManagerOption) *Manager {
	m := &Manager{
		binaryPath: "zeroclaw",
		dataDir:    "",
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Available checks if ZeroClaw binary exists and is executable.
func (m *Manager) Available() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	path, err := exec.LookPath(m.binaryPath)
	if err != nil {
		// Try as absolute/relative path
		info, err := os.Stat(m.binaryPath)
		if err != nil {
			return false
		}
		return !info.IsDir()
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// StartConfig configures ZeroClaw startup.
type StartConfig struct {
	ProviderBaseURL string // LLM endpoint (e.g., "http://localhost:8080/v1")
	MCPServerCmd    string // Command for AIMA MCP server
	MemoryPath      string // SQLite memory DB path
	IdentityPath    string // Agent identity markdown
}

// Start launches the ZeroClaw sidecar process.
func (m *Manager) Start(ctx context.Context, config StartConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.process != nil {
		return fmt.Errorf("zeroclaw already running (pid %d)", m.process.Pid)
	}

	args := []string{}
	if config.ProviderBaseURL != "" {
		args = append(args, "--provider", config.ProviderBaseURL)
	}
	if config.MCPServerCmd != "" {
		args = append(args, "--mcp-server", config.MCPServerCmd)
	}
	if config.MemoryPath != "" {
		args = append(args, "--memory", config.MemoryPath)
	}
	if config.IdentityPath != "" {
		args = append(args, "--identity", config.IdentityPath)
	}

	cmd := exec.CommandContext(ctx, m.binaryPath, args...)
	if m.dataDir != "" {
		cmd.Dir = m.dataDir
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return fmt.Errorf("create stdout pipe: %w", err)
	}

	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return fmt.Errorf("start zeroclaw: %w", err)
	}

	m.process = cmd.Process
	m.stdin = stdin
	m.stdout = bufio.NewScanner(stdout)
	m.stdout.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	slog.Info("zeroclaw started", "pid", m.process.Pid)
	return nil
}

// Stop gracefully stops the ZeroClaw process.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.process == nil {
		return nil
	}

	// Close stdin to signal the process to exit
	if m.stdin != nil {
		m.stdin.Close()
		m.stdin = nil
	}

	// Send termination signal (os.Interrupt is not supported on Windows)
	if runtime.GOOS == "windows" {
		m.process.Kill()
	} else if err := m.process.Signal(os.Interrupt); err != nil {
		slog.Debug("interrupt failed, killing", "error", err)
		m.process.Kill()
	}

	// Wait for process with timeout
	done := make(chan error, 1)
	go func() {
		_, err := m.process.Wait()
		done <- err
	}()

	select {
	case <-ctx.Done():
		m.process.Kill()
		return ctx.Err()
	case err := <-done:
		m.process = nil
		m.stdout = nil
		if err != nil {
			slog.Debug("zeroclaw exited with error", "error", err)
		}
		return nil
	}
}

// Health checks if the ZeroClaw process is alive.
func (m *Manager) Health() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.process == nil {
		return false
	}

	// On Unix, signal 0 checks process existence without side effects.
	// On Windows, signals are not supported; we rely on process != nil
	// and let sendQuery detect actual failures.
	if runtime.GOOS != "windows" {
		if err := m.process.Signal(syscall.Signal(0)); err != nil {
			m.process = nil
			return false
		}
	}
	return true
}

// Ask sends a query to ZeroClaw via stdio pipe.
func (m *Manager) Ask(ctx context.Context, query string) (string, error) {
	return m.sendQuery(ctx, "", query)
}

// AskWithSession continues a named session.
func (m *Manager) AskWithSession(ctx context.Context, sessionID, query string) (string, error) {
	return m.sendQuery(ctx, sessionID, query)
}

type zcRequest struct {
	Query     string `json:"query"`
	SessionID string `json:"session_id,omitempty"`
}

type zcResponse struct {
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

func (m *Manager) sendQuery(ctx context.Context, sessionID, query string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.process == nil {
		return "", fmt.Errorf("zeroclaw not running")
	}

	req := zcRequest{
		Query:     query,
		SessionID: sessionID,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	data = append(data, '\n')

	if _, err := m.stdin.Write(data); err != nil {
		return "", fmt.Errorf("write to zeroclaw: %w", err)
	}

	// Read response. Non-blocking channel writes prevent goroutine leak
	// when the context is cancelled before the read completes.
	respCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		if m.stdout.Scan() {
			select {
			case respCh <- m.stdout.Text():
			default:
			}
		} else {
			select {
			case errCh <- fmt.Errorf("read from zeroclaw: %w", m.stdout.Err()):
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-errCh:
		return "", err
	case line := <-respCh:
		var resp zcResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			return "", fmt.Errorf("unmarshal zeroclaw response: %w", err)
		}
		if resp.Error != "" {
			return "", fmt.Errorf("zeroclaw error: %s", resp.Error)
		}
		return resp.Content, nil
	}
}
