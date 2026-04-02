package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// FleetEndpoint represents a discovered remote LLM endpoint.
type FleetEndpoint struct {
	BaseURL string // e.g., "http://<REDACTED_IP>:6188/v1"
	Model   string // first model ID
}

// DiscoverFunc discovers fleet LLM endpoints via mDNS.
// Called lazily when the local endpoint has no models.
type DiscoverFunc func(ctx context.Context, apiKey string) []FleetEndpoint

// OpenAIClient implements LLMClient using the OpenAI-compatible chat completions API.
type OpenAIClient struct {
	baseURL     string
	model       string
	apiKey      string
	userAgent   string
	extraParams map[string]any
	httpClient  *http.Client
	discoverFn  DiscoverFunc

	manageTimeout bool
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

// WithUserAgent sets a custom User-Agent header (some providers require this).
func WithUserAgent(ua string) OpenAIOption {
	return func(c *OpenAIClient) { c.userAgent = ua }
}

// WithHTTPClient sets a custom http.Client.
func WithHTTPClient(hc *http.Client) OpenAIOption {
	return func(c *OpenAIClient) {
		c.httpClient = hc
		c.manageTimeout = false
	}
}

// WithRequestTimeout overrides the default request timeout.
func WithRequestTimeout(timeout time.Duration) OpenAIOption {
	return func(c *OpenAIClient) {
		if c.httpClient == nil {
			c.httpClient = &http.Client{}
		}
		if timeout > 0 {
			c.httpClient.Timeout = timeout
			c.manageTimeout = false
			return
		}
		c.httpClient.Timeout = defaultRequestTimeout(c.baseURL)
		c.manageTimeout = true
	}
}

// WithDiscoverFunc sets a fleet discovery function for LLM endpoint fallback.
func WithDiscoverFunc(fn DiscoverFunc) OpenAIOption {
	return func(c *OpenAIClient) { c.discoverFn = fn }
}

// WithExtraParams sets provider-specific parameters merged into every request body.
// Example: {"temperature": 0.6} or {"extra_body": {"thinking": {"type": "enabled"}}}.
func WithExtraParams(params map[string]any) OpenAIOption {
	return func(c *OpenAIClient) { c.extraParams = params }
}

// NewOpenAIClient creates an OpenAI-compatible LLM client.
// baseURL should include the /v1 prefix (e.g. "http://localhost:6188/v1").
func NewOpenAIClient(baseURL string, opts ...OpenAIOption) *OpenAIClient {
	baseURL = EnsureHTTPScheme(baseURL)
	c := &OpenAIClient{
		baseURL:       baseURL,
		httpClient:    &http.Client{Timeout: defaultRequestTimeout(baseURL)},
		manageTimeout: true,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Endpoint returns the current base URL.
func (c *OpenAIClient) Endpoint() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL
}

// IsLocalEndpoint returns true when the client targets a loopback address.
func (c *OpenAIClient) IsLocalEndpoint() bool {
	return IsLoopbackEndpoint(c.Endpoint())
}

// SetEndpoint updates the base URL at runtime (hot-swap, no restart).
// If the URL has no scheme, "http://" is prepended automatically.
func (c *OpenAIClient) SetEndpoint(baseURL string) {
	baseURL = EnsureHTTPScheme(baseURL)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseURL = baseURL
	if c.manageTimeout && c.httpClient != nil {
		c.httpClient.Timeout = defaultRequestTimeout(baseURL)
	}
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

// SetUserAgent updates the User-Agent header at runtime.
func (c *OpenAIClient) SetUserAgent(ua string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.userAgent = ua
}

// SetExtraParams updates provider-specific extra parameters at runtime.
func (c *OpenAIClient) SetExtraParams(params map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.extraParams = params
}

// SetRequestTimeout updates the request timeout at runtime. Passing 0 restores the default policy.
func (c *OpenAIClient) SetRequestTimeout(timeout time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.httpClient == nil {
		c.httpClient = &http.Client{}
	}
	if timeout > 0 {
		c.httpClient.Timeout = timeout
		c.manageTimeout = false
		return
	}
	c.httpClient.Timeout = defaultRequestTimeout(c.baseURL)
	c.manageTimeout = true
}

// ChatCompletion sends a chat completion request with optional tool definitions.
func (c *OpenAIClient) ChatCompletion(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error) {
	// Snapshot mutable fields under read lock (don't hold during I/O)
	c.mu.RLock()
	baseURL := c.baseURL
	apiKey := c.apiKey
	userAgent := c.userAgent
	extraParams := c.extraParams
	c.mu.RUnlock()

	model, err := c.resolveModel(ctx)
	if err != nil {
		return nil, err
	}

	wireMessages := make([]chatMessage, len(messages))
	for i, m := range messages {
		wireMessages[i] = chatMessage{
			Role:             m.Role,
			Content:          m.Content,
			ReasoningContent: m.ReasoningContent,
			ToolCallID:       m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			wireMessages[i].ToolCalls = append(wireMessages[i].ToolCalls, chatToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: chatFunction{
					Name:      sanitizeToolName(tc.Name),
					Arguments: tc.Arguments,
				},
			})
		}
	}

	// Build request body as map so extraParams can inject arbitrary top-level fields.
	reqBody := map[string]any{
		"model":    model,
		"messages": wireMessages,
	}

	// Some LLM providers (e.g. Kimi) reject dots in function names.
	// Sanitize: "deploy.apply" → "deploy__apply", and build a reverse map.
	wireToOrig := make(map[string]string, len(tools))
	if len(tools) > 0 {
		apiTools := make([]chatTool, len(tools))
		for i, t := range tools {
			wireName := sanitizeToolName(t.Name)
			wireToOrig[wireName] = t.Name
			apiTools[i] = chatTool{
				Type: "function",
				Function: chatToolDef{
					Name:        wireName,
					Description: t.Description,
					Parameters:  t.InputSchema,
				},
			}
		}
		reqBody["tools"] = apiTools
	}

	// Merge extra params (temperature, top_p, thinking, etc.) — provider-agnostic.
	for k, v := range extraParams {
		if k != "model" && k != "messages" && k != "tools" {
			reqBody[k] = v
		}
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	if contextWindow, ok := c.resolveLocalContextWindow(ctx, baseURL, model, apiKey, userAgent); ok {
		estimatedPromptTokens := estimatePromptTokens(body)
		if estimatedPromptTokens > contextWindow {
			return nil, fmt.Errorf("chat completions preflight: estimated prompt tokens %d exceed local context window %d for model %s; reduce prompt/tool load or redeploy with a larger ctx_size", estimatedPromptTokens, contextWindow, model)
		}
	}

	url := baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if userAgent != "" {
		httpReq.Header.Set("User-Agent", userAgent)
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
		// Invalidate cached model on 404/503 so the next call re-resolves
		if httpResp.StatusCode == http.StatusNotFound || httpResp.StatusCode == http.StatusServiceUnavailable {
			c.mu.Lock()
			c.cachedModel = ""
			c.modelCachedAt = time.Time{}
			c.mu.Unlock()
		}
		return nil, fmt.Errorf("chat completions (POST %s, model=%s): HTTP %d: %s", url, model, httpResp.StatusCode, respBody)
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("chat completions: empty choices")
	}

	msg := chatResp.Choices[0].Message
	resp := &Response{Content: msg.Content, ReasoningContent: msg.ReasoningContent}
	for _, tc := range msg.ToolCalls {
		name := tc.Function.Name
		// Reverse-map sanitized name back to original (e.g. "deploy__apply" → "deploy.apply")
		if orig, ok := wireToOrig[name]; ok {
			name = orig
		}
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      name,
			Arguments: tc.Function.Arguments,
		})
	}
	return resp, nil
}

const modelCacheTTL = 30 * time.Second

func defaultRequestTimeout(baseURL string) time.Duration {
	if IsLoopbackEndpoint(baseURL) {
		return 30 * time.Minute
	}
	return 5 * time.Minute
}

func estimatePromptTokens(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	return (len(body)+3)/4 + 64
}

// IsLoopbackEndpoint returns true if the URL targets a loopback address.
func IsLoopbackEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func statusURLFromBaseURL(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	trimmed = strings.TrimSuffix(trimmed, "/")
	trimmed = strings.TrimSuffix(trimmed, "/v1")
	return trimmed + "/status"
}

func (c *OpenAIClient) resolveLocalContextWindow(ctx context.Context, baseURL, model, apiKey, userAgent string) (int, bool) {
	if !IsLoopbackEndpoint(baseURL) || strings.TrimSpace(model) == "" || c.httpClient == nil {
		return 0, false
	}
	statusCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(statusCtx, http.MethodGet, statusURLFromBaseURL(baseURL), nil)
	if err != nil {
		return 0, false
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	var status struct {
		Models []struct {
			ModelName           string `json:"model_name"`
			ContextWindowTokens int    `json:"context_window_tokens"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256*1024)).Decode(&status); err != nil {
		return 0, false
	}
	for _, candidate := range status.Models {
		if strings.EqualFold(candidate.ModelName, model) && candidate.ContextWindowTokens > 0 {
			return candidate.ContextWindowTokens, true
		}
	}
	return 0, false
}

func (c *OpenAIClient) resolveModel(ctx context.Context) (string, error) {
	// Snapshot mutable fields under read lock
	c.mu.RLock()
	model := c.model
	cached := c.cachedModel
	cachedAt := c.modelCachedAt
	c.mu.RUnlock()

	if cached != "" && time.Since(cachedAt) < modelCacheTTL {
		return cached, nil
	}

	if model != "" {
		models, err := c.fetchModels(ctx)
		if err != nil {
			return model, nil
		}
		if matched := matchConfiguredModel(models, model); matched != "" {
			c.mu.Lock()
			c.cachedModel = matched
			c.modelCachedAt = time.Now()
			c.mu.Unlock()
			return matched, nil
		}
		return model, nil
	}

	models, err := c.fetchModels(ctx)
	if err != nil {
		// Local endpoint unreachable — try fleet discovery
		if ep, fErr := c.discoverFleetEndpoint(ctx); fErr == nil {
			return ep.Model, nil
		}
		return "", fmt.Errorf("fetch models: %w", err)
	}
	if len(models) == 0 {
		// Local has no models — try fleet discovery
		if ep, fErr := c.discoverFleetEndpoint(ctx); fErr == nil {
			return ep.Model, nil
		}
		c.mu.RLock()
		baseURL := c.baseURL
		c.mu.RUnlock()
		return "", fmt.Errorf("no models available at %s/models", baseURL)
	}

	// Update cache under write lock
	c.mu.Lock()
	c.cachedModel = models[0].ID
	c.modelCachedAt = time.Now()
	result := c.cachedModel
	c.mu.Unlock()
	return result, nil
}

// discoverFleetEndpoint tries to find a remote LLM endpoint via fleet mDNS discovery.
// On success, hot-swaps baseURL and caches the discovered model.
func (c *OpenAIClient) discoverFleetEndpoint(ctx context.Context) (*FleetEndpoint, error) {
	c.mu.RLock()
	discoverFn := c.discoverFn
	apiKey := c.apiKey
	c.mu.RUnlock()

	if discoverFn == nil {
		return nil, fmt.Errorf("no discover function configured")
	}

	slog.Debug("local LLM endpoint has no models, trying fleet discovery")
	endpoints := discoverFn(ctx, apiKey)
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no fleet endpoints with models found")
	}

	ep := &endpoints[0]
	slog.Info("discovered fleet LLM endpoint", "baseURL", ep.BaseURL, "model", ep.Model)

	// Hot-swap to discovered endpoint
	c.mu.Lock()
	c.baseURL = ep.BaseURL
	c.cachedModel = ep.Model
	c.modelCachedAt = time.Now()
	c.mu.Unlock()

	return ep, nil
}

// Available checks if the LLM endpoint is reachable.
func (c *OpenAIClient) Available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	c.mu.RLock()
	model := c.model
	c.mu.RUnlock()

	if model != "" {
		models, err := c.fetchModels(ctx)
		if err != nil {
			return false
		}
		return matchConfiguredModel(models, model) != ""
	}

	_, err := c.resolveModel(ctx)
	return err == nil
}

func matchConfiguredModel(models []modelData, requested string) string {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return ""
	}
	for _, candidate := range models {
		if candidate.ID == requested {
			return candidate.ID
		}
	}
	for _, candidate := range models {
		if strings.EqualFold(candidate.ID, requested) {
			return candidate.ID
		}
	}
	return ""
}

func (c *OpenAIClient) fetchModels(ctx context.Context) ([]modelData, error) {
	c.mu.RLock()
	baseURL := c.baseURL
	apiKey := c.apiKey
	userAgent := c.userAgent
	c.mu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create models request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read models response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models endpoint: HTTP %d: %s", resp.StatusCode, body)
	}

	var modelsResp modelsResponse
	if err := json.Unmarshal(body, &modelsResp); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	return modelsResp.Data, nil
}

// --- JSON wire types ---

type chatMessage struct {
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
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

// sanitizeToolName converts MCP dot-separated names to LLM-compatible names.
// "deploy.apply" → "deploy__apply" (double underscore to avoid collision with
// names that naturally contain single underscores like "fleet.list_devices").
func sanitizeToolName(name string) string {
	if !strings.Contains(name, ".") {
		return name
	}
	return strings.ReplaceAll(name, ".", "__")
}

// EnsureHTTPScheme prepends "http://" when the URL has no scheme at all.
// URLs that already contain "://" (http, https, or anything else) are returned as-is.
func EnsureHTTPScheme(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	if s == "" || strings.Contains(s, "://") {
		return s
	}
	return "http://" + s
}
