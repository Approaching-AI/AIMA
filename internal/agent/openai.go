package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// OpenAIClient implements LLMClient using the OpenAI-compatible chat completions API.
type OpenAIClient struct {
	baseURL    string
	model      string
	apiKey     string
	httpClient *http.Client

	mu            sync.RWMutex
	cachedModel   string
	modelCachedAt time.Time
}

// OpenAIOption configures the OpenAI client.
type OpenAIOption func(*OpenAIClient)

// WithModel sets the model name. If empty, the client auto-discovers via /models.
func WithModel(model string) OpenAIOption {
	return func(c *OpenAIClient) { c.model = model }
}

// WithAPIKey sets the API key for authenticated endpoints.
func WithAPIKey(key string) OpenAIOption {
	return func(c *OpenAIClient) { c.apiKey = key }
}

// WithHTTPClient sets a custom http.Client.
func WithHTTPClient(hc *http.Client) OpenAIOption {
	return func(c *OpenAIClient) { c.httpClient = hc }
}

// NewOpenAIClient creates an OpenAI-compatible LLM client.
// baseURL should include the /v1 prefix (e.g. "http://localhost:6188/v1").
func NewOpenAIClient(baseURL string, opts ...OpenAIOption) *OpenAIClient {
	c := &OpenAIClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 5 * time.Minute},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// SetEndpoint updates the base URL at runtime (hot-swap, no restart).
func (c *OpenAIClient) SetEndpoint(baseURL string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseURL = baseURL
}

// SetModel updates the model name at runtime and invalidates the cached model.
func (c *OpenAIClient) SetModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = model
	c.cachedModel = ""
	c.modelCachedAt = time.Time{}
}

// SetAPIKey updates the API key at runtime.
func (c *OpenAIClient) SetAPIKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiKey = key
}

// ChatCompletion sends a chat completion request with optional tool definitions.
func (c *OpenAIClient) ChatCompletion(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error) {
	// Snapshot mutable fields under read lock (don't hold during I/O)
	c.mu.RLock()
	baseURL := c.baseURL
	apiKey := c.apiKey
	c.mu.RUnlock()

	model, err := c.resolveModel(ctx)
	if err != nil {
		return nil, err
	}

	req := chatRequest{
		Model:    model,
		Messages: make([]chatMessage, len(messages)),
	}
	for i, m := range messages {
		req.Messages[i] = chatMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			req.Messages[i].ToolCalls = append(req.Messages[i].ToolCalls, chatToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: chatFunction{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
	}

	if len(tools) > 0 {
		apiTools := make([]chatTool, len(tools))
		for i, t := range tools {
			apiTools[i] = chatTool{
				Type: "function",
				Function: chatToolDef{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.InputSchema,
				},
			}
		}
		req.Tools = apiTools
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(httpResp.Body, 10*1024*1024)) // 10 MB limit
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("chat completions: HTTP %d: %s", httpResp.StatusCode, respBody)
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("chat completions: empty choices")
	}

	msg := chatResp.Choices[0].Message
	resp := &Response{Content: msg.Content}
	for _, tc := range msg.ToolCalls {
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return resp, nil
}

const modelCacheTTL = 30 * time.Second

func (c *OpenAIClient) resolveModel(ctx context.Context) (string, error) {
	// Snapshot mutable fields under read lock
	c.mu.RLock()
	model := c.model
	baseURL := c.baseURL
	apiKey := c.apiKey
	cached := c.cachedModel
	cachedAt := c.modelCachedAt
	c.mu.RUnlock()

	if model != "" {
		return model, nil
	}

	if cached != "" && time.Since(cachedAt) < modelCacheTTL {
		return cached, nil
	}

	// Fetch from /models endpoint (no lock held during I/O)
	httpReq, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/models", nil)
	if err != nil {
		return "", fmt.Errorf("create models request: %w", err)
	}
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("fetch models: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 10*1024*1024)) // 10 MB limit
	if err != nil {
		return "", fmt.Errorf("read models response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("models endpoint: HTTP %d: %s", httpResp.StatusCode, body)
	}

	var modelsResp modelsResponse
	if err := json.Unmarshal(body, &modelsResp); err != nil {
		return "", fmt.Errorf("decode models: %w", err)
	}
	if len(modelsResp.Data) == 0 {
		return "", fmt.Errorf("no models available at %s/models", baseURL)
	}

	// Update cache under write lock
	c.mu.Lock()
	c.cachedModel = modelsResp.Data[0].ID
	c.modelCachedAt = time.Now()
	result := c.cachedModel
	c.mu.Unlock()
	return result, nil
}

// Available checks if the LLM endpoint is reachable.
func (c *OpenAIClient) Available(ctx context.Context) bool {
	c.mu.RLock()
	baseURL := c.baseURL
	c.mu.RUnlock()

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/models", nil)
	if err != nil {
		return false
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// --- JSON wire types ---

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Tools    []chatTool    `json:"tools,omitempty"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string      `json:"type"`
	Function chatToolDef `json:"function"`
}

type chatToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type modelsResponse struct {
	Data []modelData `json:"data"`
}

type modelData struct {
	ID string `json:"id"`
}
