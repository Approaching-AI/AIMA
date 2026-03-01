package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client makes REST API calls to remote AIMA devices.
type Client struct {
	http   *http.Client
	apiKey string
}

// NewClient creates a fleet HTTP client.
func NewClient(apiKey string) *Client {
	return &Client{
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
		apiKey: apiKey,
	}
}

// GetDeviceInfo fetches GET /api/v1/device from a remote device.
func (c *Client) GetDeviceInfo(ctx context.Context, d *Device) (json.RawMessage, error) {
	url := fmt.Sprintf("http://%s:%d/api/v1/device", d.AddrV4, d.Port)
	return c.doGet(ctx, url)
}

// ListTools fetches GET /api/v1/tools from a remote device.
func (c *Client) ListTools(ctx context.Context, d *Device) (json.RawMessage, error) {
	url := fmt.Sprintf("http://%s:%d/api/v1/tools", d.AddrV4, d.Port)
	return c.doGet(ctx, url)
}

// CallTool calls POST /api/v1/tools/{name} on a remote device.
func (c *Client) CallTool(ctx context.Context, d *Device, toolName string, params json.RawMessage) (json.RawMessage, error) {
	url := fmt.Sprintf("http://%s:%d/api/v1/tools/%s", d.AddrV4, d.Port, toolName)
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	return c.doPost(ctx, url, params)
}

// HealthCheck pings GET /health on a remote device.
func (c *Client) HealthCheck(ctx context.Context, d *Device) error {
	url := fmt.Sprintf("http://%s:%d/health", d.AddrV4, d.Port)
	_, err := c.doGet(ctx, url)
	return err
}

func (c *Client) doGet(ctx context.Context, url string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	return json.RawMessage(body), nil
}

func (c *Client) doPost(ctx context.Context, url string, payload json.RawMessage) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	return json.RawMessage(body), nil
}

func (c *Client) setAuth(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}
