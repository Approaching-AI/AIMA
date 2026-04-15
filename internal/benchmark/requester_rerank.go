package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RerankRequester benchmarks OpenAI/Cohere-style rerank endpoints.
type RerankRequester struct {
	Model       string
	InputTokens int
	Prompt      string
	APIKey      string
	Timeout     time.Duration
}

func (r *RerankRequester) Modality() string    { return "reranker" }
func (r *RerankRequester) WarmupRequests() int { return 1 }

func (r *RerankRequester) Do(ctx context.Context, endpoint string, seq int) (*Sample, error) {
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
	query := strings.TrimSpace(r.Prompt)
	if query == "" {
		query = "Rank the candidate passages by relevance to the device benchmark query."
	}
	documents := buildRerankDocuments(query, inputTokens)

	var lastSample *Sample
	for _, suffix := range []string{"/v1/rerank", "/rerank"} {
		sample := r.doRequest(ctx, baseEndpoint(endpoint)+suffix, seq, query, documents, inputTokens)
		lastSample = sample
		if sample == nil || sample.Error == nil {
			return sample, nil
		}
		if !isRetryableMissingRerankEndpoint(sample.Error) {
			return sample, nil
		}
	}
	if lastSample == nil {
		lastSample = &Sample{Seq: seq, Error: fmt.Errorf("rerank endpoint unavailable")}
	}
	return lastSample, nil
}

func (r *RerankRequester) doRequest(ctx context.Context, url string, seq int, query string, documents []string, inputTokens int) *Sample {
	payload := map[string]any{
		"model":     r.Model,
		"query":     query,
		"documents": documents,
		"top_n":     len(documents),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("marshal rerank request: %w", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return &Sample{Seq: seq, Error: fmt.Errorf("create rerank request: %w", err)}
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
			Error:     fmt.Errorf("send rerank request: %w", err),
		}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return &Sample{
			Seq:       seq,
			LatencyMs: float64(time.Since(start).Microseconds()) / 1000.0,
			Error:     fmt.Errorf("read rerank response: %w", err),
		}
	}
	if resp.StatusCode != http.StatusOK {
		return &Sample{
			Seq:       seq,
			LatencyMs: float64(time.Since(start).Microseconds()) / 1000.0,
			Error:     fmt.Errorf("rerank HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 512)),
		}
	}

	var parsed struct {
		Results []struct {
			Index int `json:"index"`
		} `json:"results"`
		Data []struct {
			Index int `json:"index"`
		} `json:"data"`
		Usage *struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
			InputTokens  int `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return &Sample{
			Seq:       seq,
			LatencyMs: float64(time.Since(start).Microseconds()) / 1000.0,
			Error:     fmt.Errorf("parse rerank response: %w", err),
		}
	}

	rankedCount := len(parsed.Results)
	if rankedCount == 0 {
		rankedCount = len(parsed.Data)
	}
	if rankedCount == 0 {
		return &Sample{
			Seq:       seq,
			LatencyMs: float64(time.Since(start).Microseconds()) / 1000.0,
			Error:     fmt.Errorf("rerank response missing ranked results"),
		}
	}

	if parsed.Usage != nil {
		switch {
		case parsed.Usage.TotalTokens > 0:
			inputTokens = parsed.Usage.TotalTokens
		case parsed.Usage.PromptTokens > 0:
			inputTokens = parsed.Usage.PromptTokens
		case parsed.Usage.InputTokens > 0:
			inputTokens = parsed.Usage.InputTokens
		}
	}

	return &Sample{
		Seq:          seq,
		LatencyMs:    float64(time.Since(start).Microseconds()) / 1000.0,
		InputTokens:  inputTokens,
		OutputTokens: rankedCount,
	}
}

func buildRerankDocuments(query string, inputTokens int) []string {
	perDocTokens := inputTokens / 4
	if perDocTokens < 32 {
		perDocTokens = 32
	}
	base := strings.TrimSpace(query)
	if base == "" {
		base = "device benchmark query"
	}
	return []string{
		buildPromptText(base+" relevant benchmark evidence", perDocTokens),
		buildPromptText(base+" partially related deployment notes", perDocTokens),
		buildPromptText("unrelated container startup logs", perDocTokens),
		buildPromptText("general hardware inventory and memory summary", perDocTokens),
	}
}

func isRetryableMissingRerankEndpoint(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "http 404") || strings.Contains(msg, "http 405")
}
