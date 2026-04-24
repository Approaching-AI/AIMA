package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jguan/aima/internal/proxy"
)

// FleetEndpoint represents a discovered remote LLM endpoint.
type FleetEndpoint struct {
	BaseURL             string // e.g., "http://<REDACTED_IP>:6188/v1"
	Model               string // selected model ID
	ParameterCount      string
	ContextWindowTokens int
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
	cachedBaseURL string
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
	c.cachedBaseURL = ""
	c.cachedModel = ""
	c.modelCachedAt = time.Time{}
	if c.manageTimeout && c.httpClient != nil {
		c.httpClient.Timeout = defaultRequestTimeout(baseURL)
	}
}

// SetModel updates the model name at runtime and invalidates the cached model.
func (c *OpenAIClient) SetModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = model
	c.cachedBaseURL = ""
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

type preparedChatRequest struct {
	url        string
	model      string
	apiKey     string
	userAgent  string
	body       []byte
	wireToOrig map[string]string
}

func (c *OpenAIClient) prepareChatRequest(ctx context.Context, messages []Message, tools []ToolDefinition, stream bool) (*preparedChatRequest, error) {
	// Snapshot mutable fields under read lock (don't hold during I/O)
	c.mu.RLock()
	apiKey := c.apiKey
	userAgent := c.userAgent
	extraParams := c.extraParams
	c.mu.RUnlock()

	target, err := c.resolveTarget(ctx)
	if err != nil {
		return nil, err
	}
	baseURL := target.BaseURL
	model := target.Model

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

	reqBody := map[string]any{
		"model":    model,
		"messages": wireMessages,
	}
	if stream {
		reqBody["stream"] = true
		reqBody["stream_options"] = map[string]any{"include_usage": true}
	}

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

	return &preparedChatRequest{
		url:        baseURL + "/chat/completions",
		model:      model,
		apiKey:     apiKey,
		userAgent:  userAgent,
		body:       body,
		wireToOrig: wireToOrig,
	}, nil
}

func buildChatHTTPRequest(ctx context.Context, prepared *preparedChatRequest) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", prepared.url, bytes.NewReader(prepared.body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if prepared.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+prepared.apiKey)
	}
	if prepared.userAgent != "" {
		httpReq.Header.Set("User-Agent", prepared.userAgent)
	}
	return httpReq, nil
}

func decodeChatResponse(respBody []byte, wireToOrig map[string]string) (*Response, error) {
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
		if orig, ok := wireToOrig[name]; ok {
			name = orig
		}
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      name,
			Arguments: tc.Function.Arguments,
		})
	}
	if chatResp.Usage != nil {
		resp.PromptTokens = chatResp.Usage.PromptTokens
		resp.CompletionTokens = chatResp.Usage.CompletionTokens
		resp.TotalTokens = chatResp.Usage.TotalTokens
	}
	logLLMOutput("chat_response", "", "", string(respBody), resp)
	return resp, nil
}

// ChatCompletion sends a chat completion request with optional tool definitions.
func (c *OpenAIClient) ChatCompletion(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error) {
	prepared, err := c.prepareChatRequest(ctx, messages, tools, false)
	if err != nil {
		return nil, err
	}
	httpReq, err := buildChatHTTPRequest(ctx, prepared)
	if err != nil {
		return nil, err
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
	logLLMOutput("chat_http_response", prepared.url, prepared.model, string(respBody), nil)
	if httpResp.StatusCode != http.StatusOK {
		// Invalidate cached model on 404/503 so the next call re-resolves
		if httpResp.StatusCode == http.StatusNotFound || httpResp.StatusCode == http.StatusServiceUnavailable {
			c.invalidateTargetCache()
		}
		return nil, fmt.Errorf("chat completions (POST %s, model=%s): HTTP %d: %s", prepared.url, prepared.model, httpResp.StatusCode, respBody)
	}
	return decodeChatResponse(respBody, prepared.wireToOrig)
}

// ChatCompletionStream sends a streamed chat completion request and emits content deltas as they arrive.
func (c *OpenAIClient) ChatCompletionStream(ctx context.Context, messages []Message, tools []ToolDefinition, onDelta func(CompletionDelta)) (*Response, error) {
	prepared, err := c.prepareChatRequest(ctx, messages, tools, true)
	if err != nil {
		return nil, err
	}
	httpReq, err := buildChatHTTPRequest(ctx, prepared)
	if err != nil {
		return nil, err
	}

	// Streaming: use a shallow copy with no timeout so the connection stays open.
	// The copy shares the underlying transport (connection pool) but has its own timeout.
	// Context-based cancellation still applies.
	streamClient := *c.httpClient
	streamClient.Timeout = 0
	httpResp, err := streamClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, readErr := io.ReadAll(io.LimitReader(httpResp.Body, 10*1024*1024))
		if readErr != nil {
			return nil, fmt.Errorf("read response: %w", readErr)
		}
		if httpResp.StatusCode == http.StatusNotFound || httpResp.StatusCode == http.StatusServiceUnavailable {
			c.invalidateTargetCache()
		}
		return nil, fmt.Errorf("chat completions (POST %s, model=%s): HTTP %d: %s", prepared.url, prepared.model, httpResp.StatusCode, respBody)
	}

	if !strings.Contains(strings.ToLower(httpResp.Header.Get("Content-Type")), "text/event-stream") {
		respBody, err := io.ReadAll(io.LimitReader(httpResp.Body, 10*1024*1024))
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}
		logLLMOutput("chat_stream_fallback_http_response", prepared.url, prepared.model, string(respBody), nil)
		return decodeChatResponse(respBody, prepared.wireToOrig)
	}

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	resp := &Response{}
	var lastUsage *chatUsage
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		logLLMOutput("chat_stream_chunk", prepared.url, prepared.model, payload, nil)
		if payload == "[DONE]" {
			break
		}
		var chunk chatStreamResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return nil, fmt.Errorf("decode stream chunk: %w", err)
		}
		if chunk.Usage != nil {
			lastUsage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		delta := choice.Delta
		if delta.Content == "" && choice.Message.Content != "" {
			delta.Content = choice.Message.Content
		}
		if delta.ReasoningContent == "" && choice.Message.ReasoningContent != "" {
			delta.ReasoningContent = choice.Message.ReasoningContent
		}
		if len(delta.ToolCalls) == 0 && len(choice.Message.ToolCalls) > 0 {
			delta.ToolCalls = choice.Message.ToolCalls
		}
		if delta.Content != "" {
			resp.Content += delta.Content
		}
		if delta.ReasoningContent != "" {
			resp.ReasoningContent += delta.ReasoningContent
		}
		var deltaToolCalls []ToolCall
		if len(delta.ToolCalls) > 0 {
			deltaToolCalls = mergeStreamToolCalls(nil, prepared.wireToOrig, delta.ToolCalls)
			resp.ToolCalls = mergeStreamToolCalls(resp.ToolCalls, prepared.wireToOrig, delta.ToolCalls)
		}
		if onDelta != nil && (delta.Content != "" || delta.ReasoningContent != "" || len(deltaToolCalls) > 0) {
			onDelta(CompletionDelta{
				Content:          delta.Content,
				ReasoningContent: delta.ReasoningContent,
				ToolCalls:        deltaToolCalls,
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}
	if lastUsage != nil {
		resp.PromptTokens = lastUsage.PromptTokens
		resp.CompletionTokens = lastUsage.CompletionTokens
		resp.TotalTokens = lastUsage.TotalTokens
	}
	// Fallback: if the provider didn't return usage (e.g. stream_options unsupported),
	// estimate completion tokens from output length so token budgets still work.
	if resp.TotalTokens == 0 && (resp.Content != "" || resp.ReasoningContent != "") {
		outputChars := len(resp.Content) + len(resp.ReasoningContent)
		for _, tc := range resp.ToolCalls {
			outputChars += len(tc.Arguments)
		}
		resp.CompletionTokens = outputChars / 4 // rough 4 chars/token estimate
		resp.TotalTokens = resp.CompletionTokens
	}
	if resp.Content == "" && resp.ReasoningContent == "" && len(resp.ToolCalls) == 0 {
		return nil, fmt.Errorf("chat completions: empty stream response")
	}
	logLLMOutput("chat_stream_response", prepared.url, prepared.model, "", resp)
	return resp, nil
}

func llmOutputLoggingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AIMA_LLM_LOG_OUTPUT"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func logLLMOutput(kind, url, model, payload string, resp *Response) {
	if !llmOutputLoggingEnabled() {
		return
	}
	attrs := []any{"kind", kind}
	if url != "" {
		attrs = append(attrs, "url", url)
	}
	if model != "" {
		attrs = append(attrs, "model", model)
	}
	if payload != "" {
		const maxPayloadLog = 512
		if len(payload) > maxPayloadLog {
			attrs = append(attrs, "payload", payload[:maxPayloadLog]+"…(truncated)")
		} else {
			attrs = append(attrs, "payload", payload)
		}
	}
	if resp != nil {
		attrs = append(attrs,
			"content", resp.Content,
			"reasoning_content", resp.ReasoningContent,
			"tool_calls", resp.ToolCalls,
			"prompt_tokens", resp.PromptTokens,
			"completion_tokens", resp.CompletionTokens,
			"total_tokens", resp.TotalTokens,
		)
	}
	// Stream chunks are high-volume; log at Debug to avoid noise.
	if kind == "chat_stream_chunk" {
		slog.Debug("llm output", attrs...)
	} else {
		slog.Info("llm output", attrs...)
	}
}

func mergeStreamToolCalls(existing []ToolCall, wireToOrig map[string]string, deltas []chatToolCall) []ToolCall {
	for _, tc := range deltas {
		idx := streamToolCallIndex(existing, tc)
		if idx < 0 {
			existing = append(existing, ToolCall{})
			idx = len(existing) - 1
		}
		for len(existing) <= idx {
			existing = append(existing, ToolCall{})
		}
		call := &existing[idx]
		if tc.ID != "" {
			call.ID = tc.ID
		}
		if name := strings.TrimSpace(tc.Function.Name); name != "" {
			if orig, ok := wireToOrig[name]; ok {
				name = orig
			}
			call.Name = name
		}
		call.Arguments = mergeStreamArguments(call.Arguments, tc.Function.Arguments)
	}
	return existing
}

func streamToolCallIndex(existing []ToolCall, tc chatToolCall) int {
	if tc.Index != nil && *tc.Index >= 0 {
		return *tc.Index
	}
	if tc.ID != "" {
		for i := range existing {
			if existing[i].ID == tc.ID {
				return i
			}
		}
	}
	return -1
}

func mergeStreamArguments(existing, fragment string) string {
	if fragment == "" {
		return existing
	}
	if existing == "" {
		return fragment
	}
	if strings.HasPrefix(fragment, existing) {
		return fragment
	}
	if strings.HasPrefix(existing, fragment) {
		return existing
	}
	return existing + fragment
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

type resolvedTarget struct {
	BaseURL string
	Model   string
}

// RouteCandidate describes a single model candidate considered for agent routing.
type RouteCandidate struct {
	BaseURL             string `json:"base_url"`
	Model               string `json:"model"`
	ParameterCount      string `json:"parameter_count,omitempty"`
	ContextWindowTokens int    `json:"context_window_tokens,omitempty"`
	EndpointIsLoopback  bool   `json:"endpoint_is_loopback"`
	FromFleet           bool   `json:"from_fleet,omitempty"`
}

// RouteProbe reports what the configured endpoint advertised during probing.
type RouteProbe struct {
	BaseURL            string           `json:"base_url"`
	EndpointIsLoopback bool             `json:"endpoint_is_loopback"`
	Available          bool             `json:"available"`
	Models             []RouteCandidate `json:"models,omitempty"`
	Error              string           `json:"error,omitempty"`
}

// RouteStatus captures the current LLM routing decision and supporting evidence.
type RouteStatus struct {
	Available                  bool             `json:"available"`
	ConfiguredEndpoint         string           `json:"configured_endpoint"`
	ConfiguredEndpointLoopback bool             `json:"configured_endpoint_is_loopback"`
	ConfiguredModel            string           `json:"configured_model,omitempty"`
	ConfiguredEndpointProbe    RouteProbe       `json:"configured_endpoint_probe"`
	FleetCandidates            []RouteCandidate `json:"fleet_candidates,omitempty"`
	Selected                   *RouteCandidate  `json:"selected,omitempty"`
	SelectionReason            string           `json:"selection_reason,omitempty"`
	Error                      string           `json:"error,omitempty"`
}

func (c *OpenAIClient) resolveModel(ctx context.Context) (string, error) {
	target, err := c.resolveTarget(ctx)
	if err != nil {
		return "", err
	}
	return target.Model, nil
}

func (c *OpenAIClient) resolveTarget(ctx context.Context) (resolvedTarget, error) {
	c.mu.RLock()
	baseURL := c.baseURL
	model := c.model
	cachedBaseURL := c.cachedBaseURL
	cachedModel := c.cachedModel
	cachedAt := c.modelCachedAt
	c.mu.RUnlock()

	if cachedModel != "" && time.Since(cachedAt) < modelCacheTTL {
		if cachedBaseURL == "" {
			cachedBaseURL = baseURL
		}
		return resolvedTarget{BaseURL: cachedBaseURL, Model: cachedModel}, nil
	}

	if model != "" {
		target, ok, err := c.resolveConfiguredTarget(ctx, baseURL, model)
		if err == nil && ok {
			c.cacheTarget(target)
			return target, nil
		}
		if fleetTarget, found := c.discoverFleetTarget(ctx, model); found {
			c.cacheTarget(fleetTarget)
			return fleetTarget, nil
		}
		if err != nil {
			return resolvedTarget{}, fmt.Errorf("resolve configured model %q at %s: %w", model, baseURL, err)
		}
		return resolvedTarget{}, fmt.Errorf("configured model %q not available at %s/models", model, baseURL)
	}

	target, ok, err := c.bestTargetFromEndpoint(ctx, baseURL)
	if err == nil && ok {
		c.cacheTarget(target)
		return target, nil
	}
	if fleetTarget, found := c.discoverFleetTarget(ctx, ""); found {
		c.cacheTarget(fleetTarget)
		return fleetTarget, nil
	}
	if err != nil {
		return resolvedTarget{}, fmt.Errorf("fetch models: %w", err)
	}
	return resolvedTarget{}, fmt.Errorf("no models available at %s/models", baseURL)
}

func (c *OpenAIClient) discoverFleetTarget(ctx context.Context, requestedModel string) (resolvedTarget, bool) {
	c.mu.RLock()
	discoverFn := c.discoverFn
	apiKey := c.apiKey
	c.mu.RUnlock()

	if discoverFn == nil {
		return resolvedTarget{}, false
	}

	slog.Debug("local LLM endpoint has no models, trying fleet discovery")
	endpoints := discoverFn(ctx, apiKey)
	if len(endpoints) == 0 {
		return resolvedTarget{}, false
	}

	if requestedModel != "" {
		if matched := matchFleetEndpoint(endpoints, requestedModel); matched != nil {
			slog.Info("discovered fleet LLM endpoint", "baseURL", matched.BaseURL, "model", matched.Model)
			return resolvedTarget{BaseURL: matched.BaseURL, Model: matched.Model}, true
		}
	}

	ep := endpoints[0]
	slog.Info("discovered fleet LLM endpoint", "baseURL", ep.BaseURL, "model", ep.Model)
	return resolvedTarget{BaseURL: ep.BaseURL, Model: ep.Model}, true
}

// Available checks if the LLM endpoint is reachable.
func (c *OpenAIClient) Available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	_, err := c.resolveTarget(ctx)
	return err == nil
}

// RouteStatus returns a diagnostic snapshot of the current LLM routing decision.
// It probes the configured endpoint and fleet fallback candidates without mutating
// the configured endpoint.
func (c *OpenAIClient) RouteStatus(ctx context.Context) RouteStatus {
	c.mu.RLock()
	baseURL := c.baseURL
	model := c.model
	c.mu.RUnlock()

	status := RouteStatus{
		ConfiguredEndpoint:         baseURL,
		ConfiguredEndpointLoopback: IsLoopbackEndpoint(baseURL),
		ConfiguredModel:            model,
		ConfiguredEndpointProbe: RouteProbe{
			BaseURL:            baseURL,
			EndpointIsLoopback: IsLoopbackEndpoint(baseURL),
		},
	}

	localModels, localErr := c.fetchAdvertisedModels(ctx, baseURL)
	if localErr != nil {
		status.ConfiguredEndpointProbe.Error = localErr.Error()
	} else {
		status.ConfiguredEndpointProbe.Available = true
	}
	status.ConfiguredEndpointProbe.Models = routeCandidatesFromAdvertised(baseURL, false, localModels)

	fleetCandidates := c.discoverFleetCandidates(ctx)
	status.FleetCandidates = fleetCandidates

	selected, reason, err := selectRouteStatus(baseURL, model, localModels, localErr, fleetCandidates)
	if selected != nil {
		status.Available = true
		status.Selected = selected
		status.SelectionReason = reason
		return status
	}
	if err != nil {
		status.Error = err.Error()
	}
	return status
}

func matchConfiguredAdvertisedModel(models []proxy.AdvertisedModel, requested string) string {
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

func matchFleetEndpoint(endpoints []FleetEndpoint, requested string) *FleetEndpoint {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return nil
	}
	for i := range endpoints {
		if endpoints[i].Model == requested {
			return &endpoints[i]
		}
	}
	for i := range endpoints {
		if strings.EqualFold(endpoints[i].Model, requested) {
			return &endpoints[i]
		}
	}
	return nil
}

func (c *OpenAIClient) discoverFleetCandidates(ctx context.Context) []RouteCandidate {
	c.mu.RLock()
	discoverFn := c.discoverFn
	apiKey := c.apiKey
	c.mu.RUnlock()

	if discoverFn == nil {
		return nil
	}
	endpoints := discoverFn(ctx, apiKey)
	candidates := make([]RouteCandidate, 0, len(endpoints))
	for _, ep := range endpoints {
		if strings.TrimSpace(ep.BaseURL) == "" || strings.TrimSpace(ep.Model) == "" {
			continue
		}
		candidates = append(candidates, RouteCandidate{
			BaseURL:             ep.BaseURL,
			Model:               ep.Model,
			ParameterCount:      strings.TrimSpace(ep.ParameterCount),
			ContextWindowTokens: ep.ContextWindowTokens,
			EndpointIsLoopback:  IsLoopbackEndpoint(ep.BaseURL),
			FromFleet:           true,
		})
	}
	sortRouteCandidates(candidates)
	return candidates
}

func routeCandidatesFromAdvertised(baseURL string, fromFleet bool, models []proxy.AdvertisedModel) []RouteCandidate {
	candidates := make([]RouteCandidate, 0, len(models))
	for _, model := range models {
		if strings.TrimSpace(model.ID) == "" {
			continue
		}
		candidates = append(candidates, RouteCandidate{
			BaseURL:             baseURL,
			Model:               model.ID,
			ParameterCount:      strings.TrimSpace(model.ParameterCount),
			ContextWindowTokens: model.ContextWindowTokens,
			EndpointIsLoopback:  IsLoopbackEndpoint(baseURL),
			FromFleet:           fromFleet,
		})
	}
	sortRouteCandidates(candidates)
	return candidates
}

func sortRouteCandidates(candidates []RouteCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		return betterRouteCandidate(candidates[i], candidates[j])
	})
}

func betterRouteCandidate(a, b RouteCandidate) bool {
	return proxy.BetterAdvertisedModel(
		proxy.AdvertisedModel{
			ID:                  a.Model,
			ParameterCount:      a.ParameterCount,
			ContextWindowTokens: a.ContextWindowTokens,
			Remote:              !a.EndpointIsLoopback,
		},
		proxy.AdvertisedModel{
			ID:                  b.Model,
			ParameterCount:      b.ParameterCount,
			ContextWindowTokens: b.ContextWindowTokens,
			Remote:              !b.EndpointIsLoopback,
		},
	)
}

func selectRouteStatus(baseURL, configuredModel string, localModels []proxy.AdvertisedModel, localErr error, fleetCandidates []RouteCandidate) (*RouteCandidate, string, error) {
	if strings.TrimSpace(configuredModel) != "" {
		if discoveryUnsupported(localErr) || (localErr == nil && len(localModels) == 0) {
			return &RouteCandidate{
				BaseURL:            baseURL,
				Model:              configuredModel,
				EndpointIsLoopback: IsLoopbackEndpoint(baseURL),
			}, "configured_model_endpoint_unverified", nil
		}
		for _, model := range localModels {
			if matched := matchConfiguredAdvertisedModel([]proxy.AdvertisedModel{model}, configuredModel); matched != "" {
				candidate := routeCandidatesFromAdvertised(baseURL, false, []proxy.AdvertisedModel{model})[0]
				return &candidate, "configured_model_endpoint", nil
			}
		}
		for _, candidate := range fleetCandidates {
			if strings.EqualFold(candidate.Model, configuredModel) {
				cp := candidate
				return &cp, "configured_model_fleet", nil
			}
		}
		if localErr != nil {
			return nil, "", fmt.Errorf("resolve configured model %q at %s: %w", configuredModel, baseURL, localErr)
		}
		if len(localModels) > 0 {
			candidates := routeCandidatesFromAdvertised(baseURL, false, localModels)
			if len(candidates) > 0 {
				return &candidates[0], "configured_model_unavailable_local_fallback", nil
			}
		}
		return nil, "", fmt.Errorf("configured model %q not available at %s/models", configuredModel, baseURL)
	}

	if len(localModels) > 0 {
		candidates := routeCandidatesFromAdvertised(baseURL, false, localModels)
		if len(candidates) > 0 {
			return &candidates[0], "best_local_model", nil
		}
	}
	if len(fleetCandidates) > 0 {
		cp := fleetCandidates[0]
		return &cp, "fleet_fallback", nil
	}
	if localErr != nil {
		return nil, "", fmt.Errorf("fetch models: %w", localErr)
	}
	return nil, "", fmt.Errorf("no models available at %s/models", baseURL)
}

func (c *OpenAIClient) fetchModelsAt(ctx context.Context, baseURL string) ([]modelData, error) {
	c.mu.RLock()
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

func (c *OpenAIClient) resolveConfiguredTarget(ctx context.Context, baseURL, requestedModel string) (resolvedTarget, bool, error) {
	models, err := c.fetchAdvertisedModels(ctx, baseURL)
	if err != nil {
		if discoveryUnsupported(err) {
			return resolvedTarget{BaseURL: baseURL, Model: requestedModel}, true, nil
		}
		return resolvedTarget{}, false, err
	}
	if len(models) == 0 {
		return resolvedTarget{BaseURL: baseURL, Model: requestedModel}, true, nil
	}
	if matched := matchConfiguredAdvertisedModel(models, requestedModel); matched != "" {
		return resolvedTarget{BaseURL: baseURL, Model: matched}, true, nil
	}
	return resolvedTarget{}, false, nil
}

func (c *OpenAIClient) bestTargetFromEndpoint(ctx context.Context, baseURL string) (resolvedTarget, bool, error) {
	models, err := c.fetchAdvertisedModels(ctx, baseURL)
	if err != nil {
		return resolvedTarget{}, false, err
	}
	best, ok := proxy.BestAdvertisedModel(models)
	if !ok || strings.TrimSpace(best.ID) == "" {
		return resolvedTarget{}, false, nil
	}
	return resolvedTarget{BaseURL: baseURL, Model: best.ID}, true, nil
}

func (c *OpenAIClient) fetchAdvertisedModels(ctx context.Context, baseURL string) ([]proxy.AdvertisedModel, error) {
	models, err := c.fetchStatusModels(ctx, baseURL)
	if err == nil && len(models) > 0 {
		return models, nil
	}

	fallback, fallbackErr := c.fetchModelsAt(ctx, baseURL)
	if fallbackErr != nil {
		if err != nil {
			return nil, err
		}
		return nil, fallbackErr
	}
	ads := make([]proxy.AdvertisedModel, 0, len(fallback))
	for _, model := range fallback {
		if strings.TrimSpace(model.ID) == "" {
			continue
		}
		ads = append(ads, proxy.AdvertisedModel{ID: model.ID})
	}
	proxy.SortAdvertisedModels(ads)
	return ads, nil
}

func (c *OpenAIClient) fetchStatusModels(ctx context.Context, baseURL string) ([]proxy.AdvertisedModel, error) {
	c.mu.RLock()
	apiKey := c.apiKey
	userAgent := c.userAgent
	c.mu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURLFromBaseURL(baseURL), nil)
	if err != nil {
		return nil, fmt.Errorf("create status request: %w", err)
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
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		return nil, fmt.Errorf("status endpoint: HTTP %d: %s", resp.StatusCode, body)
	}

	var payload struct {
		Models []struct {
			ModelName           string `json:"model_name"`
			Ready               *bool  `json:"ready"`
			Remote              bool   `json:"remote"`
			ParameterCount      string `json:"parameter_count"`
			ContextWindowTokens int    `json:"context_window_tokens"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256*1024)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode status: %w", err)
	}

	models := make([]proxy.AdvertisedModel, 0, len(payload.Models))
	for _, model := range payload.Models {
		if model.Ready != nil && !*model.Ready {
			continue
		}
		if strings.TrimSpace(model.ModelName) == "" {
			continue
		}
		models = append(models, proxy.AdvertisedModel{
			ID:                  model.ModelName,
			ParameterCount:      model.ParameterCount,
			ContextWindowTokens: model.ContextWindowTokens,
			Remote:              model.Remote,
		})
	}
	proxy.SortAdvertisedModels(models)
	return models, nil
}

func (c *OpenAIClient) cacheTarget(target resolvedTarget) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cachedBaseURL = target.BaseURL
	c.cachedModel = target.Model
	c.modelCachedAt = time.Now()
}

func (c *OpenAIClient) invalidateTargetCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cachedBaseURL = ""
	c.cachedModel = ""
	c.modelCachedAt = time.Time{}
}

func discoveryUnsupported(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "HTTP 404") ||
		strings.Contains(msg, "HTTP 405") ||
		strings.Contains(msg, "HTTP 501")
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
	Index    *int         `json:"index,omitempty"`
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

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage,omitempty"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type chatStreamResponse struct {
	Choices []chatStreamChoice `json:"choices"`
	Usage   *chatUsage         `json:"usage,omitempty"`
}

type chatStreamChoice struct {
	Delta   chatMessage `json:"delta"`
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
// names that naturally contain single underscores after sanitization).
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
