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

	mu            sync.Mutex
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

// ChatCompletion sends a chat completion request with optional tool definitions.
func (c *OpenAIClient) ChatCompletion(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error) {
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

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
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
	if c.model != "" {
		return c.model, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cachedModel != "" && time.Since(c.modelCachedAt) < modelCacheTTL {
		return c.cachedModel, nil
	}

	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/models", nil)
	if err != nil {
		return "", fmt.Errorf("create models request: %w", err)
	}
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("fetch models: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
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
		return "", fmt.Errorf("no models available at %s/models", c.baseURL)
	}

	c.cachedModel = modelsResp.Data[0].ID
	c.modelCachedAt = time.Now()
	return c.cachedModel, nil
}

// Available checks if the LLM endpoint is reachable.
func (c *OpenAIClient) Available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/models", nil)
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
