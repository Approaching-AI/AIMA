package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// EmbeddingRequester benchmarks OpenAI-compatible /v1/embeddings endpoints.
type EmbeddingRequester struct {
	Model       string
	InputTokens int
	Prompt      string
	APIKey      string
	Timeout     time.Duration
}

func (r *EmbeddingRequester) Modality() string    { return "embedding" }
func (r *EmbeddingRequester) WarmupRequests() int { return 2 }

func (r *EmbeddingRequester) Do(ctx context.Context, endpoint string, seq int) (*Sample, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	inputTokens := r.InputTokens
	if inputTokens <= 0 {
		inputTokens = 128
	}
	payload := map[string]any{
		"model": r.Model,
		"input": buildPromptText(r.Prompt, inputTokens),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("marshal embeddings request: %w", err)}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseEndpoint(endpoint)+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("create embeddings request: %w", err)}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	if r.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &Sample{
			Seq:       seq,
			LatencyMs: float64(time.Since(start).Microseconds()) / 1000.0,
			Error:     fmt.Errorf("send embeddings request: %w", err),
		}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return &Sample{
			Seq:       seq,
			LatencyMs: float64(time.Since(start).Microseconds()) / 1000.0,
			Error:     fmt.Errorf("read embeddings response: %w", err),
		}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return &Sample{
			Seq:       seq,
			LatencyMs: float64(time.Since(start).Microseconds()) / 1000.0,
			Error:     fmt.Errorf("embeddings HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 512)),
		}, nil
	}

	var parsed struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage *struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return &Sample{
			Seq:       seq,
			LatencyMs: float64(time.Since(start).Microseconds()) / 1000.0,
			Error:     fmt.Errorf("parse embeddings response: %w", err),
		}, nil
	}
	if len(parsed.Data) == 0 {
		return &Sample{
			Seq:       seq,
			LatencyMs: float64(time.Since(start).Microseconds()) / 1000.0,
			Error:     fmt.Errorf("embeddings response missing data"),
		}, nil
	}

	if parsed.Usage != nil && parsed.Usage.PromptTokens > 0 {
		inputTokens = parsed.Usage.PromptTokens
	}

	return &Sample{
		Seq:                 seq,
		LatencyMs:           float64(time.Since(start).Microseconds()) / 1000.0,
		InputTokens:         inputTokens,
		EmbeddingDimensions: len(parsed.Data[0].Embedding),
	}, nil
}
