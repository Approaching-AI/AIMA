package central

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenAICompleter implements LLMCompleter using the OpenAI-compatible chat API.
type OpenAICompleter struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// OpenAIOption configures the OpenAI completer.
type OpenAIOption func(*OpenAICompleter)

// WithOpenAIModel sets the model name (default: gpt-4).
func WithOpenAIModel(model string) OpenAIOption {
	return func(c *OpenAICompleter) { c.model = model }
}

// NewOpenAICompleter creates a completer using the OpenAI chat completions API.
func NewOpenAICompleter(baseURL, apiKey string, opts ...OpenAIOption) *OpenAICompleter {
	c := &OpenAICompleter{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   "gpt-4",
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *OpenAICompleter) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	reqBody := map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0.3,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return result.Choices[0].Message.Content, nil
}
