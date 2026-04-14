package benchmark

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

// ChatRequester implements Requester for LLM and VLM modalities.
// It sends OpenAI-compatible chat/completions requests with SSE streaming.
// When ImageURLs is non-empty, it operates in VLM mode with vision content blocks.
type ChatRequester struct {
	Model          string
	MaxTokens      int
	InputTokens    int
	Prompt         string
	Temperature    float64
	APIKey         string
	Timeout        time.Duration
	MinOutputRatio float64
	MaxRetries     int
	RetryDelay     time.Duration
	ImageURLs      []string // non-empty = VLM mode
}

func (r *ChatRequester) Modality() string {
	if len(r.ImageURLs) > 0 {
		return "vlm"
	}
	return "llm"
}

func (r *ChatRequester) WarmupRequests() int { return 2 }

func (r *ChatRequester) Do(ctx context.Context, endpoint string, seq int) (*Sample, error) {
	sample := r.sendWithRetry(ctx, endpoint)
	return sample, nil
}

func (r *ChatRequester) sendWithRetry(ctx context.Context, endpoint string) *Sample {
	if r.MaxRetries <= 0 && r.MinOutputRatio <= 0 {
		return r.sendStreamingRequest(ctx, endpoint)
	}

	const maxDelay = 30 * time.Second
	delay := r.RetryDelay
	if delay <= 0 {
		delay = time.Second
	}
	var lastSample *Sample
	maxRetries := r.MaxRetries
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return &Sample{Error: ctx.Err()}
			case <-time.After(delay):
				delay = min(delay*2, maxDelay)
			}
		}
		lastSample = r.sendStreamingRequest(ctx, endpoint)

		if attempt == maxRetries {
			break
		}

		if lastSample.Error != nil {
			continue
		}

		if r.MinOutputRatio > 0 {
			minTokens := int(float64(r.MaxTokens) * r.MinOutputRatio)
			if lastSample.OutputTokens < minTokens {
				continue
			}
		}

		break
	}
	return lastSample
}

func (r *ChatRequester) sendStreamingRequest(ctx context.Context, endpoint string) *Sample {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Normalize endpoint: strip any known suffix and append /v1/chat/completions
	endpoint = baseEndpoint(endpoint) + "/v1/chat/completions"

	messages := r.buildMessages()

	maxTokens := r.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 256
	}
	temperature := r.Temperature
	if temperature <= 0 {
		temperature = 0.01
	}

	payload := map[string]any{
		"model":       r.Model,
		"messages":    messages,
		"max_tokens":  maxTokens,
		"temperature": temperature,
		"stream":      true,
		"stream_options": map[string]bool{
			"include_usage": true,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return &Sample{Error: fmt.Errorf("marshal request: %w", err)}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return &Sample{Error: fmt.Errorf("create request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	if r.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}

	startTime := time.Now()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &Sample{Error: fmt.Errorf("send request: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &Sample{Error: fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))}
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	var ttft time.Duration
	var outputTokens, inputTokens int

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					Reasoning        string `json:"reasoning"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}

		if chunk.Usage != nil {
			inputTokens = chunk.Usage.PromptTokens
			outputTokens = chunk.Usage.CompletionTokens
		}

		if len(chunk.Choices) > 0 {
			d := chunk.Choices[0].Delta
			content := d.Content + d.Reasoning + d.ReasoningContent
			if content != "" && ttft == 0 {
				ttft = time.Since(startTime)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return &Sample{Error: fmt.Errorf("read SSE stream: %w", err)}
	}

	totalTime := time.Since(startTime)
	return &Sample{
		TTFTMs:       float64(ttft.Microseconds()) / 1000.0,
		LatencyMs:    float64(totalTime.Microseconds()) / 1000.0,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
}

func (r *ChatRequester) buildMessages() any {
	inputTokens := r.InputTokens
	if inputTokens <= 0 {
		inputTokens = 128
	}
	prompt := buildPromptText(r.Prompt, inputTokens)

	if len(r.ImageURLs) > 0 {
		// VLM mode: construct multi-content message with image_url blocks
		content := []map[string]any{
			{
				"type": "text",
				"text": prompt,
			},
		}
		for _, url := range r.ImageURLs {
			content = append(content, map[string]any{
				"type": "image_url",
				"image_url": map[string]string{
					"url": url,
				},
			})
		}
		return []map[string]any{
			{
				"role":    "user",
				"content": content,
			},
		}
	}

	// LLM mode: simple text message
	return []map[string]string{
		{"role": "user", "content": prompt},
	}
}

func buildPromptText(prompt string, targetTokens int) string {
	if strings.TrimSpace(prompt) == "" {
		return generatePrompt(targetTokens)
	}
	targetChars := targetTokens * 4
	if targetChars <= 0 {
		targetChars = len(prompt)
	}
	base := fmt.Sprintf("[%d] %s", rand.Uint64(), strings.TrimSpace(prompt))
	if len(base) >= targetChars {
		return base[:targetChars]
	}
	need := targetChars - len(base)
	if need > len(promptPadding) {
		var sb strings.Builder
		sb.Grow(targetChars)
		sb.WriteString(base)
		for sb.Len() < targetChars {
			sb.WriteString(" ")
			sb.WriteString(prompt)
			sb.WriteString(" ")
			sb.WriteString(promptPadding)
		}
		return sb.String()[:targetChars]
	}
	return base + promptPadding[:need]
}

// promptPadding is a pre-generated padding string (64KB) reused across calls.
// Only the random prefix differs per call, avoiding O(n) string building.
var promptPadding = func() string {
	const unit = "The quick brown fox jumps over the lazy dog. "
	var sb strings.Builder
	sb.Grow(64 * 1024)
	for sb.Len() < 64*1024 {
		sb.WriteString(unit)
	}
	return sb.String()
}()

// generatePrompt generates a randomized prompt of approximately targetTokens length.
// Each call produces a unique prompt to avoid KV cache prefix matching.
func generatePrompt(targetTokens int) string {
	prefix := fmt.Sprintf("[%d] Please write a detailed response about the following topic. ", rand.Uint64())
	targetChars := targetTokens * 4
	if len(prefix) >= targetChars {
		return prefix[:targetChars]
	}
	need := targetChars - len(prefix)
	if need > len(promptPadding) {
		// Extremely large prompt -- extend padding on the fly (rare path)
		var sb strings.Builder
		sb.Grow(targetChars)
		sb.WriteString(prefix)
		for sb.Len() < targetChars {
			sb.WriteString(promptPadding)
		}
		return sb.String()[:targetChars]
	}
	return prefix + promptPadding[:need]
}
