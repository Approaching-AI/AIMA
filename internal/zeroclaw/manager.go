package zeroclaw

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxStartAttempts = 3

// Manager handles the ZeroClaw sidecar process lifecycle.
type Manager struct {
	binaryPath string
	dataDir    string
	cmd        *exec.Cmd
	process    *os.Process
	gatewayURL string
	waitCh     chan error
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

// Start launches the ZeroClaw sidecar process.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.process != nil {
		pid := m.process.Pid
		m.mu.Unlock()
		return fmt.Errorf("zeroclaw already running (pid %d)", pid)
	}
	m.mu.Unlock()

	var lastErr error
	for attempt := 1; attempt <= maxStartAttempts; attempt++ {
		if err := m.startOnce(ctx); err != nil {
			lastErr = err
			if attempt == maxStartAttempts || ctx.Err() != nil {
				break
			}
			slog.Debug("zeroclaw start attempt failed", "attempt", attempt, "error", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		return nil
	}
	return lastErr
}

func (m *Manager) startOnce(ctx context.Context) error {
	port, err := reserveLoopbackPort()
	if err != nil {
		return fmt.Errorf("reserve zeroclaw gateway port: %w", err)
	}

	args := []string{"daemon"}
	if cfgDir := m.configDir(); cfgDir != "" {
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			return fmt.Errorf("create zeroclaw config dir: %w", err)
		}
		args = append(args, "--config-dir", cfgDir)
	}
	args = append(args, "-p", strconv.Itoa(port), "--host", "127.0.0.1")

	cmd := exec.CommandContext(ctx, m.binaryPath, args...)
	if m.dataDir != "" {
		cmd.Dir = m.dataDir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdout.Close()
		return fmt.Errorf("create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start zeroclaw: %w", err)
	}

	waitCh := make(chan error, 1)
	exitCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		waitCh <- err
		exitCh <- err
	}()
	go consumeLogs(stdout)
	go consumeLogs(stderr)

	gatewayURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	m.mu.Lock()
	m.cmd = cmd
	m.process = cmd.Process
	m.gatewayURL = gatewayURL
	m.waitCh = waitCh
	m.mu.Unlock()

	startCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := waitForGateway(startCtx, gatewayURL, exitCh); err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = m.Stop(stopCtx)
		return fmt.Errorf("wait for zeroclaw gateway: %w", err)
	}

	slog.Info("zeroclaw started", "pid", cmd.Process.Pid, "gateway", gatewayURL)
	return nil
}

// Stop gracefully stops the ZeroClaw process.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	process := m.process
	waitCh := m.waitCh
	m.cmd = nil
	m.process = nil
	m.gatewayURL = ""
	m.waitCh = nil
	m.mu.Unlock()

	if process == nil {
		return nil
	}

	// Send termination signal (os.Interrupt is not supported on Windows)
	if runtime.GOOS == "windows" {
		_ = process.Kill()
	} else if err := process.Signal(os.Interrupt); err != nil {
		slog.Debug("interrupt failed, killing", "error", err)
		_ = process.Kill()
	}

	select {
	case <-ctx.Done():
		_ = process.Kill()
		return ctx.Err()
	case err := <-waitCh:
		if err != nil {
			slog.Debug("zeroclaw exited with error", "error", err)
		}
		return nil
	}
}

// Health checks if the ZeroClaw process is alive.
func (m *Manager) Health() bool {
	m.mu.Lock()
	process := m.process
	gatewayURL := m.gatewayURL
	m.mu.Unlock()

	if process == nil {
		return false
	}

	// On Unix, signal 0 checks process existence without side effects.
	// On Windows, signals are not supported; we rely on process != nil
	// and let sendQuery detect actual failures.
	if runtime.GOOS != "windows" {
		if err := process.Signal(syscall.Signal(0)); err != nil {
			return false
		}
	}
	if gatewayURL == "" {
		return true
	}
	healthCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(healthCtx, http.MethodGet, gatewayURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// SupportsSessions reports whether the current ZeroClaw transport preserves named conversation state.
func (m *Manager) SupportsSessions() bool {
	return false
}

// Ask sends a query to ZeroClaw. When the daemon gateway is healthy, use webhook mode.
func (m *Manager) Ask(ctx context.Context, query string) (string, error) {
	if !m.Health() {
		return m.runOneShot(ctx, query)
	}
	return m.sendWebhook(ctx, query)
}

// AskWithSession continues a named session.
func (m *Manager) AskWithSession(ctx context.Context, sessionID, query string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return m.Ask(ctx, query)
	}
	return "", fmt.Errorf("zeroclaw daemon webhook mode does not support named sessions")
}

// Plan asks ZeroClaw to return a structured exploration plan JSON blob.
func (m *Manager) Plan(ctx context.Context, request json.RawMessage) (json.RawMessage, error) {
	if !m.Health() {
		return m.planOneShot(ctx, request)
	}
	content, err := m.sendWebhook(ctx, buildPlanPrompt(request))
	if err != nil {
		return nil, err
	}
	if extracted := extractJSON(content); extracted != "" {
		return json.RawMessage(extracted), nil
	}
	return nil, fmt.Errorf("zeroclaw returned no plan payload")
}

func (m *Manager) sendWebhook(ctx context.Context, query string) (string, error) {
	m.mu.Lock()
	gatewayURL := m.gatewayURL
	m.mu.Unlock()
	if gatewayURL == "" {
		return "", fmt.Errorf("zeroclaw gateway unavailable")
	}

	reqBody, err := json.Marshal(map[string]string{"message": query})
	if err != nil {
		return "", fmt.Errorf("marshal webhook request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL+"/webhook", strings.NewReader(string(reqBody)))
	if err != nil {
		return "", fmt.Errorf("create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("call zeroclaw webhook: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read zeroclaw webhook response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("zeroclaw webhook: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Response string `json:"response"`
		Error    string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode zeroclaw webhook response: %w", err)
	}
	if payload.Error != "" {
		return "", fmt.Errorf("zeroclaw webhook: %s", payload.Error)
	}
	return strings.TrimSpace(payload.Response), nil
}

func (m *Manager) planOneShot(ctx context.Context, request json.RawMessage) (json.RawMessage, error) {
	output, err := m.runOneShot(ctx, buildPlanPrompt(request))
	if err != nil {
		return nil, err
	}
	if extracted := extractJSON(output); extracted != "" {
		return json.RawMessage(extracted), nil
	}
	return nil, fmt.Errorf("zeroclaw one-shot output did not contain JSON plan")
}

func buildPlanPrompt(request json.RawMessage) string {
	return strings.TrimSpace(`Return only minified JSON for an AIMA ExplorationPlan.
No markdown, no prose, no code fences.
Preserve requested kind, target, source_ref, constraints, and benchmark_profile unless you have a strong reason to tighten them.
` + string(request))
}

func extractJSON(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```JSON")
		trimmed = strings.TrimPrefix(trimmed, "```")
		if idx := strings.LastIndex(trimmed, "```"); idx >= 0 {
			trimmed = trimmed[:idx]
		}
	}
	start := strings.IndexByte(trimmed, '{')
	end := strings.LastIndexByte(trimmed, '}')
	if start < 0 || end <= start {
		return ""
	}
	return strings.TrimSpace(trimmed[start : end+1])
}

func (m *Manager) runOneShot(ctx context.Context, query string) (string, error) {
	args := []string{"agent"}
	cfg := m.loadOneShotConfig()
	if cfgDir := m.configDir(); cfgDir != "" {
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			return "", fmt.Errorf("create zeroclaw config dir: %w", err)
		}
		args = append(args, "--config-dir", cfgDir)
	}
	if cfg.Provider != "" {
		args = append(args, "-p", cfg.Provider)
	}
	model := cfg.Model
	if model == "" && cfg.APIURL != "" {
		if discovered, err := discoverOneShotModel(ctx, cfg.APIURL, cfg.APIKey); err == nil {
			model = discovered
		}
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "-m", query)

	cmd := exec.CommandContext(ctx, m.binaryPath, args...)
	if m.dataDir != "" {
		cmd.Dir = m.dataDir
	}
	cmd.Env = os.Environ()
	if cfg.APIKey != "" && os.Getenv("OPENAI_API_KEY") == "" {
		cmd.Env = append(cmd.Env, "OPENAI_API_KEY="+cfg.APIKey)
	}
	if cfg.APIURL != "" && os.Getenv("OPENAI_BASE_URL") == "" {
		cmd.Env = append(cmd.Env, "OPENAI_BASE_URL="+cfg.APIURL)
	}
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		if output != "" {
			return "", fmt.Errorf("run zeroclaw agent: %w: %s", err, output)
		}
		return "", fmt.Errorf("run zeroclaw agent: %w", err)
	}
	return output, nil
}

func (m *Manager) configDir() string {
	if m.dataDir == "" {
		return ""
	}
	return filepath.Join(m.dataDir, "zeroclaw")
}

func reserveLoopbackPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener addr type %T", ln.Addr())
	}
	return addr.Port, nil
}

func waitForGateway(ctx context.Context, gatewayURL string, exitCh <-chan error) error {
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+"/health", nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case err := <-exitCh:
			if err != nil {
				return fmt.Errorf("zeroclaw exited before gateway became ready: %w", err)
			}
			return fmt.Errorf("zeroclaw exited before gateway became ready")
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func consumeLogs(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		slog.Debug("zeroclaw", "line", scanner.Text())
	}
}

type oneShotConfig struct {
	Provider string
	Model    string
	APIURL   string
	APIKey   string
}

func (m *Manager) loadOneShotConfig() oneShotConfig {
	configPath := filepath.Join(m.configDir(), "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return oneShotConfig{}
	}
	return oneShotConfig{
		Provider: extractConfigString(data, "default_provider"),
		Model:    extractConfigString(data, "default_model"),
		APIURL:   extractConfigString(data, "api_url"),
		APIKey:   extractConfigString(data, "api_key"),
	}
}

func extractConfigString(data []byte, key string) string {
	prefix := key + " = "
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
		return strings.Trim(value, `"`)
	}
	return ""
}

func discoverOneShotModel(ctx context.Context, apiURL, apiKey string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/models", nil)
	if err != nil {
		return "", fmt.Errorf("create models request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("models endpoint: HTTP %d", resp.StatusCode)
	}

	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		return "", fmt.Errorf("decode models: %w", err)
	}
	if len(models.Data) == 0 || models.Data[0].ID == "" {
		return "", fmt.Errorf("no models available")
	}
	return models.Data[0].ID, nil
}
