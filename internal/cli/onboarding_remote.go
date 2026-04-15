package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// remoteOnboardingClient is the per-subcommand state captured by the --remote
// and --api-key flags. When endpoint is empty the CLI falls back to its
// in-process ToolDeps closures (offline-first); when it is set, every
// onboarding action issues a JSON-RPC tools/call against {endpoint}/mcp so the
// CLI can drive a remote `aima serve` exactly as the Web UI / MCP would.
type remoteOnboardingClient struct {
	endpoint string
	apiKey   string
}

// configured reports whether --remote was supplied (either via flag or the
// AIMA_REMOTE environment variable).
func (c *remoteOnboardingClient) configured() bool {
	return strings.TrimSpace(c.endpoint) != ""
}

// callOnboarding posts a tools/call for the onboarding tool and returns the
// raw inner JSON payload (the same shape the local closures return, so the
// existing per-subcommand printers work unchanged).
func (c *remoteOnboardingClient) callOnboarding(ctx context.Context, action string, args map[string]any) (json.RawMessage, error) {
	if args == nil {
		args = map[string]any{}
	}
	args["action"] = action

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "onboarding",
			"arguments": args,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode mcp request: %w", err)
	}

	endpoint := strings.TrimRight(c.endpoint, "/") + "/mcp"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build mcp request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	// Long-running actions (init/deploy) can take many minutes — use a generous
	// per-request timeout but still honour ctx cancellation.
	httpClient := &http.Client{Timeout: 30 * time.Minute}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read mcp response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mcp %s returned %d: %s", action, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var rpc struct {
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &rpc); err != nil {
		return nil, fmt.Errorf("decode mcp response: %w", err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("mcp error %d: %s", rpc.Error.Code, rpc.Error.Message)
	}
	if rpc.Result == nil || len(rpc.Result.Content) == 0 {
		return nil, fmt.Errorf("mcp %s: empty result", action)
	}
	text := rpc.Result.Content[0].Text
	if rpc.Result.IsError {
		return nil, fmt.Errorf("mcp %s: %s", action, strings.TrimSpace(text))
	}
	return json.RawMessage(text), nil
}

// envOrFlag returns the env-var value if the flag was left at its zero value.
func envOrFlag(flag, envKey string) string {
	if strings.TrimSpace(flag) != "" {
		return flag
	}
	return strings.TrimSpace(os.Getenv(envKey))
}
